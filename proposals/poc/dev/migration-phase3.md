# PoC Migration - Phase 3 Implementation Status

## Overview

Phase 3 wires V1/V2 dispatch and adds guards across DAPI, connecting Phase 1 (on-chain) and Phase 2 (DAPI V1 logic) to enable runtime switching via `poc_v2_enabled` governance parameter.

**Goal**: Enable governance-controlled switching between V1 and V2 PoC without node restart.

---

## Completed

### 3.1 EpochState PocV2Enabled Flag

| Item | Status | Details |
|------|--------|---------|
| Add `PocV2Enabled` to `EpochState` | Done | `chainphase/phase_tracker.go` |
| Add `pocV2Enabled` to `ChainPhaseTracker` | Done | Cached from params |
| Add `UpdatePocV2Enabled()` method | Done | Called by dispatcher |
| Add `IsPocV2Enabled()` method | Done | Thread-safe getter |
| Update dispatcher to set flag | Done | `internal/event_listener/new_block_dispatcher.go` |

### 3.2 Broker Dispatch

| Item | Status | Details |
|------|--------|---------|
| Add `IsPoCv2Enabled()` to Broker | Done | `broker/broker.go` |
| Add V1 callback URL helper | Done | `GetPoCv1CallbackBaseURL()` |
| Dispatch in `getCommandForState()` | Done | V1/V2 command dispatch |

**Dispatch Logic**:
- `PocStatusGenerating`: `StartPoCNodeCommandV1` or `StartPoCNodeCommandV2`
- `PocStatusValidating`: `InitValidateNodeCommandV1` or `TransitionPoCToValidatingV2Command`

### 3.3 Orchestrator Dispatch

| Item | Status | Details |
|------|--------|---------|
| Add `validatorV1 *OnChainValidator` | Done | `poc/orchestrator.go` |
| Add `isPoCv2Enabled func() bool` | Done | Injected at construction |
| Update `NewOrchestrator()` | Done | Creates both validators |
| Dispatch in `ValidateReceivedArtifacts()` | Done | V1/V2 validator dispatch |

### 3.4 CommitWorker Check-Inside

| Item | Status | Details |
|------|--------|---------|
| Add V1 mode skip in `tick()` | Done | `poc/commit_worker.go` |

**Behavior**: When `poc_v2_enabled=false`, `tick()` returns early (no commits or distribution needed in V1 mode).

### 3.5 Proof API Check-Inside

| Item | Status | Details |
|------|--------|---------|
| Add V1 mode check in `postPocProofs()` | Done | `internal/server/public/poc_handler.go` |
| Add V1 mode check in `getPocArtifactsState()` | Done | Returns 503 in V1 mode |

### 3.6 Callback Handler Guards

| Handler | Status | Details |
|---------|--------|---------|
| `postGeneratedBatchesV1()` | Done | Rejects when V2 mode |
| `postValidatedBatchesV1()` | Done | Rejects when V2 mode |
| `postGeneratedArtifactsV2()` | Done | Rejects when V1 mode |
| `postValidatedArtifactsV2()` | Done | Rejects when V1 mode |

### 3.7 Testermint E2E Tests

| Test | Status | Details |
|------|--------|---------|
| V1 mode test | Done | `poc v1 mode - batches on chain, no store commits` |
| V2 mode test | Done | `poc v2 mode - store commits on chain, proof api works` |
| Migration test | Done | `poc migration - v1 to v2 via governance without restart` |

**Test File**: `testermint/src/test/kotlin/PoCMigrationTests.kt`

---

## File Summary

### Files Modified

```
decentralized-api/chainphase/phase_tracker.go
  - Added PocV2Enabled to EpochState
  - Added pocV2Enabled field to ChainPhaseTracker
  - Added UpdatePocV2Enabled() and IsPocV2Enabled() methods

decentralized-api/internal/event_listener/new_block_dispatcher.go
  - Added call to phaseTracker.UpdatePocV2Enabled() from params query

decentralized-api/broker/broker.go
  - Added IsPoCv2Enabled() method
  - Added GetPoCv1CallbackBaseURL() helper
  - Updated getCommandForState() with V1/V2 dispatch

decentralized-api/poc/orchestrator.go
  - Added validatorV1 field
  - Added isPoCv2Enabled function
  - Updated NewOrchestrator() to create both validators
  - Updated ValidateReceivedArtifacts() with dispatch

decentralized-api/poc/commit_worker.go
  - Added V1 mode early return in tick()

decentralized-api/internal/server/public/poc_handler.go
  - Added V1 mode check in postPocProofs()
  - Added V1 mode check in getPocArtifactsState()

decentralized-api/internal/server/mlnode/post_generated_artifacts_v1_handler.go
  - Added V2 mode guard to V1 handlers

decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go
  - Added V1 mode guard to V2 handlers
```

### Files Created

```
testermint/src/test/kotlin/PoCMigrationTests.kt  # V1/V2/migration E2E tests
```

---

## Behavior Summary

### When `poc_v2_enabled = true` (V2 mode, default)

| Component | Behavior |
|-----------|----------|
| Broker dispatch | Returns V2 commands (`StartPoCNodeCommandV2`, `TransitionPoCToValidatingV2Command`) |
| Orchestrator | Uses `OffChainValidator` |
| CommitWorker | Runs commits and distribution |
| Proof API | Available (200) |
| V1 callbacks | Return 503 |
| V2 callbacks | Accept requests |

### When `poc_v2_enabled = false` (V1 mode)

| Component | Behavior |
|-----------|----------|
| Broker dispatch | Returns V1 commands (`StartPoCNodeCommandV1`, `InitValidateNodeCommandV1`) |
| Orchestrator | Uses `OnChainValidator` |
| CommitWorker | Skips (returns early) |
| Proof API | Return 503 |
| V1 callbacks | Accept requests |
| V2 callbacks | Return 503 |

---

## Runtime Switching

The system supports runtime switching between V1 and V2 without node restart:

1. **Governance proposal** updates `poc_v2_enabled` parameter
2. **Dispatcher** updates `phaseTracker.UpdatePocV2Enabled()` on each block
3. **All components** check the flag dynamically:
   - Broker checks before creating commands
   - Orchestrator checks before validating
   - CommitWorker checks in tick()
   - Handlers check before processing requests

---

## Migration Complete

With Phase 3 complete, the full V1/V2 migration is implemented:

- **Phase 1**: On-chain V1 logic with guards (complete)
- **Phase 2**: DAPI V1 logic in isolated files (complete)
- **Phase 3**: Dispatch wiring and guards (complete)

The system now supports:
- Running V2 (default) for off-chain artifacts
- Rolling back to V1 via governance if needed
- Runtime switching without restart

---

## Related Documents

- `migration.md` - Full migration plan
- `migration-phase1.md` - Phase 1 (on-chain V1 logic)
- `migration-phase2.md` - Phase 2 (DAPI V1 logic)
- `offchain.md` - PoC V2 off-chain artifacts proposal
- `manager-v6.md` - Phase 5 domain consolidation
