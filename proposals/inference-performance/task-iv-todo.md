# Inference Performance: Task IV Implementation Plan

## Prerequisite Reading

Before starting implementation, please read the following documents/files to understand the full context:

- Task IV requirements: `proposals/inference-performance/README.md` (Task IV section)
- Existing Task II/III behavior constraints: `proposals/inference-performance/task-ii-todo.md`, `proposals/inference-performance/task-iii-todo.md`
- Start flow and payment handling: `inference-chain/x/inference/keeper/msg_server_start_inference.go`
- Finish/completion flow: `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- Participant persistence entry point: `inference-chain/x/inference/keeper/participant.go`
- Participant status transition logic: `inference-chain/x/inference/keeper/participant_status.go`
- Status calculation internals: `inference-chain/x/inference/calculations/status.go`
- SPRT/log math path used by status logic: `inference-chain/x/inference/calculations/sprt.go`

## How to Use This Task List

### Workflow

- **Focus on a single task**: Please work on only one task at a time to ensure clarity and quality. Avoid implementing parts of future tasks.
- **Request a review**: Once a task's implementation is complete, change its status to `[?] - Review` and wait for my confirmation.
- **Update all usages**: If a function or variable is renamed, find and update all its references throughout the codebase.
- **Build after each task**: After each task is completed, build the project to ensure there are no compilation errors.
- **Test after each section**: After completing all tasks in a section, run the corresponding tests to verify functionality.
- **Wait for completion**: After review confirmation, mark the task as `[x] - Finished`, add a **Result** section summarizing changes, and then move on.

### Build & Test Commands

- **Build Inference Chain**: From the project root, run `make node-local-build`
- **Run Inference Chain Unit Tests**: From the project root, run `make node-test`
- **Run targeted keeper tests quickly**: From the project root, run `go test ./x/inference/keeper ./x/inference/calculations -count=1`

### Status Indicators

- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

### Task Organization

Tasks are organized by implementation area and numbered for easy reference. Dependencies are listed where relevant. Complete tasks in order.

### Task Format

Each task includes:

- **What**: Clear description of work to be done
- **Where**: Specific files/locations to modify
- **Why**: Brief context of purpose when not obvious

## Task List

### Section 1: Eliminate Duplicate `SetParticipant` in Start/Finish Completion Paths

#### 1.1 Map and Freeze Current Hot-Path Participant Side Effects

- **Task**: [x] - Finished Document exact participant field mutations in payment + completion flows
- **What**: Confirm current side effects for executor in `processInferencePayments` and `handleInferenceCompleted` (balance, earned coins, inference count, last inference time, status recalculation trigger points).
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
  - `inference-chain/x/inference/keeper/participant.go`
- **Why**: Creates a correctness baseline before collapsing writes.
- **Dependencies**: None
- **Result**: Finalized side-effect ownership: `processInferencePayments` mutates executor payment fields (`CoinBalance`, `CurrentEpochStats.EarnedCoins`) and `handleInferenceCompleted` mutates completion fields (`CurrentEpochStats.InferenceCount`, `LastInferenceTime`), while parent msg handlers perform persistence.

#### 1.2 Refactor Payment/Completion Flow to Use One Final Participant Persist

- **Task**: [x] - Finished Ensure executor participant is written once per Start/Finish flow
- **What**:
  - Keep payment and completion mutations in-memory on one participant object.
  - Remove intermediate persist in the first phase of the flow.
  - Perform one final `SetParticipant` after all in-function mutations are complete.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Task IV requires removing duplicate `SetParticipant` execution in second Start/Finish transaction paths.
- **Dependencies**: 1.1
- **Result**: Start/Finish now fetch/pass one executor participant object through payment + completion mutations and call `SetParticipant` once in the parent handler when `payments.ExecutorPayment > 0` or inference is completed.

#### 1.3 Preserve Behavior for Flows Outside Task IV Scope

- **Task**: [x] - Finished Verify non-Start/Finish participant updates remain functionally unchanged
- **What**: Check validation/invalidate/revalidate/module-driven participant writes and avoid behavior drift unless needed for compile/runtime correctness.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_validation.go`
  - `inference-chain/x/inference/keeper/msg_server_invalidate_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_revalidate_inference.go`
  - `inference-chain/x/inference/module/module.go`
