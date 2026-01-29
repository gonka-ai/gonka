# PoC V1 → V2 Migration Plan

## Motivation

### Why Migration is Needed

Today, `main` still represents the **PoC V1** flow. This branch contains the **PoC V2 off-chain** implementation and Phase 5 consolidation work.

**Intended migration flow (high level):**
1. Deploy the upgrade with `poc_v2_enabled=false` (network continues running PoC V1 behavior after the upgrade).
2. When the network is ready, submit a governance parameter update to set `poc_v2_enabled=true` and **everyone switches to PoC V2**.

After step (2), the steady-state behavior is **V2-only**. (V1 code may remain temporarily only as a rollback safety lever during the stabilization window, and can be removed later.)

We need a clean migration path that:

1. **Preserves backward compatibility** - Production can continue running V1 while V2 is tested
2. **Enables governance-controlled switch** - Network decides when to activate V2
3. **Maintains clear code separation** - V1 code isolated in `*_v1.go` files for easy removal later
4. **Supports rollback** - Can switch back to V1 if issues arise

### Status / Reality Check (Current Repo State)

This document is a **plan + checklist**. It must stay consistent with the actual codebase and endpoints.

Key facts (as of this branch):
- **PoC V2 off-chain stack is implemented** (commit messages, proof API, commit worker, validator, testermint coverage).
- **Phase 5 domain consolidation is effectively already done in code** (PoC domain lives in `decentralized-api/poc/`, artifacts under `decentralized-api/poc/artifacts/`, seed moved to `decentralized-api/internal/seed/`). The `manager-v6.md` document is still useful as the design record.
- **Runtime V1 is NOT currently wired end-to-end**:
  - Chain V1 msg handlers are currently deprecated stubs (they return `ErrDeprecated`).
  - DAPI “V1 callback endpoints” currently return `410 Gone` (deprecated handler).
  - There is no `poc_v2_enabled` param on-chain yet (it must be added to params).

### What Changed Between V1 and V2

| Aspect | V1 (main branch) | V2 (current branch) |
|--------|------------------|---------------------|
| **Artifact format** | `nonces[]` + `dist[]` (distance values) | `nonce` + `vector` (embedding bytes) |
| **Storage** | On-chain `PoCBatch` collection | Off-chain local files + MMR commits |
| **Chain messages** | `MsgSubmitPocBatch` | `MsgPoCV2StoreCommit`, `MsgMLNodeWeightDistribution` |
| **Validation messages** | `MsgSubmitPocValidation` | `MsgSubmitPocValidationsV2` |
| **Weight calculation** | Count unique nonces in batches | Use `commit.count` from StoreCommit |
| **DAPI↔MLNode callback paths** | `/v1/poc-batches/{generated,validated}` | `/v2/poc-batches/{generated,validated}` |
| **MLNode start** | “PoW v1” endpoints (generate/init/stop semantics) + stop-before-transition | “PoW v2” endpoints (`/api/v1/inference/pow/...`) + no stop for state transitions |
| **MLNode validate** | V1 uses an explicit init-validate flow (MLNode network call) | V2 uses broker state transition + `GenerateV2()` for validation requests (no `/init/validate` endpoint) |
| **Validation flow** | Query chain for batches | Query chain for commit, fetch proofs from participant API |

### Key Behavioral Differences

**MLNode Coordination (Critical)**:
- V1: MLNode MUST be stopped before some state transitions (historic behavior on `main`).
- V2: MLNode can continue running. No stop required for state transitions. A single `StopPowV2()` may be used before validation to ensure clean state.

**Artifact Submission**:
- V1: DAPI submits full `PoCBatch` (nonces + distances) to chain on every callback
- V2: DAPI stores artifacts locally, `CommitWorker` periodically commits MMR root + count to chain

**Validation**:
- V1: Validator queries chain for `PoCBatch`, samples nonces, sends to MLNode
- V2: Validator fetches proofs from participant's API, verifies MMR proofs, sends to MLNode

---

## Goal

Enable smooth switching between PoC V1 and V2 via governance parameter.

**Parameter**: Add `bool poc_v2_enabled` to `PocParams`
- `poc_v2_enabled = false`: V1 behavior (on-chain batches, MLNode stop required)
- `poc_v2_enabled = true`: V2 behavior (off-chain commits, no MLNode stop)

**Default (post-migration)**: `true` (V2 is the primary behavior going forward; V1 is rollback-only)

