# Off-Chain PoC Artifacts: Phase 3.5 - Commit Worker & Chain Validations

## Status: Complete

## Overview

Phase 3.5 implements:
1. **CommitWorker**: Time-based commit worker (5s interval) replacing per-request commits
2. **Chain Validations**: Strict count increase, same-block rate limit, weight sum validation
3. **Nonce Type Change**: `PoCArtifactV2.nonce` changed from `int64` to `int32`

## Motivation

- **Chain Spam**: Per-request commits create excessive transactions during high-throughput generation
- **Missing Distribution**: Confirmation PoC distribution was never submitted (bug)
- **Scattered Logic**: Flush control and distribution spread across handler, dispatcher, and main
- **Replay Protection**: Need to prevent old/duplicate commits from overwriting newer state

## Changes

### New Files

| File | Description |
|------|-------------|
| `internal/pocv2/commit_worker.go` | Time-based worker for commits and distribution |
| `internal/pocv2/commit_worker_test.go` | Unit tests for the worker |

### Modified Files

| File | Changes |
|------|---------|
| `internal/server/mlnode/post_generated_artifacts_v2_handler.go` | Removed `submitStoreCommit()` call and method |
| `internal/event_listener/new_block_dispatcher.go` | Removed flush/distribution logic, removed `artifactStore`/`recorder` fields |
| `internal/event_listener/event_listener.go` | Removed `SetArtifactStore()` method |
| `main.go` | Added `CommitWorker` creation, removed `listener.SetArtifactStore()` |

## CommitWorker Design

```
┌─────────────────────────────────────────────────────────┐
│                   CommitWorker (5s tick)                │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  tick() {                                               │
│    1. Check phase via broker.IsInPoCGeneratePhase()     │
│    2. Get pocHeight (regular or confirmation PoC)       │
│                                                         │
│    // Distribution (state-based, not edge-based)        │
│    if shouldHaveDistributedWeights() {                  │
│      submitWeightDistribution()                         │
│    }                                                    │
│                                                         │
│    // Commits (window-aware)                            │
│    if inGeneration && shouldAcceptStoreCommit() {       │
│      maybeSubmitCommit()  // skips if unchanged         │
│    }                                                    │
│  }                                                      │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Key Methods

**`shouldAcceptStoreCommit(epochState, pocHeight)`**
- Regular PoC: `IsPoCExchangeWindow(currentHeight)`
- Confirmation PoC: `IsInBatchSubmissionWindow(currentHeight, epochParams)`

**`shouldHaveDistributedWeights(epochState)`**
- Returns true for: `PoCValidatePhase`, `PoCValidateWindDownPhase`, `PoCGenerateWindDownPhase`
- Also true for confirmation PoC validation phase

**`submitWeightDistribution(pocHeight)`**
1. Query chain for last commit (`PoCV2StoreCommit`)
2. Flush local store
3. Get local distribution
4. Verify sum matches committed count
5. Submit `MsgMLNodeWeightDistribution`

**`maybeSubmitCommit(pocHeight)`**
- Tracks `lastCommitted map[int64]commitState`
- Skips submission if state unchanged (count + rootHash)

### Flush Lifecycle

| Before | After |
|--------|-------|
| Dispatcher called `startArtifactFlush()` at PoC start | Worker always has flush running |
| Dispatcher called `stopArtifactFlush()` at end of generation | Worker owns flush lifecycle |

## Bugs Fixed

**Confirmation PoC Distribution Missing**
- Before: `submitNodeWeightDistribution()` only called for regular PoC (dispatcher line 405)
- After: Worker's state-based check handles both regular and confirmation PoC

## Testing

Unit tests cover:
- `shouldAcceptStoreCommit()` for various phases
- `shouldHaveDistributedWeights()` for all phases
- `getPocStageHeight()` for regular and confirmation PoC
- `maybeSubmitCommit()` duplicate detection
- Worker start/stop lifecycle

Integration testing via testermint (existing `PoCOffChainTests.kt` continues to work).

## Chain Validations Added

### PoCV2StoreCommit Validations

| Validation | Description |
|------------|-------------|
| Strict count increase | `msg.Count > existing.Count` - rejects commits that don't increase artifact count |
| Same-block rate limit | `existing.CommitBlockHeight != currentBlockHeight` - only one commit per block allowed |

**Proto change**: Added `commit_block_height` field to `PoCV2StoreCommit` storage type.

### MLNodeWeightDistribution Validation

| Validation | Description |
|------------|-------------|
| Exact sum match | `sum(weights) == commit.Count` - weight sum must equal committed artifact count |

The DAPI `commit_worker.go` now returns early (with error log) instead of submitting when sum mismatch is detected.

### Nonce Type Change

Changed `PoCArtifactV2.nonce` from `int64` to `int32` in proto. Updated:
- `inference-chain/proto/inference/inference/poc_v2.proto`
- `inference-chain/x/inference/module/chainvalidation.go` (uniqueNonces map)
- `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go` (cast)
- `decentralized-api/internal/pocv2/node_orchestrator.go` (cast)

Note: External API (mlnodeclient) keeps `int64` for compatibility; casts happen at boundaries.

## Related Documents

- `proposals/poc/offchain.md` — Main off-chain artifacts proposal
- `proposals/poc/offchain-phase3.md` — Phase 3: Chain messages
- `proposals/poc/manager-v6.md` — Future refactoring (deferred)
