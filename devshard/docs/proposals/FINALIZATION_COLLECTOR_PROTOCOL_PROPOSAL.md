# Finalization protocol proposal (host-initiated, collectors, commit certificate)

## Summary

This proposal defines a complete finalization protocol where **any host** can start finalization (not only the user/sequencer), with explicit trigger proofs, deterministic collector selection, commit-phase safety, and mainnet verification requirements.

It is designed to:

- preserve safety under Byzantine behavior,
- allow finalization progress when the user is offline or cheating,
- reduce message flood via deterministic collector set,
- make mainnet accept finalization only when commit phase is provably complete.

Related proposal: [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md).

---

## Important to consider

1. **No gossip as the finalization gate.** Under the model this proposal targets, we **do not** rely on “gossip first, then finalize.” Whatever **runtime** exists today for nonce fan-out during normal execution is **not** specified here as the way hosts align immediately before finalization.

2. **Nonce ↔ executor schedule (hypothesis).** With a **strict binding** between `nonce` and **executor host** (e.g. `N` hosts in fixed order): if host **A** executed nonce **`NoID`**, then host **A** is scheduled again at nonce **`NoID + N`**, and so on. After a **full round** of **`N`** consecutive nonces, each host has executed one step. **Open assumption:** that implies every other host is then **aware** of the same linear history (or can derive it) without extra messaging. **This must be proved** under the exact scheduling, transport, and storage rules before building safety arguments on it.

3. **State sharing is separate.** Finalization needs an explicit **state sharing** protocol (what to exchange, who proves what, how to detect lag or fork). That belongs in **another proposal document** (not this file). This proposal describes **vote / commit / mainnet** only **after** state sharing has succeeded.

4. **Phase order for finalization.** End-to-end finalization is:

   1. **State sharing** — run the dedicated protocol until all required parties share a common view of terminal state (or abort). When this phase completes, participants also **align on the same latest mainnet height** used for finalization: the value is the **aligned mainnet height** in the sense of [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) (section 1 `HeightSyncSection`, proof verification, and **How the two views converge** / timeout-related **aligned height** rules). That scalar is what **`FinalizeInit.aligned_mainnet_height`** must carry into phases 2–4.
   2. **Proposal commitment** — broadcast / lock `FinalizeInit` (or equivalent) with `finalization_hash`, **`aligned_mainnet_height`**, and evidence.
   3. **Voting** — `FinalizeVote` and aggregation into `VoteQC` (**QC** = **Quorum Certificate**: proof enough hosts voted for the same `finalization_hash`).
   4. **Vote commitment** — `FinalizeCommit` and aggregation into `CommitQC` (commit phase on the agreed proposal), then `FinalizeSubmit` to mainnet.

The message schemas below map to phases **2–4**; phase **1** is out of scope here pending the state-sharing spec.

---

## Goals

1. Finalization can be initiated by any host with one of the allowed trigger reasons.
2. Trigger reason must be backed by cryptographic evidence all hosts can verify.
3. Finalization result must be unique/safe (commit certificate, not vote-only).
4. Mainnet must verify commit completion before applying settlement.
5. Network communications should be bounded; avoid all-to-all flooding where possible.

---

## Trigger reasons and required evidence

`FinalizationTriggerReason`:

- `USER_CHEATING`
- `EPOCH_CHANGE_IMMINENT`
- `USER_TIMEOUT`

### 1) `USER_CHEATING`

Required evidence:

- one or more user-signed messages proving protocol violation, for example:
  - conflicting signed fragments at same nonce (fork/equivocation),
  - invalid sequence transition with user signature,
  - malformed signed user request that cannot be repaired by later diffs,
  - `sync_vector` claiming `ACKED` at a nonce whose `Diff` entry has no matching `MsgHeightAck` ([`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §11.1). This is a contradiction against the log the user signed, not a probe-derived "dropped ack".

A missing `MsgHeightAck` in `Diff` is **not** `USER_CHEATING` evidence.
The user↔host hop has no receipt; a later host-signed ack recovered by
a repair probe does not prove the sequencer ever received one (height-sync §11.3).

Each evidence item must include:

- raw signed payload bytes,
- user signature,
- nonce/height/topic context,
- hash of previous fragment (if available).

### 2) `EPOCH_CHANGE_IMMINENT`

Required evidence:

- signed mainnet block header (or light-client proof) proving next block transitions epoch.
- `epoch_current`, `epoch_next`, `height_proven`.

### 3) `USER_TIMEOUT`

Required evidence:

- latest **user-signed** height claim for the session — `MsgHeartbeat.observed_height`, covered by the user's diff signature and durable in `Diff` ([`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §10.5),
- per-host highest observed response/request height for the same session (from validated `MsgHeightAck` entries and section 1 on both directions),
- per-host `close-ready` evidence: `(slot, last_signal_height, armed_at_height, last complete turn_seq, degraded turns)` from `CloseReadyView` (height-sync §12.4),
- timeout window in blocks.

Timeout is computed against:

- `max(user_height_seen, host_response_height_seen)` and current mainnet tip.

> **Do not read this evidence off the request-leg `HeightSyncSection`.** Height-sync §15 signs the **response** leg only; the request-leg section carries **no** sender signature, so a "user `HeightSyncSection` … with sender signature" does not exist on the transport plane. The attributable user height claim lives in the **log plane** (`MsgHeartbeat`, height-sync §10.4–§10.5), which is why the heartbeat is a mandatory part of height sync rather than an optimization.
>
> **Voting rule.** A host that is **armed** close-ready MAY vote `AGREE` on a `USER_TIMEOUT` `FinalizeInit`; a host that is **not armed** MUST vote `REJECT` — an unarmed host is one the user is still serving, so the timeout claim is false from its view. Arming itself emits nothing (height-sync §12.2), so a partitioned minority that all sees silence still cannot reach `2f + 1`.

Envelope format, proofs, and signatures are defined in:
[`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) (structured HTTP body: height section + message section; log-plane messages in §10).

---

## Epoch-bound escrow: L1 validators from epoch participants

When subnet **life is limited to one mainnet epoch** and the subnet is **finalized on epoch switch** (aligned with `EPOCH_CHANGE_IMMINENT` / epoch transition policy), **escrow start need not include the L1 validator set.** The **CometBFT validators that may sign blocks during that epoch** are defined by **mainnet epoch participants** (canonical on-chain state; exact module/query TBD). Each host **loads that participant list once per epoch**, converts entries to **Cosmos / CometBFT consensus validator addresses** (same derivation as in blocks), caches them, and uses the result to compute the **expected** `validators_hash` at height `H` for **Step 3b** in [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md), so peer `LightBlock`s cannot use a fabricated validator set.

Escrow creation may still fix **which epoch** applies (e.g. via creation height or an optional `epoch_id` field) without duplicating validator keys on the start message.

---

## State commitment and finalization hash

To avoid ambiguity and enable deterministic collector choice, define:

- `NonceLeaf_i = H(nonce_i || state_hash_i || tx_digest_i)`
- `NonceMerkleRoot = MerkleRoot(NonceLeaf_1..NonceLeaf_N)` (ordered by nonce)

Then finalization candidate digest:

`FinalizationHash = H(escrow_id || round || trigger_reason || trigger_evidence_hash || nonce_merkle_root || terminal_nonce || settlement_payload_hash)`

`trigger_evidence_hash` is Merkle root of all trigger evidence items.

**`aligned_mainnet_height` is not part of this digest.** It is carried on **`FinalizeInit`** (and echoed on **`FinalizeSubmit`**) so everyone agrees which mainnet tip the subnet used when opening the round; it is mixed into the **collector selection seed** only (see **Deterministic collectors**).

Rationale:

- Merkle root is smaller and easier to verify incrementally than concatenating all nonce hashes.
- proofs for individual nonces/evidence are compact.

---

## Deterministic collectors

Collectors are selected from active slots using **`FinalizationHash`** and **`aligned_mainnet_height`** from **`FinalizeInit`** (same pair every host and mainnet must use for the round).

Parameters:

- `collector_count = c` (for example `c=3` or `c=5`, chain param)
- **`aligned_mainnet_height`:** `uint64` from **`FinalizeInit`**, the agreed aligned mainnet height after state sharing / height sync per [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md).
- selection seed (canonical encoding: big-endian 8-byte `aligned_mainnet_height`):
  - `seed = H(FinalizationHash || uint64_be(aligned_mainnet_height) || "collectors")`

The hash `H` is the same function used elsewhere in this proposal (e.g. `FinalizationHash`). **Deterministic “random” shuffle:** the seed fixes a pseudorandom permutation of slots; all verifiers derive the **same** collector set without extra messaging.

Algorithm:

1. Build deterministic active-slot list (sorted slot ids).
2. Use **seed**-driven deterministic shuffle.
3. Take first `c` unique slots as collectors.

Collectors responsibilities:

- aggregate votes and build `VoteQC`,
- collect commit attestations and build `CommitQC`,
- after `CommitQC` exists, submit **one** signed `FinalizeSubmit` to mainnet (no earlier mainnet traffic for vote/commit).

Fallback:

- if collector timeout expires, any host may take over submission with the same certificates.

---

## Exact protocol (initiator, order, transport)

This section fixes **who starts**, **in what order messages flow**, and **which path** each message takes. It assumes **state sharing** (phase 1) has already succeeded and participants share the **same `aligned_mainnet_height`** per **Important to consider** and [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md).

### Initiator

- **Who:** **Any host** in the active participant set may initiate a finalization **attempt** for an escrow, provided it attaches **valid trigger evidence** for one of **`FinalizationTriggerReason`** (see **Trigger reasons and required evidence**).
- **What they send first:** A signed **`FinalizeInit`** is the **first message of a round**. There is no separate “elected leader” in this draft: the **first valid `FinalizeInit`** that hosts accept for `(escrow_id, round)` under **dedup / lock / unlock rules** opens that round. (Policy may later add proposer rotation; this spec keeps initiation permissionless among honest hosts.)
- **Round `1`:** `FinalizeInit` with `round = 1` and **no** `unlock_timeout_certificate` (first attempt for the escrow’s finalization flow).
- **Round `> 1`:** `FinalizeInit` **must** include a valid **`unlock_timeout_certificate`** proving the **immediately prior attempted round** (or the round named in the certificate) **exhausted** without settlement — see **`unlock_timeout_certificate`** under **`FinalizeInit`**. Hosts **reject** a higher round without that proof if they are still **locked** from an earlier round (see **Voting lock and multiple rounds**).

### Message order (single round, happy path)

For a fixed `(escrow_id, round)`:

1. **`FinalizeInit`** — one logical proposal per round (may be gossip-duplicated; verifiers dedup by `message_hash`).
2. **`FinalizeVote`** — each participating host sends **at most one** vote per `(escrow_id, round)` after validating `FinalizeInit`.
3. **`VoteQC`** — produced **only by collectors** after `2f+1` matching `AGREE` on the same `finalization_hash`; collectors **broadcast** the compact QC to all hosts (same transport as other collector fan-out).
4. **`FinalizeCommit`** — each host that accepts `VoteQC` sends **at most one** commit per `(escrow_id, round)` referencing that `VoteQC` (e.g. `vote_qc_hash`), **to the collectors only** (no host-to-host commit gossip; see **Transport**).
5. **`CommitQC`** — collectors aggregate commits, **broadcast** compact `CommitQC`.
6. **`FinalizeSubmit`** — **one** signed transaction to **mainnet**, sent by a **collector** (or **fallback submitter** after collector timeout) carrying `VoteQC`, `CommitQC`, and settlement fields.

**Unhappy path:** If step 3 never yields `VoteQC` (insufficient `AGREE`, all `REJECT`, or timeout), or step 5 never yields `CommitQC`, **no** mainnet submit occurs for that round. Hosts **wait for** `unlock_timeout_certificate` (or local timeout participation in forming it), then a **new** `FinalizeInit` with **`round := round + 1`** may begin the next attempt.

### Transport

| Message | From | To | Mechanism |
|--------|------|-----|-----------|
| `FinalizeInit` | Initiating host(s) | All hosts | **Subnet gossip** (bounded fan-out `K`, dedup by `(escrow_id, round, message_hash)`) |
| `FinalizeVote` | Each host | **Deterministic collectors** for `(FinalizationHash, aligned_mainnet_height)` from **`FinalizeInit`** | **Directed send** (or gossip addressed to collector slots — implementation choice) so collectors can aggregate without all-to-all |
| `VoteQC` | Collectors | All hosts | **Broadcast / gossip** from collectors (compact) |
| `FinalizeCommit` | Each host | **Collectors only** (same deterministic set) | **Directed send to collectors only** — hosts do **not** gossip `FinalizeCommit` to other peers; **`CommitQC`** is how everyone learns the commit phase outcome |
| `CommitQC` | Collectors | All hosts | **Broadcast / gossip** from collectors (compact) |
| `FinalizeSubmit` | One collector (or fallback) | **Mainnet** | **Single Cosmos tx** (no prior vote/commit txs in this design) |

**Minimizing commit traffic:** It is **enough** that **`FinalizeCommit`** goes **only** to collectors; that bounds commit fan-out to **O(hosts × collector_count)** instead of host-to-host flooding. No separate all-to-all commit path is required.

**Evidence payloads:** Large `trigger_evidence_items` SHOULD use **hash + fetch** after first advertisement; **`FinalizeInit`** carries roots and optional references consistent with **Communication minimization**.

---

## Protocol messages

These messages implement **phases 2–4** in **Important to consider** (after **state sharing**). They do not define state sharing.

All messages must include:

- `escrow_id`
- `round` (monotonic uint64)
- `sender_slot`
- `sender_sig`
- `message_type`
- `message_hash`

### `FinalizeInit`

Fields:

- `escrow_id`
- `round`
- `aligned_mainnet_height` (`uint64`): **must** match the **aligned mainnet height** all required parties agreed when **state sharing** completed, as defined by [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) (same scalar parties use for `max(local, H_peer)` / timeout alignment after verified `LightBlock`s). Hosts **reject** `FinalizeInit` if this value **diverges** from their own aligned height for the escrow session unless policy allows a defined tolerance. Collectors for the round are derived from **`FinalizationHash`** and this field (**Deterministic collectors**).
- `trigger_reason`
- `trigger_evidence_root`
- `trigger_evidence_items` (or fetch references)
- `nonce_merkle_root`
- `terminal_nonce`
- `settlement_payload_hash`
- `finalization_hash`
- `unlock_timeout_certificate`: **Omit on `round = 1`.** On **`round > 1`**, **required** (see **Exact protocol — Initiator**): quorum-signed (or policy-defined) proof that a **prior round** closed **without** successful settlement so this round may start. Must cover at least: **(a)** round **`r`** did **not** obtain **`CommitQC`**, and/or **(b)** round **`r`** did **not** obtain a valid **`VoteQC`** by deadline (vote phase failed). Exact encoding TBD; functionally a **“round `r` exhausted”** certificate.

**Advancing rounds:** To open **`round = r_new > 1`**, the initiator includes **`unlock_timeout_certificate`** for the **prior** attempt (typically **`r_new - 1`**) unless chain policy defines a different mapping. Hosts verify the certificate before accepting the new `FinalizeInit` if they hold a **lock** from an earlier round.

### `FinalizeVote`

Fields:

- `escrow_id`
- `round`
- `finalization_hash`
- `vote` (`AGREE` / `REJECT`)
- `reject_code` (optional)
- `reject_evidence_hash` (optional)

### `VoteQC`

Produced by collectors after `2f+1` matching `AGREE` votes. Gossiped to all hosts after aggregation. Included in **`FinalizeSubmit`** to mainnet.

Fields:

- `escrow_id`
- `round`
- `finalization_hash`
- `vote_signers_bitmap`
- `aggregated_vote_signature` (or explicit sig list)
- `vote_count`

### `FinalizeCommit`

Sent by hosts **to collectors only** after validating `VoteQC` (not gossiped host-to-host; see **Exact protocol — Transport**).

Fields:

- `escrow_id`
- `round`
- `finalization_hash`
- `vote_qc_hash`
- `commit` (`COMMIT`)

### `CommitQC`

Produced by collectors after commit threshold.

Fields:

- `escrow_id`
- `round`
- `finalization_hash`
- `vote_qc_hash`
- `commit_signers_bitmap`
- `aggregated_commit_signature` (or explicit sig list)
- `commit_count`

### `FinalizeSubmit` (to mainnet)

Fields:

- `escrow_id`
- `round`
- `aligned_mainnet_height` (`uint64`): **must** match the value from the accepted **`FinalizeInit`** for this `(escrow_id, round)` so the keeper (and auditors) can re-derive the collector set with **`FinalizationHash`**.
- `trigger_reason`
- `trigger_evidence_root`
- `nonce_merkle_root`
- `terminal_nonce`
- `settlement_payload`
- `finalization_hash`
- `vote_qc`
- `commit_qc`

---

## Voting lock and multiple rounds

Rule:

- A host that sent **`FinalizeVote`** `AGREE` in round `r` for `finalization_hash` **`H`** is **locked** on **`(r, H)`**.
- Host must not send a second conflicting **`FinalizeVote`** for the same `(escrow_id, r)`.
- Host may accept **`FinalizeInit`** for **`r_new > r`** and vote again only if **`unlock_timeout_certificate`** (and any stricter chain policy) shows round **`r`** **exhausted without settlement** — so advancing the round is **justified**, not an arbitrary view flip.

**Changing `finalization_hash` in a later round:** Implementations SHOULD require **`unlock_timeout_certificate`** before accepting **`FinalizeInit`** with a **different** `finalization_hash` than the hash the host previously voted `AGREE` for, so a dishonest initiator cannot bounce honest voters between conflicting proposals without a timed-out or failed prior round.

**Practical per-host state:**

- `locked_round`, `locked_hash` after an `AGREE` vote until unlock or successful mainnet settlement for the escrow.
- On **`FinalizeInit(r_new)`:** if `r_new > locked_round`, require valid **`unlock_timeout_certificate`** covering the stalled round(s) per policy; then update lock when voting in `r_new`.

**Retry without bumping round (optional optimization):** If **`VoteQC`** exists but **`CommitQC`** never forms, an implementation MAY **re-gossip `VoteQC`** and run **another `FinalizeCommit` wave** at the **same** `round` instead of incrementing `round`. This doc’s **default** is **round monotonicity + new `FinalizeInit`** with **`unlock_timeout_certificate`** so every retry is explicitly opened.

---

## No double finalization (idempotency)

Subnet gossip may deliver the same `FinalizeSubmit` material many times, and **several collectors** are allowed to broadcast/submit. **Settlement must still happen at most once per escrow** on mainnet.

Guarantees come from **application-level rules in the keeper**, not from the vote/commit protocol alone:

1. **Terminal escrow state:** The chain stores each `escrow_id` in a lifecycle (e.g. `ACTIVE` → `SETTLED` / `CLOSED`). The handler **rejects** any finalize message if that escrow is **already settled** (or already aborted under a defined dispute path). This is the primary “never twice” gate.

2. **Optional digest bind:** The keeper MAY record `last_finalization_hash` (or `settled_terminal_nonce` + payload commitment) for the escrow and **reject** a second submission that differs (conflicting retry). A **byte-identical** replay of the same valid submission should be **rejected as duplicate** or treated as **no-op** (idempotent success), so collectors racing on the same certificates do not double-apply effects.

3. **Same certificates, one effect:** `VoteQC` and `CommitQC` tie submission to a **single** `finalization_hash`. Honest hosts only sign one committed outcome per successful round; **two different** outcomes would need **different** hashes and could not both pass unless the chain allowed a second round — which must still respect (1).

4. **Tx-level dedup:** Cosmos SDK **sequence** / mempool dedup prevents the **same signed tx** from executing twice; distinct txs repeating the same settlement must still fail at (1)–(2).

The **commit phase** prevents *conflicting* finals from being signed by a quorum without a new round and unlock rules; **mainnet idempotency** prevents *repeated application* of settlement for the same escrow.

---

## Mainnet interaction: single round-trip

**Vote and commit phases are devshard-local only.** `FinalizeInit` and collector-originated QCs use subnet gossip; **`FinalizeCommit`** is **collector-directed only** (see **Transport**). **There is no message to mainnet** while those phases run — no “report vote QC” or “report commit QC” as separate chain transactions.

**One chain interaction:** when the subnet is done, a **collector** (or fallback submitter) broadcasts a **single signed** `FinalizeSubmit` transaction that includes settlement payload, **`aligned_mainnet_height`**, `VoteQC`, `CommitQC`, and all fields needed to recompute `finalization_hash`. The node returns **tx success** (settlement applied) or **failure** (validation error → rejected).

**Why not more mainnet steps:** Intermediate “vote verified” / “commit verified” events would imply extra devshard↔mainnet traffic and do not add safety if the keeper only applies state on the **final** accepted message. Verification of `VoteQC` and `CommitQC` happens **inside** that one handler.

**Events (for devshard hosts, not extra round-trips):** The keeper should emit **Cosmos events** on outcome so **every** devshard host can learn the result by **watching the chain**, even if the submitting collector crashes before gossiping success back:

- `finalization_settled` — include `escrow_id`, `finalization_hash`, and any index fields needed for wallets/indexers.
- `finalization_rejected` — include `escrow_id`, `finalization_hash` (if parseable), and **reason code** / error class.

Optional: one generic `finalization_outcome` with an enum `SETTLED` | `REJECTED` instead of two event types. **Do not** emit separate chain events that mirror internal vote vs commit checks unless a product need requires auditing (that can stay in logs, not p2p to mainnet).

---

## Mainnet verification changes

Mainnet handler must reject finalize submissions unless:

1. `finalization_hash` recomputes exactly from payload and evidence.
2. `aligned_mainnet_height` is present and **consistent** with the submitted evidence where height is implied (e.g. `USER_TIMEOUT` / height-sync proofs); optional **strict** check that signers in `VoteQC` / `CommitQC` are exactly the collector set from **`H(FinalizationHash || uint64_be(aligned_mainnet_height) || "collectors")`** if the chain verifies collector membership.
3. Trigger evidence is valid for the declared reason.
4. `VoteQC` is valid and meets threshold `2f+1`.
5. `CommitQC` is valid, references `VoteQC`, and meets threshold `2f+1` (or configured commit threshold).
6. Round and replay checks pass (`escrow_id`, `round` monotonicity, no duplicate finalization hash).
7. **Idempotency:** escrow not already settled per **No double finalization** above; duplicate or conflicting resubmission rules enforced.

Items 4–5 are **in-memory checks inside this single tx**, not separate mainnet messages.

---

## Communication minimization

- Hosts send `FinalizeVote` to collectors (directed or collector-addressed relay); **`FinalizeCommit`** is **collectors only** — no host-to-host commit gossip (sufficient to cap commit traffic).
- Collectors aggregate and broadcast only compact `VoteQC` / `CommitQC`.
- Evidence blobs transferred by reference (hash+fetch) after first propagation.
- Dedup by `(escrow_id, round, message_hash)`.

---

## Open questions

- **TODO:** Formalize how **unfinished inferences** (e.g. still pending or in progress) and **unfinished validations** (e.g. inference finished but validation window open or required checks incomplete) are treated when **finalization is started** — including whether they are **excluded** from `nonce_merkle_root` / settlement, **forced** to timeout or default outcomes, given a **grace period**, or **blocked** until cleared; must align with [`shard-state-trim-inferences-by-height.md`](../issues/shard-state-trim-inferences-by-height.md) and [`VALIDATION_PROTOCOL_RANDOMNESS_PROPOSAL.md`](./VALIDATION_PROTOCOL_RANDOMNESS_PROPOSAL.md) once validation timing is fixed.
- state sharing before finalization proposal is committed,
- aggregated BLS,
- evidence retention duration and pruning guarantees,
- whether `REJECT` path should also produce commit certificates for deterministic default settlement.

---

## Status

Draft proposal.
