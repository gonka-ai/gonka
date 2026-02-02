# PoC Migration - Phase 1 Implementation Status

## Overview

Phase 1 focuses on restoring on-chain V1 logic from `main` branch in isolated files, adding the `poc_v2_enabled` governance parameter, and wiring dispatch logic.

**Goal**: Enable governance-controlled switching between V1 and V2 PoC on-chain behavior.

---

## Completed

### 1.1 Governance Parameter

| Item | Status | Details |
|------|--------|---------|
| Add `poc_v2_enabled` to `PocParams` proto | Done | `inference-chain/proto/inference/inference/params.proto` |
| Regenerate proto Go code | Done | `ignite generate proto-go` |
| Set default to `true` in Go | Done | `types/params.go` → `DefaultPocParams()` |
| Set default to `true` in testermint | Done | `testermint/src/main/kotlin/data/AppExport.kt` → `PocParams` |

### 1.2 V1 Handler Files (Keeper)

| File | Status | Contents |
|------|--------|----------|
| `keeper/msg_server_poc_v1.go` | Created | `SubmitPocBatchV1()` with V1 guard + `submitPocBatchV1()` |
| `keeper/msg_server_poc_validation_v1.go` | Created | `SubmitPocValidationV1()` with V1 guard + `submitPocValidationV1()` + `toPoCValidation()` |

**V1 Guards**: When `poc_v2_enabled=true`, V1 handlers reject with `ErrNotSupported`.

### 1.3 V1 Module Files (Weight Calculation)

| File | Status | Contents |
|------|--------|----------|
| `module/chainvalidation_v1.go` | Created | `ComputeNewWeightsV1()`, `WeightCalculatorV1`, `filterPoCBatchesFromInferenceNodesV1()` |
| `module/confirmation_poc_v1.go` | Created | `UpdateConfirmationWeightsV1()`, `GetNotPreservedTotalWeightByParticipantV1()`, `checkConfirmationSlashingV1()` |

### 1.4 V2 Guards

| File | Handler | Status |
|------|---------|--------|
| `keeper/msg_server_poc_v2_commit.go` | `PoCV2StoreCommit()` | Guard added |
| `keeper/msg_server_poc_v2_commit.go` | `MLNodeWeightDistribution()` | Guard added |
| `keeper/msg_server_poc_validations_v2.go` | `SubmitPocValidationsV2()` | Guard added |

**V2 Guards**: When `poc_v2_enabled=false`, V2 handlers reject with `ErrNotSupported`.

### 1.5 Dispatch Logic

| File | Function | Status | Behavior |
|------|----------|--------|----------|
| `module/module.go` | `onEndOfPoCValidationStage()` | Dispatch added | V1 → `ComputeNewWeightsV1()`, V2 → `ComputeNewWeights()` |
| `module/confirmation_poc.go` | `updateConfirmationWeights()` | Dispatch added | V1 → `UpdateConfirmationWeightsV1()`, V2 → `updateConfirmationWeightsV2()` |

### 1.6 Error Type

| Item | Status | Details |
|------|--------|---------|
| `ErrNotSupported` | Added | `types/errors.go` - code 1159 |

### 1.7 Tests

| Test File | Status | Coverage |
|-----------|--------|----------|
| `keeper/msg_server_poc_v1_test.go` | Created | 7 tests: blocklist, empty NodeId, confirmation PoC routing, window validation |
| `module/chainvalidation_v1_test.go` | Created | 5 tests: staking validators, first epoch, not enough validations, fraud detection, unique nonces |
| `module/confirmation_poc_v1_test.go` | Created | 4 tests: basic calculation, no batches, multiple participants, fraud rejection |

**All tests pass.**

---

## Not Implemented (Deferred to Phase 2/3)

### DAPI V1 Logic (Phase 2)

