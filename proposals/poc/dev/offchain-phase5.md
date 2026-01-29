# Off-Chain PoC - Phase 5: Cleanup & Domain Consolidation

**Status: Complete**

## Overview

Phase 5 consolidates all PoC-related code into an isolated `poc/` domain package, removes dead code related to on-chain batch submissions, and simplifies the orchestrator. This improves code organization, maintainability, and prepares for future V1/V2 migration.

## Motivation

Before this phase:
- PoC logic was scattered across `internal/pocv2/`, `pocartifacts/`, `broker/`, and `internal/poc/`
- Phase predicates were duplicated between `broker/phase_helpers.go` and `commit_worker.go`
- Dead code remained from the old on-chain batch submission flow (`MsgSubmitPocBatchesV2`)
- The orchestrator had unnecessary complexity from the chain bridge pattern
- The `internal/poc/` package misleadingly contained only seed logic

## Goals

1. **Domain Isolation**: All PoC v2 logic in one `poc/` package
2. **Single Source of Truth**: Consolidated phase predicates in `poc/phase.go`
3. **Dead Code Removal**: Remove `MsgSubmitPocBatchesV2` and related code from DAPI and Chain
4. **Clear Ownership**: `poc/` owns artifact storage, validation, commits; `seed/` owns random seeds
5. **Testability**: Comprehensive tests for phase predicates
6. **Simplified Orchestrator**: Minimal orchestrator as a switch point for future V1/V2 migration

## Implementation

### DAPI Package Restructuring

#### New Package Structure

```
decentralized-api/
├── poc/
│   ├── artifacts/
│   │   ├── managed_store.go      # ManagedArtifactStore (from pocartifacts/)
│   │   ├── managed_store_test.go
│   │   ├── mmr.go                # MMR implementation
│   │   ├── store.go              # ArtifactStore
│   │   └── store_test.go
│   ├── commit_worker.go          # CommitWorker (from internal/pocv2/)
│   ├── commit_worker_test.go
│   ├── orchestrator.go           # Simplified orchestrator
│   ├── phase.go                  # Phase predicate functions (NEW)
│   ├── phase_test.go             # Comprehensive phase tests (NEW)
│   ├── proof_client.go           # ProofClient (from internal/pocv2/)
│   └── validator.go              # OffChainValidator (from internal/pocv2/)
├── internal/
│   └── seed/
│       └── seed.go               # RandomSeedManager (from internal/poc/)
```

#### Phase Predicates (`poc/phase.go`)

New centralized phase predicate functions:

```go
// ShouldAcceptGeneratedArtifacts - DAPI should accept incoming artifacts
func ShouldAcceptGeneratedArtifacts(epochState *chainphase.EpochState) bool

// ShouldAcceptValidatedArtifacts - DAPI should accept validation results
func ShouldAcceptValidatedArtifacts(epochState *chainphase.EpochState) bool

// ShouldSubmitStoreCommit - DAPI should submit MsgPoCV2StoreCommit
func ShouldSubmitStoreCommit(epochState *chainphase.EpochState, pocStageStartHeight int64) bool

// ShouldDistributeWeights - weights have been distributed
func ShouldDistributeWeights(epochState *chainphase.EpochState) bool

// GetPocStageHeight - returns correct PoC stage height for context
func GetPocStageHeight(epochState *chainphase.EpochState) int64
```

Each function handles both regular PoC and confirmation PoC scenarios.

#### Simplified Orchestrator (`poc/orchestrator.go`)

```go
// Orchestrator interface for PoC orchestration
// Kept as a potential switch point for V1/V2 migration (see migration.md)
type Orchestrator interface {
    ValidateReceivedArtifacts(pocStageStartBlockHeight int64)
}

// orchestratorImpl delegates to OffChainValidator
type orchestratorImpl struct {
    validator *OffChainValidator
}

func NewOrchestrator(...) Orchestrator
```

#### CommitWorker Simplification

The `CommitWorker` constructor now accepts `participantAddress` directly instead of relying on the broker:

```go
// Before
func NewCommitWorker(store, client, tracker, broker, interval)

// After  
func NewCommitWorker(store, client, tracker, participantAddress, interval)
```

### DAPI Dead Code Removal

Removed all references to `MsgSubmitPocBatchesV2`:

| Component | File | Change |
|-----------|------|--------|
| Interface method | `cosmosclient/cosmosclient.go` | Removed `SubmitPocBatchesV2` |
| Mock method | `cosmosclient/mock_cosmos_message_client.go` | Removed mock |
| Deadline config | `cosmosclient/tx_manager/tx_deadline_config.go` | Removed entry |
| Batch consumer | `cosmosclient/tx_manager/batch_consumer.go` | Removed `PublishPocBatchV2`, `pocV2Batch`, handlers |
| Consumer tests | `cosmosclient/tx_manager/batch_consumer_test.go` | Removed `TestBatchConsumer_PocV2Batching` |
| NATS stream | `internal/nats/server/server.go` | Removed `TxsBatchPocV2Stream` |
| Phase helpers | `broker/phase_helpers.go` | Deleted file (moved to `poc/phase.go`) |

### Chain Dead Code Removal

