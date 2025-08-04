# Proposal: Request Bandwidth Limitations

## Overview

This document analyzes the data bandwidth requirements for inference requests on the chain and proposes limits to ensure sustainable operation. We examine how much data is transmitted and stored on-chain per inference request, broken down by input/output tokens, to establish appropriate rate limiting.


## Objective

1. **Estimate bandwidth consumption**: Calculate KB per input token and KB per output token
2. **Define request limits**: Establish maximum requests/tokens the service can handle 
3. **Implement rate limiting**: Apply limits at the Transfer Agent level (proportional to node weight)

## System Architecture

**Load Distribution:**
- **Transfer Agent**: Controls request initiation and applies rate limits (per node)
- **Executor**: Handles inference execution (randomly assigned by chain, no additional limits needed)

## Transaction Flow and Data Transmission

### Transfer Agent (TA):
- Receives request
- Sample executor, sign request and proxy request to the executor
- [async]: Creates `MsgStartInference` transaction

### Executor
- Receives request from TA
- Make inference on MLNode and proxy results back 
- Creates `MsgFinishInference`

### Validator (another executors)
- Per each `MsgFinishInference` sample of it has to be validates
- If validation is needed - validate and create `MsgValidation`
- Validation probability is per executor proportionally to reputation and changed from 1 to 0.01 


## Transaction Messages

### `MsgStartInference`

```protobuf
message MsgStartInference {
  option (cosmos.msg.v1.signer) = "creator";
  string creator        = 1;
  string inference_id   = 2;
  string prompt_hash    = 3;
  string prompt_payload = 4; // Full payload JSON with signatures, seeds, etc.
  string model          = 6;
  string requested_by   = 7;
  string assigned_to    = 8;
  string node_version   = 9;
  uint64 max_tokens     = 10;
  uint64 prompt_token_count = 11;
  int64  request_timestamp = 12;
  string transfer_signature = 14;
  string original_prompt = 15; // Original payload JSON
}
```

#### [Not in current scope] TODO:
- don't send payload twice


### `MsgFinishInference`
```protobuf
message MsgFinishInference {
  option (cosmos.msg.v1.signer) = "creator";
  string creator                = 1;
  string inference_id           = 2;
  string response_hash          = 3;
  string response_payload       = 4; // Response JSON
  uint64 prompt_token_count     = 5;
  uint64 completion_token_count = 6;
  string executed_by            = 7;
  string transferred_by         = 8;
  int64  request_timestamp      = 9;
  string transfer_signature     = 10;
  string executor_signature     = 11;
  string requested_by           = 12;
  string original_prompt        = 13; // Original payload JSON
}
```

#### [Not in current scope] TODO:
- don't input payload once again
- don't send full response payload till requested (=> on-demand)


### `MsgValidation` (sent by Validators, let's say ~20% of inferences in the early phases)
```protobuf
message MsgValidation {
  option (cosmos.msg.v1.signer) = "creator";
  string creator          = 1;
  string id               = 2;
  string inference_id     = 3;
  string response_payload = 4;
  string response_hash    = 5;
  double value            = 6;
  bool   revalidation     = 7;
}
```
- don't send response payload


#### Inference

*That's just for example what is actually stored for a longer time*

That's what actually will be saved for the future
```protobuf
message Inference {
    ...
}
```

## Bandwidth Analysis

### Chain Capacity
- **Block size limit**: 22MB per block (genesis: `max_bytes: "22020096"`)
- **Block time**: ~5-6s (6s theoretical, 5s observed)  
- **Effective bandwidth**: ~3.7-4.4MB/s

### Data Size Metrics

#### Per-Token Data Consumption  
Based on observed network traffic with top-k=5 logprobs (enforced by Transfer Agent):

| Metric | Mean | P90 | Notes |
|--------|------|-----|-------|
| **Input tokens** | 0.0023 KB/token | 0.0037 KB/token | Doubles for short prompts (<200 tokens) |
| **Output tokens** | 0.6424 KB/token | 0.7125 KB/token | Includes top-k=5 logprobs (auto-enabled) |

#### Typical Request Profile (QwQ Model)
- **Input**: 4,000 tokens
- **Output**: 150 tokens  
- **Observed throughput**: 0.11 requests/second (realistic), 0.25 requests/second (target)

### Payload Size Analysis

#### Complete Request Payload
Observed from network logs (full input + output JSONs):

| Validation Rate | Mean Size | P90 Size |
|----------------|-----------|----------|
| No validation | 85 KB | 197 KB |
| 15% validation | 98 KB | 226 KB |
| **~20% validation** (dynamic) | **102 KB** | **236 KB** |
| 50% validation (startup) | 139 KB | 325 KB |

#### Message-Level Breakdown
Each inference generates multiple messages with data duplication:

**Per inference transmission:**
- `MsgStartInference`: PromptPayload (~9.2 KB for 4K tokens)
- `MsgFinishInference`: ResponsePayload + OriginalPrompt (~96 KB + 9.2 KB)  
- `MsgValidation`: ResponsePayload (20% of inferences, ~96 KB)

**Total per inference**: 28.5 KB (20% validation) to 57.4 KB (50% validation)

## Capacity Calculations

### Theoretical Limits

Using the formula: `0.0023 × Input_tokens + 0.6424 × Output_tokens`

**Block capacity**: 22,000 KB - 500 KB (safety buffer) = 21,500 KB

**Maximum inferences per block** (typical 4K input, 150 output):
```
21,500 KB ÷ (0.0023 × 4,000 + 0.6424 × 150) = 204 inferences/block
```

### Practical Throughput Limits

Based on observed payload sizes:

| Scenario | Payload Size | Inferences/Block | Inferences/Second |
|----------|--------------|------------------|-------------------|
| **Current (~20% validation)** | 102 KB (mean) | 211 | 42 |
| **Current (~20% validation)** | 236 KB (P90) | 91 | 18 |
| Conservative estimate | 250 KB | 86 | 17 |
| High validation (50%) | 325 KB (P90) | 66 | 13 |

### Per Transfer Agent Limits

**Formula per node**: `(Chain_capacity_KB ÷ Number_of_transfer_agents) ÷ Payload_size_KB`

For multiple Transfer Agents, divide the total capacity proportionally based on node weight or equally.


## Recommendations

### Immediate Implementation
1. **Transfer Agent rate limiting**: 15-20 inferences/second per node (conservative P90 estimate)
2. **Token limits**: Maximum 4,000 input + 150 output tokens per request (DefaultMaxTokens)
3. **Payload monitoring**: Alert when individual requests exceed 250 KB

### Optimization Opportunities
1. **Reduce data duplication**: OriginalPrompt is stored in both MsgStartInference and MsgFinishInference
2. **Validation efficiency**: Consider reducing validation rate from 20% to 15% during normal operation  
3. **Data pruning**: Implemented - removes inference payloads after configurable epoch threshold
4. **Compression**: Implement payload compression for large requests

### Monitoring Metrics
- Track actual payload sizes vs. estimates
- Monitor block utilization percentage  
- Alert on sustained throughput above 80% capacity