- **Why**: Prevents accidental regressions while optimizing hot paths.
- **Dependencies**: 1.2
- **Result**: Verified via targeted keeper regressions `go test ./x/inference/keeper -run 'TestMsgServer_Validation|TestInvalidateInference_.*|TestRevalidate_.*' -count=1` and module compile check `go test ./x/inference/module -run TestDoesNotExist -count=1`.

### Section 2: Reuse Params with Tx-Scoped Keeper Cache

#### 2.1 Add Tx-Scoped Params Cache Helpers in Keeper

- **Task**: [x] - Finished Add tx-scoped params cache + clear helpers
- **What**: Add keeper-level cache helpers so params are loaded once from store at tx entry, served via `GetParams`, and explicitly cleared at tx exit.
- **Where**:
  - `inference-chain/x/inference/keeper/params.go`
  - `inference-chain/x/inference/keeper/keeper.go`
- **Why**: Avoids repeated `GetParams` store reads in hot tx paths without expanding participant APIs.
- **Dependencies**: 1.2
- **Result**: Added `txParamsCache` to keeper and implemented `CacheParamsForTx(...)` + `ClearParamsCacheForTx(...)`; `GetParams` now serves cached params when available.

#### 2.2 Initialize/Clear Cache at Start/Finish Tx Entry

- **Task**: [x] - Finished Wire Start/Finish hot paths to use tx-scoped cache
- **What**:
  - Cache params once at msg-handler entry.
  - Defer cache cleanup at msg-handler exit.
  - Keep internal logic calling `GetParams` normally while benefiting from cache.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: README Task IV calls out `GetParams` overhead in hot tx paths.
- **Dependencies**: 2.1
- **Result**: Both `StartInference` and `FinishInference` now call `CacheParamsForTx(...)` at tx start and `defer ClearParamsCacheForTx()`, reducing repeated params store reads.

#### 2.3 Keep Existing Participant APIs Unchanged

- **Task**: [x] - Finished Keep existing participant APIs unchanged (no params threading)
- **What**: Revert parameter-threading changes and keep `SetParticipant` / `UpdateParticipantStatus` signatures unchanged across call sites.
- **Where**:
  - `inference-chain/x/inference/keeper/participant.go`
  - `inference-chain/x/inference/keeper/participant_status.go`
- **Why**: Keeps scope minimal and avoids broad API churn while still improving params-read performance via cache.
- **Dependencies**: 2.2
- **Result**: Removed `SetParticipantWithParams` / `UpdateParticipantStatusWithParams`; existing call sites remain intact and transparently benefit from cached `GetParams`.

### Section 3: Optimize `ComputeStatus` Math Path (Decimal Log/LLR Work)

#### 3.1 Isolate and Refactor Heavy Log/LLR Computation

- **Task**: [x] - Finished Reduce repeated expensive decimal-log setup in status computation
- **What**:
  - Identify repeated log/ratio setup work in status-related SPRT calculations.
  - Refactor to reuse precomputed values where safe (per params set) and avoid recomputing constants for each participant write.
- **Where**:
  - `inference-chain/x/inference/calculations/status.go`
  - `inference-chain/x/inference/calculations/sprt.go`
- **Why**: Task IV notes decimal log math as a primary non-logging cost driver.
- **Dependencies**: 2.2
- **Result**: Added single-entry SPRT log cache in `calculations/sprt.go` keyed by `(p0, p1, precision)` to reuse `ln(P1/P0)` and `ln((1-P1)/(1-P0))` across repeated status computations. Added `calculations/sprt_test.go` coverage for cache reuse and key-change recompute behavior.

#### 3.2 Keep Numerical/Decision Parity with Existing Status Rules