**Prerequisite**: Phase 5 (manager-v6.md) domain consolidation is complete.

---

## Design Principles

### Clean Dispatch

Every version check dispatches to a dedicated function. No function contains both V1 and V2 logic.

```go
// GOOD: Clean dispatch
func (b *Broker) getStartPoCCommand(...) NodeWorkerCommand {
    if b.isPoCv2Enabled() {
        return b.getStartPoCCommandV2(...)
    }
    return b.getStartPoCCommandV1(...)
}

// BAD: Mixed logic in same function
func (b *Broker) getStartPoCCommand(...) NodeWorkerCommand {
    if b.isPoCv2Enabled() {
        // 50 lines of V2 logic here
    } else {
        // 50 lines of V1 logic here
    }
}
```

### Runtime Switching Without Restart

V2-only components (CommitWorker, Proof API) always start but check `isPoCv2Enabled()` inside and skip operations when V1 is enabled. No dynamic lifecycle management needed. Governance can switch versions without node restart.

```go
// CommitWorker.tick() - always runs, checks version inside
func (w *CommitWorker) tick() {
    if !w.isPoCv2Enabled() {
        return  // V1 mode - skip commit
    }
    // V2 commit logic
}

// Proof API handler - always registered, checks version inside
func (s *Server) handlePocProofs(ctx echo.Context) error {
    if !s.isPoCv2Enabled() {
        return echo.NewHTTPError(http.StatusServiceUnavailable, "proof API requires V2")
    }
    // V2 proof logic
}
```

---

## Switch Points

### Chain Switch Points

| File | Function | Type | V1 | V2 |
|------|----------|------|----|----|
| `keeper/msg_server_poc_v1.go` | `SubmitPocBatch()` | Guard | `submitPocBatchV1()` | Reject |
| `keeper/msg_server_poc_validation_v1.go` | `SubmitPocValidation()` | Guard | `submitPocValidationV1()` | Reject |
| `keeper/msg_server_poc_v2_commit.go` | `PoCV2StoreCommit()` | Guard | Reject | Accept |
| `keeper/msg_server_poc_v2_commit.go` | `MLNodeWeightDistribution()` | Guard | Reject | Accept |
| `keeper/msg_server_poc_validations_v2.go` | `SubmitPocValidationsV2()` | Guard | Reject | Accept |
| `module/module.go` | `onEndOfPoCValidationStage()` | Dispatch | `ComputeNewWeightsV1()` | `ComputeNewWeights()` |
| `module/confirmation_poc.go` | `updateConfirmationWeights()` | Dispatch | `updateConfirmationWeightsV1()` | `updateConfirmationWeightsV2()` |

### DAPI Switch Points

| File | Function | Type | V1 | V2 |
|------|----------|------|----|----|
| `broker/broker.go` | `getCommandForState()` | Dispatch | `getStartPoCCommandV1()` | `getStartPoCCommandV2()` |
| `broker/broker.go` | `getCommandForState()` | Dispatch | `getValidateCommandV1()` | `getValidateCommandV2()` |
| `poc/orchestrator.go` | `ValidateReceivedArtifacts()` | Dispatch | `validatorV1.ValidateAll()` | `validatorV2.ValidateAll()` |
| `internal/server/mlnode/server.go` | PoC callback routes | Dispatch | accept `/v1/poc-batches/*` | accept `/v2/poc-batches/*` |
| `poc/commit_worker.go` | `tick()` | Check-inside | Skip | Execute |
| `internal/server/public/server.go` | `/v1/poc/proofs` | Check-inside | Return 503 | Execute |

---

## Version Flag Access

### Chain

```go
params := k.GetParams(ctx)
if params.PocParams.PocV2Enabled {
    // V2 path
}
```

### DAPI

```go
// poc/version.go - shared helper
package poc

func IsPoCv2Enabled(params *types.Params) bool {
    if params == nil || params.PocParams == nil {
        return true  // default V2
    }
    return params.PocParams.PocV2Enabled
}
```

Broker caches the flag, refreshed on block events. Components query via tracker or broker.

---

## Gaps / TODOs Required for Runtime V1 Support

This section is the “double-check” against what changed from `main`. If any item below stays undone, a governance flip to V1 will not work at runtime.

