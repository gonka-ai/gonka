# Height sync proposal (structured HTTP body — periodic anchors, Strong on disagreement)

## Summary

User–host traffic uses a **two-section HTTP body**: **(1)** optional **mainnet attestation** (signed **`mainnet_height`** + **`BlockID.Hash`** on an **Anchor** schedule, or full **`LightBlock`** in **Strong** mode) plus session fields; **(2)** the **application message**. Large **`LightBlock`** proofs are **not** sent on the periodic **`K`**-block path.

**Normative pattern:** **Anchor** messages (every **`K`** mainnet blocks, e.g. **`K = 10`**, TBD) carry **signed height + hash**, **`light_block` empty** — this is **height sync**, not Strong sync. **Between** Anchors, section 1 **MUST NOT** include **`mainnet_height`**, **`mainnet_height_hash`**, or **`light_block`** — parties rely on the **last Anchor** and each **mainnet listener**. **Strong** (`LightBlock` + verify) is required when **`|H_peer − H_local_aligned| > D`** (proposal **`D = 2`**) and for finalization/dispute evidence when policy says so.

Historical note: an early draft put proofs in headers; that hits size limits. **Normative transport is the structured body below.**

Related proposals:

- [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)
- [`CPOC_PROTOCOL.md`](./CPOC_PROTOCOL.md) — skip attestation, nonce binding; height in application payload between Anchors; Strong if **`> D`**

---

## Goals

1. Align mainnet time with **periodic Anchor** messages (`H` + `BlockID.Hash`, **signed**, **no** `LightBlock` on the **`K`**-block path), without mainnet height/hash on every envelope.
2. Use **Strong** (`LightBlock`) **only** when **`|H_peer − H_local_aligned| > D`** (default **`D = 2`**) or when finalization/disputes require it — **not** as the default every-`K` sync.
3. Make **timeout** and **dispute** triggers auditable: **Anchors** + **deferred** hash verification when local block **`H`** is not yet available; **Strong** when disagreement exceeds **`D`**.
4. Keep **mainnet attestation** (when present) separate from the **application message** (section 2).
5. **No** mainnet **`height` / `hash`** fields **between** Anchors (no per-message “trusted window” on the inference path).

---

## Sync modes

| Mode | Section 1: `mainnet_height` / `mainnet_height_hash` | `light_block` | When |
|------|-----------------------------------------------------|---------------|------|
| **Omit** | **Must not** be sent | absent | **Between** Anchor messages (default) |
| **Anchor** | Present, **signed**; hash vs local **`H`** or **deferred** | **empty** | Every **`K`** mainnet blocks (e.g. **10**) + session/escrow start per policy |
| **Strong** | Present; must match `LightBlock` header | **non-empty**, verified | **`|H_peer − H_local_aligned| > D`** (default **`D = 2`**) or finalization/dispute |

Periodic **`K`**-block sync is **Anchor** only (not Strong). **Strong** is for disagreement **> `D`**, not for each **`K`** tick.

**Deferred verification (Anchor):** If block **`H`** is not local yet, enqueue **`(H, hash)`**; when synced, compare to canonical **`BlockID.Hash`**; mismatch ⇒ misrepresentation evidence.

---

## High-level protocol (request / response and height alignment)

Two parties exchange work over HTTP: **user** (client) and **host** (server). Each **request** and **response** uses **section 2** (message body). **Section 1** is present in **Anchor** / **Strong** modes (mainnet fields + signature) or reduced / omitted in **Omit** mode per proto. Parse enough of section 1 to enforce DoS hygiene and routing **before** heavy section 2 parsing.

**Periodic Anchor obligation:** At least every **`K`** mainnet blocks (and on session/escrow start per policy), the next applicable envelope **SHOULD** include **Anchor** mode (signed `mainnet_height` + `mainnet_height_hash`, **empty** `light_block`). This is **height sync**, not Strong sync.

**Strong escalation:** If a peer’s claimed **`mainnet_height`** differs from the receiver’s **aligned** height by **more than `D`** (default **2**), the sender **MUST** use **Strong** (`LightBlock` verified) for that mainnet attestation, or the receiver rejects the height claim (policy: hold / `INVALID`).

**Short-circuit (Strong only):** If `light_block` is present and `mainnet_height <= local_aligned_height`, the receiver **MAY** skip full `VerifyCommit` when policy treats the message as non-advancing (same spirit as earlier drafts).

