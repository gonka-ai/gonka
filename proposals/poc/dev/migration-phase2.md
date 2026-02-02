# PoC Migration - Phase 2 Implementation Status

## Overview

Phase 2 focuses on restoring DAPI V1 logic from `main` branch in isolated `*_v1.go` files. No dispatch wiring yet - that's Phase 3.

**Goal**: Create all V1 DAPI components so they can be dispatched based on `poc_v2_enabled` in Phase 3.

---

## Completed

### 2.1 MLNode Client V1 Methods

| Item | Status | Details |
|------|--------|---------|
| `InitGenerateV1()` | Done | `mlnodeclient/poc_v1_requests.go` |
| `InitValidateV1()` | Done | `mlnodeclient/poc_v1_requests.go` |
| `ValidateBatchV1()` | Done | `mlnodeclient/poc_v1_requests.go` |
| `GetPowStatusV1()` | Done | `mlnodeclient/poc_v1_requests.go` |
| `InitDtoV1` type | Done | Request body for V1 PoC endpoints |
| `BuildInitDtoV1()` | Done | Constructs V1 init DTO with model params |
| V1 model params | Done | `TestNetParamsV1`, `MainNetParamsV1` |

**V1 Endpoints**: `/api/v1/pow/*` (vs V2's `/api/v1/inference/pow/*`)

### 2.2 MLNode Interface Update

| Item | Status | Details |
|------|--------|---------|
| Add V1 methods to interface | Done | `mlnodeclient/interface.go` |
| Mock V1 implementations | Done | `mlnodeclient/mock.go` |
| V1 error injection fields | Done | `InitGenerateV1Error`, etc. |
| V1 call tracking fields | Done | `InitGenerateV1Called`, etc. |

### 2.3 Node Worker Commands V1

| File | Status | Contents |
|------|--------|----------|
| `broker/node_worker_commands_v1.go` | Created | `StartPoCNodeCommandV1`, `InitValidateNodeCommandV1` |

**V1 Behavior**: MLNode MUST be stopped before state transitions (unlike V2).

### 2.4 Cosmos Client V1 Methods

| Item | Status | Details |
|------|--------|---------|
| `SubmitPocBatch()` | Done | `cosmosclient/cosmosclient_v1.go` |
| `SubmitPoCValidation()` | Done | `cosmosclient/cosmosclient_v1.go` |
| Interface update | Done | `cosmosclient/cosmosclient.go` |
| Mock implementations | Done | `cosmosclient/mock_cosmos_message_client.go` |

### 2.5 V1 Callback Handlers

| File | Status | Contents |
|------|--------|----------|
| `internal/server/mlnode/post_generated_artifacts_v1_handler.go` | Created | `postGeneratedBatchesV1()`, `postValidatedBatchesV1()` |
| `internal/server/mlnode/post_generated_batches_handler.go` | Deleted | Was returning 410 Gone |
| `internal/server/mlnode/server.go` | Updated | Routes now use V1 handlers |

**Route Registration**:
- `/v1/poc-batches/generated` → `postGeneratedBatchesV1()`
- `/v1/poc-batches/validated` → `postValidatedBatchesV1()`
- `/v2/poc-batches/generated` → `postGeneratedArtifactsV2()`
- `/v2/poc-batches/validated` → `postValidatedArtifactsV2()`

### 2.6 V1 On-Chain Validator

| File | Status | Contents |
|------|--------|----------|
| `poc/validator_v1.go` | Created | `OnChainValidator`, `ValidateAll()` |

**V1 Validation Flow**:
1. Query chain for `PoCBatch` via `PocBatchesForStage`
2. Sample nonces deterministically
3. Send batches to MLNode via `ValidateBatchV1()`

### 2.7 Version Helper

| File | Status | Contents |
|------|--------|----------|
| `poc/version.go` | Created | `IsPoCv2Enabled()` helper |

**Default**: Returns `true` (V2) when params are nil.

### 2.8 Tests

| Test File | Status | Coverage |
|-----------|--------|----------|
| `mlnodeclient/poc_v1_requests_test.go` | Created | 7 tests: DTOs, params, status constants, mock methods |
| `broker/node_worker_commands_v1_test.go` | Created | 7 tests: idempotency, stop behavior, error handling |
| `poc/validator_v1_test.go` | Created | 6 tests: sampling, determinism, config defaults |
| `poc/version_test.go` | Created | 4 tests: nil handling, enabled/disabled states |

**All tests pass.**

---

## File Summary

### Files Created

```
decentralized-api/mlnodeclient/
├── poc_v1_requests.go              # V1 MLNode client methods
└── poc_v1_requests_test.go         # V1 client tests

decentralized-api/broker/
├── node_worker_commands_v1.go      # V1 start/validate commands
└── node_worker_commands_v1_test.go # V1 command tests

decentralized-api/cosmosclient/
└── cosmosclient_v1.go              # V1 chain message submission

decentralized-api/internal/server/mlnode/
└── post_generated_artifacts_v1_handler.go  # V1 callback handlers

decentralized-api/poc/
├── validator_v1.go                 # V1 on-chain validator
├── validator_v1_test.go            # V1 validator tests
├── version.go                      # IsPoCv2Enabled helper
└── version_test.go                 # Version helper tests
```

### Files Modified

```
decentralized-api/mlnodeclient/interface.go      # Added V1 methods to interface
decentralized-api/mlnodeclient/mock.go           # Added V1 mock implementations
decentralized-api/cosmosclient/cosmosclient.go   # Added V1 methods to interface
decentralized-api/cosmosclient/mock_cosmos_message_client.go  # Added V1 mocks
decentralized-api/internal/server/mlnode/server.go  # Updated route registration
```

### Files Deleted

```
decentralized-api/internal/server/mlnode/post_generated_batches_handler.go  # Replaced
```

---

## Behavior Summary

### V1 vs V2 Key Differences (DAPI)

| Aspect | V1 | V2 |
|--------|----|----|
| MLNode endpoints | `/api/v1/pow/*` | `/api/v1/inference/pow/*` |
| Stop before transition | Required | Not required |
| Artifact storage | On-chain `PoCBatch` | Off-chain with MMR commits |
| Validation source | Query chain batches | Fetch proofs from participant API |
| Callback paths | `/v1/poc-batches/*` | `/v2/poc-batches/*` |

### V1 Commands Behavior

**StartPoCNodeCommandV1**:
1. Check if already generating (idempotency)
2. Stop node if not STOPPED (V1 requirement)
3. Call `InitGenerateV1()` to start PoC

**InitValidateNodeCommandV1**:
1. Check if already validating (idempotency)
2. Stop node if in INFERENCE (allow POW to continue)
3. Call `InitValidateV1()` to start validation

### V1 Validation Flow

```
OnChainValidator.ValidateAll()
    │
    ├─► Query PocBatchesForStage from chain
    │
    ├─► For each participant:
    │   ├─► Collect nonces/dist from batches
    │   ├─► Sample nonces deterministically
    │   └─► Send to MLNode via ValidateBatchV1()
    │
    └─► MLNode callback → postValidatedBatchesV1() → MsgSubmitPocValidation
```

---

## Phase 3 (Completed)

### Dispatch Wiring

| Item | Status | Notes |
|------|--------|-------|
| `broker/broker.go` dispatch | Done | `getCommandForState()` V1/V2 switch |
| `poc/orchestrator.go` dispatch | Done | `ValidateReceivedArtifacts()` switch |
| `poc/commit_worker.go` check-inside | Done | Skip when V1 mode |
| `internal/server/public/poc_handler.go` check-inside | Done | Return 503 when V1 mode |
| Version check at callback handlers | Done | Guard V1/V2 handlers by mode |
| Testermint E2E tests | Done | `PoCMigrationTests.kt` |

See `migration-phase3.md` for implementation details.

---

## Migration Complete

All migration phases are now complete:
- **Phase 1**: On-chain V1 logic with guards
- **Phase 2**: DAPI V1 logic in isolated files
- **Phase 3**: Dispatch wiring and guards

---

## Related Documents

- `migration.md` - Full migration plan
- `migration-phase1.md` - Phase 1 (on-chain V1 logic)
- `migration-phase3.md` - Phase 3 (dispatch and guards)
- `offchain.md` - PoC V2 off-chain artifacts proposal
- `manager-v6.md` - Phase 5 domain consolidation
