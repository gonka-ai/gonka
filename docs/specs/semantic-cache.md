## Semantic Cache Quality

### Overview

The semantic cache layer allows API nodes to serve verified inference results from an in-memory
vector store when a semantically equivalent prompt has already been computed. Participants that
provide reusable results earn an additional weight bonus at epoch settlement, creating a trust
feedback loop aligned with the existing Proof-of-Computation model.

The feature is **disabled by default** and activated via governance (`CacheQualityParams.Enabled`).

---

### Architecture — Two-Level Cache

```
User Request
│
▼ handleTransferRequest
  MsgStartInference (async goroutine) ──► chain (fee paid, cycle open)
  │
  ▼ handleExecutorRequest
  [L1: PromptHash exact-match] ─── HIT ──► verify ResponseHash (sha256)
  │                                         send MsgFinishInference
  │                                         node earns CacheQualityWeight
  │ MISS
  [L2: cosine similarity ≥ threshold] ── HIT ──► verify ResponseHash (sha256)
  │                                               send MsgFinishInference
  │                                               node earns CacheQualityWeight
  │ MISS
  ▼
  GPU Inference → MsgFinishInference → StoreResult(embedding, payload)
```

**Guarantee per level:**
- L1: `sha256(canonical_JSON)` identical → 100% same result. Cryptographically certain.
- L2: cosine similarity ≥ `SimilarityThresholdBps` (default 9700 bps = 97%). Probabilistic,
  governance-controlled.

`MsgFinishInference` is sent on every HIT (both L1 and L2) so that the node closes the on-chain
cycle and accrues `CacheQualityWeight` for the reuse event.

---

### Chain-Side Components

**CacheQualityParams** (governance, field 14 in `Params`):
- `Enabled`: gates the feature; must be activated via `MsgUpdateParams`
- `SimilarityThresholdBps`: minimum cosine similarity for a valid L2 hit (default 9700)
- `MaxCacheAgeEpochs`: TTL for off-chain cached results (default 10 epochs ≈ 33 min)
- `MaxWeightFractionBps`: cap on cache bonus as fraction of standard PoC weight (default 3000 = 30%)
- `EmbeddingModelVersion`: shared model identifier; mismatch invalidates cached vectors
- `PruningEpochThreshold`: how many epochs of summaries to retain on-chain

**MsgSubmitCacheQualitySummary**: Participants submit per-epoch metrics
(`CacheReuseCount`, `OriginalComputeCount`, `AvgSimilarityBps`, `EmbeddingModelVersion`).
Stored keyed by `(epoch_index, participant)`. One summary per epoch per participant.
`EmbeddingModelVersion` must match the current governance parameter; mismatches are rejected.

**Weight Integration**: In `chainvalidation.go`, `calculateParticipantWeight` adds
`CacheQualityWeight` to `baseCount`, capped by `MaxWeightFractionBps`. Summaries are loaded
via `GetAllCacheQualityEpochSummariesForEpoch` at epoch settlement.

**Pruning**: `CacheQualityEpochSummaries` pruned by `PruningEpochThreshold` (field 7 of
`PruningState`). Upgrade handler seeds `CacheQualityParams` with defaults when nil.

---

### API-Side Components

**SemanticCache** (`decentralized-api/semanticcache/cache.go`):
- Wraps `Embedder` and `CacheStore`
- `LookupByPromptHash`: L1 exact-match on `sha256(canonical_JSON)`; O(1), no embedding needed
- `Lookup`: L2 cosine similarity search; embeds prompt text, queries `InMemoryCacheStore`
- Both lookups enforce `ValidUntilEpoch` (TTL) and `ModelVersion` match
- `StoreResult`: sets `ModelVersion`, `OriginalEpoch`, `ValidUntilEpoch` from governance params
- `UpdateCacheParams`: refreshes threshold, `modelVersion`, `maxCacheAgeEpochs` at runtime
  (mutex-protected); called every 30 s from the governance sync goroutine
- `EvictExpired`: delegates TTL cleanup to the underlying store; called at each epoch boundary

**CachedResult fields**: `PromptHash`, `ResponsePayload`, `ResponseHash`, `BLSSignature`
(reserved), `OriginalEpoch`, `OriginalParticipantAddress`, `SimilarityBps`, `ModelVersion`,
`ValidUntilEpoch`.

**Trust model (current)**: `ResponseHash = sha256(ResponsePayload)`, identical to
`MsgFinishInference.ResponseHash` committed on-chain. Verified on every cache hit before
the response is served.

**Trust model (future)**: `BLSSignature` field is reserved for a protocol extension where
the executor quorum produces a threshold BLS signature over the inference response. When
implemented, cache hits will carry a verifiable quorum proof without requiring fresh GPU
computation.