### Chain
- Restore full V1 logic from `main` behind a `poc_v2_enabled=false` guard:
  - `SubmitPocBatch` (includes participant blocklist gating, NodeId checks, confirmation PoC routing, PoC window validation, storing `PoCBatch`).
  - `SubmitPocValidation` (V1 validation submission logic from `main`).
- Add `poc_v2_enabled` to `PocParams` in `inference-chain/proto/inference/inference/params.proto` and regenerate Go.
- Add guards on V2 handlers so they reject when `poc_v2_enabled=false` (`PoCV2StoreCommit`, `MLNodeWeightDistribution`, `SubmitPocValidationsV2`).
- Add module-level dispatch so weight calculation and confirmation-PoC weight updates use V1 vs V2 paths based on the flag.

### DAPI (runtime toggle)
- Implement the V1 callback handlers (currently `/v1/poc-batches/*` return `410 Gone`), so in V1 mode DAPI can accept generated/validated batches and submit V1 chain messages.
- Implement the V1 MLNode client methods using the **real** MLNode endpoints used by PoC V1 on `main`.
  - Note: current DAPI `Stop()` uses `/api/v1/stop` and `NodeState()` uses `/api/v1/state` (not `/api/v1/inference/node/*`).
  - V2 PoW endpoints are `/api/v1/inference/pow/...` and are already implemented in `mlnodeclient/poc_v2_requests.go`.
- Ensure broker/dispatcher/orchestrator dispatches consistently based on the flag (commands, commit worker, proof API).

### MLNode
- Ensure MLNode continues to expose the V1 PoW endpoints required for V1 runtime (as provided on `main`).
- Keep V2 endpoints `/api/v1/inference/pow/{init/generate,generate,status,stop}` available.
- Confirm that V2 does not require `/init/validate` (it uses `GenerateV2()` with `validation` payload).
---

## Current State Summary

### V1 Types (exist in codebase, handler disabled)

```protobuf
// PoCBatch - V1 artifact storage
message PoCBatch {
  string participant_address = 1;
  int64 poc_stage_start_block_height = 2;
  int64 received_at_block_height = 3;
  repeated int64 nonces = 4;      // V1: array of nonces
  repeated double dist = 5;       // V1: array of distances
  string batch_id = 6;
  string node_id = 7;
}

// MsgSubmitPocBatch - V1 submission (currently returns deprecated error)
message MsgSubmitPocBatch {
  string creator = 1;
  int64 poc_stage_start_block_height = 2;
  string batch_id = 3;
  repeated int64 nonces = 4;
  repeated double dist = 5;
  string node_id = 6;
}
```

### V2 Types (active in codebase)

```protobuf
// PoCArtifactV2 - V2 artifact format
message PoCArtifactV2 {
  int32 nonce = 1;    // V2: single nonce
  bytes vector = 2;   // V2: embedding vector
}

// MsgPoCV2StoreCommit - V2 off-chain commit
message MsgPoCV2StoreCommit {
  string creator = 1;
  int64 poc_stage_start_block_height = 2;
  uint32 count = 3;      // number of artifacts
  bytes root_hash = 4;   // MMR root
}
```

---

## File Organization

### Principle: V1 in `*_v1.go` files

All V1 backward-compatibility code goes in dedicated files with `_v1` suffix. Main files contain V2 logic (the default going forward).

### Chain (`inference-chain/x/inference/`)

```
keeper/
├── msg_server_poc_v1.go           # V1: SubmitPocBatch handler (restore from main)
├── msg_server_poc_v2_commit.go    # V2: PoCV2StoreCommit (add guard)
├── poc_batch.go                   # V1: PoCBatch storage (exists)
└── poc_v2.go                      # V2: StoreCommit storage (exists)

module/
├── module.go                      # Dispatch in onEndOfPoCValidationStage
├── chainvalidation.go             # V2: ComputeNewWeights (exists)
├── chainvalidation_v1.go          # V1: ComputeNewWeightsV1 (new)
├── confirmation_poc.go            # Dispatch in updateConfirmationWeights
└── confirmation_poc_v1.go         # V1: updateConfirmationWeightsV1 (new)
```

### DAPI (`decentralized-api/`)

