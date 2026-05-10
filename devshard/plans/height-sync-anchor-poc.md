# Plan — Anchor-only height sync PoC (light, no proofs, no verification)

This plan describes a minimal proof-of-concept implementation of the height
sync protocol from
[`HEIGHT_SYNC_HEADERS_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md),
restricted to the **Anchor** schedule with **no cryptographic proofs and no
verification**. The goal is to land an end-to-end wire path that user and host
can use to align on `mainnet_height` + `mainnet_height_hash` on a periodic
`K`-nonce cadence, and observe in logs that the alignment works in the
testenv. Anything that the proposal classifies as Strong, deferred, signed,
or evidence-bearing is **out of scope**.

---

## 1. Scope

**In scope (Proof of Concept, light):**

- New wire field carrying a stripped-down `HeightSyncSection` on every
  user→host request and host→user response.
- Two modes only:
  - **Omit** — no mainnet attestation in the section.
  - **Anchor** — `mainnet_height` + `mainnet_block_hash` (the
    canonical block hash **at that height**, i.e. CometBFT
    `BlockID.Hash` for `H`) + `chain_id` +
    `proof_type = "height-anchor-v1"`, `light_block` empty, **no
    signature, no verification**.
- **Sync-turn cadence** (proposal-aligned, round-robin friendly):
  the schedule is keyed on the **outgoing per-direction nonce** but
  emits Anchor over a **window of `slots_num` consecutive nonces**
  starting at each anchor cadence point, not just on a single nonce.
  `slots_num` is the **number of host slots in the escrow group**
  (e.g. 4 in the testenv) — that is exactly one round-robin pass
  across all hosts. Concretely, Anchor is emitted when:
  - **Initial sync turn:** `1 <= nonce <= slots_num` — covers the
    first round-robin pass so every host returns its current
    `(height, block_hash)` and both sides become aligned.
  - **Periodic sync turn:** for any `i >= 1`, while
    `i*K <= nonce <= i*K + slots_num - 1` — i.e. the cadence anchor
    nonce `A = i*K` plus the next `slots_num - 1` follow-up nonces.
  - All other nonces are Omit.
  - Constraint: deployments configure `K >= slots_num` so sync turns
    don't overlap; the PoC validates this in config.
- **Symmetric on the response side.** Hosts apply the same rule to
  their outgoing response nonces, so during a sync turn each
  responding host attaches `(height, block_hash)` too.
- **Manual-force Anchor** policy points for selected message classes
  (for example cPoC skip/carry messages that require aligned height).
- **Session-start hint** (`SessionStart=true`) remains as an explicit
  caller override (e.g. session reset) but is **redundant during the
  initial sync turn**: the cadence rule alone forces Anchor for
  `nonce <= slots_num`, so no per-direction "first message" flag is
  needed to bootstrap height sync — it is the natural side effect of
  the initial sync turn.
- Receiver classification (Anchor vs Omit) and per-envelope debug log
  with `(peer_height, peer_block_hash[:8], local_aligned, mode,
  delta)`.
- Receiver **records** every observed `(peer_id, mainnet_height,
  mainnet_block_hash)` triplet (in-memory ring + log) so a later
  verifier can compare each attestation against the canonical
  `BlockID.Hash` for that height and prove that a peer previously
  emitted a wrong block hash for an honest height. The PoC does not
  run that comparison; it only preserves the audit trail.
- Configuration knob for `K` in testenv config and as a host/user option.
- Unit tests for the cadence scheduler + envelope (de)serialization.
- Testenv e2e validation: with `heightsyncd` ticking, drive a sequence
  of inferences and observe alternating Anchor / Omit envelopes per
  `K` boundary on both sides.

**Out of scope (deferred):**

- **Strong** mode (`light_block`, `VerifyCommit`, Step 3b epoch
  validator binding).
- `sender_signature` over the height-sync signing payload.
- Deferred verification queue (`(H, hash)` followers).
- `> D` Strong escalation; `D` is not consulted in the PoC.
- Anti-replay binding via `request_id` / `nonce_num` inside the height
  section (existing transport auth still applies).
- Protobuf representation A (`GonkaUserHostEnvelope`). PoC uses JSON
  representation B layered on the existing JSON wire.
- `INVALID` / `DEFERRED_FAIL` paths: PoC accepts everything, only logs.
- **Explicit height-sync RPC** (`POST .../sessions/:id/height-sync`,
  `HostClient.FetchHeightSync`, `devshardctl height-sync` CLI,
  `/v1/debug/height-sync` proxy). With sync-turn cadence,
  height bootstrap is **self-healing** through the initial
  `nonce <= slots_num` window: a lost first response is followed by
  a request to the next slot, which still emits Anchor inside the
  same window. The endpoint is therefore not protocol-critical.
  Kept as a deferred operator-tooling feature for the scenarios
  listed below — see § 12 Follow-ups.

---

## 2. Mapping to the proposal

| Proposal concept                                 | PoC behaviour                                             |
| ------------------------------------------------ | --------------------------------------------------------- |
| Two-section HTTP body                            | One JSON object with optional `height_sync` field         |
| `HeightSyncSection.proof_type`                   | Only `height-anchor-v1` when present; otherwise omitted   |
| `mainnet_height` + `mainnet_height_hash`         | Renamed on the wire to `mainnet_height` + `mainnet_block_hash` (the canonical block hash at that height); value is identical (CometBFT `BlockID.Hash` for `H`) |
| `light_block`, `sender_signature`                | **Always absent / unset**                                 |
| Anchor every `K` envelope nonces                 | Implemented as a **sync-turn window of `slots_num` consecutive nonces** starting at each cadence anchor (and at nonce=1); default `K = 10`, default `slots_num` from escrow group, both configurable |
| Anchor on session/escrow start (per policy)      | Implemented as initial sync turn (`1 <= nonce <= slots_num`); both directions emit Anchor on every envelope in this window |
| Manual-force Anchor (e.g. cPoC skip/carry)       | Implemented via `DecideHints.ForceAnchor`                 |
| Omit between Anchors                             | Implemented (no `height_sync` field on wire)              |
| Stand-alone height query                         | **Deferred** to operator tooling (see § 12); not in PoC   |
| Strong on `\|Δ\| > D`                            | Out of scope; receiver only logs `Δ`, never rejects       |
| Validation pipeline (steps 4, 5, 6, 7)           | Skipped; only step 2 (classify) + step 9 (log class)      |
| `VALID_OMIT`, `VALID_ANCHOR` classes             | Used as log labels                                        |
| `VALID_STRONG`, `VALID_STALE`, `DEFERRED_FAIL`   | Not produced                                              |
| Sender signature signing input                   | Not built, not signed, not verified                       |
| Step 3b epoch participant binding                | Not run                                                   |

---

## 3. Wire format (JSON, light Anchor)

PoC wraps the existing `InferenceRequest` / `InferenceResponse` JSON bodies
in a thin envelope, additive and backwards compatible: when the envelope is
absent, the server falls back to the current decode path.

Outer JSON object on POST `…/sessions/:id/chat/completions` (and SSE
`devshard_receipt` / response):

```json
{
  "schema_version": 1,
  "height_sync": {
    "chain_id": "gonka-testenv-1",
    "proof_type": "height-anchor-v1",
    "mainnet_height": 1234,
    "mainnet_block_hash_hex": "a1b2…",
    "timestamp_unix_ms": 1714975123456,
    "direction": "request"
  },
  "message": { /* current InferenceRequest / InferenceResponse */ }
}
```

Rules for the PoC:

- `height_sync` is **omitted entirely** in Omit mode.
- When present, only the fields above are populated. `light_block`,
  `sender_signature`, `session_id`, `sender_id`, `nonce_num`, `request_id`
  are not emitted by the PoC sender and are ignored if seen by the PoC
  receiver.
- `mainnet_block_hash_hex` is lowercase hex of the **block hash at
  `mainnet_height`** (CometBFT `BlockID.Hash` for that height). This
  is the same value the proposal calls `mainnet_height_hash`; the
  PoC uses the clearer name on the wire because the field is
  designed to be replayed against the canonical chain — a stored
  attestation `(peer_id, mainnet_height, mainnet_block_hash)` whose
  hash does not match the canonical `BlockID.Hash` for that height
  is direct evidence that the peer previously cheated.
- Backwards compatibility: if the receiver decodes a body that has no
  `schema_version` / `height_sync` / `message` keys, it treats the body as
  the legacy `InferenceRequest` / `InferenceResponse` directly.

---

## 4. Anchor cadence

Cadence is **nonce-driven**, per direction, with **sync-turn
windows** of `slots_num` consecutive nonces. Each outgoing envelope
on a given session direction (user→host requests, host→user
responses) carries a monotonic `nonce`. The scheduler decides Anchor
vs Omit purely from `nonce`, with hints for explicit overrides. The
block oracle is consulted only to fill the Anchor payload
(`mainnet_height`, `mainnet_block_hash`).

```go
// devshard/heightsync/anchor.go (new package)
type AnchorScheduler struct {
    K        uint64                  // envelope nonces between sync turns; >0
    SlotsNum uint64                  // sync-turn window width; >=1
    oracle   blockoracle.BlockOracle
}

type DecideHints struct {
    Nonce        uint64 // monotonic outgoing nonce in this session direction (>=1)
    SessionStart bool   // explicit override; redundant when Nonce<=SlotsNum (cadence already emits Anchor)
    ForceAnchor  bool   // true for manual-force points (explicit height-sync RPC, cPoC skip/carry)
}

// Decide returns the section to attach for the outgoing message at
// the given nonce, or nil for Omit. The cadence rule is:
//
//   inSyncTurn(nonce, K, slots_num) ==
//     (nonce >= 1 && nonce <= slots_num) ||                // initial sync turn
//     (nonce >= K && nonce % K < slots_num)                // periodic sync turns
//
//   emit Anchor IFF
//     hints.ForceAnchor || hints.SessionStart ||
//     inSyncTurn(hints.Nonce, K, slots_num)
//
// All other nonces are Omit. Constraint: K >= slots_num so sync
// turns never overlap.
func (s *AnchorScheduler) Decide(ctx context.Context, h DecideHints) (*HeightSyncSection, error)
```

Properties:

- **Schedulers are scoped per session (per `escrow_id`)** on both
  sides. The user creates a new scheduler when it opens an
  `HTTPClient` for an escrow; the host creates a new scheduler when
  the first request for a session is observed (or eagerly at server
  start — implementation detail). Per-direction nonces (request side
  and response side) are tracked by the caller, not by the
  scheduler.
- **Why `slots_num`-wide windows.** The user round-robins requests
  across the escrow's `slots_num` host slots. A single-nonce Anchor
  would only sync **one** host per period; a window of `slots_num`
  consecutive nonces guarantees **every host in the group** sees an
  Anchor request and replies with its own Anchor inside one sync
  turn. That is the smallest sufficient window for "all hosts learn
  the user's height, the user learns each host's height" without
  flooding the cadence.
- **Two sync-turn families.**
  - **Initial sync turn:** `1 <= nonce <= slots_num` — bootstraps
    the session. Replaces the older "session-start = nonce 1"
    rule on both directions.
  - **Periodic sync turn:** for any `i >= 1`, while
    `i*K <= nonce <= i*K + slots_num - 1`. Equivalent to
    `nonce >= K && nonce % K < slots_num`.
- **`SessionStart` hint** is kept as an explicit override for
  unusual cases (e.g. session reset, test harness) but is **not
  required** by the steady-state design — the cadence rule alone
  covers the bootstrap window.
- **`ForceAnchor` hint:** policy-driven Anchor outside of cadence.
  Used by:
  - manual-force points named in the proposal (e.g. cPoC skip /
    carry messages that require tighter alignment),
  - the deferred explicit height-sync RPC (§ 5.3 / 5.4) when it is
    eventually added.
- **Oracle error handling.** If the oracle returns an error or a nil
  header, Decide returns `nil` (Omit) and logs at debug level — PoC
  must never block traffic on the oracle. When `ForceAnchor` is
  true and the oracle errors, Decide returns the error to the
  caller (so a `ForceAnchor=true` caller — e.g. a future
  height-sync RPC — can surface "no oracle yet" instead of silently
  degrading to Omit).
- **Defaults and validation.**
  - `K` defaults to `10` (proposal example).
  - `SlotsNum` defaults to the escrow's group size (e.g. `4` in
    the testenv); must be `>= 1`.
  - **`K >= SlotsNum`** is enforced at scheduler construction. Set
    `K = SlotsNum` to make sync turns wall-to-wall (every nonce
    emits Anchor); `K = 1, SlotsNum = 1` is allowed and makes every
    nonce Anchor for testing.

No timers — cadence is sampled on outgoing messages. This is the
"piggyback" semantics the proposal expects ("the next applicable
envelope SHOULD include Anchor"). Sessions with no inference traffic
are not addressed by the PoC; if that gap matters in production, the
deferred explicit height-sync RPC (§ 5.3 / 5.4 / § 12) can fill it.

---

## 5. Sender-side changes

### 5.1 User side (`devshard/transport/client.go`, `devshard/cmd/devshardctl`)

- Add a `HeightSync *AnchorScheduler` to `ClientConfig` (nil = no
  height_sync emission, fully backwards compatible). Constructed
  per-escrow / per-session, matching the lifecycle of `HTTPClient`.
- `HostRequest.Nonce` is already monotonic per session direction in
  the existing transport. The client uses that value directly as
  `DecideHints.Nonce` — **no first-send tracking flag needed**, the
  cadence rule emits Anchor for `nonce <= slots_num` automatically.
- On `Send`, the client calls
  `scheduler.Decide(ctx, DecideHints{Nonce: req.Nonce})`. If non-nil,
  wrap the JSON body into the envelope with `direction = "request"`.
  Otherwise emit the legacy body unchanged.
- Concretely with `K = 10`, `slots_num = 4`, the user attaches a
  `height_sync` to outgoing requests at `nonce ∈ {1,2,3,4, 10,11,12,13, 20,21,22,23, …}`
  and Omit elsewhere. The first sync turn (`1..4`) covers all
  hosts in the round-robin so every host returns its own
  `(height, block_hash)` on its response inside the same window.
- Manual-force points (e.g. cPoC skip/carry on the user side, when
  added) pass `ForceAnchor = true` in addition to `Nonce` via
  `InferenceParams.ForceHeightSyncAnchor` → `HostRequest` →
  `HTTPClient.Send` (`DecideHints.ForceAnchor`).
  > **Limitation of the current per-request flag.** The PoC version
  > only forces Anchor on the single envelope it rides on. That is
  > **not** sufficient for cPoC-style "carry" semantics: a forced
  > Anchor on one nonce only synchronizes one host slot, not the
  > round-robin group. The corrected design — **forced sync turn**
  > anchored in a stateful diff message — is specified in §5.5.
- `devshardctl` constructs an `AnchorScheduler` in `main.go` from
  the same `BlockOracle` it already runs (the PoC requires
  `devshardctl` to subscribe to `heightsyncd` directly via
  `devshard/blockoracle/client`; this is a **new** dependency for
  `devshardctl` and is part of the PoC). `slots_num` is read from
  the escrow's group config.

### 5.2 Host side (`devshard/transport/server.go`)

- Add `WithHeightSyncScheduler(*AnchorScheduler)` option to
  `transport.Server`.
- The server keeps a small `map[sessionID]*sessionState` (created on
  first request per session) holding `responseNonce uint64`
  (per-direction monotonic counter for responses) and any
  per-session scheduler instance. **No `firstResponseDone` flag is
  needed**, the cadence rule covers the bootstrap window via
  `responseNonce <= slots_num`.
- In `HandleInference` (and any other handler that returns SSE /
  JSON), the host increments `responseNonce` for this session, then
  calls the scheduler with
  `DecideHints{Nonce: state.responseNonce}` before writing the
  response. With `K = 10`, `slots_num = 4` the host therefore
  attaches `height_sync` on its responses at
  `responseNonce ∈ {1,2,3,4, 10,11,12,13, …}` (the "1..4" portion
  is the initial sync turn — every host that participates in the
  first round-robin pass replies with an Anchor on its very first
  response in the session, regardless of which absolute request
  nonce it serves).
- If the scheduler returns non-nil, wrap the response body / receipt
  in the envelope with `direction = "response"`. Otherwise emit the
  legacy body.
- Manual-force points (e.g. cPoC skip responses) pass
  `ForceAnchor = true` together with the current response nonce via
  `InferenceRequest.force_height_sync_anchor` on the incoming POST body
  (`HostRequest.ForceHeightSyncAnchor` after decode). **PoC
  limitation:** identical to §5.1 — the corrected design uses the
  diff-anchored forced sync turn from §5.5.
- The host already has `host.WithBlockOracle(...)` wired in
  `devshardd-testenv`; the scheduler reuses that same oracle.

### 5.3 Explicit height-sync endpoint (host) — DEFERRED

> **Status: deferred to operator tooling.** With sync-turn cadence,
> a session's initial `nonce <= slots_num` window already gives every
> reachable host an Anchor obligation, so the bootstrap and
> lost-first-response recovery cases are self-healing through the
> normal inference path. This endpoint is not part of the PoC.
> See § 12 Follow-ups for the scenarios that may justify it later.

Sketch retained for the future implementation only:

```go
g.POST("/sessions/:id/height-sync", s.HandleHeightSync)
```

Semantics (when implemented):

- Authenticated via the existing `AuthMiddleware` (signed body).
- Request body empty JSON `{}`; future revisions may carry a "min
  height" hint.
- Response body exclusively the envelope with a populated
  `height_sync` (Anchor); inner `message` `null` / absent. Handler
  calls `scheduler.Decide(ctx, DecideHints{ForceAnchor: true})` (no
  cadence nonce — out of band, does not consume an inference nonce);
  on oracle error returns 503 + JSON error so callers can
  distinguish "no oracle yet" from "host is up".
- Does **not** increment per-session `responseNonce` and is **not**
  counted as part of any sync turn.

### 5.5 Forced sync turn (diff-anchored manual-force) — PROPOSED

> **Status:** proposed redesign. **Supersedes** the per-request
> `force_height_sync_anchor` flag added to `HostRequest` /
> `InferenceRequest` / `InferenceParams` in the PoC. Those flags
> become the **trigger** for opening a forced turn but, on their own,
> are not enough.

#### Why a single-message force is wrong

A user-side `InferenceParams.ForceHeightSyncAnchor` on nonce `N`
under the current PoC plumbing forces Anchor only on the request to
`hostIdx = N % slots_num`. That host's first response also Anchors,
but the **other** `slots_num - 1` hosts in the round-robin still see
Omit. The whole point of the cPoC manual-force path is to align
**every** host in the group on the same `(mainnet_height,
mainnet_block_hash)` before a sensitive transition — exactly the
"sync-turn" semantics defined in §4.

The corrected rule is therefore:

> A `ForceAnchor` request **opens a forced sync turn** of `slots_num`
> consecutive nonces, the same width as a cadence sync turn, then
> closes itself.

#### Wire / state-machine changes

1. **New tx `MsgForceHeightSyncTurn`** (proto):

   ```proto
   message MsgForceHeightSyncTurn {
     uint64 trigger_nonce = 1;     // nonce of the diff that opened the turn
     uint64 end_nonce     = 2;     // trigger_nonce + slots_num - 1, inclusive
     string reason        = 3;     // e.g. "cpoc_skip", "cpoc_carry", "operator"
   }
   ```

   Carried inside `DevshardTx` in the diff that **opens** the turn.
   A diff may contain at most one `MsgForceHeightSyncTurn`.

2. **Escrow-state field `ForcedHeightSyncTurn`**:

   ```go
   type ForcedHeightSyncTurn struct {
       Active     bool   // true while EndNonce >= LatestNonce
       StartNonce uint64 // trigger_nonce
       EndNonce   uint64 // trigger_nonce + slots_num - 1
       Reason     string
   }
   ```

   Set by `state.applyTx(MsgForceHeightSyncTurn)`; cleared automatically
   in `applyCore` once `LatestNonce > EndNonce`.

3. **Idempotence inside an active forced turn.**
   `applyTx(MsgForceHeightSyncTurn)` while `Active == true` and the
   incoming `trigger_nonce <= EndNonce` returns no error but is **a
   no-op** (the message is dropped from `applied`). This satisfies
   *"any `ForceHeightSyncAnchor` inside a non-finished height-sync
   turn is ignored"*. The transport-level
   `InferenceParams.ForceHeightSyncAnchor` flag becomes a **request
   to open** a turn that the state machine ratifies only when no turn
   is active.

4. **Coalescing with cadence sync turns.**
   `AnchorScheduler.Decide` is extended to accept the active turn
   from state:

   ```go
   type DecideHints struct {
       Nonce        uint64
       SessionStart bool
       ForcedTurn   *ForcedHeightSyncTurn // optional, from escrow state
   }
   ```

   The cadence rule becomes:

   - **`ForcedTurn.Active && Nonce <= ForcedTurn.EndNonce`** → emit
     **Anchor** unconditionally.
   - Else apply the existing initial / periodic sync-turn rule from
     §4.
   - **Coalesce.** If the cadence rule **also** marks `Nonce` as
     Anchor (e.g. forced turn opens at `nonce = K - 1` and the
     periodic window starts at `K`), the forced turn extends to
     `max(EndNonce, K + slots_num - 1)` only when the spans **touch
     or overlap**. Strictly: if `K <= EndNonce + 1`, then the cadence
     window for that period is **swallowed** by the active forced
     turn — the scheduler emits Anchor for the union and then closes,
     and **`AnchorScheduler` does not start the cadence window
     again** until the next `i*K`.
   - When the forced turn closes mid-period, the next planned
     cadence window resumes unaffected.

5. **Triggering hooks (no behavior change to the PoC API surface).**
   Both client-side (`InferenceParams.ForceHeightSyncAnchor`) and
   host-side (`InferenceRequest.force_height_sync_anchor` on the
   inbound POST) keep their current shapes. They are now interpreted
   as: *"propose to add `MsgForceHeightSyncTurn` to the next diff."*
   Concretely, on `Session.PrepareInference`, when the flag is set
   **and** the local escrow state's `ForcedHeightSyncTurn` is not
   active, the `composeDiffLocked` step prepends a
   `MsgForceHeightSyncTurn{TriggerNonce: nextNonce, EndNonce:
   nextNonce + slots_num - 1, Reason: ...}` tx. While a turn is
   active, the flag is silently dropped (request still goes through;
   forced-turn state is not re-opened).

6. **Single source of truth: the diff, not the HTTP envelope.**
   The forced turn is **carried exactly once** in the chain — the
   diff that contains `MsgForceHeightSyncTurn` at `trigger_nonce`.
   Every host that applies that diff updates its escrow state
   identically and therefore knows the active window without any
   per-request HTTP signal. That is the **only** authoritative
   trigger; the `force_height_sync_anchor` HTTP flag remains a
   convenience that asks the user-side composer to insert the diff
   tx, nothing more.

   **Why HTTP-level enforcement on the request path is not normative.**
   A malicious user can simply omit `height_sync` from any outbound
   request, including ones inside the forced window. Hosts cannot
   distinguish a "user that decided to skip Anchor" from a "user
   that never wanted to sync" without the diff. The diff makes the
   intent **public and replicated**, so:

   - **Host responses are normatively bound.** Every host whose
     `responseNonce` falls in `[StartNonce, EndNonce]` **MUST**
     emit Anchor on the receipt; its escrow state forces the
     scheduler. Honest hosts that share the diff therefore deliver
     one fresh `(H, hash)` per host slot to the user **regardless
     of what the user puts on the request side**.
   - **User requests SHOULD Anchor**, and the honest reference
     client does (`composeDiffLocked` + `transport.HTTPClient.Send`
     attach `EscrowHeightSyncHints` so the scheduler emits Anchor
     in the window). But this is **best-effort**, not enforced.
     A non-Anchor request inside `[StartNonce, EndNonce]` is **not
     INVALID** at the receiving host — the host just records that
     fact (audit ring + debug log) and serves normally.
   - **What hosts MAY do for evidence, not rejection:** every host
     SHOULD log a `height_sync_force_request_anchor_missing` warn
     line for each in-window request whose `height_sync` is absent
     or non-Anchor. This is dispute material (the user signed a
     diff that opens the turn, then chose not to honour it on the
     wire), but it does not block the request.
   - **Net effect on alignment.** Even with a fully malicious user,
     the round-robin step still bumps the request nonce by 1 per
     host, so each of the `slots_num` hosts produces one
     oracle-trusted **outbound** Anchor in the window. The user
     ends the turn with `slots_num` host attestations regardless;
     skipping request Anchors only deprives the user of evidence
     against itself.

7. **Audit / log marking.**
   Anchors emitted under a forced turn carry an extra structured log
   key `forced_turn_reason=<reason>` (debug log only), and audit ring
   entries set a new `Trigger` field equal to `forced` (not yet on
   the wire — local-only marker for evidence ordering).

#### Backwards compatibility

- Nodes running the PoC version (with the per-request
  `force_height_sync_anchor` flag but no diff message) treat any
  `MsgForceHeightSyncTurn` they don't recognize as **opaque** and the
  diff still applies because state-machine validation is allow-list
  based on known tx kinds (today; if not, we add it). They continue
  to emit Anchor only on the trigger envelope and the rest of the
  group will Omit until they upgrade — i.e. the cPoC alignment is
  not achieved against legacy peers, but no one INVALIDates traffic.
- After §5.5 lands, the per-request flag is **kept** purely as the
  trigger; the state-machine path is the only thing that actually
  drives the multi-nonce behavior.

#### Test coverage (cross-references to §9)

- **Unit (heightsync, `anchor_test.go`)**
  - `TestAnchorScheduler_ForcedTurn_EmitsForFullWindow` — given
    `ForcedTurn{Start: N, End: N + slots - 1}`, every `Decide` for
    `Nonce ∈ [Start, End]` returns Anchor; outside, normal cadence.
  - `TestAnchorScheduler_ForcedTurn_CoalescesWithCadenceWindow` —
    forced turn ending exactly at `K - 1` does **not** double-open
    the cadence window; result is one continuous Anchor span
    `[Start, K + slots - 1]` (when `K - Start <= slots`).
  - `TestAnchorScheduler_ForcedTurn_NonOverlappingDoesNotExtend` —
    forced turn ending well before `K` leaves the cadence window
    (`[K, K + slots - 1]`) intact.
- **State machine (`devshard/state` or wherever escrow state lives)**
  - `TestState_MsgForceHeightSyncTurn_OpensAndCloses` — apply diff
    with the message at `nonce = N`; after `slots` successive
    inference txs, state's `ForcedHeightSyncTurn.Active` flips to
    `false`.
  - `TestState_MsgForceHeightSyncTurn_IgnoredWhileActive` — second
    `MsgForceHeightSyncTurn` arriving before `EndNonce` is dropped;
    `Active`, `StartNonce`, `EndNonce` unchanged; the diff is still
    accepted (no error).
  - `TestState_MsgForceHeightSyncTurn_AtMostOnePerDiff` — two such
    messages in the same diff produce
    `ErrMultipleForceHeightSyncTurnMsgs` (mirror of
    `ErrMultipleStartMsgs`).
- **Transport (`devshard/transport`)**
  - `TestServer_Inference_HeightSync_ForcedTurn_AnchorsAcrossSlots`
    — using `transport`-level fake state with an active
    `ForcedHeightSyncTurn`, the host emits Anchor on every response
    inside the window, including responses whose `responseNonce`
    would normally be Omit.
  - `TestServer_Inference_HeightSync_ForcedTurn_NoReopenAfterClose`
    — once `responseNonce > EndNonce`, host returns to Omit cadence.
  - `TestHTTPClient_Send_HeightSync_ForcedTurn_AnchorsAcrossSlots` —
    same on the user side: a single `InferenceParams.ForceHeightSyncAnchor`
    propagates Anchor on `slots_num` consecutive request envelopes,
    then drops back to cadence.
- **E2E (`devshard/testenv/scenarios`)**
  - **Replaces** `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`
    with the new `TestHeightSyncAnchor_E2E_ForcedSyncTurn_*` family
    described in `SCENARIOS.md` "Manual-force forced sync turn".

### 5.4 Explicit height-sync RPC (user) — DEFERRED

> **Status: deferred** with § 5.3 above.

Sketch retained for the future implementation only:

- Add `HostClient.FetchHeightSync(ctx) (*HeightSyncSection, error)`
  POSTing to the host endpoint and decoding the envelope.
- `devshardctl` exposes this via:
  - A CLI subcommand `devshardctl height-sync [--escrow id]` printing
    the returned `HeightSyncSection` as JSON.
  - The OpenAI-compatible proxy gains a debug path
    `GET /v1/debug/height-sync?escrow=...` that fans out to the
    chosen host and returns the same JSON. This is the interface the
    follow-up `mode=light` / `mode=full` debug API will hang off.

---

## 6. Receiver-side changes

### 6.1 Decode

In `transport.HostRequestFromJSON` / `HostResponseFromJSON` and the
SSE parser:

1. Try to decode the outer envelope shape (`schema_version` +
   `message`).
2. If the envelope is present, classify:
   - `height_sync == nil` → **Omit**.
   - `height_sync.proof_type == "height-anchor-v1"` and
     `mainnet_height > 0` and `len(mainnet_block_hash_hex) > 0` →
     **Anchor**.
   - Anything else → **Omit** (PoC is permissive; future spec rejects).
3. Fall back to legacy decode if the envelope shape is absent.

### 6.2 Log

For every inbound envelope, emit one debug line via `devshard/logging`:

```text
heightsync: peer attestation received
  subsystem=heightsync direction=request|response
  mode=omit|anchor
  peer_id=<address or slot>
  peer_height=<H or 0>
  peer_block_hash_prefix=<first 8 hex chars of mainnet_block_hash or "">
  local_aligned=<H_local>
  delta=<H - H_local or 0>
  trust_level=<see below>   # present when mode=anchor on inbound/outbound paths that classify trust
```

Emit lines also include `trust_level` on host **outbound** anchors and user **outbound** anchors (`trusted_oracle`). **Inbound** classification uses:

| `trust_level` (audit + logs) | Meaning |
|------------------------------|---------|
| `trusted_oracle` | The `(mainnet_height, mainnet_block_hash)` was filled from this side’s **local block oracle** (host response anchors; user request anchors when height-sync is enabled). |
| `untrusted_peer` | **Inbound** Anchor whose `mainnet_height` is **strictly greater** than the local oracle height at receipt time (“ahead-of-oracle” sync — the peer carried a newer tip before this host’s oracle caught up). |
| `peer_aligned` | **Inbound** Anchor at or below the local oracle height at receipt time (peer not ahead of oracle). |

PoC rule: **never** mark the message INVALID based on the height
section. The legacy auth and decode paths are unchanged. The log is
the verification surface for the PoC.

#### Reconciliation when oracle catches up

When the host previously recorded an **`untrusted_peer`** tip at height `H`
from the user (because `H` was greater than the oracle height), it keeps a
per-session **pending** `(H, hash_peer)`. On each subsequent inference for that
session, **before** applying the new inbound envelope, the host compares this
pending tip against the **current** oracle header:

- If `oracle.height == H` and `oracle.BlockHash != hash_peer`, emit a **Warn**
  log (`heightsync: untrusted peer tip disagrees with oracle at reconciled height`)
  with session id, height, peer id, and both hash prefixes — then clear pending.
- If the hashes **match**, clear pending without warning.
- If the oracle tip moves **past** `H` without ever reporting exactly height `H`
  on this path, pending is dropped (no comparison).

This gives operators a concrete signal when a carried-forward peer tip disagrees
with the canonical chain once the oracle reaches the same height.

### 6.3 Audit trail (light, in-memory)

Every Anchor classified at the receiver is appended to a small ring
buffer keyed by `peer_id`:

```go
// devshard/heightsync/audit.go (new file)
type AnchorAttestation struct {
    PeerID           string
    Direction        string   // "request" | "response"
    MainnetHeight    int64
    MainnetBlockHash []byte   // raw bytes of BlockID.Hash for that height
    ObservedAtUnixMs int64
    SourceMessage    string   // e.g. "POST /v1/devshard/sessions/<id>/chat/completions"
    Trust            AttestationTrust // trusted_oracle | untrusted_peer | peer_aligned (see §6.2)
}
```

- Bounded ring (default 1024 entries per peer; configurable later).
- Attestations are **not** verified in the PoC. The follow-up plan
  walks this ring against the canonical `BlockID.Hash` from the local
  oracle and produces evidence for any
  `(mainnet_height, mainnet_block_hash)` that disagrees with the
  canonical chain.
- Exposed in-process for future `host.Host` and `devshardctl` debug
  endpoints; **no HTTP surface in this PoC**.

### 6.4 Minimal extra host state

Aside from the audit ring, the host keeps only an **optional pending
untrusted tip per escrow session** used for reconciliation (§6.2): no
`height_seen_max`, deferred validation queues, or timeout-driving state beyond
what transport already tracks (`responseNonce`, etc.).

---

## 7. Configuration

### 7.1 Testenv config (`devshard/testenv/config/config.go`, `config.yaml`)

Add an optional block:

```yaml
height_sync:
  url: http://height-sync:9100
  ...
  anchor_period_nonces: 10   # K in envelope nonces; 0 or unset defaults to 10
  # sync_turn_slots is implied: defaults to the escrow group_size
  # (devshard.group_size in this config), so an override here is
  # optional. PoC validates K >= sync_turn_slots.
  sync_turn_slots: 0         # 0 or unset → use devshard.group_size
```

- `anchor_period_nonces == 0` or unset → PoC default `K = 10`.
- `anchor_period_nonces < 0` → config validation error.
- `sync_turn_slots == 0` or unset → use `devshard.group_size`.
- `K < sync_turn_slots` → config validation error (sync turns must
  not overlap).
- The values are plumbed into both `devshardd-testenv` (host
  scheduler) and `devshardctl` (user scheduler) via the existing
  config flow.

### 7.2 Host process flag / env

`devshardd-testenv` already reads `HEIGHT_SYNC_URL`. Add:

- `HEIGHT_SYNC_ANCHOR_PERIOD_NONCES` (int) — overrides `K`.
- `HEIGHT_SYNC_SYNC_TURN_SLOTS` (int) — overrides `slots_num`
  (defaults to `devshard.group_size`).

So we can toggle either knob without regenerating compose during
exploration.

### 7.3 `devshardctl`

Mirror the same env / flags (`--height-sync-anchor-period-nonces`,
`--height-sync-sync-turn-slots`), defaulting to `10` and
`devshard.group_size` respectively. Document in
`devshard/docs/testenv-blockoracle-integration.md`.

---

## 8. Observability (debug-only PoC)

- Sender log on every outgoing message: `heightsync: emit
  mode=anchor|omit nonce=n in_sync_turn=true|false height=H
  block_hash=<8hex>`.
- Receiver log as in §6.2.
- One scheduler-level info log at every sync-turn boundary:
  - on entering a sync turn:
    `heightsync: sync turn opened nonce=n window=[n,n+slots_num) K=K slots_num=S`
  - on leaving:
    `heightsync: sync turn closed nonce=n next_window_start=A+K`

No metrics, no traces in the PoC. Hooks for those go in the follow-up.

---

## 9. Tests

### 9.1 Unit (`devshard/heightsync/anchor_test.go`)

- **Initial sync turn:** with `K = 10`, `slots_num = 4`, sweep
  `nonce = 1..3K`. Expected Anchor at
  `{1,2,3,4, 10,11,12,13, 20,21,22,23, 30,31,32,33}`; Omit at
  every other nonce.
- **`SlotsNum = 1` collapses to single-anchor cadence:** with
  `K = 10`, `slots_num = 1`, Anchor only at `{1, 10, 20, 30}` —
  matches the previous `nonce % K == 0` rule.
- **`K = SlotsNum` makes the schedule wall-to-wall:** with
  `K = slots_num = 4`, every nonce in `1..N` is Anchor.
- **`SessionStart` override** still works inside the sync turn (it
  is redundant) and outside it (it forces Anchor even at
  e.g. nonce 5 with `slots_num = 4`).
- **`ForceAnchor` override** forces Anchor at any nonce.
- **Policy-forced message classes** (table-driven: normal inference
  = not forced, cPoC skip/carry = forced).
- **Oracle error handling:** Scheduler returns nil (Omit) and does
  not panic when the oracle errors or returns a nil header. With
  `ForceAnchor = true`, oracle errors propagate to the caller.
- **Constructor validation:** `K < SlotsNum` returns a config error
  / panics in tests; `K = 0` defaults to `K = 10`; `SlotsNum = 0`
  defaults to `1`.

- **`InboundTrust` (`heightsync/audit_test.go`):** inbound Anchor
  classification vs local oracle height (`untrusted_peer` vs
  `peer_aligned`).

### 9.1b Host trust + reconciliation (`devshard/transport/server_test.go`)

- **`TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`:**
  user sends an Anchor **ahead** of the host oracle; oracle later
  reaches the **same** height with a **different** `BlockHash` → one
  **Warn** reconciling the pending untrusted tip.
- **`TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn`:**
  same setup but oracle hash matches the stored peer tip → **no** Warn.

### 9.2 Envelope (`devshard/transport/envelope_test.go`)

- Round-trip Anchor and Omit envelopes through the JSON encoder /
  decoder.
- Legacy bodies (no envelope) decode through the backwards-compat path.
- Forward-compat: an envelope with extra unknown fields in
  `height_sync` decodes as Anchor and ignores the extras.

### 9.3 End-to-end (testenv)

In `devshard/testenv/scenarios/`:

1. Stand up the existing 4-host stack with `heightsyncd` configured
   for short blocks (`block_interval_delta: 1s` to keep iteration
   fast), `devshard.group_size: 4`, and
   `anchor_period_nonces: 8` (so `K = 8`, `slots_num = 4`, sync
   turn windows of 4 nonces every 8 nonces — plenty of Omit
   visibility between turns).
2. **Initial sync turn (round-robin all hosts).** Open a fresh
   escrow / `HTTPClient` and send `nonces 1..4`. Assert:
   - **All four** outgoing user requests carry Anchor (cadence
     covers `nonce <= slots_num`).
   - **All four** host responses (one per host slot) also carry
     Anchor on their `responseNonce = 1` reply (each host's first
     response in the session falls inside the host's own
     `1..slots_num` window).
   - Both sides log `mode=anchor`; user-side audit ring contains
     four host attestations (one per `peer_id`); each host's
     audit ring contains one user attestation. All recorded
     `mainnet_block_hash` values match the canonical block hash
     reported by the local oracle for the same height.
3. From `devshardctl`, send `nonces 5..16` (covers nonces 5..7
   = Omit, 8..11 = next sync turn, 12..15 = Omit, then 16 = next
   sync turn start).
4. Assert (by scraping host + `devshardctl` logs):
   - User outgoing modes match the expected pattern:
     `5..7 = omit`, `8..11 = anchor`, `12..15 = omit`, `16 = anchor`.
   - Each receiving host logs `mode=anchor` exactly for the
     subset of those nonces it actually serves (depends on the
     round-robin slot mapping); other nonces it serves are
     `mode=omit`.
   - The block-hash prefixes printed by the user's emitted Anchors
     match what the host reports as `peer_block_hash_prefix` for
     the same nonce (no verification, just string equality).
   - User audit ring keeps growing across all four `peer_id`s as
     subsequent sync turns roll past.
4.1. **Cross-host higher-tip carry-forward.** Configure mixed oracle
   heights for one sync turn:
   - host 1 at height `X`,
   - host 2 at height `X+1`,
   - host 3 and host 4 at height `X`,
   - client local oracle at height `X`.
   Then assert:
   - once client receives host 2's attestation at `X+1`, subsequent
     in-turn user Anchors carry `X+1` to later hosts served in the
     same sync turn;
   - those receiving hosts store inbound user attestation `X+1` in
     their audit rings even before their own oracle follower reaches
     `X+1`.
5. **Lost-first-response self-healing** (replaces the older
   "recovery via explicit fetch" case). Kill the host that serves
   nonce = 1 immediately after `devshardctl` sends the request but
   before the response is delivered. Without restarting that host,
   send nonce = 2 (round-robins to the next slot). Assert:
   - The nonce = 2 request still carries Anchor (still inside the
     initial `1..slots_num` sync turn).
   - The receiving host responds with Anchor on its own first
     response (also inside the initial sync turn).
   - The user's audit ring is populated with at least one host
     attestation by the time nonce 4 completes — i.e. height-sync
     bootstrap completes through the normal inference path even
     though one host was lost. **No explicit `height-sync` RPC is
     used.**
6. **Manual-force path (cPoC-style) — forced sync turn.** Drive a
   `MsgForceHeightSyncTurn` (see §5.5) at a nonce that falls
   **outside** any cadence sync turn. The trigger is **a single
   diff tx** (not per-host HTTP flags); every host learns the
   forced window by applying that one diff. Asserts across the
   **entire `slots_num`-wide window**, not just the trigger nonce:
   - **Authoritative state replicated via the diff.** Each host's
     escrow state, after applying the trigger diff, has
     `ForcedHeightSyncTurn{Active: true, Start: trigger,
     End: trigger + slots_num - 1}`. No per-request HTTP signal
     is required to keep this in sync.
   - **Host→user response Anchors are normatively bound.** Every
     host whose response falls in `[StartNonce, EndNonce]` emits
     Anchor on the receipt regardless of cadence (so each host
     slot replies with its own `(height, hash)` observation in
     the same window). User-side audit ring accumulates exactly
     `slots_num` host Anchors with `direction=response`.
   - **User→host request Anchors are best-effort.** The honest
     reference client emits Anchor on every request in the
     window via `EscrowHeightSyncHints`. A malicious user that
     omits `height_sync` on in-window requests does **not** make
     the request INVALID — receiving hosts log a
     `height_sync_force_request_anchor_missing` warn entry into
     the audit ring as dispute evidence, but the request still
     processes. Honest-user assertions (each host's audit ring
     records exactly one inbound user Anchor in the window) live
     in the dedicated honest-user e2e test; the malicious-user
     test asserts the warn entries instead.
   - A second `MsgForceHeightSyncTurn` issued **before** `EndNonce`
     is **silently ignored** by the state machine (no double-open,
     no extension), regardless of whether the user re-sets the
     HTTP flag.
   - When the forced window ends within the next periodic cadence
     window, cadence Anchors **resume** at `i*K` unaffected.
   - When the forced window **overlaps or touches** the next
     periodic cadence window (`K - StartNonce <= slots_num`),
     cadence emission for that period is **swallowed** by the
     forced one — there is no double-Anchor on the boundary nonce.

   **Status:** **superseded.** The per-request PoC wiring
   (`DecideHints.ForceAnchor` plumbed through `HostRequest` /
   `InferenceRequest` / `InferenceParams`) and its tests
   (`TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest`,
   `TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip`,
   `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`) only cover
   the single-message variant; they will be **kept** to lock the
   trigger plumbing and **replaced** by the §5.5 forced-sync-turn
   tests:
   - `heightsync`: `TestAnchorScheduler_ForcedTurn_*`
   - `state`: `TestState_MsgForceHeightSyncTurn_*`
   - `transport`: `TestServer_Inference_HeightSync_ForcedTurn_*`,
     `TestHTTPClient_Send_HeightSync_ForcedTurn_AnchorsAcrossSlots`
   - `scenarios`: `TestHeightSyncAnchor_E2E_ForcedSyncTurn_*`
     (see `SCENARIOS.md` "Manual-force forced sync turn").
7. **Cheating-trail check** (still no live verification): inject a
   modified `mainnet_block_hash` for one outgoing user request
   (test helper that bypasses the scheduler). Assert the host's
   audit ring stores that bogus hash verbatim against the same
   `mainnet_height` the canonical chain has, so a future verifier
   comparing the ring to the local oracle would flag this entry.
   **Status:** implemented — `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`
   (`transport.ClientConfig.HeightSyncRequestMutateHook`); scenario write-up in
   `devshard/testenv/scenarios/SCENARIOS.md`.
8. **Negative.** Stop `heightsyncd` mid-run; subsequent inference
   sends in a sync turn emit `mode=omit` and produce no errors.
   Inferences keep succeeding.
   **Status:** implemented in-process — `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`
   (`sharedStoppingOracle` simulates a shared feed failure for all hosts + user); scenario
   and proposed compose follow-ups in `devshard/testenv/scenarios/SCENARIOS.md`.

### 9.4 Smoke

Add a shell helper under `devshard/testenv` (or extend the existing
one) that runs the e2e scenario above and `grep`s the log markers,
exit non-zero if no Anchor is observed.

> **Container-level E2E follow-up.** The §9.3 scenarios above are
> currently implemented **in-process** (`httptest` hosts, static
> `BlockOracle`). The plan to re-implement every scenario against the
> real `docker compose` stack — real `heightsyncd` + `mockdapi` SSE
> client + Loki / VictoriaMetrics assertions — lives in
> [`devshard/testenv/scenarios/CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md).
> The §9.4 smoke wrapper is delivered as
> `TestContainerE2E_HeightSync_Smoke` in that plan's Phase C.