| Item | Status | Notes |
|------|--------|-------|
| `broker/node_worker_commands_v1.go` | Not done | V1 PoC start/validate commands |
| `poc/validator_v1.go` | Not done | On-chain validator for V1 batches |
| `poc/version.go` | Not done | `IsPoCv2Enabled()` helper |
| `internal/server/mlnode/post_generated_artifacts_v1_handler.go` | Not done | V1 callback handler |
| `cosmosclient/cosmosclient_v1.go` | Not done | V1 chain message submission |
| `mlnodeclient/poc_v1_requests.go` | Not done | V1 MLNode client methods |

### DAPI Dispatch (Phase 3)

| Item | Status | Notes |
|------|--------|-------|
| `broker/broker.go` dispatch | Not done | `getCommandForState()` V1/V2 switch |
| `poc/orchestrator.go` dispatch | Not done | `ValidateReceivedArtifacts()` switch |
| `poc/commit_worker.go` check-inside | Not done | Skip when V1 mode |
| `internal/server/public/poc_handler.go` check-inside | Not done | Return 503 when V1 mode |
| V1 callback route registration | Not done | `/v1/poc-batches/*` handlers |

### MLNode (Phase 3)

| Item | Status | Notes |
|------|--------|-------|
| Verify V1 endpoints available | Not verified | MLNode must expose V1 PoW endpoints |

---

## File Summary

### Files Created

```
inference-chain/x/inference/keeper/
├── msg_server_poc_v1.go            # NEW - V1 batch handler
└── msg_server_poc_validation_v1.go # NEW - V1 validation handler

inference-chain/x/inference/module/
├── chainvalidation_v1.go           # NEW - V1 weight calculation
└── confirmation_poc_v1.go          # NEW - V1 confirmation weights

inference-chain/x/inference/keeper/
└── msg_server_poc_v1_test.go       # NEW - V1 handler tests

inference-chain/x/inference/module/
├── chainvalidation_v1_test.go      # NEW - V1 weight tests
└── confirmation_poc_v1_test.go     # NEW - V1 confirmation tests
```

### Files Modified

```
inference-chain/proto/inference/inference/params.proto  # Added poc_v2_enabled
inference-chain/x/inference/types/params.go             # DefaultPocParams() default true
inference-chain/x/inference/types/errors.go             # Added ErrNotSupported
inference-chain/x/inference/keeper/msg_server_poc_v2_commit.go      # V2 guards
inference-chain/x/inference/keeper/msg_server_poc_validations_v2.go # V2 guard
inference-chain/x/inference/module/module.go            # Dispatch in onEndOfPoCValidationStage
inference-chain/x/inference/module/confirmation_poc.go  # Dispatch in updateConfirmationWeights
testermint/src/main/kotlin/data/AppExport.kt            # Added pocV2Enabled field
```

---

## Behavior Summary

### When `poc_v2_enabled = true` (default, V2 mode)

| Component | Behavior |
|-----------|----------|
| `SubmitPocBatchV1` | Rejects with `ErrNotSupported` |
| `SubmitPocValidationV1` | Rejects with `ErrNotSupported` |
| `PoCV2StoreCommit` | Accepts |
| `MLNodeWeightDistribution` | Accepts |
| `SubmitPocValidationsV2` | Accepts |
| Weight calculation | Uses `ComputeNewWeights()` (V2, from commits) |
| Confirmation weights | Uses `updateConfirmationWeightsV2()` |

### When `poc_v2_enabled = false` (V1 mode)

| Component | Behavior |
|-----------|----------|
| `SubmitPocBatchV1` | Accepts, stores `PoCBatch` |
| `SubmitPocValidationV1` | Accepts, stores `PoCValidation` |
| `PoCV2StoreCommit` | Rejects with `ErrNotSupported` |
| `MLNodeWeightDistribution` | Rejects with `ErrNotSupported` |
| `SubmitPocValidationsV2` | Rejects with `ErrNotSupported` |
| Weight calculation | Uses `ComputeNewWeightsV1()` (V1, from batches) |
| Confirmation weights | Uses `UpdateConfirmationWeightsV1()` |

---

## Next Steps

1. **Phase 2**: Implement DAPI V1 logic in isolated files
2. **Phase 3**: Wire DAPI dispatch, add integration tests, verify MLNode endpoints