**Omit mode:** Between Anchors, section 1 **MUST NOT** include `mainnet_height`, `mainnet_height_hash`, or `light_block`; session nonces / replay binding may still appear in section 1 or in signed section 2 (implementation choice — see Open questions).

### How the two views converge

**User → host** and **host → user**: classify each envelope as **Omit** (no mainnet fields), **Anchor** (signed `H`+hash, empty `light_block`), or **Strong** (`LightBlock` present and verified). Update **local aligned height** from the mainnet follower; update **peer anchor state** from **Anchor** (hash immediate or deferred) and from **Strong**. **Omit** does not carry peer mainnet claims. Details: **Validation pipeline**, **Processing rules**; **Timeout derivation** uses **Anchor** + **Strong** for `height_seen_max` (not bare Omit).

### Role of the host’s mainnet listener

The host advances **local** aligned height from its own follower. It sends **Anchor** on the **`K`**-block schedule; it sends **Strong** when disagreeing with the peer by **`> D`** or when finalization/disputes require proof. **Between** Anchors it sends **Omit** (no mainnet height/hash in section 1). It processes the **deferred queue** for Anchor hash checks as the follower advances.

---

## Body envelope (normative)

Two representations are allowed; implementations SHOULD prefer **protobuf** for compact `light_block` bytes (no base64 expansion).

### Representation A — Protobuf (recommended, compact)

Wire type: serialized `GonkaUserHostEnvelope` (message name is illustrative; place in repo proto when implemented).

- `schema_version` (`uint32`): envelope schema; use `1` for this document.
- `height_sync` (`HeightSyncSection`): section 1 — height, proof, metadata, sender signature (see table below).
- `message_body` (`bytes`): section 2 — opaque application payload (JSON, protobuf, etc.).

#### `HeightSyncSection` fields

- `session_id` (`string`): session / escrow-scoped id.
- `sender_id` (`string`): sender identity (address, slot, etc.).
- `nonce_num` (`uint64`): monotonic message nonce for this session direction.
- `chain_id` (`string`): deployment mainnet `chain_id`. If `light_block` is **non-empty**, MUST equal decoded `light_block` header `chain_id`. On **Anchor** messages (`light_block` empty, `proof_type == height-anchor-v1`), MUST equal configured deployment `chain_id`. **Omit** mode: when section 1 omits mainnet fields entirely, `chain_id` may be omitted or carried for routing only (proto TBD).
- `mainnet_height` (`uint64`): height `H`; **required** for **Anchor** and **Strong**; **MUST NOT** be set in **Omit** mode (between anchors).
- `mainnet_height_hash` (`bytes`): CometBFT `BlockID.Hash` for block `H` (typically 32 bytes); **required** for **Anchor** and **Strong**; **MUST NOT** be set in **Omit** mode.
- `proof_type` (`string`): **`cometbft-light-block-v1`** when `light_block` is non-empty (**Strong**). **`height-anchor-v1`** when `light_block` is empty and height/hash are present (**Anchor**). **Omit** mode: no height attestation — use optional `height_sync` / oneof (see Open questions).
- `light_block` (`bytes`): raw protobuf `tendermint.types.LightBlock` (not base64). **Empty** for **Anchor**; **non-empty** for **Strong**; **absent** with no height/hash for **Omit**.
- `timestamp_unix_ms` (`uint64`): when sender built this section.
- `direction` (`string`): `request` or `response` (or enum in proto).
- `request_id` (`bytes`): binds this envelope to transport (unique per direction).
- `sender_signature` (`bytes`): signature over height-sync signing payload (see Sender signature section).

Implementations **SHOULD** use proto3 **`optional`** or a oneof for `light_block` presence so verifiers distinguish “omit proof” from “zero-length error.”

#### HTTP mapping

- `Content-Type`: implementation-defined vendor type (for example `application/x-protobuf` with agreed message type) or `application/vnd.gonka.user-host-envelope+protobuf`.
- **No** height/proof/signature fields are required in HTTP headers except as needed for routing.

### Representation B — JSON (optional, debugging / tooling)

Top-level object:

- `schema_version`: `1`
- `height_sync`: object with the same logical fields as `HeightSyncSection`; `mainnet_height_hash_hex` (lowercase hex) instead of raw hash bytes; `light_block` as **standard base64** of the **raw protobuf** `LightBlock` bytes.
- `message`: either a nested JSON object or `message_b64` for opaque bytes.