---

## 10. Step-by-step implementation order

1. **`devshard/heightsync` package** — `HeightSyncSection` JSON struct
   (with `mainnet_block_hash_hex`), `AnchorScheduler` with
   `DecideHints{Nonce, SessionStart, ForceAnchor}` and the
   sync-turn cadence rule (`nonce <= SlotsNum` ∪
   `nonce >= K && nonce % K < SlotsNum`), `AnchorAttestation` +
   audit ring, unit tests. No transport hookup yet.
2. **Envelope wire** — add the wrapper struct + decode helpers in
   `devshard/transport/envelope.go`, with backwards-compat decode
   and round-trip tests.
3. **Host send / receive** — wire `WithHeightSyncScheduler` and
   classification log in `transport.Server`; per-session monotonic
   `responseNonce` drives the `Nonce` hint (no first-response flag
   needed — initial sync turn is covered by cadence); append every
   classified Anchor to the audit ring.
4. **User send / receive** — wire scheduler into `HTTPClient.Send`,
   the request's existing monotonic nonce (`HostRequest.Nonce`)
   drives the `Nonce` hint (no first-send flag needed); hook into
   the SSE response parser; append every classified Anchor to the
   audit ring on the user side.
5. **(Deferred) Explicit height-sync RPC** — `POST .../height-sync`
   route + `HandleHeightSync` on the host, `HostClient.FetchHeightSync`
   on the user, `devshardctl` CLI subcommand and
   `/v1/debug/height-sync` proxy path. **Not implemented in the PoC.**
   Sync-turn cadence makes bootstrap and lost-first-response
   recovery self-healing through the normal inference path; this
   step is reserved for operator tooling (see § 12 Follow-ups for
   the scenarios that justify reviving it).
