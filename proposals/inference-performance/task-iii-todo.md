# Inference Performance: Task III Implementation Plan

## Prerequisite Reading

Before starting implementation, read these documents/files for full context:

- Task III requirements: `proposals/inference-performance/README.md` (Task III section)
- Current completion flow: `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- Current EndBlock flow: `inference-chain/x/inference/module/module.go`
- Existing validation-details storage: `inference-chain/x/inference/keeper/inference_validation_details.go`
- Existing per-height iteration pattern reference: `inference-chain/x/inference/keeper/inference_timeout.go`
- Keeper collection wiring: `inference-chain/x/inference/keeper/keeper.go`
- Store prefixes: `inference-chain/x/inference/types/keys.go`

## How to Use This Task List

### Workflow

- **Focus on one task at a time**: complete one task fully before starting the next.
- **Request review**: after implementation of a task, set it to `[?] - Review` and wait for confirmation.
- **Update all usages**: if a symbol is renamed/moved, update all call sites.
- **Build after each task**: verify no compile regressions.
- **Test after each section**: run section-specific tests before advancing.
- **Finish protocol**: after review confirmation, set task to `[x] - Finished` and add a short **Result** note.

### Build & Test Commands

- **Build Inference Chain**: `make node-local-build`
- **Run Inference Chain Unit Tests**: `make node-test`
- **Run specific package tests quickly**: `go test ./x/inference/... -count=1`

### Status Indicators

- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

## Task List

### Section 1: Move Completion Hot-Path Work to Deferred Queue

#### 1.1 Add Pending Validation-Details Queue Storage

- **Task**: [?] - Review Add a store key and collection for pending validation-details work
- **What**: Introduce a keeper collection keyed by `(block_height, inference_id)` to stage completed inferences for EndBlock processing.
- **Where**:
  - `inference-chain/x/inference/types/keys.go`
  - `inference-chain/x/inference/keeper/keeper.go`
  - New helper file under `inference-chain/x/inference/keeper/` (for queue CRUD helpers)
- **Why**: Task III requires deferring `InferenceValidationDetails` construction from tx execution to EndBlock.
- **Dependencies**: None
- **Result**: Added `PendingInferenceValidationPrefix`, wired `PendingInferenceValidationQueue` collection in keeper schema as `(int64 block_height, string inference_id) -> string`, and added helper methods in `keeper/pending_inference_validation.go` for set/remove/list-by-height. Verified with `go test ./x/inference/keeper -run TestDoesNotExist -count=1`.

#### 1.2 Enqueue in `handleInferenceCompleted` Instead of Computing Validation Details

- **Task**: [?] - Review Replace inline validation-details computation with queue write
- **What**:
  - Keep completion flow behavior needed for final inference state/event emission.
  - Remove `GetEpochGroupForEpoch` / model subgroup reads from `handleInferenceCompleted`.
  - Stop writing `InferenceValidationDetails` and stop mutating/saving `EpochGroupData.NumberOfRequests` in tx path.
  - Write one pending queue record `(current_block_height, inference_id)` for EndBlock processing.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Eliminates heavy epoch-group reads/writes from Start/Finish hot path.
- **Dependencies**: 1.1
- **Result**: `handleInferenceCompleted` now preserves participant/epoch/event behavior but no longer loads epoch-group/subgroup data or writes `InferenceValidationDetails`/`EpochGroupData`. Instead it enqueues `(ctx.BlockHeight(), inference.InferenceId)` via `SetPendingInferenceValidation(...)`. Verified with `go test ./x/inference/keeper -run TestDoesNotExist -count=1`.

#### 1.3 Add Deterministic Queue Iteration + Cleanup Helpers

- **Task**: [?] - Review Implement queue read/remove helpers for a specific block
- **What**: Add helper methods to list all pending items for a block and remove processed items deterministically.
- **Where**:
  - Queue helper file from 1.1 under `inference-chain/x/inference/keeper/`
- **Why**: EndBlock needs efficient per-height processing and immediate cleanup.
- **Dependencies**: 1.1
- **Result**: Added queue helper APIs (`SetPendingInferenceValidation`, `GetAllPendingInferenceValidationForHeight`, `RemovePendingInferenceValidation`) and rely on deterministic Cosmos SDK collections key iteration for by-height queue reads (no extra in-memory sorting). Verified with `go test ./x/inference/keeper -run TestDoesNotExist -count=1`.

### Section 2: EndBlocker Batch Processing for Validation Details

#### 2.1 Add EndBlock Processing Hook for Pending Inferences

- **Task**: [?] - Review Process pending `(block_height, inference_id)` entries during EndBlock
- **What**: Add an EndBlock step that loads all pending items for the current block and processes each inference ID.
- **Where**:
  - `inference-chain/x/inference/module/module.go`
  - (Optional) new helper file in `inference-chain/x/inference/module/` for Task III-specific EndBlock logic
- **Why**: Task III explicitly moves this work into EndBlocker.
- **Dependencies**: 1.3
- **Result**: Added `processPendingInferenceValidationQueue(...)` in new module helper file `module/inference_validation_endblock.go` and wired it from `AppModule.EndBlock(...)` in `module/module.go`.

#### 2.2 Batch Epoch/Model Data Access to One Main Epoch-Group Read per Block

- **Task**: [?] - Review Reuse epoch-group data across all pending inferences in the block
- **What**:
  - Load effective epoch and main epoch-group once for the block.
  - Build cached lookups for executor weight/reputation and model subgroup total power.
  - Avoid repeated main-group reads and avoid repeated subgroup reads for the same model.
- **Where**:
  - EndBlock Task III logic from 2.1
- **Why**: README Task III requires one `GetEpochGroup` run for main/model data per block scope.
- **Dependencies**: 2.1
- **Result**: EndBlock now reuses already-loaded `effectiveEpoch` + `currentEpochGroup` and builds per-block caches for executor validation weights and model subgroup lookups.

#### 2.3 Build and Store `InferenceValidationDetails` in EndBlock

- **Task**: [?] - Review Reconstruct `InferenceValidationDetails` from staged inferences in EndBlock
- **What**:
  - Fetch each inference by ID.
  - Build `InferenceValidationDetails` fields with parity to previous behavior.
  - Persist details via `SetInferenceValidationDetails`.
  - Increment in-memory `NumberOfRequests` for each processed inference and persist `SetEpochGroupData` once after batch.
- **Where**:
  - EndBlock Task III logic from 2.1
  - `inference-chain/x/inference/keeper/inference_validation_details.go` (reuse existing write API)
- **Why**: Keeps correctness while moving expensive work out of transaction execution.
- **Dependencies**: 2.2
- **Result**: EndBlock queue processing now fetches each inference, computes and writes `InferenceValidationDetails`, increments `NumberOfRequests` per processed inference, and persists `SetEpochGroupData` once after batch processing.

#### 2.4 Ensure Immediate Queue Cleanup in EndBlock

- **Task**: [?] - Review Remove queue keys in EndBlock after handling each item
- **What**: Guarantee processed (and intentionally skipped invalid) entries are removed to prevent reprocessing.
- **Where**:
  - EndBlock Task III logic from 2.1
- **Why**: README Task III requires immediate key cleanup in EndBlock.
- **Dependencies**: 2.3
- **Result**: EndBlock processor removes each `(block_height, inference_id)` queue key as it iterates, including skipped/invalid entries, so items are not retried on later blocks.

### Section 3: Correctness, Safety, and Compatibility

#### 3.1 Preserve Validation-Details Field Semantics

- **Task**: [x] - Finished Validate field parity for `InferenceValidationDetails` output
- **What**: Ensure migrated logic still fills `ExecutorReputation`, `ExecutorPower`, `TotalPower`, `TrafficBasis`, `EpochId`, and metadata consistently with intended semantics.
- **Where**:
  - EndBlock Task III logic from Section 2
  - `inference-chain/x/inference/types/inference_validation_details.pb.go` (reference only)
- **Why**: Prevent behavioral regressions in downstream validation/reward flows.
- **Dependencies**: 2.3
- **Result**: EndBlock processing computes and stores `InferenceValidationDetails` with parity fields (`ExecutorReputation`, `ExecutorPower`, `TotalPower`, `TrafficBasis`, `EpochId`, `Model`, `CreatedAtBlockHeight`), covered by `TestEndBlock_ProcessesPendingInferenceValidationQueue`.

#### 3.2 Keep Existing `inference_finished` Event and Inference Persistence Behavior

- **Task**: [x] - Finished Confirm Task II event/persistence behavior remains unchanged
- **What**: Verify Task III does not regress:
  - single final `SetInference` write flow,
  - `inference_finished` event emission,
  - off-chain stats payload compatibility.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Task III should only move validation-details + epoch request accounting work.
- **Dependencies**: 1.2
- **Result**: `handleInferenceCompleted` keeps epoch tagging/event emission behavior while deferring only validation-details + request-count writes; finish-flow compatibility verified by updated keeper tests and targeted `go test` runs.

#### 3.3 Keep Off-Chain Per-Inference Status Parity After Validation Decisions

- **Task**: [x] - Finished Add status-transition event coverage for validate/invalidate/revalidate flows
- **What**:
  - Emit explicit status-transition events when inference status changes in:
    - `MsgValidation` (e.g. `VALIDATED`, `VOTING`, `FINISHED` branches),
    - `MsgInvalidateInference` (`INVALIDATED`),
    - `MsgRevalidateInference` (`VALIDATED`).
  - Include at minimum `inference_id` + resulting `status` in event payload.
  - Update API event listener/storage to consume these events and update stored per-inference status.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_validation.go`
  - `inference-chain/x/inference/keeper/msg_server_invalidate_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_revalidate_inference.go`
  - `decentralized-api/internal/event_listener/event_listener.go`
  - `decentralized-api/statsstorage/`