JSON is **larger** (base64 on `light_block`); semantics are identical to protobuf after decoding.

---

## Sender signature (section 1 only)

The **height-sync signing payload** is a deterministic byte string (or its hash, if the signature algorithm requires a fixed digest input — document per algorithm). Normative content to bind:

`height_sync_signing_input = "gonka.height_sync.v1" || session_id || sender_id || nonce_num || chain_id || LE64(mainnet_height) || mainnet_height_hash || proof_type || SHA256(light_block_raw_bytes) || LE64(timestamp_unix_ms) || direction || request_id`

- `LE64` = little-endian 8-byte unsigned integer encoding.
- `light_block_raw_bytes` = if `height_sync.light_block` is empty, use **32 zero bytes** as the literal input to `SHA256(...)` in the signing string (i.e. bind “no proof” explicitly). Otherwise: contents after base64 decode in JSON, or the protobuf `bytes` field directly.

`sender_signature` verifies over `height_sync_signing_input` (or `SHA256(height_sync_signing_input)` if the stack signs digests only).

**Note:** Alternative sentinel (e.g. ASCII `"empty"`) is allowed if all implementations agree; document the chosen rule in the repo proto.

**Independence from section 2:** Application payload in `message_body` is **not** included in this input. If the application also signs its payload, that remains a separate signature (or MAC) inside section 2 conventions.

---

## What the mainnet proof is (Cosmos SDK / CometBFT)

Cosmos chains do not expose a separate “mainnet height signature” API. Consensus produces a **quorum of validator precommits** for a unique `(height, BlockID)`. The portable object for “height `H` was finalized with this block hash” is a `**LightBlock`**: `SignedHeader` (`Header` + `Commit`) plus `**ValidatorSet`** so a verifier can check `+⅔` voting power for the correct `BlockID`.

---

## Trust model

### Strong profile (full proof)

Height/hash pair is **strongly trusted** only if **both** hold:

1. CometBFT `LightBlock` verifies `(chain_id, height, block_hash)` (see **CometBFT `LightBlock` verification**).
2. `sender_signature` verifies the height-sync signing payload for this transport context.

If either fails, the envelope MUST NOT advance **strong** anchor state (`H_anchor`).

### Anchor profile (periodic height sync)

When `proof_type == height-anchor-v1`, `light_block` is **empty**, and **`mainnet_height`** / **`mainnet_height_hash`** are present with valid `sender_signature`:

- If the receiver’s block store already has height **`H`**, it **MUST** verify **`mainnet_height_hash`** equals canonical **`BlockID.Hash`** for **`H`** (or reject).
- Otherwise it **MUST** **enqueue deferred verification** for **`(H, hash)`** when the follower reaches **`H`**.

If deferred verification fails (hash mismatch), the Anchor is **invalid** for evidence / advancing peer anchor state.

### Omit mode (between anchors)

No mainnet attestation in section 1. Peers do not learn new height claims from the envelope; they use the **last Anchor** and their **local follower**.

### Combined rule

**Strong** updates consensus-grade peer height state. **Anchor** updates signed peer **`(H, hash)`** subject to immediate or deferred verification. **Omit** does not update peer mainnet state from section 1.

### Mainnet validators are not subnet hosts

The signatures inside `Commit` are from the **Cosmos / CometBFT validator set of the Gonka mainnet (L1)** for that height — the operators who run consensus on **this chain**, not the **subnet / devshard host** roster.

You **do not** prove with a `LightBlock` that “these signers are valid Gonka subnet participants.” You prove that **this chain**, at **this height**, finalized **this block** under that chain’s staking/consensus rules. That is the right object for:

- a shared **block-clock** (timeouts in blocks, epoch boundaries),
- **randomness / anchor** binding to canonical mainnet history,
- agreement that user and host refer to the **same** `chain_id` and tip regime.

**Why that is enough for height sync:** Subnet correctness (who may host, who signed inference, escrow membership) is established by **other** means: on-chain registration, subnet slot keys, escrow/group crypto, gossip finalization, etc. The envelope’s section 1 only pins **mainnet time**; section 2 + subnet protocols pin **work authorship**.

**What you must still trust for the proof to mean “Gonka mainnet”:**