6. **Config plumbing** — extend `testenv/config` with
   `anchor_period_nonces` and `sync_turn_slots` (defaulting to
   `devshard.group_size`), plumb into `devshardd-testenv` and
   `devshardctl`. Regenerate `docker-compose.yml` via `gencompose`.
7. **Testenv scenario** — add the e2e scenario described in §9.3
   and the smoke wrapper.
8. **Docs** — add a short paragraph in
   `devshard/docs/testenv-blockoracle-integration.md` pointing at
   the new debug logs and manual verification commands.

Each step is independently reviewable and ships the next thinnest
slice of the PoC.

---

## 11. Files touched (expected)

- `devshard/heightsync/anchor.go` — new package, `AnchorScheduler`,
  `HeightSyncSection` (with `mainnet_block_hash` field).
- `devshard/heightsync/audit.go` — `AnchorAttestation` + bounded ring
  buffer keyed by `peer_id`.
- `devshard/heightsync/anchor_test.go` — unit tests for scheduler.
- `devshard/heightsync/audit_test.go` — unit tests for the ring.
- `devshard/transport/envelope.go` — new file, JSON envelope + helpers.
- `devshard/transport/envelope_test.go` — round-trip tests.
- `devshard/transport/client.go` — `ClientConfig.HeightSync`, wrap
  request body using `HostRequest.Nonce`, classify response.
  (`FetchHeightSync` deferred with Step 5.)
