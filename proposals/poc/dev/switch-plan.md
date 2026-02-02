# PoC v2 Design

> **Scope**: PoC v2 implementation for weight calculation and orchestration.
> Builds on top of [messages-plan.md](./messages-plan.md) which defined proto messages and storage.

## Overview

PoC v2 handles weight calculation and orchestration for ML node validation.
Configuration via `poc_v2_params` chain parameters.

## Design Goals

1. **Minimal code** - Single implementation path, no fallbacks
2. **Governance-controlled** - Configuration via parameter updates
3. **Code separation** - v2 logic in dedicated files
4. **testermint compatibility** - Full integration test coverage
5. **No MLNode stop calls** - v2 flow does not call `.Stop()` on MLNode during generation

## Chain Parameters

### New `PoCv2Params` in `params.proto`

```protobuf
message PoCv2Params {
  bool enabled = 1;      // false by default, enabled via governance
  string model_id = 2;   // Required when enabled
  int64 seq_len = 3;     // Required when enabled
}
```

Embedded in main `Params` message:
```protobuf
message Params {
  // ... existing fields ...
  PoCv2Params poc_v2_params = 13;
}
```

### Validation

- `enabled = false`: No validation required
- `enabled = true`: `model_id` must be non-empty, `seq_len` must be positive

## Weight Calculation

### Entry Point: `ComputeNewWeights`

Routes to `ComputeNewWeightsV2()` in `pocv2_chainvalidation.go`.

- Query `PoCArtifactBatchesV2` and `PoCValidationsV2` by stage
- For each participant: `validated_weight > 0` → valid vote
- TODO: Derive explicit weight from voting once artifacts are off-chain
- Same `validation_sample_size` semantics as v1

## Confirmation PoC

Handled in `confirmation_poc.go` via `calculateConfirmationWeightsV2()`.

## decentralized-api

### MLNode Client (`mlnodeclient/poc_v2_requests.go`)

New methods for v2 endpoints:
- `InitGenerateV2()` → `POST /api/v1/inference/pow/init/generate`
- `GenerateV2()` → `POST /api/v1/inference/pow/generate`
- `GetPowStatusV2()` → `GET /api/v1/inference/pow/status`

Request differences from v1:
- Includes `model_id`, `seq_len` from chain params
- Does NOT include `k_dim` (removed for v2)
- No `.Stop()` calls during generation
- `StopPowV2()` called once at validation stage transition (not per-batch)

### Node Worker Commands (`broker/node_worker_commands.go`)

- `StartPoCNodeCommandV2`: Starts PoC generation (no Stop() before init)
- Validation handled by orchestrator via `StopPowV2` + `GenerateV2` with validation artifacts

### Orchestrator (`internal/pocv2/node_orchestrator_v2.go`)

- `ValidateReceivedArtifacts()`: Query v2 artifact batches, sample, call `GenerateV2()` on MLNodes
- Calls `StopPowV2()` once on all nodes before starting validation requests
- Reuses chain bridge pattern from v1

## testermint Updates

### Mock Routes (`PowV2Routes.kt`)

Relaxed state preconditions:
- `init/generate`: Allowed if not already `GENERATING`
- `generate`: Allowed in any state (transitions to `POW_VALIDATING`)

This aligns with the "no Stop()" requirement - nodes can receive v2 commands without requiring a stop first.

## Configuration

Update `poc_v2_params` via governance:
```json
{
  "poc_v2_params": {
    "enabled": true,
    "model_id": "model-name",
    "seq_len": 256
  }
}
```

## Not Covered

- Off-chain artifact storage migration
- Multi-validator consensus on `validated_weight`

---

## Implementation Status

See [switch-impl.md](./switch-impl.md) for detailed implementation.

| Area | Status |
|------|--------|
| Chain parameters | Done |
| Chain weight calculation | Done |
| Chain confirmation weights | Done |
| Chain gRPC query | Done |
| DAPI v2 orchestrator | Done |
| DAPI v2 commands | Done |
| DAPI StopPowV2 client | Done |
| v2 command unit tests | Done |
| testermint compatibility | Done |
| v1 code removal | Done |

**Active on chain** - v1 code removed, v2 is the only implementation.