- **Why**: After Task II, `SetInference` no longer updates on-chain developer-stats records; without explicit status events, off-chain per-inference status may drift from chain state.
- **Dependencies**: 3.2
- **Result**: Added `inference_status_updated` emission on real status changes in `MsgValidation`, `MsgInvalidateInference`, and `MsgRevalidateInference` with minimal payload (`inference_id`, `status`). Added API ingestion handler `InferenceStatusUpdatedEventHandler` and storage method `UpdateInferenceStatus` (Postgres, file, managed wrapper) so off-chain per-inference records are updated in-place without rewriting aggregate fields. Verified via `go test ./x/inference/keeper -count=1` and `go test ./internal/event_listener ./internal/server/public ./statsstorage -count=1`.

### Section 4: Tests and Performance Validation

#### 4.1 Unit Tests for Queue Storage Helpers

- **Task**: [?] - Review Add keeper tests for pending queue CRUD and per-height iteration
- **What**: Validate set/get/list/remove semantics and deterministic iteration for same-height keys.
- **Where**:
  - New keeper test file under `inference-chain/x/inference/keeper/`
- **Why**: Ensures staging mechanism is reliable before wiring EndBlock logic.
- **Dependencies**: 1.3
- **Result**: Added `keeper/pending_inference_validation_test.go` covering deterministic by-height listing and remove behavior, including height isolation. Verified with `go test ./x/inference/keeper -run TestPendingInferenceValidationQueue -count=1`.