- `devshard/transport/server.go` — `WithHeightSyncScheduler`,
  classify request, wrap response using per-session `responseNonce`.
  (`HandleHeightSync` deferred with Step 5.)
- `devshard/cmd/devshardctl/main.go` — construct scheduler, plumb
  oracle, `K`, and `slots_num` (from escrow group config).
  (`height-sync` CLI subcommand and `/v1/debug/height-sync` proxy
  path deferred with Step 5.)
- `devshard/testenv/cmd/devshardd-testenv/main.go` — read
  `anchor_period_nonces` and `sync_turn_slots` (defaulting to
  `devshard.group_size`), construct scheduler, pass to server.
- `devshard/testenv/config/config.go` — add `AnchorPeriodNonces`
  and `SyncTurnSlots` fields with validation
  (`K >= sync_turn_slots`, `sync_turn_slots >= 1`).
- `devshard/testenv/config.yaml` — regenerated with the new field.
- `devshard/testenv/scenarios/heightsync_anchor_test.go` — new
  scenario.
- `devshard/docs/testenv-blockoracle-integration.md` — short manual
  verification note pointing at the new logs.

---

## 12. Follow-ups (after this PoC lands)

Tracked, **not** implemented by this plan:

- Replace JSON Representation B with protobuf Representation A
  (`GonkaUserHostEnvelope`) once the wire stabilizes.