```
broker/
├── broker.go                      # Dispatch: getCommandForState, isPoCv2Enabled
├── node_worker_commands.go        # V2: StartPoCNodeCommandV2 (exists)
└── node_worker_commands_v1.go     # V1: StartPoCNodeCommandV1 (new)

poc/
├── orchestrator.go                # Dispatch: ValidateReceivedArtifacts
├── validator.go                   # V2: OffChainValidator (exists)
├── validator_v1.go                # V1: OnChainValidator (new)
├── version.go                     # IsPoCv2Enabled helper (new)
├── commit_worker.go               # Always runs, check-inside (modify)
└── artifacts/                     # V2 (exists)

internal/server/mlnode/
├── server.go                      # Dispatch in handler
├── post_generated_artifacts_v2_handler.go  # V2 (exists)
└── post_generated_artifacts_v1_handler.go  # V1 (new)

internal/server/public/
└── poc_handler.go                 # Check-inside for proof API (modify)

cosmosclient/
├── cosmosclient.go                # Shared + V2 methods
└── cosmosclient_v1.go             # V1: SubmitPocBatch (new)

mlnodeclient/
├── poc_v2_requests.go             # V2 (exists)
├── poc_v1_requests.go             # V1: InitGenerate, InitValidate, Stop (new)
└── interface.go                   # Add V1 methods to interface
```

---

## Implementation Phases

The migration is split into four phases for isolated testing:

- **Phase 1**: Restore on-chain V1 logic + add `poc_v2_enabled` param (isolated `*_v1.go` files, no dispatch) ✅ **COMPLETE**
- **Phase 2**: Restore DAPI V1 logic (isolated `*_v1.go` files, no dispatch) ✅ **COMPLETE**
- **Phase 3**: Add version switches and guards, wire dispatch, integration tests ✅ **COMPLETE**
- **Phase 4**: Dual migration mode with `confirmation_poc_v2_enabled` and auto-switch logic ✅ **COMPLETE**

See detailed implementation notes in:
- `migration-phase1.md` - Phase 1 details
- `migration-phase2.md` - Phase 2 details
- `migration-phase3.md` - Phase 3 details
- `migration-dual.md` - Phase 4 details (dual migration mode + auto-switch)

---

### Phase 1: On-Chain V1 Logic (Isolated)

Restore V1 chain logic from `main` branch in isolated files. No dispatch wiring yet.

#### 1.1 Add Governance Parameter

**File**: `inference-chain/proto/inference/inference/params.proto`

```protobuf
message PocParams {
  option (gogoproto.equal) = true;
  int32 default_difficulty = 1;
  int32 validation_sample_size = 2;
  uint64 poc_data_pruning_epoch_threshold = 3;
  Decimal weight_scale_factor = 4;
  PoCModelParams model_params = 5 [deprecated = true];
  string model_id = 6;
  int64 seq_len = 7;
  bool poc_v2_enabled = 8;  // NEW: false = V1, true = V2
}
```

Run: `ignite generate proto-go`

#### 1.2 Create `keeper/msg_server_poc_v1.go`

```go
package keeper

// submitPocBatchV1 handles V1 batch submission (restored from main).
func (k msgServer) submitPocBatchV1(ctx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
    // Full V1 logic from main branch including:
    // - Participant blocklist check
    // - Confirmation PoC event handling
    // - Regular PoC window validation
    // - Store PoCBatch
}

// submitPocValidationV1 handles V1 validation submission (restored from main).
func (k msgServer) submitPocValidationV1(ctx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
    // Full V1 logic from main branch
}
```

#### 1.3 Create `module/chainvalidation_v1.go`

```go
package inference

// ComputeNewWeightsV1 computes weights from on-chain PoCBatch (V1 flow).
func (am AppModule) ComputeNewWeightsV1(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
    // Restore from main branch:
    // - Query PoCBatch via GetPoCBatchesByStage
    // - Query PoCValidation via GetPoCValidationByStage
    // - Calculate weight from unique nonces
    // - Return ActiveParticipants
}
```

#### 1.4 Create `module/confirmation_poc_v1.go`

```go
package inference

// updateConfirmationWeightsV1 calculates confirmation weights using V1 on-chain batches.
func (am AppModule) updateConfirmationWeightsV1(ctx context.Context, event *types.ConfirmationPoCEvent, currentValidatorWeights map[string]int64, weightScaleFactor mathsdk.LegacyDec) []*types.ActiveParticipant {
    // V1: Query PoCBatch and PoCValidation for confirmation PoC
}
```

#### 1.5 Phase 1 Tests

- `keeper/msg_server_poc_v1_test.go` - handler tests (blocklist, window, confirmation PoC)
- `module/chainvalidation_v1_test.go` - weight calculation tests (restore from main with PoCBatch types)
- `module/confirmation_poc_v1_test.go` - confirmation weight tests