- **`chain_id`** matches the deployment’s intended network (and optionally genesis / fork version policy).
- Verifier follows **this** chain’s software (CometBFT version, signature schemes) when running `VerifyCommit`.
- Full nodes additionally rely on **sync** and **fork choice** so their `local_tip` is the canonical fork; light-client-style verifiers need an appropriate **trusted base** or RPC policy (out of scope here).

### Epoch-bound escrow: anchoring `LightBlock` to epoch L1 validators

When a subnet **escrow’s on-chain lifetime is limited to a single mainnet epoch** and **finalization is driven by epoch switch** (see [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)), **escrow start does not need to embed the L1 validator list.** The **authoritative CometBFT validator set for heights in that epoch** is always obtainable from **mainnet epoch participants** (the chain’s canonical definition of who may sign blocks during that epoch — e.g. staking / epoch / consensus-participant state; exact module and query path is chain-specific).

Every host derives the **same expected** validator material for a given height `H` (or epoch index) from **that public state**, then checks peer `LightBlock`s against it.

**Why:** A bare `LightBlock` proves “+⅔ of *this embedded* `validator_set` signed this block,” but a malicious peer could embed a **fake** set. After Step 3 (embedded set matches `header.validators_hash`), you still need to know that set is the **real** one for mainnet at that height. **Epoch participants on mainnet** supply that ground truth: compute `expected_vals_hash` (or compare full `ValidatorSet`) from mainnet for the epoch/height that applies to this escrow, and require equality with `vals_hash` from the proof (allowing per-height validator updates inside the epoch if the chain rotates the set by height — then `expected_vals_hash` is taken for **`H_claimed`**, not a single static root for the whole epoch unless the protocol fixes one).

**How hosts obtain it:** Prefer a **synced full node** (application state + block store). **Light** verifiers need an **IAVL/state proof**, **multiple RPC quorum**, or another agreed trust path to the same epoch-participant state — same bootstrap problem as any light client, but the **source of truth is mainnet epoch data**, not fields duplicated in the escrow start message.

**Once per epoch (operational):** Each host **fetches the epoch participant list once** when that epoch becomes relevant (e.g. at epoch boundary or first escrow/message tied to that epoch). It **maps every participant to CometBFT consensus validator addresses** using the same rules as the chain (typically derived from each participant’s **consensus public key** so addresses match `Validator.address` in blocks and `ValidatorAddress` in commits — e.g. Cosmos SDK / CometBFT address derivation from the `PubKey` type the chain registers). The host **caches** that list for the whole epoch and uses it to build the **expected** `ValidatorSet` / `validators_hash` for Step 3b (and to sanity-check precommit signers against the allowlist if desired). If participant membership or keys change mid-epoch per mainnet rules, refresh policy follows the chain spec (e.g. re-fetch on notified updates, or derive per-height set from state).

**Escrow start** may still record **`epoch_id`** or bounds **implicitly** (e.g. from the block height of escrow creation); that only selects **which** epoch’s participant set to use, not a second copy of validator keys.

If a product requirement is “height attestation must also cite **subnet** membership,” that remains a **separate** layer (host keys, escrow slots, etc.) — orthogonal to **epoch L1 validator** anchoring above.

---

## CometBFT `LightBlock` verification

Normative when `proof_type == cometbft-light-block-v1` **and** `height_sync.light_block` is **non-empty**. Matches Cosmos SDK / CometBFT full node or light client checks for “block `H` with this `BlockID` was finalized.”

When `light_block` is **empty** and `proof_type == height-anchor-v1`, skip this section (**Anchor** uses hash check / deferred path, not `VerifyCommit`). When section 1 has **no** mainnet fields (**Omit**), skip this section.

### Inputs from `HeightSyncSection`

- `chain_id_claimed` ← `height_sync.chain_id`
- `H_claimed` ← `height_sync.mainnet_height`
- `block_hash_claimed` ← `height_sync.mainnet_height_hash` (raw bytes; JSON: decode from hex)
- `proof_bytes` ← `height_sync.light_block` (raw protobuf bytes; non-empty)

### Step 1 — Decode

1. Unmarshal `proof_bytes` as `tendermint.types.LightBlock` → `lb`.
2. Let `sh = lb.signed_header`, `hdr = sh.header`, `commit = sh.commit`, `valset = lb.validator_set`.
3. Reject if any of `hdr`, `commit`, `valset` is missing.

### Step 2 — Header vs claimed scalars

