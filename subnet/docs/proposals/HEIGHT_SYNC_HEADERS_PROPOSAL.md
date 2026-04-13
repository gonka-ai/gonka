# Height sync envelope proposal (structured HTTP body)

## Summary

User–host traffic carries a **two-section HTTP body**: **(1) height sync** — mainnet height, CometBFT `LightBlock` proof, metadata, and a **sender signature** over that section; **(2) message** — the normal application payload (which may use its own signing rules). Large proofs stay out of HTTP headers; only routing metadata (for example `Content-Type`) belongs in headers.

Historical note: an early draft put proofs in headers; that hits size limits. **Normative transport is the structured body below.**

Related proposal: `[FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)`.

---

## Goals

1. Keep all participants close to the same latest-heilistght view.
2. Make timeout triggers auditable and cryptographically provable.
3. Provide consistent chain-height source for validation and finalization randomness and trigger checks.
4. Keep **height/proof attestation** cryptographically separate from the **application message** (second section).
5. Ensure height is trusted only if the CometBFT proof verifies under mainnet consensus rules.
6. Avoid oversized or fragmented HTTP headers; carry `LightBlock` as **raw bytes** inside section 1 when using protobuf.

## High-level protocol (request / response and height alignment)

Two parties exchange work over HTTP: **user** (client) and **host** (server). Every **request** and every **response** uses the same **envelope**: **section 1** (height sync + proof + sender signature) and **section 2** (message body). Parse section 1 enough to read scalars (height, chain id, nonce, signature fields) **before** heavy parsing of section 2 (DoS hygiene).

**Short-circuit (lower-or-equal height):** If `height_sync.mainnet_height` is **less than or equal to** the receiver’s **local aligned height** (the same value used in `max(receiver_aligned, H_peer)` for timeout / alignment), the sender’s height **cannot** raise aligned height or `height_seen_max`. The receiver **MAY skip CometBFT `LightBlock` verification** for that message (`VerifyCommit`, validator-set checks, etc.), since that proof will never be used for state. Implementations **SHOULD** still verify `**sender_signature`** and anti-replay rules on section 1 when they are required for session authenticity; only the **mainnet proof** path is optional in this case.

### How the two views converge

**User → host** and **host → user** follow the same pattern: validate section 1 (signature/replay; CometBFT proof unless short-circuited), optionally raise **aligned height** to `max(local, H_peer)` when allowed, then handle section 2. Each side’s aligned height is monotone and tends toward the **max** of its own chain/RPC tip and **peer heights that verified**; stale or invalid proofs never increase it. Details: **Validation pipeline**, **Processing rules**; timeout formulas: **Timeout derivation**.

### Role of the host’s mainnet listener

The host advances aligned height from its own chain follower. Outgoing responses should attach a **fresh `LightBlock`** for a height **at least** as high as `max(peer_aligned, local_tip)` when policy allows, so the user stays chain-anchored even if the client is behind.

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
- `chain_id` (`string`): must equal decoded `light_block` header `chain_id`.
- `mainnet_height` (`uint64`): height `H` this proof attests.
- `mainnet_height_hash` (`bytes`): CometBFT `BlockID.Hash` for block `H` (typically 32 bytes).
- `proof_type` (`string`): must be `cometbft-light-block-v1`.
- `light_block` (`bytes`): raw protobuf `tendermint.types.LightBlock` (not base64).
- `timestamp_unix_ms` (`uint64`): when sender built this section.
- `direction` (`string`): `request` or `response` (or enum in proto).
- `request_id` (`bytes`): binds this envelope to transport (unique per direction).
- `sender_signature` (`bytes`): signature over height-sync signing payload (see Sender signature section).

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
- `light_block_raw_bytes` = contents of `height_sync.light_block` after base64 decode in JSON, or the protobuf `bytes` field directly.

`sender_signature` verifies over `height_sync_signing_input` (or `SHA256(height_sync_signing_input)` if the stack signs digests only).

**Independence from section 2:** Application payload in `message_body` is **not** included in this input. If the application also signs its payload, that remains a separate signature (or MAC) inside section 2 conventions.

---

## What the mainnet proof is (Cosmos SDK / CometBFT)

Cosmos chains do not expose a separate “mainnet height signature” API. Consensus produces a **quorum of validator precommits** for a unique `(height, BlockID)`. The portable object for “height `H` was finalized with this block hash” is a `**LightBlock`**: `SignedHeader` (`Header` + `Commit`) plus `**ValidatorSet`** so a verifier can check `+⅔` voting power for the correct `BlockID`.

---

## Trust model

Height is trusted only if **both** hold:

1. CometBFT `LightBlock` verifies `(chain_id, height, block_hash)` (see below).
2. `sender_signature` verifies the height-sync signing payload for this transport context.

If either fails, height must not advance aligned-height state.

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

Normative when `proof_type == cometbft-light-block-v1`. Matches Cosmos SDK / CometBFT full node or light client checks for “block `H` with this `BlockID` was finalized.”

### Inputs from `HeightSyncSection`

- `chain_id_claimed` ← `height_sync.chain_id`
- `H_claimed` ← `height_sync.mainnet_height`
- `block_hash_claimed` ← `height_sync.mainnet_height_hash` (raw bytes; JSON: decode from hex)
- `proof_bytes` ← `height_sync.light_block` (raw protobuf bytes)

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
- Any step fails → **INVALID**; do not update aligned height.
- If mainnet data is invalid we should trust this as sender/replyer cheating and use `section 1` as the proof for punishment.

### Liveness / “latest height” semantics

A proof can be cryptographically **VALID** but **stale**. For “latest seen height,” additionally require:

- `H >= local_trusted_tip - max_lag_blocks`, and/or
- `H` on the verifier’s canonical main fork.