---

### Phase 2: DAPI V1 Logic (Isolated)

Restore DAPI V1 logic from `main` branch in isolated files. No dispatch wiring yet.

#### 2.1 Create `mlnodeclient/poc_v1_requests.go`

```go
package mlnodeclient

func (c *Client) InitGenerateV1(ctx context.Context, dto InitGenerateDto) error {
    // V1 endpoint on MLNode (from main) - confirm exact path and payload
}

func (c *Client) InitValidateV1(ctx context.Context, dto InitValidateDto) error {
    // V1 endpoint on MLNode (from main) - confirm exact path and payload
}
```

#### 2.2 Create `broker/node_worker_commands_v1.go`

```go
package broker

type StartPoCNodeCommandV1 struct {
    BlockHeight int64
    BlockHash   string
    PubKey      string
    CallbackUrl string
    TotalNodes  int
    ModelParams *types.PoCModelParams
}

func (c StartPoCNodeCommandV1) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
    // V1: Check state, Stop() if needed, then InitGenerateV1()
}

type InitValidateNodeCommandV1 struct {
    BlockHeight int64
    BlockHash   string
    PubKey      string
    CallbackUrl string
    TotalNodes  int
    ModelParams *types.PoCModelParams
}

func (c InitValidateNodeCommandV1) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
    // V1: Makes network call to MLNode via InitValidateV1()
}
```

#### 2.3 Create `cosmosclient/cosmosclient_v1.go`

```go
package cosmosclient

func (c *InferenceCosmosClient) SubmitPocBatch(msg *inference.MsgSubmitPocBatch) error {
    return c.SendTransactionAsyncWithRetry(msg)
}

func (c *InferenceCosmosClient) SubmitPocValidation(msg *inference.MsgSubmitPocValidation) error {
    return c.SendTransactionAsyncWithRetry(msg)
}
```

#### 2.4 Create `internal/server/mlnode/post_generated_artifacts_v1_handler.go`

```go
package mlnode

func (s *Server) postGeneratedArtifactsV1(ctx echo.Context) error {
    // V1: Submit MsgSubmitPocBatch to chain
}
```

#### 2.5 Create `poc/validator_v1.go`

```go
package poc

type OnChainValidator struct {
    recorder     cosmosclient.CosmosMessageClient
    nodeBroker   *broker.Broker
    callbackUrl  string
    pubKey       string
}

func NewOnChainValidator(...) *OnChainValidator { ... }

func (v *OnChainValidator) ValidateAll(pocStageStartBlockHeight int64) {
    // V1: Query chain for PoCBatch, sample nonces, send to MLNode
}
```

#### 2.6 Phase 2 Tests

- `broker/node_worker_commands_v1_test.go` - V1 command tests
- `poc/validator_v1_test.go` - V1 validation tests
- `internal/server/mlnode/post_generated_artifacts_v1_handler_test.go` - V1 handler tests

---

### Phase 3: Switches, Guards, and Dispatch

Wire up version dispatch and add guards. This phase connects Phase 1 and Phase 2 code.

#### 3.1 Update Chain Handlers with Guards

**File**: `keeper/msg_server_submit_poc_batch.go`

```go
func (k msgServer) SubmitPocBatch(ctx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
    params := k.GetParams(ctx)
    if params.PocParams.PocV2Enabled {
        return nil, sdkerrors.Wrap(types.ErrNotSupported, "V1 disabled when poc_v2_enabled=true")
    }
    return k.submitPocBatchV1(ctx, msg)
}
```

**File**: `keeper/msg_server_poc_v2_commit.go`

```go
func (k msgServer) PoCV2StoreCommit(ctx context.Context, msg *types.MsgPoCV2StoreCommit) (*types.MsgPoCV2StoreCommitResponse, error) {
    params := k.GetParams(ctx)
    if !params.PocParams.PocV2Enabled {
        return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
    }
    return k.poCV2StoreCommitImpl(ctx, msg)
}
```

#### 3.2 Add Dispatch to `module/module.go`

```go
func (am AppModule) onEndOfPoCValidationStage(ctx context.Context) {
    params := am.keeper.GetParams(ctx)
    var activeParticipants []*types.ActiveParticipant
    
    if params.PocParams.PocV2Enabled {
        activeParticipants = am.ComputeNewWeights(ctx, *upcomingEpoch)
    } else {
        activeParticipants = am.ComputeNewWeightsV1(ctx, *upcomingEpoch)
    }
    // ... rest unchanged
}
```

