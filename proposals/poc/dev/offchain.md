# PoC v2 Off-Chain Artifacts Proposal

## Related Documents
- `proposals/poc/offchain-phase1.md` — Phase 1 implementation (storage & MMR).
- `proposals/poc/offchain-phase2.md` — Phase 2 implementation (proof API & managed storage).
- `proposals/poc/offchain-phase3.md` — Phase 3 implementation (chain messages).
- `proposals/poc/offchain-phase35.md` — Phase 3.5 implementation (CommitWorker).
- `proposals/poc/offchain-phase4.md` — Phase 4 implementation (validation switchover).
- `proposals/poc/manager-v6.md` — Phase 5 domain consolidation plan.

---

## 1. Motivation

The current PoC v2 stores **full artifact batches on-chain**, resulting in:
- High memory usage on chain nodes.
- Many large transactions per epoch.
- Scaling bottleneck as participant count or artifact volume grows.

**Goal:** Replace on-chain artifact storage with **commit-only** (Merkle root + count), keeping artifacts off-chain while preserving verifiability.

---

## 2. Definitions

| Term | Meaning |
|------|---------|
| `leaf_index` | Zero-based sequential position of an artifact in a participant's MMR (`uint32`). |
| `nonce_value` | The `int32` field from `PoCArtifactV2.nonce` (may be sparse). |
| `count` | Total number of leaves (artifacts) in participant's MMR at commit time (`uint32`). |
| `root_hash` | 32-byte MMR root committed on-chain. |
| `snapshot` | Historical state defined by `count`; proofs are verified against a snapshot. |

See `merkle-tree.md §1` for full definitions and `§5` for normative MMR spec.

---

## 3. Current Implementation vs. Proposed Change

### Current (On-Chain Artifacts)

```
┌────────────────────────────────────────────────────────────────────────┐
│ Generation Phase                                                        │
│   Participant generates artifacts → submits MsgSubmitPocBatchesV2      │
│   (full artifact bytes stored on-chain, keyed by height/addr/node_id)  │
└────────────────────────────────────────────────────────────────────────┘
         ↓
┌────────────────────────────────────────────────────────────────────────┐
│ Validation Phase                                                        │
│   Validator queries chain for batches → samples by nonce → validates   │
│   Submits MsgSubmitPocValidationsV2                                    │
└────────────────────────────────────────────────────────────────────────┘
```

**Code references (current):**
- `inference-chain/x/inference/keeper/msg_server_submit_poc_v2.go` — `SubmitPocBatchesV2` stores `batch.Artifacts` on-chain.
- `inference-chain/proto/inference/inference/tx.proto` lines 231-248 — Comment: `"Note: Current iteration stores artifacts on-chain; later iteration moves fully off-chain."`

### Proposed (Off-Chain Artifacts)

```
┌────────────────────────────────────────────────────────────────────────┐
│ Generation Phase                                                        │
│   Participant generates artifacts → stores locally in MMR             │
│   Periodically submits PoCV2StoreCommit(root_hash, count) to chain    │
└────────────────────────────────────────────────────────────────────────┘
         ↓
┌────────────────────────────────────────────────────────────────────────┐
│ After Generation Phase                                                  │
│   Submits MLNodeWeightDistribution (weight per node)                   │
└────────────────────────────────────────────────────────────────────────┘
         ↓
┌────────────────────────────────────────────────────────────────────────┐
│ Validation Phase                                                        │
│   Validator queries chain for last commit (root_hash, count)          │
│   Samples leaf_indices in [0, count) using deterministic randomness   │
│   Requests artifacts + proofs directly from participant's API         │
│   Verifies proofs → submits MsgSubmitPocValidationsV2                 │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 4. New On-Chain Messages

### 4.1 `PoCV2StoreCommit`

Commits the current MMR state to chain.

```protobuf
message PoCV2StoreCommit {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;                        // participant address
  int64 poc_stage_start_block_height = 2;
  uint32 count = 3;                          // number of leaves
  bytes root_hash = 4;                       // 32-byte MMR root
}
```

**Chain acceptance rules (enforced on-chain):**
1. Must be within the PoC exchange window (same gating as `MsgSubmitPocBatchesV2`).
   - Use existing `CheckPoCMessageTooLate(ctx, startBlockHeight, PoCWindowBatch)`.
2. **Strict count increase:** `count > last_recorded.count` for this `(creator, poc_stage_start_block_height)`. Reject if equal or lower.
3. **Rate limit:** At most one accepted commit per participant per N blocks (e.g., N=1 or N=5). Track `last_commit_block` per participant.

**Code reference:** Window validation logic is in `inference-chain/x/inference/keeper/poc_period_validation.go`.

### 4.2 `MLNodeWeightDistribution`

Reports per-node weight distribution after generation phase.

```protobuf
message MLNodeWeight {
  string node_id = 1;
  uint32 weight = 2;   // number of unique nonces attributed to this node
}

