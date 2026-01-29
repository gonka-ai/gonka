# Off-Chain PoC Artifacts: Phase 3 - Chain Commit Messages

## Overview

Phase 3 implements on-chain commit messages for off-chain artifact storage. These messages allow DAPI nodes to report their local artifact store state (MMR root hash, count) and per-node weight distribution to the chain without storing the actual artifact data on-chain.

## Motivation

- **Verifiable Off-Chain Storage**: Commits provide cryptographic evidence of local artifact storage state
- **Weight Distribution Tracking**: Per-node artifact counts enable fair reward distribution
- **Minimal On-Chain Footprint**: Only commitments (32-byte hashes + counts) are stored on-chain

## New Chain Messages

### MsgPoCV2StoreCommit

Commits the current state of the local artifact store (MMR root and leaf count).

```protobuf
message MsgPoCV2StoreCommit {
  string creator = 1;
  int64 poc_stage_start_block_height = 2;
  uint32 count = 3;       // number of leaves in MMR
  bytes root_hash = 4;    // 32-byte MMR root hash
}
```

- **When sent**: After each artifact batch is stored locally and flushed
- **Validation**: Must be within PoC exchange window, count > 0, root_hash == 32 bytes
- **Storage**: Latest commit overwrites previous (idempotent)

### MsgMLNodeWeightDistribution

Reports per-node artifact distribution after generation phase completes.

```protobuf
message MsgMLNodeWeightDistribution {
  string creator = 1;
  int64 poc_stage_start_block_height = 2;
  repeated MLNodeWeight weights = 3;
}

message MLNodeWeight {
  string node_id = 1;
  uint32 weight = 2;  // artifact count from this node
}
```

- **When sent**: At end of PoC generation stage (after artifact flush stops)
- **Validation**: Must be within exchange-to-validation window, weights non-empty
- **Storage**: Latest distribution overwrites previous (idempotent)

## Query Endpoints

### PoCV2StoreCommit Query

```
GET /productscience/inference/inference/poc_v2_store_commit/{poc_stage_start_block_height}/{participant_address}
```

Returns: `{ count, root_hash, found }`

### MLNodeWeightDistribution Query

```
GET /productscience/inference/inference/mlnode_weight_distribution/{poc_stage_start_block_height}/{participant_address}
```

Returns: `{ weights: [{node_id, weight}], found }`

## Implementation Details

### Artifact Store Node Tracking

The `ArtifactStore` was extended to track which MLNode contributed each artifact:

- `AddWithNode(nonce, vector, nodeId)` - tracks nodeId alongside artifact
- `GetNodeDistribution()` - returns `map[string]uint32` of node counts
- Node counts are persisted in `nodes.json` during flush

### Submission Flow

1. **During Generation**: Each `postGeneratedArtifactsV2` call:
   - Stores artifacts via `AddWithNode(nonce, vector, nodeId)`
   - After `SubmitPocBatchesV2` succeeds, calls `submitStoreCommit()`
   - `submitStoreCommit()` reads flushed root/count and submits `MsgPoCV2StoreCommit`

2. **At End of Generation**: When `IsEndOfPoCStage(blockHeight)`:
   - `stopArtifactFlush()` called (ensures all data persisted)
   - `submitNodeWeightDistribution()` called (goroutine)
   - Reads `GetNodeDistribution()` and submits `MsgMLNodeWeightDistribution`

## Files Modified

### Proto Definitions

| File | Changes |
|------|---------|
| `inference-chain/proto/inference/inference/tx.proto` | Added `MsgPoCV2StoreCommit`, `MsgMLNodeWeightDistribution` messages and service RPCs |
| `inference-chain/proto/inference/inference/query.proto` | Added `QueryPoCV2StoreCommitRequest/Response`, `QueryMLNodeWeightDistributionRequest/Response` |
| `inference-chain/proto/inference/inference/poc_v2.proto` | Added `MLNodeWeight`, `PoCV2StoreCommit`, `MLNodeWeightDistribution` storage types |

### Chain Keeper

| File | Changes |
|------|---------|
| `inference-chain/x/inference/types/keys.go` | Added `PoCV2StoreCommitPrefix` (39), `MLNodeWeightDistributionPrefix` (40) |
| `inference-chain/x/inference/keeper/keeper.go` | Added `PoCV2StoreCommits`, `MLNodeWeightDistributions` collections |
| `inference-chain/x/inference/keeper/msg_server_poc_v2_commit.go` | **New file** - message handlers for both messages |
| `inference-chain/x/inference/keeper/query_poc_v2_commit.go` | **New file** - query handlers for both endpoints |
| `inference-chain/x/inference/module/autocli.go` | Added CLI command definitions with positional args |

### DAPI - Artifact Store

| File | Changes |
|------|---------|
| `decentralized-api/pocartifacts/store.go` | Added `nodeCounts`, `flushedNodeCounts` maps; `AddWithNode()`, `GetNodeDistribution()` methods; `nodes.json` persistence |

### DAPI - Cosmos Client

| File | Changes |
|------|---------|
| `decentralized-api/cosmosclient/cosmosclient.go` | Added `SubmitPoCV2StoreCommit()`, `SubmitMLNodeWeightDistribution()` interface + implementation |
| `decentralized-api/cosmosclient/mock_cosmos_message_client.go` | Added mock implementations |

### DAPI - Handlers

| File | Changes |
|------|---------|
| `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go` | Updated `addToLocalStorage()` to use `AddWithNode()`; added `submitStoreCommit()` |
| `decentralized-api/internal/event_listener/new_block_dispatcher.go` | Added `recorder` field; added `submitNodeWeightDistribution()` at `IsEndOfPoCStage` |

### Testermint

| File | Changes |
|------|---------|
| `testermint/src/main/kotlin/ApplicationCLI.kt` | Added `getPoCV2StoreCommit()`, `getMLNodeWeightDistribution()` query methods + response data classes |
| `testermint/src/test/kotlin/PoCOffChainTests.kt` | Added test `poc v2 store commit and weight distribution are recorded on chain` |

## Storage Schema

### Chain Collections

```go
// Key: (poc_stage_start_block_height, participant_address)
PoCV2StoreCommits collections.Map[collections.Pair[int64, sdk.AccAddress], types.PoCV2StoreCommit]

// Key: (poc_stage_start_block_height, participant_address)  
MLNodeWeightDistributions collections.Map[collections.Pair[int64, sdk.AccAddress], types.MLNodeWeightDistribution]
```

### Local Artifact Store

```
{store_dir}/
  artifacts.data    # Binary artifact data (existing)
  nodes.json        # JSON map: { "node_id": count, ... }
```

## Transaction Characteristics

| Message | Retry | Reason |
|---------|-------|--------|
| `MsgPoCV2StoreCommit` | No (`SendTransactionAsyncNoRetry`) | Idempotent - latest wins |
| `MsgMLNodeWeightDistribution` | No (`SendTransactionAsyncNoRetry`) | Idempotent - latest wins |


## Related Documents

- `proposals/poc/offchain.md` — Main off-chain artifacts proposal
- `proposals/poc/offchain-phase1.md` — Phase 1: Storage & MMR
- `proposals/poc/offchain-phase2.md` — Phase 2: Proof API
- `proposals/poc/manager-v5.md` — PoC package consolidation (next steps)

## Status

**Completed** - All components implemented and tested.