#### 3.3 Add Dispatch to `confirmation_poc.go`

```go
func (am AppModule) updateConfirmationWeights(ctx context.Context, event *types.ConfirmationPoCEvent) error {
    params := am.keeper.GetParams(ctx)
    var confirmationParticipants []*types.ActiveParticipant
    
    if params.PocParams.PocV2Enabled {
        confirmationParticipants = am.updateConfirmationWeightsV2(ctx, event, ...)
    } else {
        confirmationParticipants = am.updateConfirmationWeightsV1(ctx, event, ...)
    }
    // ... rest unchanged
}
```

#### 3.4 Update `broker/broker.go` with Dispatch

```go
func (b *Broker) getCommandForState(nodeState *NodeState, pocGenParams *pocParams, pocGenErr error, totalNodes int) NodeWorkerCommand {
    switch nodeState.IntendedStatus {
    case types.HardwareNodeStatus_POC:
        switch nodeState.PocIntendedStatus {
        case PocStatusGenerating:
            if b.isPoCv2Enabled() {
                return b.getStartPoCCommandV2(pocGenParams, totalNodes)
            }
            return b.getStartPoCCommandV1(pocGenParams, totalNodes)
        case PocStatusValidating:
            if b.isPoCv2Enabled() {
                return b.getValidateCommandV2()
            }
            return b.getValidateCommandV1(pocGenParams, totalNodes)
        }
    // ...
    }
}
```

#### 3.5 Update `poc/orchestrator.go` with Dispatch

```go
type orchestratorImpl struct {
    validatorV1    *OnChainValidator
    validatorV2    *OffChainValidator
    isPoCv2Enabled func() bool
}

func (o *orchestratorImpl) ValidateReceivedArtifacts(pocStageStartBlockHeight int64) {
    if o.isPoCv2Enabled() {
        o.validatorV2.ValidateAll(pocStageStartBlockHeight)
        return
    }
    o.validatorV1.ValidateAll(pocStageStartBlockHeight)
}
```

#### 3.6 Update `poc/commit_worker.go` with Check-Inside

```go
func (w *CommitWorker) tick() {
    if !w.isPoCv2Enabled() {
        return  // V1 mode - no commits needed
    }
    // existing V2 commit logic
}
```

#### 3.7 Update `internal/server/public/poc_handler.go` with Check-Inside

```go
func (s *Server) handlePocProofs(ctx echo.Context) error {
    if !s.isPoCv2Enabled() {
        return echo.NewHTTPError(http.StatusServiceUnavailable, "proof API requires poc_v2_enabled=true")
    }
    // existing V2 proof logic
}
```

#### 3.8 Phase 3 Tests (Testermint E2E)

- V1 test: `poc_v2_enabled = false` - verify PoCBatch on chain
- V2 test: `poc_v2_enabled = true` - verify StoreCommit on chain
- Migration test: switch from V1 to V2 via governance

---

## Code to Extract from Main Branch

```bash
# Chain - weight calculation
git show main:inference-chain/x/inference/module/chainvalidation.go > /tmp/chainvalidation_main.go

# Chain - handler
git show main:inference-chain/x/inference/keeper/msg_server_submit_poc_batch.go
git show main:inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go

# DAPI - node commands
git show main:decentralized-api/broker/node_worker_commands.go > /tmp/node_commands_main.go

# DAPI - mlnode client
git show main:decentralized-api/mlnodeclient/client.go > /tmp/mlnodeclient_main.go
```

---

## Migration Sequence

1. **Phase 1**: Restore on-chain V1 logic + add `poc_v2_enabled` param (isolated, testable)
2. **Phase 2**: Restore DAPI V1 logic (isolated, testable)
3. **Phase 3**: Wire dispatch, add guards, testermint E2E tests
4. **Deploy**: Production uses V2 by default (`poc_v2_enabled = true`)
5. **Rollback** (if needed): Governance proposal to set `poc_v2_enabled = false`

---

## Testing Strategy

### Testermint Configuration

Add to `AppExport.kt`:

```kotlin
data class PocParams(
    val defaultDifficulty: Int,
    val validationSampleSize: Int,
    @SerializedName("poc_data_pruning_epoch_threshold")
    val pocDataPruningEpochThreshold: Long,
    @SerializedName("weight_scale_factor")
    val weightScaleFactor: Decimal? = null,
    @SerializedName("model_id")
    val modelId: String? = null,
    @SerializedName("seq_len")
    val seqLen: Long? = null,
    @SerializedName("poc_v2_enabled")
    val pocV2Enabled: Boolean? = null,  // NEW
)
```

### V1 Test (poc_v2_enabled = false)

```kotlin
@Test
fun `poc v1 - batch submission and weight calculation`() {
    val v1Config = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::pocParams] = spec<PocParams> {
                        this[PocParams::pocV2Enabled] = false
                    }
                }
            }
        })
    )
    val (cluster, genesis) = initCluster(reboot = true, config = v1Config)
    cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
    
    genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
    genesis.node.waitForNextBlock(3)
    
    val epochData = genesis.getEpochData()
    val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
    val participantAddress = genesis.node.getColdAddress()
    
    // Assert: PoCBatch on chain
    val batches = genesis.node.getPoCBatchesByStage(pocStartHeight)
    assertThat(batches).isNotEmpty()
    
    // Assert: No StoreCommit
    val commits = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
    assertThat(commits.found).isFalse()
}
```

### V2 Test (poc_v2_enabled = true)

```kotlin
@Test
fun `poc v2 - store commit and proofs`() {
    // Default config uses V2
    val (cluster, genesis) = initCluster(reboot = true)
    cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
    
    genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
    genesis.node.waitForNextBlock(3)
    
    val epochData = genesis.getEpochData()
    val pocStartHeight = epochData.latestEpoch.pocStartBlockHeight
    val participantAddress = genesis.node.getColdAddress()
    
    // Assert: StoreCommit on chain
    val commits = genesis.node.getPoCV2StoreCommit(pocStartHeight, participantAddress)
    assertThat(commits.found).isTrue()
    assertThat(commits.count).isGreaterThan(0)
    
    // Assert: Weight distribution on chain
    val distribution = genesis.node.getMLNodeWeightDistribution(pocStartHeight, participantAddress)
    assertThat(distribution.found).isTrue()
}
```

### Migration Test (V1 to V2 via governance, no restart)

```kotlin
@Test
fun `poc v1 to v2 migration via governance`() {
    // 1. Start with V1
    val v1Config = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::pocParams] = spec<PocParams> {
                        this[PocParams::pocV2Enabled] = false
                    }
                }
            }
        })
    )
    val (cluster, genesis) = initCluster(reboot = true, config = v1Config)
    cluster.allPairs.forEach { it.waitForMlNodesToLoad() }
    
    // 2. Run V1 PoC cycle
    genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
    genesis.node.waitForNextBlock(3)
    
    val epochData = genesis.getEpochData()
    val v1PocHeight = epochData.latestEpoch.pocStartBlockHeight
    
    // Verify V1: PoCBatch exists
    assertThat(genesis.node.getPoCBatchesByStage(v1PocHeight)).isNotEmpty()
    
    // 3. Switch to V2 via governance (no restart)
    val params = genesis.getParams()
    val v2Params = params.copy(
        pocParams = params.pocParams.copy(pocV2Enabled = true)
    )
    genesis.runProposal(cluster, UpdateParams(params = v2Params))
    
    // 4. Run V2 PoC cycle
    genesis.waitForStage(EpochStage.END_OF_POC, offset = 1)
    genesis.node.waitForNextBlock(3)
    
    val newEpochData = genesis.getEpochData()
    val v2PocHeight = newEpochData.latestEpoch.pocStartBlockHeight
    val participantAddress = genesis.node.getColdAddress()
    
    // Verify V2: StoreCommit exists
    val commits = genesis.node.getPoCV2StoreCommit(v2PocHeight, participantAddress)
    assertThat(commits.found).isTrue()
}
```

---

## Future Cleanup

Once V2 is stable and V1 is deprecated:
- Delete all `*_v1.go` files
- Remove V1 methods from interfaces
- Remove `poc_v2_enabled` parameter (or keep as always-true)
- Remove V1 proto messages

---

## Related Documents

- `offchain.md` - PoC V2 off-chain artifacts proposal
- `manager-v6.md` - Phase 5 domain consolidation plan (prerequisite)
- `offchain-phase1.md` through `offchain-phase4.md` - V2 implementation details
