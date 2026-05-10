# Height sync proposal (structured HTTP body — sync-turn anchors, Strong on disagreement)

## Summary

User–host traffic uses a **two-section HTTP body**: **(1)** optional **mainnet attestation** — signed `mainnet_height` + `mainnet_block_hash` on an **Anchor** schedule, or full `LightBlock` in **Strong** mode, plus session fields; **(2)** the **application message**. `LightBlock` proofs are **never** sent on the periodic path.

**Normative cadence** (per session direction, on outgoing nonce `n`):

- **Anchor** is emitted when `n` falls inside a **sync-turn window** of `slots_num` consecutive nonces. Windows are:
  - **Initial sync turn:** `1 <= n <= slots_num` — bootstraps the session by walking the round-robin once across all host slots.
  - **Periodic sync turns:** for every `i >= 1`, `i*K <= n <= i*K + slots_num - 1`.
- **Omit** between sync turns.
- **Manual-force** is **not** a per-message flag — it opens a **forced sync turn** of `slots_num` consecutive nonces, identical in width to a cadence sync turn (so every host slot in the round-robin gets exactly one Anchor obligation). Re-issuing the trigger while a forced turn is still active is silently ignored. A forced window that touches/overlaps the next cadence window swallows it (no double-Anchor on the boundary). See §"Forced sync turn (manual-force, normative)".
- Constraint: `K >= slots_num` so sync turns never overlap.

**Strong** (`LightBlock` + `VerifyCommit`) is required when `|H_peer − H_local_aligned| > D` (default `D = 2`) and for finalization / dispute evidence. Carry-forward of an Anchor received from another host is also bounded by `D`: outside the band the forwarder MUST request Strong rather than re-stating the foreign `(H, hash)`.

**Why sync-turn windows of `slots_num` nonces.** Users round-robin requests across the escrow's `slots_num` host slots. A single-nonce Anchor would only sync one host per period; a window of `slots_num` consecutive nonces guarantees every host both sees an Anchor request and replies with its own Anchor inside one sync turn.

Related proposals:

- [Dataflow-only summary](../height-sync-dataflow.md)
- [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)
- [`CPOC_PROTOCOL.md`](./CPOC_PROTOCOL.md)

---

## Goals

1. Align mainnet time with periodic Anchor messages (`H` + `BlockID.Hash`, **signed**, no `LightBlock`).
2. Use Strong (`LightBlock`) **only** when `|H_peer − H_local_aligned| > D` (default `D = 2`) or when finalization / disputes require it — never as the default cadence.
3. Make timeout and dispute triggers auditable: Anchors plus deferred hash verification when block `H` is not yet local; Strong when disagreement exceeds `D`.
4. Keep mainnet attestation (when present) separate from the application message.
5. No mainnet `height` / `hash` fields between sync turns (no per-message "trusted window" on the inference path).
6. Manual-force points (cPoC carry, dispute open, operator force) drive a full **forced sync turn** of `slots_num` consecutive nonces — not a per-envelope override — so the whole round-robin group aligns on the same `(H, hash)` before sensitive transitions (§"Forced sync turn").
7. Provide cryptographic **provenance** for any `(H, hash)` pair propagated across hosts so deferred-mismatch and same-height/different-hash disputes are attributable to the originating signer, not the propagator (§"Provenance, carry-forward, and malware-host attribution").

---

## Sync modes

| Mode       | Section 1: `mainnet_height` / `mainnet_block_hash` | `light_block`       | When                                                                                                                   |
| ---------- | -------------------------------------------------- | ------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| **Omit**   | Must not be sent                                   | absent              | Between sync turns (default)                                                                                           |
| **Anchor** | Present, signed; hash vs local `H` or deferred     | empty               | Inside a sync-turn window (initial `1..slots_num` or periodic `i*K..i*K+slots_num-1`) and at manual-force points       |
| **Strong** | Present; must match `LightBlock` header            | non-empty, verified | `\|H_peer − H_local_aligned\| > D` (default `D = 2`) or finalization / dispute                                         |

Periodic sync is **Anchor** only. Strong is for disagreement `> D`, not for each cadence tick.

**Deferred verification (Anchor):** If block `H` is not yet local, enqueue `(H, hash, origin_sender_id, origin_signed_blob)`; when synced, compare to canonical `BlockID.Hash`. Mismatch ⇒ misrepresentation evidence **attributed to `origin_sender_id`** (the host that originally signed the claim), not to the carrier — see §"Provenance, carry-forward, and malware-host attribution".