Removed `MsgSubmitPocBatchesV2` and related types from inference-chain:

#### Proto Files

| File | Removed |
|------|---------|
| `tx.proto` | `rpc SubmitPocBatchesV2`, `MsgSubmitPocBatchesV2`, `MsgSubmitPocBatchesV2Response` |
| `poc_v2.proto` | `PoCBatchV2`, `PoCBatchPayloadV2` |
| `query.proto` | `rpc PocV2BatchesForStage`, `QueryPocV2BatchesForStageRequest`, `QueryPocV2BatchesForStageResponse`, `PoCBatchesWithParticipantsV2` |

#### Keeper Files

| File | Change |
|------|--------|
| `msg_server_submit_poc_v2.go` | Renamed to `msg_server_poc_validations_v2.go`, removed `SubmitPocBatchesV2` handler |
| `poc_v2.go` | Removed `SetPocBatchV2`, `GetPoCBatchesV2ByStage` |
| `query_poc_v2.go` | Removed `PocV2BatchesForStage` query handler |
| `keeper.go` | Removed `PoCBatchesV2` collection declaration and initialization |
| `keys.go` | Removed `PoCBatchV2Prefix` constant |

#### App/Middleware Files

| File | Change |
|------|--------|
| `ante_poc_period.go` | Removed `MsgSubmitPocBatchesV2` case |
| `permissions.go` | Removed `MsgSubmitPocBatchesV2` from `AllAcceptedMessages` |

### Consumer Updates

Updated imports across DAPI to use new package paths:

| File | Old Import | New Import |
|------|------------|------------|
| `main.go` | `internal/pocv2` | `poc` |
| `main.go` | `pocartifacts` | `poc/artifacts` |
| `event_listener.go` | `internal/pocv2` | `poc` |
| `new_block_dispatcher.go` | `internal/poc`, `internal/pocv2` | `internal/seed`, `poc` |
| `reward_recovery.go` | `internal/poc` | `internal/seed` |
| `validation_recovery_handler.go` | `internal/poc` | `internal/seed` |
| `server.go` (public) | `pocartifacts` | `poc/artifacts` |
| `poc_handler.go` | `pocartifacts` | `poc/artifacts` |
| `server.go` (mlnode) | `pocartifacts` | `poc/artifacts` |
| `post_generated_artifacts_v2_handler.go` | `broker.IsInPoCGeneratePhase()` | `poc.ShouldAcceptGeneratedArtifacts()` |

### Broker Updates

Added `GetPhaseTracker()` method to expose the phase tracker for handlers:

```go
func (b *Broker) GetPhaseTracker() *chainphase.ChainPhaseTracker {
    return b.phaseTracker
}
```

## What Remains (Still In Use)

- `PoCArtifactV2` - used for local artifact storage format
- `MsgSubmitPocValidationsV2` - validators still submit validations on-chain
- `PoCValidationsV2` collection and related keeper methods
- `PocV2ValidationsForStage` query
- Off-chain commit messages: `MsgPoCV2StoreCommit`, `MsgMLNodeWeightDistribution`

## Testing

### Phase Predicate Tests (`poc/phase_test.go`)

Comprehensive tests covering:
- Regular PoC: all phases (generate, validate, wind-down, inference)
- Confirmation PoC: all phases
- Nil/not-synced states
- Exchange window boundaries

### Existing Tests

All existing tests updated and passing:
- `poc/commit_worker_test.go`
- `poc/artifacts/*_test.go`
- `cosmosclient/tx_manager/batch_consumer_test.go`
- `internal/event_listener/integration_test.go`
- `inference-chain/x/inference/keeper/*_test.go`

## Files Changed Summary

### DAPI - New Files
- `poc/phase.go`
- `poc/phase_test.go`
- `poc/orchestrator.go`
- `internal/seed/seed.go`

### DAPI - Moved Files
| From | To |
|------|-----|
| `pocartifacts/*.go` | `poc/artifacts/*.go` |
| `internal/pocv2/commit_worker.go` | `poc/commit_worker.go` |
| `internal/pocv2/offchain_validator.go` | `poc/validator.go` |
| `internal/pocv2/proof_client.go` | `poc/proof_client.go` |
| `internal/poc/random_seed.go` | `internal/seed/seed.go` |

### DAPI - Deleted Files
- `broker/phase_helpers.go`
- `internal/pocv2/` (entire directory)
- `pocartifacts/` (entire directory)
- `internal/poc/` (entire directory)

### Chain - Modified Files
- `proto/inference/inference/tx.proto`
- `proto/inference/inference/poc_v2.proto`
- `proto/inference/inference/query.proto`
- `x/inference/keeper/poc_v2.go`
- `x/inference/keeper/query_poc_v2.go`
- `x/inference/keeper/keeper.go`
- `x/inference/types/keys.go`
- `app/ante_poc_period.go`
- `x/inference/permissions.go`

### Chain - Renamed Files
- `x/inference/keeper/msg_server_submit_poc_v2.go` → `msg_server_poc_validations_v2.go`

## Future Work

- **Migration (Phase 6)**: `migration.md` describes using `poc_v2_enabled` parameter for V1/V2 switch
- The `Orchestrator` interface serves as the switch point for this migration
