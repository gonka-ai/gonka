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
- cut payload datastructure and use protobuf for it
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
- cut response datastructure and use protobuf for it
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

#### Example Data Structures

**Input Request** (compact, minimal per-token overhead):
```json
{
  "model": "gpt-4",
  "messages": [{"role": "user", "content": "What is machine learning?"}],
  "max_tokens": 150,
  "logprobs": true,
  "top_logprobs": 5
}
```

**Output Response** (verbose due to logprobs metadata):
```json
{
  "choices": [{
    "message": {"content": "Machine learning is a subset of artificial intelligence..."},
    "logprobs": {
      "content": [
        {
          "token": "9707",
          "logprob": -0.0234,
          "bytes": [77, 97, 99, 104, 105, 110, 101],
          "top_logprobs": [
            {"token": "9707", "logprob": -0.0234, "bytes": [77, 97, 99, 104, 105, 110, 101]},
            {"token": "15234", "logprob": -1.456, "bytes": [65, 114, 116, 105, 102, 105, 99, 105, 97, 108]},
            {"token": "25891", "logprob": -2.789, "bytes": [68, 101, 101, 112]},
            {"token": "34567", "logprob": -3.012, "bytes": [78, 101, 117, 114, 97, 108]},
            {"token": "42134", "logprob": -3.245, "bytes": [67, 111, 109, 112, 117, 116, 101, 114]}
          ]
        },
        {
          "token": "6754",
          "logprob": -0.1567,
          "bytes": [32, 108, 101, 97, 114, 110, 105, 110, 103],
          "top_logprobs": [
            {"token": "6754", "logprob": -0.1567, "bytes": [32, 108, 101, 97, 114, 110, 105, 110, 103]},
            {"token": "8923", "logprob": -2.234, "bytes": [32, 105, 110, 116, 101, 108, 108, 105, 103, 101, 110, 99, 101]},
            {"token": "12456", "logprob": -3.567, "bytes": [32, 118, 105, 115, 105, 111, 110]},
            ...
          ]
        },
        {
          "token": "374",
          "logprob": -0.0891,
          "bytes": [32, 105, 115],
          "top_logprobs": [
            {"token": "374", "logprob": -0.0891, "bytes": [32, 105, 115]},
            {"token": "7832", "logprob": -1.789, "bytes": [32, 105, 110, 118, 111, 108, 118, 101, 115]},
            ...
          ]
        },
        ...
      ]
    }
  }],
  "usage": {"prompt_tokens": 4, "completion_tokens": 12}
}
```
*Each output token generates ~640 bytes of metadata including top-5 alternatives with probabilities and byte representations.*

> **Note**: After PR `gm/enforced-str`, token IDs (e.g., "9707") are used instead of token strings. The actual text is preserved in the `bytes` array.

> **Node 2**: We for sure should cut A LOT of this data and don't transfer it

#### Example
**Typical Request:**
- Input: 1,000 tokens → ~2.3 KB
- Output: 150 tokens → ~96 KB  
- Total payload: ~102 KB (with 20% validation rate)

## Capacity Estimation

### Simple Formulas

**Per-request data size:**
```
Total_KB = Input_tokens × 0.0023 + Output_tokens × 0.64 + Validation_overhead
```

**Chain throughput (20% validation):**
```
Block_capacity = 21,500 KB (22MB - 500KB safety buffer)
Requests_per_block = Block_capacity ÷ Average_payload_size
Requests_per_second = Requests_per_block ÷ 5 seconds
```

### Practical Limits
- **Average payload**: 102 KB → ~211 requests/block → **42 requests/second** recoreded on chain
- **Conservative (P90)**: 236 KB → ~91 requests/block → **18 requests/second** recoreded on chain

### Per Transfer Agent Allocation

**Weight-Based Distribution** (Recommended):
```
taEstimatedLimitsPerBlockKb = EstimatedLimitsPerBlockKb × (nodeWeight / totalWeight)
```

Since all transactions have fee = 0 (no mempool priority), nodes with higher weight can record transactions faster due to:
- More frequent block proposals 
- Faster addition to their own mempool

Weight-based allocation ensures fair resource distribution proportional to each node's chain contribution.

**Alternative: Equal Distribution**:
```
Agent_limit = Total_throughput ÷ Number_of_agents
```
For 3 agents: ~14 requests/second each (conservative estimate)

> **Note**: Equal distribution is simpler but may create inefficiencies when node weights vary significantly.


### Initial Estimation + Expiration Block
**How it works**: 
1. Estimate KB based on request parameters
2. Calculate expiration block (start block + request lifespan)
3. Record estimated usage at the expiration block as deadline
4. Check acceptance by averaging usage across the window [current block : expiration block]

```go
// Estimate request size
estimatedKB := float64(promptTokens)*0.0023 + float64(maxTokens)*0.64

// Record at expiration block (deadline)
expirationBlock := startBlock + requestLifespanBlocks
usagePerBlock[expirationBlock] += estimatedKB

// Check acceptance: average window usage vs limit
totalUsage := sum(usagePerBlock[currentBlock : expirationBlock])
avgUsage := totalUsage / windowSize
canAccept := avgUsage + newRequestAvgImpact <= limitsPerBlockKB
```

**Logic**: Since inference will be recorded on-chain somewhere within the window (likely at the end), we check if the **average capacity** across the entire window stays under limits. This allows flexible timing while ensuring chain capacity constraints.

**Pros**:
- **Predictive**: Can prevent accepting requests before limits exceeded
- **Window-based averaging**: Handles timing uncertainty in chain recording
- **Simple implementation**: No proxy instrumentation needed
- **Fail-fast**: Immediate feedback to users

**Cons**:
- **Over-estimation**: Reserves for max_tokens but most responses much shorter
- **Static assumptions**: Fixed KB/token ratios may drift over time

---

**Recommendation**: Start with **Option 1** for simplicity and reliability, then consider **Option 2** if more precision needed.

### Phase 2: Payload Optimization
**Objective**: Reduce on-chain data size by 60-80%

**Changes**:
- Replace full payloads with hash signatures in `MsgStartInference` and `MsgFinishInference`
- Convert JSON payloads to protobuf structures
- Remove redundant fields:
  - `bytes` arrays in logprobs
  - Duplicate `original_prompt` fields
  - Unnecessary metadata

### Phase 3: Off-chain Response Storage
**Objective**: Eliminate response payload storage on-chain until validation

**Protocol**:
1. **Execution**: Store only response hash on-chain, executor keeps full response locally (N epochs)
2. **Validation trigger**: Validator requests response directly from executor
3. **Success path**: Validate response against hash, record result on-chain
4. **Failure path**: If executor fails to provide response:
   - Send `MsgProvideInference` requiring on-chain response storage
   - Validate against hash or apply immediate punishment