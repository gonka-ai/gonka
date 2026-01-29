# PoC Domain Isolation (v6)

## Status

Phase 4 complete. Ready for implementation as part of Phase 5.

## Executive Summary

Consolidates all Proof of Compute (PoC) logic into an isolated `poc/` domain package with clear boundaries.

**What's already implemented:**
- `CommitWorker` in `internal/pocv2/commit_worker.go` (time-based commits, 5s interval)
- `OffChainValidator` in `internal/pocv2/offchain_validator.go`
- On-chain batch submission removed from handler
- Dispatcher no longer owns flush/distribution logic

**What remains:**
- Consolidate phase predicates (currently duplicated in 4 files)
- Move packages to `poc/` domain
- Move seed logic from `internal/poc/` to `internal/seed/`
- Simplify `CommitWorker` to take `participantAddress` directly
- Simplify orchestrator (remove dead `OrchestratorChainBridgeV2`, batch types)
- Remove dead code (batch types, V1 handlers)
- Chain cleanup (remove `PoCBatchesV2` collection)

**Target package structure:**

```
decentralized-api/poc/
├── phase.go
├── phase_test.go
├── commit_worker.go
├── commit_worker_test.go
├── validator.go
├── validator_test.go
├── proof_client.go
├── proof_client_test.go
├── orchestrator.go
└── artifacts/
    ├── store.go
    ├── managed_store.go
    ├── mmr.go
    └── *_test.go
```

---

## Motivation

### Current Problems

PoC logic is scattered across 6+ packages:

```
Current Structure:
==================
broker/
├── state_commands.go      → Phase checks (duplicated)
├── phase_helpers.go       → IsInPoCGeneratePhase(), IsInPoCValidatePhase()

internal/pocv2/
├── commit_worker.go       → shouldAcceptStoreCommit(), shouldHaveDistributedWeights()
├── offchain_validator.go  → OffChainValidator
├── proof_client.go        → ProofClient
└── node_orchestrator.go   → Dead batch types, ValidateReceivedArtifacts()

internal/poc/
└── random_seed.go         → RandomSeedManager (NOT PoC v2 - reward/claims seed logic)

pocartifacts/
├── managed_store.go       → ManagedArtifactStore
├── store.go               → ArtifactStore
└── mmr.go                 → MMR implementation

internal/server/mlnode/
├── post_generated_artifacts_v2_handler.go → Uses broker.IsInPoCGeneratePhase()
└── post_generated_batches_handler.go      → Dead V1 handlers (return 410)
```

Note: `internal/poc/` is misleadingly named. It contains reward/claims seed logic, not PoC v2. It will be moved to `internal/seed/` to avoid confusion with the new `poc/` domain.

**Issue 1: Duplicated phase logic**

Same checks exist in 4 locations:
- `broker/phase_helpers.go`
- `broker/state_commands.go` (inline)
- `internal/pocv2/commit_worker.go`
- Handler uses broker method

Note: `state_commands.go` has additional height-based window checks (e.g., `event.GetGenerationEnd()`, `event.IsInValidationWindow()`). These are intentionally different from `poc/phase.go` predicates - commands need finer-grained timing control for node state transitions.

**Issue 2: Dead code**

After Phase 4, these are unused:
- `node_orchestrator.go`: batch types, `PoCv2BatchesForStage()`, `collectUniqueArtifacts()`, `sampleArtifactsV2()`
- `node_orchestrator.go`: `OrchestratorChainBridgeV2` interface (only used for batch queries, now dead)
- `post_generated_batches_handler.go`: entire file
- `cosmosclient.go`: `SubmitPocBatchesV2` method
- Chain: `PoCBatchesV2` collection, `MsgSubmitPocBatchesV2` handler

**Issue 3: Unnecessary abstraction**

`NodePoCOrchestratorV2` is now a thin wrapper that just delegates to `OffChainValidator.ValidateAll()`. The orchestrator pattern added value when it managed batch collection and sampling, but after Phase 4 it only forwards calls.

### Goals