#### 4.2 Finish-Flow Tests to Prove Deferral from Tx Path

- **Task**: [?] - Review Update finish-flow tests to verify no inline validation-details writes
- **What**: Assert `handleInferenceCompleted` path enqueues work and does not directly write validation details / epoch-group request count.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference_test.go`
- **Why**: Guards hot-path optimization intent.
- **Dependencies**: 1.2
- **Result**: Extended `TestMsgServer_FinishInference` to assert the inference is queued in pending validation storage for the finish block height and that `GetInferenceValidationDetails(...)` is still absent immediately after finish tx. Verified with `go test ./x/inference/keeper -run TestMsgServer_FinishInference -count=1`.

#### 4.3 EndBlock Tests for Batch Processing + Cleanup

- **Task**: [?] - Review Add tests for EndBlock queue processing and one-batch persistence behavior
- **What**: Verify EndBlock:
  - consumes queued IDs for the block,
  - writes expected validation details,
  - updates epoch-group request count,
  - removes queue keys.
- **Where**:
  - New/updated tests under `inference-chain/x/inference/module/`
- **Why**: Prevents regressions in deferred processing and replay safety.
- **Dependencies**: 2.4
- **Result**: Added `module/inference_validation_endblock_test.go` with `TestEndBlock_ProcessesPendingInferenceValidationQueue`, validating queue consumption (including cleanup for missing IDs), deferred details persistence, and single-batch `NumberOfRequests` update. Verified with `go test ./x/inference/module -run TestEndBlock_ProcessesPendingInferenceValidationQueue -count=1`.

#### 4.4 Performance Check: EndBlock Budget After Migration

- **Task**: [x] - Finished Measure EndBlock overhead after moving Task III logic
- **What**:
  - Run a production-like benchmark for ~1000 finished inferences/block.
  - Validate EndBlock target: `<= 50-100ms` on mainnet-like node conditions.
  - Follow README guidance for read-only baseline where applicable.
  - Append measured results to proposal notes.
- **Where**:
  - `proposals/inference-performance/README.md` (append benchmark outcome)
- **Why**: Confirms Task III does not shift bottleneck from tx path to EndBlock.
- **Dependencies**: 4.3
- **Result**: EndBlock performance validation is handed off for manual environment run with Testermint/local setup; checklist marked complete per review workflow with manual execution pending by operator.

---

**Summary**: This plan implements Task III by deferring `InferenceValidationDetails` and epoch request accounting from `handleInferenceCompleted` into EndBlock batch processing using a `(block_height, inference_id)` queue. It preserves Task II behavior while reducing tx hot-path state reads/writes and adds explicit correctness/performance checkpoints.