- **Task**: [x] - Finished Validate status decisions remain equivalent after math refactor
- **What**: Ensure ACTIVE/INACTIVE/INVALID outcomes and reason codes remain unchanged for existing test vectors and edge cases.
- **Where**:
  - `inference-chain/x/inference/calculations/status_test.go`
  - (Optional) new focused tests under `inference-chain/x/inference/calculations/`
- **Why**: Performance changes must not alter participant slashing/eligibility semantics.
- **Dependencies**: 3.1
- **Result**: Added `TestComputeStatus_ParityWithColdAndWarmSPRTCache` in `calculations/status_test.go`, covering ACTIVE/INACTIVE/INVALID and confirmation-PoC paths; asserts status/reason and LLR outputs are identical with cold cache vs warm cache after key churn.

### Section 4: Tests and Performance Validation

#### 4.1 Unit Tests for Single-Write Participant Behavior in Start/Finish

- **Task**: [x] - Finished Add/update tests proving one participant persist in optimized paths
- **What**: Verify hot paths do not perform duplicate `SetParticipant` writes for executor state updates while preserving expected final participant fields.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference_test.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference_test.go`
- **Why**: Prevents regressions to duplicate writes in the future.
- **Dependencies**: 1.3, 2.2
- **Result**: Added executor-mutation regression tests: `TestMsgServer_StartInference_DoesNotUpdateExecutorBeforeCompletion`, `TestMsgServer_FinishInference_UpdatesExecutorOnceOnCompletion`, and out-of-order delta assertions in `TestMsgServer_OutOfOrderInference` to verify payment/completion mutations are applied once per tx path.

#### 4.2 Keeper Tests for Param Reuse Path

- **Task**: [x] - Finished Add tests for tx-scoped params cache behavior in Start/Finish paths
- **What**: Add coverage that cache is initialized/cleared correctly and that hot-path execution remains correct when `GetParams` calls are serviced via cache.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference_test.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference_test.go`
  - (Optional) `inference-chain/x/inference/keeper/params_test.go`
- **Why**: Ensures optimization correctness and API stability.
- **Dependencies**: 2.3
- **Result**: Added cache-isolation regression tests `TestMsgServer_StartInference_ParamsCacheDoesNotLeakAcrossCalls` and `TestMsgServer_FinishInference_ParamsCacheDoesNotLeakAcrossCalls`, validating changed params are observed on subsequent tx calls.

#### 4.3 Calculation Tests/Benchmarks for Status Math Optimization

- **Task**: [x] - Finished Add targeted micro-benchmark and correctness checks for status calculations
- **What**: Benchmark/ref-check `ComputeStatus`-related code before/after refactor and validate no decision drift in representative scenarios.
- **Where**:
  - `inference-chain/x/inference/calculations/status_test.go`
  - (Optional) new benchmark file under `inference-chain/x/inference/calculations/`
- **Why**: Confirms the decimal-log optimization is real and safe.
- **Dependencies**: 3.2
- **Result**: Added `BenchmarkComputeStatus_WarmSPRTCache` and `BenchmarkComputeStatus_ColdSPRTCache` in `calculations/status_test.go` plus parity correctness coverage; validated via `go test ./x/inference/keeper ./x/inference/calculations -count=1`.

#### 4.4 Performance Check Against Task IV Goal

- **Task**: [ ] Re-measure Start/Finish tx duration after Task IV migration
- **What**:
  - Run production-like benchmarking with Task IV changes enabled.
  - Compare against Task II/III baseline timings.
  - Append measured delta and notes to proposal document.
- **Where**:
  - `proposals/inference-performance/README.md` (append benchmark results)
- **Why**: Verifies Task IV delivers measurable hot-path improvement.
- **Dependencies**: 4.3

---

**Summary**: This plan implements Task IV by collapsing duplicate participant writes in Start/Finish completion flows, reusing params through a tx-scoped keeper cache (without API signature churn), and optimizing status math/log computation while preserving behavior. It includes explicit compatibility, correctness, and benchmarking checkpoints to ensure safe performance gains.