1. Reject unless `hdr.chain_id == chain_id_claimed`.
2. Reject unless `uint64(hdr.height) == H_claimed`.
3. Reject unless `len(block_hash_claimed) == len(block_id.hash)` and matches CometBFT expectations (typically 32 bytes).

### Step 3 — Validator set binds to header

1. Compute `vals_hash = MerkleHash(validator_set)` per CometBFT (same as `Header.validators_hash`; use the chain’s library).
2. Reject unless `bytes_equal(vals_hash, hdr.validators_hash)`.

### Step 3b — Match validator set to mainnet epoch participants

**Run this step only when all of the following hold:**

- The verifier is validating a `height_sync` in the context of a known **`escrow_id`** (or session bound to one escrow), and
- That escrow is under the **epoch-bound** policy (single-epoch lifetime, finalization at epoch switch; see **Epoch-bound escrow** under **Trust model**), and
- The verifier can obtain **`expected_vals_hash`** (or equivalent) for **`H_claimed`** from **cached epoch participants** (fetched once per epoch from mainnet and converted to CometBFT validator addresses — see **Once per epoch** above), or by an equivalent per-height query if the set is not static.

Then reject unless `vals_hash` equals **`expected_vals_hash`** for that height (or matches the chain’s rule if the set evolves within the epoch). Also reject if `H_claimed` falls outside the escrow’s allowed epoch height range.

**Skip Step 3b** when any of the following hold:

- **No escrow context:** height sync not tied to an epoch-bound escrow.
- **Cannot resolve epoch participants:** verifier has no synced node, no state proof, and no agreed substitute — then Step 3b cannot run; trust falls back to full-node cross-check of `Block(H)` only, light-client trusted base, or policy (see **Trust model**).
- **Multi-epoch or non-epoch-finalized escrows:** no single epoch-bound participant query is defined for the escrow; Step 3b does not apply unless a future spec defines it.
- **Explicit test / dev profile:** documented mode that disables this check (testenv only).

### Step 4 — Expected `BlockID` for this header

1. Compute `block_id = MakeBlockID(hdr)` / `Header.Hash()` per CometBFT (version-matched).
2. Reject unless `bytes_equal(block_id.hash, block_hash_claimed)`.

### Step 5 — Commit quorum (+⅔ precommits for this `BlockID`)

1. Reject unless `commit.height == hdr.height`.
2. Using `valset`, run **`VerifyCommit`** semantics for `(chain_id_claimed, block_id, H, commit)` (Ed25519 precommit signatures, **voting power** sum, strict `> 2/3`).

Prefer calling CometBFT helpers (`ValidatorSet.VerifyCommit`, light client paths) so sign bytes match exactly.

### Step 6 — Optional hardening

1. Replay policy on old commits.
2. If verifier is a **full node**: cross-check `lb` against stored `Block(H)`.

### Outcome

- All steps pass → mainnet proof **VALID**.
- Any step fails → **INVALID**; do not update height state from this **Strong** attestation.
- If mainnet data is invalid, treat as sender/receiver cheating and use `section 1` as evidence (Strong path). **Anchor** hash failures use **DEFERRED_FAIL** / immediate reject per **Anchor profile**.

### Liveness / “latest height” semantics

A proof can be cryptographically **VALID** but **stale**. For “latest seen height,” additionally require:

- `H >= local_trusted_tip - max_lag_blocks`, and/or
- `H` on the verifier’s canonical main fork.

Otherwise classify **VALID_STALE**: do not advance timeout counters; may still archive for disputes.

---

## Validation pipeline (per inbound envelope)

1. Parse body into envelope (protobuf or JSON).
2. Classify **Omit** vs **Anchor** vs **Strong** from presence of `mainnet_height` / `mainnet_height_hash` / `light_block` / `proof_type` (see **HeightSyncSection**).
3. **Omit path:** no mainnet fields; verify session framing / signatures per product proto; do not run CometBFT verification; do not update peer height from section 1.
4. **Anchor path:** `proof_type == height-anchor-v1`, `light_block` empty, height+hash present. If **`|H_claim − H_local_aligned| > D`**, **INVALID** (sender **MUST** use **Strong**, not Anchor, for that claim). Otherwise verify `sender_signature`; verify hash vs local block **`H`** if available, else **enqueue deferred** check. Do **not** run `VerifyCommit`.
5. **Strong path:** `proof_type == cometbft-light-block-v1`, `light_block` non-empty. Run **CometBFT `LightBlock` verification** (including **Step 3b** when applicable). If the peer’s claimed height differs from local aligned by **> `D`**, absence of a verifiable Strong attestation on that message makes any height-bearing section **INVALID**.
6. Verify `sender_signature` over the agreed signing payload (see **Sender signature**; **Omit** may use a different binding — open).
7. Apply **recency** for **Strong** / **Anchor** when updating timeout-driving max height per policy.
8. Anti-replay: unique `request_id`, monotonic `nonce_num`, optional dedup on `(H, block_hash)` for Anchor/Strong.
9. Classify per **Validation result classes** below.
10. Then parse and handle **`message_body`** / `message` (section 2) if not **INVALID**.