---

## High-level protocol

Two parties exchange work over HTTP: **user** (client) and **host** (server). Each request and response carries section 2 (message body). Section 1 is present in Anchor / Strong modes, omitted otherwise.

**Sync-turn obligation.** Every envelope whose outgoing nonce satisfies `n <= slots_num` (initial) or `n % K < slots_num && n >= K` (periodic) **MUST** be Anchor. All other nonces **MUST** be Omit unless a manual-force policy point applies.

**Strong escalation.** If a peer's claimed `mainnet_height` differs from the receiver's aligned height by more than `D`, the sender **MUST** use Strong for that attestation, or the receiver rejects the height claim.

**Convergence.** User → host and host → user classify every envelope as Omit / Anchor / Strong from the presence of `mainnet_height`, `mainnet_block_hash`, `light_block`, and `proof_type`. The receiver updates its local aligned height from its own mainnet follower; it updates peer anchor state only from Anchor (immediate or deferred hash check) or Strong. Omit does not carry peer mainnet claims.

**Mainnet listener.** Each host advances local aligned height from its own follower, emits Anchor on the sync-turn schedule (plus active forced sync turns; see below), emits Strong only when `|Δ| > D` or finalization requires it, and processes the deferred queue as the follower advances.

---

## Forced sync turn (manual-force, normative)

Manual-force is **not** a per-message override of the cadence rule — that semantics aligns only one host slot and leaves the rest of the round-robin out of step. The normative form is a **forced sync turn**: a `slots_num`-wide span of consecutive nonces, identical in width to a cadence sync turn, during which every envelope MUST emit Anchor.

### Trigger and lifecycle

A forced sync turn is opened by a **force directive**:

```text
ForceDirective {
  trigger_nonce : uint64    // nonce of the envelope (or diff) that opens the turn
  end_nonce     : uint64    // = trigger_nonce + slots_num - 1, inclusive
  reason        : string    // e.g. "cpoc_skip_carry", "operator_force", "dispute_open"
  signature     : bytes     // signed by the trigger author over (trigger_nonce, end_nonce, reason, session_id)
}
```

In stateful deployments (e.g. devshard) the directive is carried by a state-machine tx (`MsgForceHeightSyncTurn`) inside the next diff so every node converges on the same forced-turn record. In non-stateful deployments it MAY be carried as a standalone signed blob piggy-backed on the trigger envelope; in both cases nodes derive the same effective `ActiveForcedTurn{start, end, reason}`.

### Normative rules

1. **All envelopes in window MUST be Anchor.** While `ActiveForcedTurn.start <= nonce <= ActiveForcedTurn.end`, both directions (user→host and host→user) MUST emit Anchor regardless of the cadence rule. Receivers MUST treat Omit on those nonces as INVALID.
2. **At most one open turn per session.** A second `ForceDirective` arriving while a turn is still active (`trigger_nonce <= end_nonce`) MUST be **silently ignored**: no extension of `end_nonce`, no re-open, no error. (This is the "any force inside a non-finished sync turn is ignored" rule.)
3. **At most one directive per diff / per envelope.** A diff or trigger envelope carrying two `ForceDirective`s is INVALID.
4. **Coalescing with cadence.** If `[start, end]` touches or overlaps the next cadence window `[i*K, i*K + slots_num - 1]` (i.e. `i*K <= end + 1`), the cadence window for that period is **swallowed** by the forced turn. Concretely: receivers MUST NOT count the boundary nonce as two Anchor obligations (no duplicate audit-ring entries, no "extra" Strong escalation), and senders MUST emit a single continuous Anchor span. The next cadence window opens at the next `i*K` boundary unaffected.
5. **Closure.** When the receiving / sending node sees `nonce > end_nonce` it clears `ActiveForcedTurn`. Cadence resumes from the standard rule.
6. **Audit marking.** Anchors emitted under a forced turn are tagged `forced_turn_reason = <reason>` in audit / debug records (local-only, not on the wire) so dispute evidence can distinguish cadence Anchors from forced ones.

### Why the window must be `slots_num`-wide