Otherwise classify **VALID_STALE**: do not advance timeout counters; may still archive for disputes.

---

## Validation pipeline (per inbound envelope)

1. Parse body into envelope (protobuf or JSON).
2. Extract `height_sync`; reject if missing required fields.
3. Static checks: configured `chain_id`, `mainnet_height > 0`, nonce/session bounds, timestamp skew, `proof_type`.
4. **Short-circuit:** if `mainnet_height <= local_aligned_height`, skip step 5 (CometBFT proof verification) for this envelope; aligned height is unchanged by definition. If `mainnet_height > local_aligned_height`, require step 5.
5. Verify CometBFT proof (only when not short-circuited): `proof_bytes` and claimed scalars (**CometBFT `LightBlock` verification**, including **Step 3b** when the escrow is **epoch-bound** and the verifier can resolve **epoch participants** from mainnet).
6. Verify `sender_signature` over `height_sync_signing_input` (or its digest), unless a documented profile waives section-1 signing for restricted test modes.
7. Apply **recency** when updating aligned height (not when only storing evidence); when short-circuited, recency for “raising” height does not apply (height does not raise).
8. Anti-replay: unique `request_id`, monotonic `nonce_num`, optional dedup on `(H, block_hash)`.
9. **VALID_TRUSTED** for height state when: either short-circuit with valid signature/replay rules, or full proof verification passes and recency OK for a strictly higher `mainnet_height`.
10. Then parse and handle **`message_body`** / `message` (section 2).

### Validation result classes

- **VALID_TRUSTED:** section-1 signature and replay rules OK, and either **(a)** `mainnet_height <= local_aligned_height` with **proof verification skipped** (short-circuit), or **(b)** full CometBFT proof OK and recency OK for a **strictly higher** `mainnet_height` that may advance aligned height.
- **VALID_STALE:** `mainnet_height > local_aligned_height`, proof + signature OK, but recency fails; do not advance aligned height.
- **VALID_UNTRUSTED:** `mainnet_height > local_aligned_height`, section-1 signature OK but proof missing/invalid; no height state update.
- **INVALID:** malformed envelope, bad signature, or replay violation → reject entire HTTP message (do not process section 2).

---

## Processing rules

### On host receiving user request

1. Run validation pipeline; if **INVALID**, reject.
2. If **VALID_TRUSTED** and `H_u > host_aligned`, advance `host_aligned`.
3. Process section 2 (application).
4. Respond with an envelope whose section 1 includes host proof + host signature; section 2 carries the application response.

### On user receiving host response

1. Same validation; if **VALID_TRUSTED** and `H_h > user_aligned`, advance `user_aligned`.
2. Process section 2.

### On host mainnet listener

- Maintain proof cache for `[tip-K, tip]` to fill `light_block` cheaply.
- When building responses, prefer height `max(peer_aligned, local_tip)` with fresh `LightBlock`.

---

## Timeout derivation

For a session/escrow:

- `height_seen_request_max_trusted` / `height_seen_response_max_trusted` from **section 1** of validated user requests and host responses.
- `height_seen_max = max(height_seen_request_max_trusted, height_seen_response_max_trusted, host_chain_tip_trusted)`

Timeout:

- `timed_out = (current_chain_height - height_seen_max) >= timeout_blocks`

Finalization timeout evidence should include **serialized section-1 blobs** (or hashes + reproducible archives), not HTTP headers.

Consumed by `[FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)` (`USER_TIMEOUT` trigger).

---

## Randomness source integration

- `rand_seed = H(finalization_hash || finalized_height_anchor || block_hash_anchor)`

Anchors should come from verifiable mainnet data near the timeout/finalization decision (same `LightBlock` / tip proofs).

---

## Security properties

- Avoids header size limits and proxy fragility for large proofs.
- Timeout and alignment evidence refer to **attested section 1**, replayable by third parties.
- Section 1 can be verified **before** expensive section 2 work.
- Forged heights without valid `LightBlock` do not move state.

---

## Backward compatibility and rollout

1. Accept envelope with section 1 optional → log-only.
2. Soft-enforce section 1 present; warn on missing/invalid proof.
3. Hard-reject missing or invalid section 1 for production paths.

---

## Open questions

- **Exact protobuf package/name for `GonkaUserHostEnvelope` and registration of `Content-Type`.** Fix the repo proto package and a normative MIME (or vendor `Content-Type`) so every implementation unmarshals the same top-level message; generic `application/x-protobuf` is not enough without an agreed message type or schema hook.

- **Pinning `cometbft-light-block-v1` to CometBFT / SDK versions on mainnet.** Treat it operationally as: Gonka mainnet release **X** runs CometBFT **Y** (and matching SDK); verifiers **MUST** use that family of `LightBlock` protobuf types and verification code (`VerifyCommit`, `BlockID`/header hashing, validator-set Merkle rules). If those semantics diverge incompatibly across upgrades, bump to a new `proof_type` (e.g. `cometbft-light-block-v2`) instead of overloading `v1`.

- **Canonical `sender_id` for multi-key users.** Real users may use multiple keys (rotation, devices, escrow slot keys vs wallet keys). Receivers need **one stable string per logical sender** so section-1 signatures and anti-replay (`session_id`, `nonce_num`, dedup) always refer to the same party; the spec must say whether that id is e.g. escrow participant, slot id, or mainnet account, and how signing keys map to it.

- **Whether devshard allows protobuf-only or supports both JSON with protobuf.** Decide the dev/test profile: strict protobuf-only (parity with production) vs also accepting the JSON envelope for curl/tooling and mocks; that choice drives compliance tests and mock servers.

---

## Status

Draft proposal.