message MLNodeWeightDistribution {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;                        // participant address
  int64 poc_stage_start_block_height = 2;
  repeated MLNodeWeight weights = 3;
}
```

**Chain acceptance rules (enforced on-chain):**
1. **Exact match:** Sum of `weight` values must equal `last_commit.count` for this participant/stage. Chain queries the last `PoCV2StoreCommit` and rejects if sum differs.
2. `node_id` values are self-reported (same trust model as current `PoCBatchV2.node_id`).

**Purpose:** This message is **information-only** for analytics and weight attribution. It is not enforced on-chain beyond the sum check. Validators do not verify individual node weights during validation.

---

## 5. Off-Chain Proof Endpoint

Participants expose an HTTP endpoint for validators to request artifact proofs.

### 5.1 Endpoint

```
POST /v1/poc/proofs
```

### 5.2 Request Body

```json
{
  "poc_stage_start_block_height": 12345,
  "root_hash": "<base64-encoded 32 bytes>",
  "count": 50000,
  "leaf_indices": [0, 42, 999, 12345, 49999],
  
  "participant_address": "gonka1abc...",
  "signer_address": "gonka1warm...",
  "timestamp": 1700000000000000000,
  "signature": "<base64-encoded signature>"
}
```

| Field | Description |
|-------|-------------|
| `poc_stage_start_block_height` | Identifies the PoC stage. |
| `root_hash` | The committed root (binds request to specific snapshot). |
| `count` | The snapshot leaf count. |
| `leaf_indices` | Array of `leaf_index` values to retrieve (0-based). |
| `participant_address` | The participant's account address (API owner). Used for authz lookup. |
| `signer_address` | Operational ("warm") key address that signed this request. |
| `timestamp` | Unix nanoseconds; used to prevent replay. |
| `signature` | Signature over the request payload. |

Note: `participant_address` is included so the server can efficiently look up authz grants. The server verifies that `participant_address` matches its own address (the API owner).

### 5.3 Signature Payload

The signature covers a deterministic encoding of:

```
sign_payload = SHA256(
    poc_stage_start_block_height (LE64) ||
    root_hash (32 bytes) ||
    count (LE32) ||
    leaf_indices (LE32 each, in order) ||
    timestamp (LE64) ||
    participant_address (UTF-8 bytes) ||
    signer_address (UTF-8 bytes)
)
```

The server verifies:
1. `participant_address` matches the API owner's address (self-check).
2. `signer_address` is an authorized grantee for `participant_address` (via authz query).
3. Signature is valid for `signer_address`'s public key.
4. `timestamp` is within acceptable window (e.g., ±5 minutes).

**Code reference:** Existing signed-request pattern in `decentralized-api/internal/validation/payload_retrieval.go` uses similar headers; adapt to request-body for PoC proofs.

### 5.4 Response Body

```json
{
  "proofs": [
    {
      "leaf_index": 0,
      "nonce_value": 42,
      "vector_bytes": "<base64-encoded bytes>",
      "proof": ["<base64>", "<base64>", ...]
    },
    ...
  ]
}
```

| Field | Description |
|-------|-------------|
| `leaf_index` | The requested index. |
| `nonce_value` | The `PoCArtifactV2.nonce` value (`int32`). |
| `vector_bytes` | The `PoCArtifactV2.vector` bytes (base64). |
| `proof` | Array of sibling hashes from leaf to peak. |

Note: Sibling direction (left vs. right) is **not included** in the response. The verifier derives it from MMR position math using `leaf_index` and `count`. See `merkle-tree.md §5.4` for MMR structure details.

### 5.5 Rate Limiting

- Limit requests per `signer_address` per time window (e.g., 10 requests/minute).
- Limit total `leaf_indices` per request (e.g., max 500).

---

## 6. Validation Protocol

### 6.1 Sampling

Validators sample `leaf_index` values deterministically using:

```
sample_count = PocParams.ValidationSampleSize  // e.g., 200
seed = SHA256(validator_pubkey || block_hash || poc_stage_start_block_height)
indices = deterministic_sample(seed, count, sample_count)
```

**Code reference:** Sampling logic in `decentralized-api/internal/pocv2/offchain_validator.go`.

### 6.2 Validation Steps

1. **Query chain** for participant's last `PoCV2StoreCommit` (get `root_hash`, `count`).
2. **Sample** `leaf_indices` in `[0, count)`.
3. **Request proofs** from participant's API (see §5).
4. **Verify each proof** against `(root_hash, count, leaf_index)` using MMR verification (see `merkle-tree.md §5`).
5. **Check uniqueness:** If any two proofs return the same `nonce_value` → participant invalid.
6. **Validate artifacts:** Run statistical validation on `(nonce_value, vector_bytes)` as currently done.
7. **Submit result:** `MsgSubmitPocValidationsV2` with aggregated verdict.

**Statistical deterrence (majority model):** With 200 validators each sampling 200 artifacts from 200,000, and majority (100+) required for invalidation:
- Single duplicate pair: P(single validator catches) ≈ 0.2%, expected catches = 0.4 → **not detectable**
- 100 duplicate pairs: P(catch) ≈ 18% per validator, expected = 36 → **unlikely to reach majority**
- 1000 duplicate pairs: P(catch) ≈ 86% per validator, expected = 172 → **majority reached**

Current model catches large-scale cheating only. Small-scale cheating (few duplicates) is economically unprofitable but not cryptographically proven.

> **TODO (future improvement):** Add option to invalidate participant via TX with exact duplicate nonce IDs as cryptographic proof, enabling single-validator fraud detection.

### 6.3 Error Handling

| Condition | Action |
|-----------|--------|
| Proof verification fails | Retry once; if still fails → mark participant invalid. |
| Request timeout/error | Retry with backoff; if exhausted → mark participant invalid. |
| Duplicate `nonce_value` in response | Immediate invalidation (no retry). |

---

## 7. Commit Window & Timing

### 7.1 Window Rules

- `PoCV2StoreCommit` is accepted only during the **PoC exchange window** (same as `MsgSubmitPocBatchesV2`).
- Validators validate against the **last commit recorded before the exchange deadline**.

### 7.2 Sampling Randomness Timing

To prevent adaptive cheating:
- **Commit deadline:** End of PoC exchange window (same as batch deadline).
- **Sampling seed block:** First block of validation phase (or `PocSeedBlockHash` for confirmation PoC).
- Participants cannot see the sampling seed until after their final commit is recorded.

---

## 8. Implementation Phases

### Phase 1: Storage & MMR 
**Status:** Complete (see `offchain-phase1.md`)
- Single-file storage layout with in-memory MMR
- 40+ unit tests covering proofs, recovery, tamper detection
- Dual-write integration in artifact handler

### Phase 2: Proof API 
**Status:** Complete (see `offchain-phase2.md`)
- `POST /v1/poc/proofs` endpoint with signature verification
- `GET /v1/poc/artifacts/state` endpoint for validators
- AuthzCache with signer-binding for pubkey lookup
- ManagedArtifactStore with per-height directories and auto-pruning
- Rate limiting (100 req/min per IP)
- Integration tests with testermint

### Phase 3: New Chain Messages 
**Status:** Complete (see `offchain-phase3.md`)
- `MsgPoCV2StoreCommit` proto, handler, and query endpoint
- `MsgMLNodeWeightDistribution` proto, handler, and query endpoint
- Artifact store node tracking (`AddWithNode`, `GetNodeDistribution`)

### Phase 3.5: CommitWorker
**Status:** Complete (see `offchain-phase35.md`)
- CommitWorker sends commits every 5s (not per `/generated` request)
- Distribution queries chain for last commit and validates exact match (`sum(weights) == last_commit.count`)
- Chain enforces strict `count` increase (`new_count > last_count`)
- Chain enforces same-block rate limit (at most 1 commit per block)
- Nonce type changed from `int64` to `int32`

### Phase 4: Validation Switchover
**Status:** Complete (see `offchain-phase4.md`)
- `OffChainValidator` fetches proofs from participant APIs
- On-chain batch submission removed from handler
- Weight calculation uses store commits instead of batches
- E2E tests passing

### Phase 5: Cleanup & Domain Consolidation
**Status:** Pending (see `manager-v6.md`)

**Chain cleanup:**
- Remove `MsgSubmitPocBatchesV2` message and handler
- Remove `PoCBatchesV2` collection from keeper
- Remove `QueryPocV2BatchesForStage` query endpoint

**DAPI cleanup:**
- Remove dead batch types from `node_orchestrator.go`
- Remove `post_generated_batches_handler.go` (V1 handlers)
- Remove `SubmitPocBatchesV2` from cosmosclient

**Domain consolidation:**
- Move `pocartifacts/` to `poc/artifacts/`
- Move `internal/pocv2/` to `poc/`
- Consolidate phase predicates into `poc/phase.go`
- Add NodeProvider interface to decouple orchestrator from broker
- Delete `broker/phase_helpers.go`

---

## 9. Notes

### Normalize Weight by Block Duration
We must saving `poc_generation_start_block` and `poc_generation_end_block` to normalize weight. Protects against inflated weight if blocks are slow.

### Nonce Type Change 
`PoCArtifactV2.nonce` changed from `int64` to `int32` in Phase 3.5:
```protobuf
message PoCArtifactV2 {
  int32 nonce = 1;  // changed from int64
  bytes vector = 2;
}
```

---

## 10. Code References Summary

| Aspect | File | Notes |
|--------|------|-------|
| Window validation | `inference-chain/x/inference/keeper/poc_period_validation.go` | `CheckPoCMessageTooLate` |
| **Off-chain artifact store** | `decentralized-api/pocartifacts/store.go` | ArtifactStore with MMR |
| **MMR implementation** | `decentralized-api/pocartifacts/mmr.go` | Proof generation/verification |
| **Managed store** | `decentralized-api/pocartifacts/managed_store.go` | Per-height stores with auto-pruning |
| **Proof API handler** | `decentralized-api/internal/server/public/poc_handler.go` | POST /v1/poc/proofs, GET /v1/poc/artifacts/state |
| **Authz cache** | `decentralized-api/internal/authzcache/cache.go` | Signer pubkey lookup with TTL |
| **Store commit handler** | `inference-chain/x/inference/keeper/msg_server_poc_v2_commit.go` | MsgPoCV2StoreCommit, MsgMLNodeWeightDistribution |
| **CommitWorker** | `decentralized-api/internal/pocv2/commit_worker.go` | Time-based commits and distribution |
| **OffChainValidator** | `decentralized-api/internal/pocv2/offchain_validator.go` | Fetches proofs, verifies, sends to ML nodes |
| **ProofClient** | `decentralized-api/internal/pocv2/proof_client.go` | HTTP client for proof requests |
| **Testermint integration** | `testermint/src/test/kotlin/PoCOffChainTests.kt` | E2E proof API test |

---

## 11. Related Documents

- `offchain-phase1.md` — Phase 1 implementation details
- `offchain-phase2.md` — Phase 2 implementation details
- `offchain-phase3.md` — Phase 3 implementation details
- `offchain-phase35.md` — Phase 3.5 implementation details
- `offchain-phase4.md` — Phase 4 implementation details
- `manager-v6.md` — Phase 5 domain consolidation plan