- Add `sender_signature` over the height-sync signing input from the
  proposal (§ Sender signature) and verify on the receiver.
  - Carry-forward must **propagate the originator's signed blob
    verbatim** (no re-signing by the user) so the receiver can
    distinguish a host-originated claim from a user-fabricated one
    when the next host disagrees on the hash for the same height.
    Without a verifying signature, dispute blame defaults to the
    user. See `devshard/testenv/scenarios/SCENARIOS.md`
    §"Same-height/different-hash carry-forward → dispute trigger".
- Add deferred verification queue: enqueue `(H, hash)` when the
  receiver's local follower has not yet reached `H`; resolve on
  follower advance; emit `DEFERRED_FAIL` evidence on mismatch.
  - On the **user side**, this also drives **malware-host attribution**:
    every carried-forward tip records the `OriginPeerID` of the host
    that first attested it; when the user's local oracle reaches the
    height, deferred verification names that origin host on mismatch.
    Test scenarios captured in
    `devshard/testenv/scenarios/SCENARIOS.md`
    (§"Client-side malware-host detection and Δ semi-trust (proposed)").
- Implement Strong mode: carry `LightBlock`, run CometBFT
  `VerifyCommit`, and reject Anchor when `|Δ| > D`.
  - Add `D` (default `2`) as a configurable
    **`height_sync.strong_threshold_d`** knob; the user **MUST NOT**
    carry forward tips with `|Δ| > D` from peer attestations and
    instead requests Strong (`mode=full`, depends on Step 5 RPC) or
    falls back to its own oracle. See linked SCENARIOS.md section
    above for proposed tests.