**InMemoryCacheStore** (`decentralized-api/semanticcache/memory_store.go`):
Default `CacheStore` backend. Zero external dependencies. Stores `(vector, CachedResult)` pairs
in a sorted slice; `Lookup` performs linear cosine similarity search (sufficient for node-local
cache sizes). `EvictExpired` purges all entries whose `ValidUntilEpoch < currentEpoch`.
Both L1 (`PromptHash`) and L2 (vector) maps are maintained in the same struct.

**Embedder**: `MLNodeEmbedder` calls `/api/v1/embed` on the ML-node management port
(CPU-only, all-MiniLM-L6-v2, 384 dims). Does not lock the inference GPU.
`StubEmbedder` is available for unit tests and disables the embedding network call.

**Streaming**: Requests with `stream: true` bypass both lookup and store —
SSE format cannot be replayed as a cached JSON entry.

---

### Authz Delegation

`MsgSubmitCacheQualitySummary` is in `InferenceOperationKeyPerms`.
`GrantMLOperationalKeyPermissionsToAccount` and `RevokeMLOperationalKeyPermissionsFromAccount`
support the Grant → Exec → Revoke flow for operational keys (required by the Unified
Permissions model, #760).

---

### Key Implementation Files

| File | Description |
|---|---|
| `inference-chain/proto/inference/inference/cache_quality.proto` | Proto source for `CacheQualityEpochSummary` |
| `inference-chain/proto/inference/inference/params.proto` | `CacheQualityParams` added (field 14) |
| `inference-chain/proto/inference/inference/tx.proto` | `MsgSubmitCacheQualitySummary` RPC added |
| `inference-chain/x/inference/types/cache_quality.go` | Params and summary types (serialisation) |
| `inference-chain/x/inference/keeper/msg_server_cache_quality.go` | Submission handler with validation |
| `inference-chain/x/inference/module/chainvalidation.go` | `CacheQualityWeight` integration |
| `decentralized-api/semanticcache/cache.go` | Two-level lookup and store logic |
| `decentralized-api/semanticcache/memory_store.go` | `InMemoryCacheStore` (zero external deps) |
| `decentralized-api/semanticcache/embedder.go` | `MLNodeEmbedder` and `StubEmbedder` |
| `decentralized-api/internal/server/public/post_chat_handler.go` | L1/L2 integration in executor path |

---

### Developer Simulation (no GPU, no ML-node, no chain required)

The full L1/L2 cache path can be validated locally using `StubEmbedder` and `InMemoryCacheStore`.
No GPU, no ML-node, and no live chain are required — analogous to the PoC simulation described in
the [Host Quickstart](https://gonka.ai/host/quickstart/).

```bash
# On any machine with Go 1.22+
cd decentralized-api
go test ./semanticcache/... -v -count=1
```

Covered by the test suite:

| Test | What it proves |
|---|---|
| `TestMatrix_L1_ExactMatch` | L1 HIT returns `SimilarityBps=10000` |
| `TestMatrix_L1_WrongHash` | Different `PromptHash` → L1 MISS |
| `TestMatrix_L2_SemanticHit` | Cosine similarity ≥ 9700 bps → L2 HIT |
| `TestMatrix_L2_BelowThreshold` | Orthogonal vector → MISS |
| `TestMatrix_TTL_Eviction` | Expired entry → MISS after `EvictExpired` |
| `TestMatrix_ModelVersion_Invalidation` | Model version change → MISS |
| `TestHTTP_L1_HIT_XCacheHeader` | `X-Cache: HIT`, `X-Cache-Level: 1`, correct body |
| `TestHTTP_L1_MISS_NoXCacheHeader` | MISS → no `X-Cache` header |
| `TestHTTP_L1_VerifyFail_FallThrough` | Tampered `ResponseHash` → fall through to GPU |
| `TestHTTP_TTL_Expired_FallThrough` | Epoch 101 > `ValidUntilEpoch` 100 → MISS |
| `TestHTTP_ModelVersion_FallThrough` | v1 entry rejected under v2 governance |
| `TestHTTP_PublicAPIResponseFormat` | Response parseable, `sha256` non-empty |

---

### Known Limitations

| Item | Description | Mitigation |
|---|---|---|
| L2 self-reported similarity | `AvgSimilarityBps` is computed by the DAPI operator's own ML-node. Cannot be verified on-chain. | `MaxWeightFractionBps` caps the maximum bonus. Incentive to lie is bounded. |
| `CacheReuseCount` self-reported | Only the DAPI operator knows actual hit count. | Same cap applies. Future: on-chain event per L1 HIT. |
| In-memory cache lost on restart | Rebuilds from new inferences. | Acceptable for a cache. Original results always on-chain. |
| L2 requires live ML-node embed | If ML-node is down, L2 is disabled; L1 still works. | ML-node is part of standard gonka node stack. |