### Validation result classes

- **VALID_OMIT:** no mainnet attestation; framing/signature OK; process section 2 without updating peer mainnet height from section 1.
- **VALID_ANCHOR:** Anchor signature OK; hash verified immediately **or** deferred check enqueued successfully; may update peer anchor schedule / `height_seen_max` per policy once hash is confirmed.
- **VALID_STRONG:** `LightBlock` verification passed (or allowed short-circuit), signature/replay OK; may advance strong anchor / aligned height.
- **VALID_STALE:** Strong (or Anchor with proof-like recency rules if added): attestation OK but recency fails for timeout advancement.
- **DEFERRED_FAIL:** deferred Anchor verification found hash mismatch → misrepresentation evidence.
- **INVALID:** malformed envelope, bad signature, replay violation, Anchor hash mismatch when block **`H`** was already local, Strong proof failure, or height claim **> `D`** without valid Strong.

**Deprecated:** **VALID_LIGHTWEIGHT** / per-message trusted window (removed). Map old **VALID_TRUSTED** (full proof) → **VALID_STRONG**.

---

## Processing rules

### On host receiving user request

1. Run validation pipeline; if **INVALID**, reject.
2. If **VALID_STRONG** and `H_u` may advance policy, update **strong anchor** / `host_aligned` per deployment rules.
3. If **VALID_ANCHOR**, update peer **`(H, hash)`** per Anchor rules (immediate or deferred); may contribute to **`height_seen_max`** once hash is confirmed (policy).
4. If **VALID_OMIT**, do not update peer mainnet height from section 1.
5. Process section 2 (application).
6. Respond with section 1 mode: **Omit** between anchors; **Anchor** on **`K`**-block schedule; **Strong** when **`|Δ| > D`** or policy requires.

### On user receiving host response

1. Same validation; if **VALID_STRONG**, update user strong anchor when policy allows.
2. If **VALID_ANCHOR**, apply Anchor + deferred rules on the user side.
3. If **VALID_OMIT**, no peer height update from section 1.
4. Process section 2.

### On host mainnet listener

- Maintain proof cache for `[tip-K, tip]` to fill `light_block` cheaply when sending **Strong**.
- Process **deferred verification queue** for **Anchor** as local tip advances; emit evidence on **DEFERRED_FAIL**.
- Emit **Anchor** on **`K`**-block schedule; **Strong** only when **`|Δ| > D`** or finalization/dispute; **Omit** otherwise.

---

## Timeout derivation

For a session/escrow:

- `height_seen_request_max_trusted` / `height_seen_response_max_trusted` SHOULD be derived from **VALID_STRONG** and from **VALID_ANCHOR** once the Anchor’s **`(H, hash)`** is **confirmed** (immediate local match or successful deferred check). **VALID_OMIT** does not advance peer height. **VALID_ANCHOR** with **pending** deferred hash **SHOULD NOT** advance `height_seen_max` until the check passes (policy).
- `height_seen_max = max(height_seen_request_max_trusted, height_seen_response_max_trusted, host_chain_tip_trusted)`

Timeout:

- `timed_out = (current_chain_height - height_seen_max) >= timeout_blocks`

Finalization timeout evidence should include **serialized section-1 blobs** (or hashes + reproducible archives) for **Strong** anchors, not HTTP headers.

Consumed by `[FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)` (`USER_TIMEOUT` trigger).

---

## Randomness source integration

- `rand_seed = H(finalization_hash || finalized_height_anchor || block_hash_anchor)`

Anchors for randomness should be **Strong**-verified (`LightBlock` or full-node equivalent) near the timeout/finalization decision, or **Anchor** hashes that have been **confirmed** against local canonical blocks — not unconfirmed deferred Anchors, and not **Omit** traffic.