1. **Domain isolation**: PoC as a first-class package
2. **Single source of truth**: Phase predicates in one place
3. **Remove dead code**: Clean up batch-related code
4. **Clear ownership**: Each file owns one concern
5. **Testability**: Pure functions for predicates
6. **Simplify orchestrator**: Remove unnecessary abstraction layer

---

## Detailed Design

### 1. `poc/phase.go`

Pure functions with explicit intent. No struct, no goroutines, no side effects.

```go
package poc

import (
    "decentralized-api/chainphase"
    "github.com/productscience/inference/x/inference/types"
)

// ShouldAcceptGeneratedArtifacts returns true if the system should accept
// incoming artifact batches from MLNodes.
func ShouldAcceptGeneratedArtifacts(epochState *chainphase.EpochState) bool {
    if epochState.IsNilOrNotSynced() {
        return false
    }
    if epochState.CurrentPhase == types.PoCGeneratePhase {
        return true
    }
    if epochState.CurrentPhase == types.InferencePhase &&
        epochState.ActiveConfirmationPoCEvent != nil &&
        epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
        return true
    }
    return false
}

// ShouldAcceptValidatedArtifacts returns true if the system should accept
// incoming validation results from MLNodes.
func ShouldAcceptValidatedArtifacts(epochState *chainphase.EpochState) bool {
    if epochState.IsNilOrNotSynced() {
        return false
    }
    if epochState.CurrentPhase == types.PoCValidatePhase ||
        epochState.CurrentPhase == types.PoCValidateWindDownPhase {
        return true
    }
    if epochState.CurrentPhase == types.InferencePhase &&
        epochState.ActiveConfirmationPoCEvent != nil &&
        epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
        return true
    }
    return false
}

// ShouldHaveDistributedWeights returns true if weights should have been distributed.
func ShouldHaveDistributedWeights(epochState *chainphase.EpochState) bool {
    if epochState.IsNilOrNotSynced() {
        return false
    }
    if epochState.CurrentPhase == types.PoCValidatePhase || 
       epochState.CurrentPhase == types.PoCValidateWindDownPhase ||
       epochState.CurrentPhase == types.PoCGenerateWindDownPhase {
        return true
    }
    if epochState.CurrentPhase == types.InferencePhase &&
        epochState.ActiveConfirmationPoCEvent != nil &&
        epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
        return true
    }
    return false
}

// GetCurrentPocStageHeight returns the PoC stage start height.
func GetCurrentPocStageHeight(epochState *chainphase.EpochState) int64 {
    if epochState.IsNilOrNotSynced() {
        return 0
    }
    if epochState.ActiveConfirmationPoCEvent != nil &&
        epochState.CurrentPhase == types.InferencePhase {
        return epochState.ActiveConfirmationPoCEvent.TriggerHeight
    }
    return epochState.LatestEpoch.PocStartBlockHeight
}

// ShouldAcceptStoreCommit returns true if chain will accept MsgPoCV2StoreCommit.
func ShouldAcceptStoreCommit(epochState *chainphase.EpochState, pocStageStartHeight int64) bool {
    // ... (same logic as current commit_worker.go)
}
```

### 2. `poc/artifacts/` (moved from `pocartifacts/`)

Move existing files with package rename:

| Current | New |
|---------|-----|
| `pocartifacts/store.go` | `poc/artifacts/store.go` |
| `pocartifacts/managed_store.go` | `poc/artifacts/managed_store.go` |
| `pocartifacts/mmr.go` | `poc/artifacts/mmr.go` |
| `pocartifacts/*_test.go` | `poc/artifacts/*_test.go` |

### 3. Move `internal/pocv2/` to `poc/`

| Current | New |
|---------|-----|
| `internal/pocv2/commit_worker.go` | `poc/commit_worker.go` |
| `internal/pocv2/offchain_validator.go` | `poc/validator.go` |
| `internal/pocv2/proof_client.go` | `poc/proof_client.go` |
| `internal/pocv2/node_orchestrator.go` | Remove most code, keep minimal `poc/orchestrator.go` |

