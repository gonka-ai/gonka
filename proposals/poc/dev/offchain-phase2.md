# Off-Chain Artifacts - Phase 2: Proof API & Managed Storage

## Status

✅ **Complete** - merged in commits `e66a7efd5` and `aabeae3b1`

## Overview

Phase 2 implements the Proof API endpoint and per-stage artifact storage with automatic pruning.

**Goal**: Allow validators to request merkle proofs for artifacts stored during PoC generation.

**Key identifier**: `poc_stage_start_block_height` — the block height at which a PoC generation stage begins. This is the primary key for artifact storage directories.

## Architecture

```
┌─────────────┐     POST /v1/poc/proofs      ┌──────────────────────┐
│  Validator  │ ─────────────────────────────▶│   Public Server      │
│  (Client)   │                               │   (port 8080)        │
└─────────────┘                               └──────────┬───────────┘
                                                        │
                                                        ▼
                                             ┌──────────────────────┐
                                             │ ManagedArtifactStore │
                                             │                      │
                                             │  /poc-artifacts/     │
                                             │    100/              │
                                             │      artifacts.data  │
                                             │    200/              │
                                             │      artifacts.data  │
                                             └──────────────────────┘
```

## API Endpoints

### GET /v1/poc/artifacts/state

Query artifact store state for a given PoC stage. Used by validators/tests to get real count and root_hash.

**Request:**
```
GET /v1/poc/artifacts/state?height=100
```

**Response:**
```json
{
  "poc_stage_start_block_height": 100,
  "count": 50000,
  "root_hash": "<base64>"
}
```

**Note**: Returns only **flushed** (persisted) data via `GetFlushedRoot()`. This ensures reported state survives crashes.

### POST /v1/poc/proofs

Request proofs for artifacts at specific leaf indices.

**Request:**
```json
{
  "poc_stage_start_block_height": 100,
  "root_hash": "<base64>",
  "count": 50000,
  "leaf_indices": [0, 42, 999],
  "validator_address": "gonka1...",
  "validator_signer_address": "gonka1...",
  "timestamp": 1700000000000000000,
  "signature": "<base64>"
}
```

**Response:**
```json
{
  "proofs": [
    {
      "leaf_index": 0,
      "nonce_value": 12345,
      "vector_bytes": "<base64>",
      "proof": ["<base64>", "<base64>", ...]
    }
  ]
}
```

**Protection:**
- IP rate limiting: 100 req/min per IP
- Signature verification via AuthzCache (using `MsgSubmitPocValidationsV2` grants)
- Timestamp validation: ±5 min window
- Max 500 leaf indices per request

**Snapshot Binding Validation:**
- `count <= store.Count()` — requested count must not exceed stored artifacts
- `root_hash == store.GetRootAt(count)` — root hash must match MMR root at the requested count
- All `leaf_indices[i] < count` — indices must be within the snapshot range

## Files Changed

### New Files

| File | Description |
|------|-------------|
| `decentralized-api/pocartifacts/managed_store.go` | ManagedArtifactStore with per-stage directories and auto-pruning |
| `decentralized-api/pocartifacts/managed_store_test.go` | Tests for managed store |
| `decentralized-api/internal/authzcache/cache.go` | Shared TTL cache for authorized pubkeys with signer-binding |
| `decentralized-api/internal/authzcache/cache_test.go` | Tests for authz cache |
| `decentralized-api/internal/server/public/poc_handler.go` | POST /v1/poc/proofs and GET /v1/poc/artifacts/state handlers |
| `decentralized-api/internal/server/public/poc_handler_test.go` | Tests for PoC handlers |
| `testermint/src/main/kotlin/data/pocproofs.kt` | Kotlin data classes for proofs API |
| `testermint/src/test/kotlin/PoCOffChainTests.kt` | Integration test |

### Modified Files