- Implement Step 3b: bind validator set to mainnet epoch participants
  for epoch-bound escrows.
- Wire `height_seen_max` into the existing timeout path.
- Promote logs to metrics + traces with the observability stack.
- **Explicit height-sync RPC (Step 5, deferred — operator tooling).**
  `POST /v1/devshard/sessions/:id/height-sync` returning a forced
  Anchor without consuming an inference nonce, plus
  `HostClient.FetchHeightSync`, the `devshardctl height-sync`
  subcommand, and the `/v1/debug/height-sync` proxy. Sync-turn
  cadence already makes bootstrap and lost-first-response recovery
  self-healing through the normal inference path, so this is no
  longer protocol-critical. Revive when one of the following
  operator-tooling needs lands:
  1. **Stand-alone height fetch from `devshardctl`.** Smoke / CI
     scripts and operators want "what does host X think the
     mainnet tip is right now?" in one HTTP call, without crafting
     a real inference, signing an application payload, or
     consuming an escrow nonce. Useful for verifying a freshly
     deployed host has its oracle wired correctly before any
     traffic flows.
  2. **Forensics / dispute snapshot.** Capture what host X claims
     **right now**, regardless of session activity, so a dispute
     reviewer can compare the snapshot against canonical mainnet
     and against archived audit-ring entries. Same shape as (1)
     with a different consumer.
  3. **Liveness probe with fresh oracle data.** Heartbeat /
     monitoring path that confirms two things at once: the host
     responds, and its oracle is producing fresh `(H, hash)`. A
     plain TCP / `/healthz` check confirms only the former; this
     RPC adds oracle freshness without the cost of a real
     inference.