### 3.1 Orchestrator Simplification

The current orchestrator is a thin wrapper after Phase 4. Simplify to:

```go
// poc/orchestrator.go
package poc

// Orchestrator coordinates PoC validation.
// After Phase 4 (off-chain migration), this is a minimal wrapper around OffChainValidator.
type Orchestrator struct {
    validator *OffChainValidator
}

func NewOrchestrator(validator *OffChainValidator) *Orchestrator {
    return &Orchestrator{validator: validator}
}

func (o *Orchestrator) ValidateReceivedArtifacts(pocStageStartBlockHeight int64) {
    o.validator.ValidateAll(pocStageStartBlockHeight)
}
```

**Removed from orchestrator:**
- `OrchestratorChainBridgeV2` interface (dead - only had batch query methods)
- `OrchestratorChainBridgeV2Impl` struct and methods
- `PoCBatchesV2Response`, `PoCBatchesV2ForParticipant`, `PoCBatchV2`, `ArtifactV2` types
- `collectUniqueArtifacts()`, `sampleArtifactsV2()` functions
- `filterNodesForV2Validation()`, `stopGenerationOnAllNodes()` (move to validator if still needed)

**Alternative:** Remove orchestrator entirely and have dispatcher call `validator.ValidateAll()` directly. Decision: Keep minimal orchestrator for future extensibility (e.g., multi-stage validation).

### 4. CommitWorker Simplification

Pass `participantAddress` directly instead of introducing an interface:

```go
// BEFORE (current)
func NewCommitWorker(
    store *pocartifacts.ManagedArtifactStore,
    recorder cosmosclient.CosmosMessageClient,
    tracker *chainphase.ChainPhaseTracker,
    broker *broker.Broker,  // only used for GetParticipantAddress()
    interval time.Duration,
) *CommitWorker

// AFTER (simpler)
func NewCommitWorker(
    store *artifacts.ManagedArtifactStore,
    recorder cosmosclient.CosmosMessageClient,
    tracker *chainphase.ChainPhaseTracker,
    participantAddress string,  // direct value
    interval time.Duration,
) *CommitWorker
```

The orchestrator and validator keep the broker dependency where actually needed (`GetNodes()`, `NewNodeClient()`).

### 5. Seed Domain Move

Move reward/claims seed logic out of the PoC domain:

| Current | New |
|---------|-----|
| `internal/poc/random_seed.go` | `internal/seed/seed.go` |

Package rename: `package poc` to `package seed`

**Call sites to update:**
- `internal/event_listener/new_block_dispatcher.go`
- `internal/startup/reward_recovery.go`
- `internal/server/admin/validation_recovery_handler.go`
- `internal/event_listener/integration_test.go` (mock interface)

**Import changes:**
```go
// BEFORE
import "decentralized-api/internal/poc"
poc.RandomSeedManager
poc.NewRandomSeedManager(...)
poc.CreateSeedForEpoch(...)

// AFTER
import "decentralized-api/internal/seed"
seed.RandomSeedManager
seed.NewRandomSeedManager(...)
seed.CreateSeedForEpoch(...)
```

---

## Dead Code to Remove

### DAPI

| Component | File | Action |
|-----------|------|--------|
| `OrchestratorChainBridgeV2` interface | `node_orchestrator.go` | Remove interface |
| `OrchestratorChainBridgeV2Impl` struct | `node_orchestrator.go` | Remove struct and methods |
| `PoCBatchesV2Response`, `PoCBatchesV2ForParticipant`, `PoCBatchV2`, `ArtifactV2` | `node_orchestrator.go` | Remove types |
| `collectUniqueArtifacts()`, `sampleArtifactsV2()` | `node_orchestrator.go` | Remove functions |
| `filterNodesForV2Validation()` | `node_orchestrator.go` | Move to validator or remove |
| `stopGenerationOnAllNodes()` | `node_orchestrator.go` | Move to validator or remove |
| `post_generated_batches_handler.go` | `internal/server/mlnode/` | Delete file |
| V1 routes | `server.go` | Remove `/v1/poc-batches/*` |
| `SubmitPocBatchesV2` | `cosmosclient.go` | Remove method |
| `SubmitPocBatchesV2` | `mock_cosmos_message_client.go` | Remove mock |
| `MsgSubmitPocBatchesV2` deadline | `tx_deadline_config.go` | Remove entry |