---

## Security properties

- Avoids header size limits and proxy fragility: **Omit** + **Anchor** on inference path; **Strong** only on disagreement **> `D`** or finalization/disputes.
- **Anchor** gives signed periodic alignment without `LightBlock` every **`K`** blocks.
- **Strong** ties dispute-grade claims to mainnet consensus.
- Section 1 can be parsed **before** heavy section 2 where policy requires.
- Bad **Anchor** hashes are caught by immediate check (if block local) or **deferred** check; **> `D`** disagreement without **Strong** is rejected.

---

## Backward compatibility and rollout

1. Accept legacy envelopes that put `(height, hash)` on every message → migrate to **Omit** + periodic **Anchor**.
2. Soft-enforce **Anchor** every **`K`** blocks and **Strong** when **`|Δ| > D`**; warn on violations.
3. Hard-reject mainnet height/hash on envelopes that should be **Omit** (between anchors) once rollout completes.

---

## Open questions

- **Exact protobuf package/name for `GonkaUserHostEnvelope` and registration of `Content-Type`.** Fix the repo proto package and a normative MIME (or vendor `Content-Type`) so every implementation unmarshals the same top-level message; generic `application/x-protobuf` is not enough without an agreed message type or schema hook.

- **Pinning `cometbft-light-block-v1` to CometBFT / SDK versions on mainnet.** Treat it operationally as: Gonka mainnet release **X** runs CometBFT **Y** (and matching SDK); verifiers **MUST** use that family of `LightBlock` protobuf types and verification code (`VerifyCommit`, `BlockID`/header hashing, validator-set Merkle rules). If those semantics diverge incompatibly across upgrades, bump to a new `proof_type` (e.g. `cometbft-light-block-v2`) instead of overloading `v1`.

- **Canonical `sender_id` for multi-key users.** Real users may use multiple keys (rotation, devices, escrow slot keys vs wallet keys). Receivers need **one stable string per logical sender** so section-1 signatures and anti-replay (`session_id`, `nonce_num`, dedup) always refer to the same party; the spec must say whether that id is e.g. escrow participant, slot id, or mainnet account, and how signing keys map to it.

- **Whether devshard allows protobuf-only or supports both JSON with protobuf.** Decide the dev/test profile: strict protobuf-only (parity with production) vs also accepting the JSON envelope for curl/tooling and mocks; that choice drives compliance tests and mock servers.

- **`K`** (Anchor period, e.g. 10 mainnet blocks) and **`D`** (mandatory Strong threshold, e.g. 2 blocks) — exact scalar definitions for `|H_peer − H_local_aligned|`.

- **`height_seen_max` / `USER_TIMEOUT` with pending deferred Anchors.** Whether partial credit before hash confirms (see [CPOC_PROTOCOL.md](./CPOC_PROTOCOL.md)).

- **Signing input for Anchor:** `proof_type == height-anchor-v1` with empty `light_block` — confirm use of **SHA256(32 zero bytes)** for `light_block` hash slot in `height_sync_signing_input` (or a distinct **Anchor** signing domain).

- **`height-anchor-v1` normative name** and JSON interop.

- **Omit mode framing:** how `session_id` / `nonce` / signatures bind `message_body` when `height_sync` is absent (unified vs separate `SessionFraming` message).

- **Nonce ↔ height for downstream specs:** When section 1 is often **Omit**, which diff **nonce** anchors “height at nonce **N**” for inequalities in other proposals (e.g. [cPoC skip / nonce binding](./CPOC_PROTOCOL.md)) — **first carry** of evidence vs **executor** nonce of the skip response vs other — must be defined here so height bookkeeping is unambiguous without per-message `(H, hash)`.

- **`K` / `D` / prepare windows:** Exact values and whether **cPoC prepare** (or similar) phases reuse the same **Omit** / **Anchor** / **Strong** cadence as normal traffic (replaces obsolete “trusted window blocks” + “proof every N messages” wording).

- **Anchor `DEFERRED_FAIL` (hash mismatch after catch-up):** Proof / punishment obligation when a signed **Anchor** `(H, hash)` disagrees with canonical mainnet after the follower reaches **`H`** — which party is accountable (user vs host), what evidence is attached to finalization / mainnet, and whether slashing applies (consumer protocols such as [cPoC](./CPOC_PROTOCOL.md) depend on this rule).

---

## Status

Draft proposal.