The user round-robins requests across `slots_num` host slots. A single-nonce force only Anchors **one** of the slots; the other `slots_num - 1` hosts still see Omit on adjacent nonces and the group does not converge on the same `(mainnet_height, mainnet_block_hash)`. A `slots_num`-wide forced turn guarantees every host slot receives **exactly one** Anchor request and replies with **exactly one** Anchor response inside the same window — the same cross-host alignment property cadence sync turns deliver. This is why `MsgForceHeightSyncTurn` is the normative form and a per-envelope `ForceAnchor` flag is **not**.

---

## Provenance, carry-forward, and malware-host attribution

A malicious host can attempt to poison a session by signing an Anchor with a fabricated `(mainnet_height, mainnet_block_hash)` pair and serving it to a user. If the user re-states that pair to subsequent hosts in the same session (carry-forward), naïve attribution would blame either every receiver or the user. The proposal pins blame on the **originating signer** by requiring the user to retain and forward the originator's verbatim signed blob.

### Definitions

- **Carry-forward.** A user receives Anchor `A_h` with `(H, hash_A)` and `sender_signature` from host `h`, then sends Anchor `A_h'` with the same `(H, hash_A)` to a different host `h'` (typically because `h.mainnet_height > user.local_aligned_height`).
- **Origin attestation.** The verbatim signed `HeightSyncSection` produced by `h`, including its `sender_signature`. The user MUST persist this blob alongside `(H, hash_A)` in its peer-tip cache.
- **`D` semi-trust band.** Carry-forward Anchor is only valid when `|H − receiver.local_aligned| <= D`. Outside the band the receiver MUST require Strong (`LightBlock`); a carrier-forwarded Anchor outside `D` is INVALID.

### Wire requirement

A carry-forward Anchor MUST embed the origin attestation in a new optional field on the envelope's `HeightSyncSection`:

- `origin_attestation` (`bytes`, optional): the **verbatim** protobuf-encoded `HeightSyncSection` of the originating host (i.e. exactly the bytes the originating host signed, including its own `sender_signature`). Empty when the carrier is also the originator (i.e. `mainnet_height` was learnt from the carrier's own oracle).

The carrier signs its **own** `HeightSyncSection` as before; it does **not** alter or re-sign the embedded origin attestation. Receivers MUST treat an Anchor with a non-empty `origin_attestation` as carry-forward and run the carry-forward validation step (below).

### Validation step (carry-forward)

When the inbound Anchor's `origin_attestation` is non-empty:

1. Decode the embedded `HeightSyncSection` `O`.
2. Reject (INVALID) unless `O.session_id == envelope.session_id`, `O.chain_id == envelope.chain_id`, `O.mainnet_height == envelope.mainnet_height`, `O.mainnet_block_hash == envelope.mainnet_block_hash`, and `O.proof_type == "height-anchor-v1"`. (The carrier MUST forward the originator's pair verbatim — any deviation makes the carrier the source of the claim, see §"Attribution rules".)
3. Verify `O.sender_signature` against the originator's signing input over `O`. Failure ⇒ **DISPUTE_CARRIER** (see Result classes): the receiver cannot validate origin, so the carrier is the responsible signer. Record `origin_sender_id = envelope.sender_id` (the carrier).
4. On success, record `origin_sender_id = O.sender_id` (the originating host) for the deferred queue, audit ring, and any dispute evidence. Apply the standard `D` bound: if `|envelope.mainnet_height − receiver.local_aligned| > D`, INVALID (carrier MUST escalate to Strong, not carry-forward).

### Deferred verification with attribution

The deferred Anchor queue stores `(origin_sender_id, H, hash, origin_signed_blob)` — not just `(H, hash)`. When the receiver's mainnet follower advances past `H`:

- **Match** (`hash == canonical BlockID.Hash for H`): drop the entry; no action.
- **Mismatch:** raise `DEFERRED_FAIL` evidence **attributed to `origin_sender_id`**, attaching `origin_signed_blob` (the originator's verbatim signed section) as the cryptographic proof that the named host signed a wrong pair. The carrier is **not** at fault: it merely propagated a signed claim. This is the mechanism for identifying a malware host even when the user has already moved on to other peers.

### Same-height / different-hash dispute trigger

When an inbound Anchor claims `(H, hash_A)` and the receiver already has a confirmed local mapping `(H, hash_B)` from its oracle (or via a previous Strong) with `hash_A != hash_B`:

1. If the inbound is a carry-forward (non-empty `origin_attestation`) and origin verification succeeded, raise dispute evidence against `O.sender_id`. Result class: **DISPUTE_ORIGINATOR**.
2. Else (no `origin_attestation`, or origin signature failed), the carrier is the cryptographic signer of `(H, hash_A)`. Result class: **DISPUTE_CARRIER**, blame goes to `envelope.sender_id`.
3. In both cases the receiver MUST:
   - Log the disagreement at warn level (`disputed_height`, `local_hash`, `peer_hash`, `peer_id_blamed`, `proof_kind = origin | carrier`).
   - Persist the offending signed blob (originator's or carrier's) for the dispute layer.
   - Refuse to advance peer height state from this Anchor.
4. The receiver SHOULD continue serving the session (the disagreement is evidence, not a fatal error) so that finalization can collect the proof.

### Why the user must store origin signatures

If the user fails to embed `origin_attestation` in a carry-forward Anchor, the receiving host has no cryptographic way to attribute `(H, hash_A)` to anyone except the user, who is the only signer present. The user therefore becomes the dispute target. This is the network's primitive for "cannot prove provenance ⇒ you are the source": users are economically incentivized to retain origin signatures for every cached peer tip they intend to carry forward, and to drop or refresh tips they cannot prove.

### Interaction with forced sync turns

A forced sync turn (§"Forced sync turn") cleans up provenance ambiguity for the next `slots_num` nonces by forcing every host to sign its own current `(H, hash)` from its **own** oracle (no carry-forward). After a forced turn closes, the receiver's cache is populated with origin-signed Anchors from every reachable host slot, which is the strongest position from which to re-evaluate disputed `(H, hash)` claims. cPoC manual-force points (`cpoc_skip_carry`, dispute open) MUST therefore use forced sync turns rather than per-message Anchor flags.

---

## Body envelope (normative)

Two representations are allowed; implementations SHOULD prefer protobuf for compact `light_block` bytes.

### Representation A — Protobuf (recommended)

Wire type: `GonkaUserHostEnvelope`.

- `schema_version` (`uint32`): envelope schema; `1` for this document.
- `height_sync` (`HeightSyncSection`): section 1.
- `message_body` (`bytes`): section 2 — opaque application payload.

`HeightSyncSection` fields:

- `session_id` (`string`): session / escrow id.
- `sender_id` (`string`): sender identity (address, slot id).
- `nonce_num` (`uint64`): monotonic nonce per session direction.
- `chain_id` (`string`): deployment mainnet `chain_id`. If `light_block` is non-empty it MUST equal the decoded header `chain_id`.
- `mainnet_height` (`uint64`): height `H`. **Required** for Anchor and Strong; **MUST NOT** be set in Omit mode.
- `mainnet_block_hash` (`bytes`): canonical CometBFT `BlockID.Hash` for block `H` (typically 32 bytes). **Required** for Anchor and Strong; **MUST NOT** be set in Omit mode. The same value forms the audit trail: a stored `(sender_id, mainnet_height, mainnet_block_hash)` whose hash later disagrees with canonical `BlockID.Hash` for `H` is direct evidence the peer cheated.
- `proof_type` (`string`): `cometbft-light-block-v1` for Strong (non-empty `light_block`); `height-anchor-v1` for Anchor (empty `light_block` with height + hash). Omit: no height attestation, use proto3 `optional` / oneof.
- `light_block` (`bytes`): raw protobuf `tendermint.types.LightBlock`. Empty for Anchor, non-empty for Strong, absent for Omit.
- `origin_attestation` (`bytes`, optional): present only on carry-forward Anchor. Verbatim protobuf-encoded `HeightSyncSection` of the originating host (the host that observed `(mainnet_height, mainnet_block_hash)` from its own oracle), including the originator's `sender_signature`. The carrier MUST NOT alter the embedded blob. Receivers verify the embedded signature before treating the claim as originating from `origin_attestation.sender_id`. See §"Provenance, carry-forward, and malware-host attribution" for normative validation rules.
- `force_directive` (`bytes`, optional): present only on the envelope that opens a forced sync turn (§"Forced sync turn (manual-force, normative)"). Signed `(trigger_nonce, end_nonce, reason, session_id)` blob; ignored when a forced turn is already active.
- `timestamp_unix_ms` (`uint64`): when sender built this section.
- `direction` (`string`): `request` or `response`.
- `request_id` (`bytes`): unique transport id per direction.
- `sender_signature` (`bytes`): signature over the height-sync signing payload (see below).

Use proto3 `optional` or a oneof for `light_block` so verifiers distinguish "omit proof" from "zero-length error."

### Representation B — JSON (debugging / tooling)

Top-level: `schema_version`, `height_sync` (with `mainnet_block_hash_hex` lowercase hex; `light_block` standard base64 of the raw protobuf), `message` (nested object) or `message_b64`. Semantics identical after decoding; JSON is larger.

---

## Sender signature (section 1 only)

Deterministic byte string:

```text
height_sync_signing_input =
  "gonka.height_sync.v1" || session_id || sender_id || nonce_num ||
  chain_id || LE64(mainnet_height) || mainnet_block_hash || proof_type ||
  SHA256(light_block_raw_bytes) ||
  SHA256(origin_attestation_raw_bytes) ||
  SHA256(force_directive_raw_bytes) ||
  LE64(timestamp_unix_ms) || direction || request_id
```

- `LE64` = little-endian uint64.
- If `light_block` is empty (Anchor / Omit), use 32 zero bytes as the literal input to `SHA256` (binds "no proof" explicitly).
- If `origin_attestation` is empty (originator-signed Anchor, not carry-forward), use 32 zero bytes — same convention. This explicitly binds "I am not forwarding anyone else's signed claim" into the carrier's signature, so a carrier that **drops** a previously embedded `origin_attestation` cannot claim it was never signed.
- If `force_directive` is empty (regular cadence or in-progress forced turn), use 32 zero bytes — same convention.

`sender_signature` verifies over `height_sync_signing_input` (or `SHA256(...)` if the stack signs digests only). The application payload in `message_body` is **not** included.

---

## What the mainnet proof is

Cosmos chains do not expose a separate "mainnet height signature" API. Consensus produces a quorum of validator precommits for a unique `(height, BlockID)`. The portable object for "height `H` was finalized with this block hash" is a `LightBlock`: `SignedHeader` (`Header` + `Commit`) plus `ValidatorSet` so a verifier can check `+⅔` voting power for the correct `BlockID`.

---

## Trust model

### Strong (full proof)

Height / hash pair is strongly trusted only if both:

1. CometBFT `LightBlock` verifies `(chain_id, height, block_hash)` (see `LightBlock` verification).
2. `sender_signature` verifies the height-sync signing payload.

Either failure ⇒ MUST NOT advance strong anchor state.

### Anchor (periodic height sync)

When `proof_type == height-anchor-v1` and `light_block` is empty with valid `sender_signature`:

- If the receiver's block store already has height `H`, it MUST verify `mainnet_block_hash` equals canonical `BlockID.Hash` for `H` (or reject).
- Otherwise it MUST enqueue deferred verification for `(H, hash)`.

Deferred mismatch ⇒ Anchor invalid for evidence and for advancing peer anchor state.

### Omit

No mainnet attestation; peers do not learn new height claims from the envelope.

### Mainnet validators are not subnet hosts

`Commit` signers are the **CometBFT validator set of Gonka mainnet (L1)** for that height — not the subnet / devshard host roster. A `LightBlock` proves "this chain at this height finalized this block under that chain's consensus rules." Subnet correctness (who may host, who signed inference, escrow membership) is established by other means (on-chain registration, slot keys, escrow crypto, gossip finalization). Section 1 only pins **mainnet time**; section 2 plus subnet protocols pin **work authorship**.

What you must still trust for the proof to mean "Gonka mainnet": `chain_id` matches the deployment, the verifier follows this chain's CometBFT/SDK family when running `VerifyCommit`, and `local_tip` is on the canonical fork.

### Epoch-bound escrow (optional Step 3b)

When an escrow's on-chain lifetime is one mainnet epoch and finalization is driven by epoch switch, the authoritative validator set is obtainable from **mainnet epoch participants**, so escrow start does **not** need to embed the L1 validator list.

Each host fetches the epoch participant list once when the epoch becomes relevant, maps each participant to its CometBFT consensus validator address (derived from the registered `PubKey` per the chain's address rules), caches the result for the whole epoch, and uses it to build `expected_vals_hash` for Step 3b. Escrow start may record `epoch_id` (or derive it from the escrow creation height) only to select **which** epoch's set to use — never a second copy of the keys. If a product also wants subnet-membership attestation, that remains a separate layer.

---

## CometBFT `LightBlock` verification

Normative when `proof_type == cometbft-light-block-v1` and `light_block` is non-empty. Skip for Anchor and Omit.

Inputs: `chain_id_claimed`, `H_claimed`, `block_hash_claimed`, `proof_bytes` ← from `HeightSyncSection`.

1. **Decode.** Unmarshal `proof_bytes` as `tendermint.types.LightBlock` → `lb`. Reject if `signed_header`, `commit`, or `validator_set` is missing.
2. **Header vs claims.** Reject unless `hdr.chain_id == chain_id_claimed`, `hdr.height == H_claimed`, and `len(block_hash_claimed)` matches CometBFT expectations (typically 32 bytes).
3. **Validator set binds to header.** Compute `vals_hash = MerkleHash(validator_set)`; reject unless `vals_hash == hdr.validators_hash`.
4. **(Optional) Step 3b — epoch-bound match.** Only when the verifier has an `escrow_id` under the epoch-bound policy and can resolve `expected_vals_hash` from cached epoch participants for `H_claimed`: reject unless `vals_hash == expected_vals_hash` (or matches the chain's per-height rule), and reject if `H_claimed` falls outside the escrow's allowed epoch range. **Skip** when there is no escrow context, no resolvable participant set, the escrow is multi-epoch, or in documented test/dev profiles.
5. **Expected `BlockID`.** Compute `block_id = MakeBlockID(hdr)`; reject unless `block_id.hash == block_hash_claimed`.
6. **Commit quorum.** Reject unless `commit.height == hdr.height`. Run CometBFT `VerifyCommit` semantics for `(chain_id_claimed, block_id, H, commit)` (Ed25519 precommits, voting-power sum strictly `> 2/3`).
7. **(Optional) Hardening.** Replay policy on old commits; full nodes cross-check `lb` against stored `Block(H)`.

All steps pass ⇒ **VALID**. Any failure ⇒ **INVALID**; do not advance height state.

**Liveness / "latest" semantics.** A proof can be VALID but stale. For "latest seen height" require additionally `H >= local_trusted_tip - max_lag_blocks` and/or `H` on the canonical fork; otherwise classify **VALID_STALE** (don't advance timeout counters; may archive).

---

## Validation pipeline (per inbound envelope)

1. Parse envelope (protobuf or JSON).
2. Classify Omit / Anchor / Strong from the presence of `mainnet_height`, `mainnet_block_hash`, `light_block`, `proof_type`.
3. **Forced sync turn check (first).** If the receiver currently holds an `ActiveForcedTurn{start, end}` for this session and `start <= nonce_num <= end`: the envelope MUST be Anchor or Strong; an Omit envelope under an active forced turn is **INVALID**. If the envelope additionally carries `force_directive` while a turn is already active, drop the directive (no extension), classify the rest of the envelope normally.
4. **Omit:** verify session framing / signatures per product proto; do not run CometBFT verification; do not update peer height from section 1.
5. **Anchor:** if `|H_claim − H_local_aligned| > D`, **INVALID** (sender MUST use Strong, and a carry-forward Anchor outside `D` is doubly INVALID — see §"Provenance, carry-forward, and malware-host attribution"). Otherwise verify `sender_signature` over the carrier's section. If `origin_attestation` is non-empty: decode the embedded `HeightSyncSection`, run the **carry-forward validation step** (§"Provenance, carry-forward, and malware-host attribution"), and record `origin_sender_id` accordingly. Verify `mainnet_block_hash` against local block `H` immediately, or enqueue deferred check storing `(origin_sender_id, H, hash, origin_signed_blob)`. Do not run `VerifyCommit`.
6. **Strong:** run `LightBlock` verification (including Step 3b when applicable). Absence of valid Strong on a `> D` claim ⇒ **INVALID**.
7. **Same-height / different-hash check.** If the receiver already holds a confirmed `(H, hash_local)` (from its own oracle or a previous Strong) and the inbound claims `(H, hash_peer)` with `hash_peer != hash_local`: classify as **DISPUTE_ORIGINATOR** when origin verification succeeded, otherwise **DISPUTE_CARRIER**. Persist the offending signed blob; do **not** advance peer height state from this Anchor.
8. Verify `sender_signature` (Omit binding may differ — open).
9. Apply recency rules for Strong / Anchor when updating timeout-driving max height.
10. Anti-replay: unique `request_id`, monotonic `nonce_num`, optional dedup on `(H, block_hash)`.
11. **Forced-turn directive (last).** If the envelope carries a valid `force_directive` and no `ActiveForcedTurn` is currently set, install `ActiveForcedTurn{start = trigger_nonce, end = end_nonce, reason}` per §"Forced sync turn (manual-force, normative)". If a turn is already active, drop the directive silently.
12. Classify per result classes below.
13. Then process `message_body` (section 2) if not INVALID.

### Result classes

- **VALID_OMIT** — no mainnet attestation; framing OK; do not advance peer height from section 1.
- **VALID_ANCHOR** — Anchor signature OK; hash verified immediately or deferred enqueued; may advance `height_seen_max` once hash is confirmed. May carry a verified `origin_attestation`; in that case `origin_sender_id` is the originating host, not the carrier.
- **VALID_STRONG** — `LightBlock` verified, signature/replay OK; may advance strong anchor / aligned height.
- **VALID_STALE** — attestation cryptographically OK but recency fails for timeout advancement.
- **DEFERRED_FAIL** — deferred Anchor check found hash mismatch ⇒ misrepresentation evidence, attributed to the recorded `origin_sender_id` (the host that signed the wrong pair, not the carrier).
- **DISPUTE_ORIGINATOR** — same-height / different-hash with a verified `origin_attestation` ⇒ blame the originating host; persist `origin_signed_blob` for the dispute layer.
- **DISPUTE_CARRIER** — same-height / different-hash with no `origin_attestation` (or invalid origin signature) ⇒ blame the carrier (`envelope.sender_id`); persist the carrier's signed section for the dispute layer.
- **INVALID** — malformed envelope, bad signature, replay, Anchor hash mismatch when block was already local **and** no `origin_attestation` to assign blame elsewhere, Strong proof failure, `> D` height claim without valid Strong, Omit envelope inside an active forced sync turn, second `force_directive` while a forced turn is already open (only when policy treats it as INVALID rather than silently dropping — implementations SHOULD prefer silent drop), or carry-forward Anchor whose `origin_attestation` does not pin the same `(H, hash)` the carrier claims.

Deprecated: `VALID_LIGHTWEIGHT` and per-message trusted window. Map old `VALID_TRUSTED` (full proof) → `VALID_STRONG`.

---

## Processing rules

**Host receiving user request:** run pipeline; reject if INVALID. On VALID_STRONG, update `host_aligned` per policy. On VALID_ANCHOR, update peer `(H, hash)` (immediate or deferred), keying the pending entry by the recorded `origin_sender_id` (carrier when not carry-forward, originator when origin signature verified); contributes to `height_seen_max` once confirmed. On VALID_OMIT, no peer height update from section 1. On DISPUTE_ORIGINATOR / DISPUTE_CARRIER, log the disagreement and persist the offending signed blob; do not advance peer height. Process section 2 only if not INVALID. Respond with the appropriate mode: Omit between sync turns; Anchor on sync-turn schedule; **Anchor unconditionally while `ActiveForcedTurn.start <= responseNonce <= ActiveForcedTurn.end`**; Strong when `|Δ| > D` or policy requires.

**User receiving host response:** same pipeline; same updates per result class. Additionally, the user maintains a per-peer **origin-signed cache** of `(sender_id, H, hash, signed_blob)` so it can later attach the originator's signed blob when carrying `(H, hash)` to another host. Tips for which the user lost the origin signature MUST NOT be carried forward — re-fetch from a host inside the `D` band, or escalate to Strong, instead.

**Host mainnet listener:** maintain a proof cache for `[tip-K, tip]` to fill `light_block` cheaply when sending Strong; process the deferred Anchor queue as local tip advances; emit evidence on DEFERRED_FAIL **attributed to `origin_sender_id`** (the host that signed the wrong pair), attaching `origin_signed_blob` as the cryptographic proof for the dispute layer.

---

## Timeout derivation

`height_seen_request_max_trusted` / `height_seen_response_max_trusted` are derived from VALID_STRONG and from VALID_ANCHOR once `(H, hash)` is **confirmed** (immediate or successful deferred). VALID_OMIT does not advance peer height; pending deferred Anchors SHOULD NOT advance `height_seen_max` until they confirm.

`height_seen_max = max(height_seen_request_max_trusted, height_seen_response_max_trusted, host_chain_tip_trusted)`

`timed_out = (current_chain_height - height_seen_max) >= timeout_blocks`

Finalization timeout evidence should include serialized section-1 blobs (or hashes + reproducible archives) for Strong anchors, not HTTP headers. Consumed by [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) (`USER_TIMEOUT` trigger).

---

## Randomness source integration

```text
rand_seed = H(finalization_hash || finalized_height_anchor || block_hash_anchor)
```

Anchors used for randomness MUST be Strong-verified (or Anchor hashes confirmed against canonical mainnet) — never unconfirmed deferred Anchors and never Omit traffic.

---

## Security properties

- Avoids HTTP header size limits and proxy fragility: Omit + Anchor on the inference path; Strong only when needed.
- Sync-turn Anchor windows of `slots_num` nonces give signed periodic alignment without any `LightBlock` and let every host in the round-robin contribute its own height inside one sync turn.
- Forced sync turns extend the same `slots_num`-wide alignment property to manual-force points (cPoC, dispute open, operator force), so manual force aligns the **whole group**, not just one envelope.
- Strong ties dispute-grade claims to mainnet consensus.
- Section 1 is parseable before heavy section 2 where policy requires.
- Bad Anchor hashes are caught immediately (block local) or deferred (catch-up); `> D` disagreement without Strong is rejected.
- **Malware-host attribution.** Carry-forward Anchors require the user to embed the originator's verbatim signed blob (`origin_attestation`); a deferred mismatch or same-height/different-hash dispute is then cryptographically attributable to the **originating signer**, not the propagator. A user that forwards a foreign claim without provenance becomes the cryptographic source of that claim and absorbs the dispute (`DISPUTE_CARRIER`), so users are economically aligned with retaining provenance.

---

## Backward compatibility and rollout

1. Accept legacy envelopes that put `(height, hash)` on every message → migrate to Omit + sync-turn Anchor.
2. Soft-enforce sync-turn Anchor and Strong-on-`|Δ| > D`; warn on violations.
3. Hard-reject mainnet height/hash on envelopes that should be Omit once rollout completes.

---

## Open questions

- Exact protobuf package / `Content-Type` for `GonkaUserHostEnvelope`.
- Pinning `cometbft-light-block-v1` to mainnet's CometBFT/SDK release; bump to `v2` on incompatible upgrades rather than overloading `v1`.
- Canonical `sender_id` for multi-key users (escrow slot, mainnet account, etc.).
- Devshard wire profile: protobuf-only vs also accepting JSON for tooling.
- Defaults for `K` (envelope nonces between sync turns; e.g. 10), `slots_num` (escrow group size), `D` (Strong threshold; e.g. 2). Constraint: `K >= slots_num`.
- Whether a partial credit policy advances `height_seen_max` while a deferred Anchor is still pending (see [`CPOC_PROTOCOL.md`](./CPOC_PROTOCOL.md)).
- Anchor signing input: confirm SHA256(32 zero bytes) for the empty-`light_block` slot, or define a distinct Anchor signing domain.
- Omit-mode binding: how `session_id` / `nonce_num` / signatures bind `message_body` when `height_sync` is absent (unified vs separate `SessionFraming` message).
- Manual-force is normatively a **forced sync turn** (§"Forced sync turn (manual-force, normative)"); residual questions: which message classes (cPoC skip carry, disputes, session open) MUST trigger one vs MAY trigger one; whether finalization-collector triggers can also open turns; canonical encoding of `force_directive` outside the devshard state-machine deployment (signed standalone blob format, replay protection across sessions).
- Anchor `DEFERRED_FAIL` is now attributed to `origin_sender_id` (originating signer of the bad pair); residual questions: how the dispute layer accepts the attached `origin_signed_blob` as evidence, when (and how much) the carrier is rewarded for surfacing the malware host's signature, and whether `DEFERRED_FAIL` triggers automatic slashing of `origin_sender_id` or only feeds finalization.
- `DISPUTE_ORIGINATOR` vs `DISPUTE_CARRIER`: economic / slashing parameters for each path, and whether a host that produced both halves of a same-height/different-hash pair (e.g. equivocating across sessions) is detected by cross-session de-duplication on `(sender_id, H)` in the audit ring.

---

## Status

Draft proposal.