### Chain

| Component | File | Action |
|-----------|------|--------|
| `MsgSubmitPocBatchesV2` | `tx.proto` | Remove message |
| `SubmitPocBatchesV2` handler | `msg_server_submit_poc_v2.go` | Remove handler |
| `PoCBatchesV2` collection | `keeper.go` | Remove collection |
| `PoCBatchV2Prefix` | `keys.go` | Remove constant |
| `SetPocBatchV2`, `GetPoCBatchesV2ByStage` | `poc_v2.go` | Remove methods |
| `QueryPocV2BatchesForStage` | `query.proto`, `query_poc_v2.go` | Remove query |

---

## Consumer Updates

### `broker/state_commands.go`

**Keep existing inline logic.** State commands have height-based window checks that differ from `poc/phase.go`:

```go
// StartPocCommand checks generation window boundaries
if currentHeight >= event.GenerationStartHeight && currentHeight <= event.GetGenerationEnd(epochParams) {
    shouldRunPoC = true
}

// InitValidateCommand checks validation window boundaries
if currentHeight == event.GetExchangeEnd(epochParams) || event.IsInValidationWindow(currentHeight, epochParams) {
    shouldValidate = true
}
```

These fine-grained height checks are intentionally different from the simpler phase predicates in `poc/phase.go`. The predicates answer "are we in this phase?" while state commands answer "should we transition nodes now?"

### `broker/phase_helpers.go`

**Delete this file.** Replace broker methods with direct imports:

```go
// BEFORE
s.broker.IsInPoCGeneratePhase()

// AFTER
poc.ShouldAcceptGeneratedArtifacts(epochState)
```

### `internal/server/mlnode/post_generated_artifacts_v2_handler.go`

```go
import "decentralized-api/poc"

func (s *Server) postGeneratedArtifactsV2(ctx echo.Context) error {
    epochState := s.phaseTracker.GetCurrentEpochState()
    if !poc.ShouldAcceptGeneratedArtifacts(epochState) {
        return echo.NewHTTPError(http.StatusServiceUnavailable, "not in PoC generate phase")
    }
    // ... rest unchanged
}
```

---

## Files to Delete / Move

| File | Action |
|------|--------|
| `broker/phase_helpers.go` | Delete (replaced by `poc/phase.go`) |
| `internal/pocv2/` directory | Move to `poc/` |
| `pocartifacts/` directory | Move to `poc/artifacts/` |
| `internal/poc/` directory | Move to `internal/seed/` |
| `internal/server/mlnode/post_generated_batches_handler.go` | Delete (dead V1 handlers) |

---

## Benefits

| Before | After |
|--------|-------|
| Phase checks in 4 files | Single `poc/phase.go` |
| `internal/pocv2/` + `pocartifacts/` + `broker/` | Single `poc/` package with `artifacts/` subpackage |
| Dead batch code remains | Clean codebase |
| CommitWorker depends on Broker for address | CommitWorker receives `participantAddress` directly |
| `internal/poc/` misleadingly named | Seed logic in `internal/seed/` (clear separation) |
| Orchestrator has dead batch logic | Minimal orchestrator wrapping validator |
| `OrchestratorChainBridgeV2` interface unused | Removed entirely |

---

## Related Documents

- `offchain.md` - Main off-chain artifacts proposal
- `offchain-phase1.md` - Phase 1: Storage & MMR
- `offchain-phase2.md` - Phase 2: Proof API
- `offchain-phase3.md` - Phase 3: Chain messages
- `offchain-phase35.md` - Phase 3.5: CommitWorker
- `offchain-phase4.md` - Phase 4: Validation switchover
- `migration.md` - PoC v1/v2 smooth switch implementation (post-consolidation)