| File | Changes |
|------|---------|
| `decentralized-api/pocartifacts/store.go` | Added `GetRootAt()`, `GetFlushedRoot()`, fixed `Close()` race condition |
| `decentralized-api/internal/server/mlnode/server.go` | Changed `artifactStore` type to `*ManagedArtifactStore` |
| `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go` | Uses `GetOrCreateStore(pocStageStartHeight)` for stage-specific store |
| `decentralized-api/internal/server/public/server.go` | Added artifact routes, `WithArtifactStore` option, authzCache |
| `decentralized-api/internal/event_listener/new_block_dispatcher.go` | SetArtifactStore for event-driven storage |
| `decentralized-api/main.go` | Uses `NewManagedArtifactStore` with `retainCount=10` |
| `testermint/src/main/kotlin/ApplicationAPI.kt` | Added `getPocProofs`, `getPocArtifactsState` methods |

## ManagedArtifactStore Design

```go
type ManagedArtifactStore struct {
    mu          sync.RWMutex
    baseDir     string
    stores      map[int64]*ArtifactStore  // poc_stage_start_block_height -> store
    retainCount int                       // keep newest N stores
    cancel      context.CancelFunc        // cancels cleanup goroutine
    flushCancel context.CancelFunc        // cancels periodic flush goroutine
}
```

**Key Methods:**
- `GetOrCreateStore(pocStageStartHeight)` - Get or create store for a PoC stage
- `GetStore(pocStageStartHeight)` - Get existing store (for proof requests)
- `PruneStore(pocStageStartHeight)` - Remove store directory
- `ListStores()` - List all stage heights on disk (sorted)
- `Flush()` - Flush all open stores
- `StartPeriodicFlush(interval)` - Background flush goroutine
- `StopPeriodicFlush()` - Stop background flush with final flush
- `cleanup()` - Auto-prune loop (every 30s)

**Pruning:**
- List all store directories by `poc_stage_start_block_height`
- Sort by height (ascending)
- Keep newest `retainCount` stores
- Prune older stores

## Authorization and Signature Verification

**Message Type URL:** `/inference.inference.MsgSubmitPocValidationsV2`

Validators requesting proofs must have authz grants for `MsgSubmitPocValidationsV2`. This aligns with the capability validators need on-chain.

**Signer Binding:**

The server verifies signatures against the specific `validator_signer_address`:
1. Look up authz grants for `validator_address` with message type `MsgSubmitPocValidationsV2`
2. Find the pubkey corresponding to `validator_signer_address` (either granter's own key or a grantee)
3. Verify signature using ONLY that specific pubkey
4. Reject if `validator_signer_address` is not authorized for `validator_address`

**Signature Payload:**
```
sign_payload = hex(SHA256(
    poc_stage_start_block_height (LE64) ||
    root_hash (32 bytes) ||
    count (LE32) ||
    leaf_indices (LE32 each, in order) ||
    timestamp (LE64) ||
    validator_address (UTF-8 bytes) ||
    validator_signer_address (UTF-8 bytes)
))
```

## Testermint Integration Test

`PoCOffChainTests.kt` validates the full flow:
1. Initialize cluster with MLNodes
2. Wait for PoC generation phase to end
3. Query artifact store state (`GET /v1/poc/artifacts/state`)
4. Build signed proof request with real values
5. Request proofs (`POST /v1/poc/proofs`)
6. Verify proofs are returned for requested leaf indices

## Implementation Notes

**JSON Number Handling:**
- Request uses `StringInt64` / `StringUint32` types that unmarshal from both JSON number and string
- Kotlin test uses Long/Int which serialize as numbers
- Go handler accepts both formats

**GetFlushedRoot vs GetRoot:**
- `GetFlushedRoot()` returns only persisted (flushed) data - safe to report externally
- `GetRoot()` returns current state including unflushed buffer
- Public endpoints use `GetFlushedRoot()` for crash safety

## Next Steps (Phase 3+)

1. Add `PoCV2StoreCommit` chain message for on-chain root commitment
2. Add `MLNodeWeightDistribution` chain message
3. Update validation to fetch from API instead of chain
4. Remove on-chain artifact storage
