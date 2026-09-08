# Height-sync log-plane errors and the dispute path

**Spec:** [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §10.3.1, §14 (L0–L7), §18 (dispute layer)
**Code:** `devshard/heightsync/logplane.go` (errors, `checkL0`–`checkL7`), `devshard/heightsync/marks.go` (`MarkKind`), `devshard/heightsync/prom_logplane.go` (metrics)
**Plan:** [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md) §8.7 (L0–L7), H109
**Tests:** [`height-sync-tests.md`](./height-sync-tests.md) H13d, H13f–H13i, H30, H109

This page is the index for the five `INVALID` errors declared in
`devshard/heightsync/logplane.go` and for the `MARK(height_unbacked)` outcome
introduced with the §10.3.1 rule. It states **when** each one fires,
**where** it is logged and counted, and **which** of them are meant to
become disputes once the dispute layer (spec §18) lands in devshard. The
dispute layer is not shipped yet; until it is, the error-level log line and
the Prometheus counter are the whole operator signal, and the diff either
applies (for a mark) or is rejected (for an `INVALID`).

## The two kinds of outcome

The log plane produces two shapes of verdict, and the distinction is the
whole story of this page:

| Shape | Constant | Effect on the diff | Effect on the escrow |
| ----- | -------- | ------------------ | --------------------- |
| `INVALID(reason)` | `ErrHeightRegression`, `ErrBadFraming`, `ErrAckSigInvalid`, `ErrAckCausality` | the diff is **rejected**, the nonce is **not consumed** | no state change; a producer that resends the same payload loops on the same line until it lifts to `F(m)` or omits |
| `MARK(kind)` | `MarkHeightUnbacked`, `MarkAdmissionDelta`, `MarkDeferredFail`, `MarkDisputeOriginator`, `MarkDisputeCarrier`, `MarkVectorContradiction` | the diff **applies**; the mark is appended to the `MarkLog` | evidence retained for later aggregation |

An `INVALID` is a consensus verdict: every replaying verifier reaches it
from `Diff` alone, so refusing the diff cannot split the escrow. A `MARK`
is per-verifier evidence: it is recorded where the diff is verified, and
the same diff arriving by catch-up or gossip still applies — refusing it
on a mark would let one lagging verifier stall an escrow over a claim that
changes nothing (the §10.3.1 rationale for `height_unbacked`).

## The five `INVALID` errors

All five are declared in one block in `logplane.go`:

```go
var (
    ErrHeightRegression = errors.New("INVALID(height_regression)")
    ErrBadFraming       = errors.New("INVALID(bad_framing)")
    ErrAckSigInvalid    = errors.New("INVALID(ack_sig_invalid)")
    ErrAckCausality     = errors.New("INVALID(ack_causality)")
    // ErrStrongRequired belongs to the transport plane only ...
    ErrStrongRequired = errors.New("INVALID(strong_required)")
)
```

Four are live log-plane verdicts. The fifth, `ErrStrongRequired`, is a
**reservation** for the Strong phase (spec §15) and is never returned by
the log plane today.

### `ErrHeightRegression` — `INVALID(height_regression)`

**Fires in:** `checkL0` (every Diff-resident stamp below `F(m)`) and
`checkL0b` (a `finish` below the `confirm` of the same `inference_id`).

**Condition.** A height produced while handling nonce `m` is below
`F(m)`, the reference height the log had established at nonces `< m`, or
— for L0b only — an executor's `finish` is below its own `confirm`. An
honest producer can always satisfy the lower bound: it stamps
`max(own_tip, F(m))` or omits, so a violation is a shipping bug, a
stale-floor replica, or authored misbehaviour. The nonce is not
consumed, so retries of the same payload keep failing until the stamp is
lifted or omitted.

**Logged at:** error level, inside the check, with structured fields
(`escrow`, `leg`, `height`, `floor`, `producing_nonce` for L0; `leg`,
`height`, `confirm_height`, `inference_id` for L0b). The caller does not
re-log, because the structured fields are only visible inside the check.

**Counted at:** `ObserveLogPlaneReject("height_regression")`, called only
where the verdict is acted on — `state.checkLogPlaneLocked` and the
trial loop in `state.machine.applyTx` that drops the tx. The metric is
`devshard_heightsync_ack_rejected_total{reason="height_regression"}`.

**Documented at:** spec §14, L0 and L0b rows; plan §8.7, L0/L0b rows;
catalog H13d (below floor), H13f (ack below floor), H13g (confirm vs
producing nonce), H30 (finish below confirm).

**Dispute?** No — not on its own. A regression is a self-contradiction
against the log the signer also signed, so it is already terminal: the
diff does not apply, the nonce is not consumed, and no evidence needs to
outlive the exchange. The signer that keeps sending the same payload
loops on the rejection until it lifts or omits; there is no deferred
claim for a dispute to re-adjudicate. (A *pattern* of regressions from
one signer is operator-visible and may inform a slashing case, but that
is an off-chain aggregation, not a per-diff dispute verdict.)

### `ErrBadFraming` — `INVALID(bad_framing)`

**Fires in:** `checkL1`, on the first heartbeat or ack that violates any
of six framing rules: `slots_num` equals group size; `len(sync_vector)
≤ slots_num`; `len(observed_block_hash) ≤ 32` (empty is legal);
`ref_nonce ≠ 0`; `slot_id < slots_num`; `peer_seen` byte length fits
`slots_num`. There is no turn-id rule any more: a turn is named by its
span-start nonce, so the `1<<60` claim that once needed a stay-or-next
bound is unexpressible (P2, landed).

**Logged at:** error level, by the caller `CheckDiffLogPlane`, with
`check=L1`, `verdict=INVALID`, and the wrapped error string.

**Counted at:** `ObserveLogPlaneReject("bad_framing")`; metric
`devshard_heightsync_ack_rejected_total{reason="bad_framing"}`.

**Documented at:** spec §14, L1 row; plan §8.7, L1 row; catalog H13a.

**Dispute?** No. Framing is a wire-shape check; a malformed message is
either a bug or a denial-of-service attempt, and rejecting it without
consuming the nonce is the whole defence. There is no signed claim to
attribute — the bytes do not parse — so nothing survives to a dispute.

### `ErrAckSigInvalid` — `INVALID(ack_sig_invalid)`

**Fires in:** `checkL2`, when a `MsgHeightAck.host_sig` does not verify
over `HeightAckContent` for the key registered at `slot_id`, or when no
verifier is configured for a diff that carries acks (fail closed —
heartbeat-only diffs may still pass with no verifier).

**Logged at:** error level, by the caller, with `check=L2`.

**Counted at:** `ObserveLogPlaneReject("ack_sig_invalid")`; metric
`devshard_heightsync_ack_rejected_total{reason="ack_sig_invalid"}`.

**Documented at:** spec §14, L2 row; plan §8.7, L2 row; catalog H13a.

**Dispute?** **Yes, planned.** A bad `host_sig` is a host identity
attesting to a height it did not hold, which is exactly the
`DISPUTE_ORIGINATOR` shape of spec §18. The signature is in `Diff`, so
the evidence is replayable; the dispute layer will re-adjudicate it
against the host's registered key and slash on a verifier vote quorum.
Today the diff is rejected and the nonce is not consumed, which is
sufficient to keep the escrow correct; the dispute adds the slash.

### `ErrAckCausality` — `INVALID(ack_causality)`

**Fires in:** `checkL3`, when a `MsgHeightAck.ref_nonce` names no
`MsgHeartbeat` — neither in this diff nor already folded into the
`TurnTracker`. The turn follows from `ref_nonce`, so with `turn_seq`
gone (P2) there is no second opinion to cross-check.

**Logged at:** error level, by the caller, with `check=L3`.

**Counted at:** `ObserveLogPlaneReject("ack_causality")`; metric
`devshard_heightsync_ack_rejected_total{reason="ack_causality"}`.

**Documented at:** spec §14, L3 row; plan §8.7, L3 row; catalog H13a.

**Dispute?** **Yes, planned.** A causal ack names a heartbeat the user
never sent — the user signed the diff claiming a turn that does not
exist, which is the cPoC C3′ shape. The user's diff signature is the
evidence; the dispute layer will attribute it to the appending user.
Today the diff is rejected and the nonce is not consumed.

### `ErrStrongRequired` — `INVALID(strong_required)` (reservation)

**Fires in:** **nowhere on the log plane.** The constant exists only so
the transport plane can return it during an exchange it refuses at
admission (L5a) once Strong (spec §15) lands. The comment on the
declaration is the only place this is documented in code.

**Why not on the log plane.** Divergence between followers is
monitoring, permanently (spec §14, *Divergence is monitoring,
permanently — which is why it left the log*): a verifier that refused
an exchange at the edge and a verifier that ingests the same diff by
catch-up hold different state, and an `INVALID` here would split the
escrow through a check documented as replay-identical. The log plane
has no counterpart to L5a; Strong sharpens the refusal into a proof
obligation without widening its scope.

**Logged at:** not yet — no log-plane call site exists. The transport
plane will log its refusals when Strong lands.

**Counted at:** not yet on the log plane. `ObserveLogPlaneReject`'s
allowlist does not include `strong_required`; it will be added when the
transport plane starts returning the error.

**Documented at:** spec §14, the transport-plane `INVALID(reason)` enum
and the L5a row ("with Strong, `INVALID(strong_required)` for that
exchange; **never** a permanent diff verdict"); plan §8.7, L5a row;
catalog H13c (replay yields no `INVALID`).

**Dispute?** **Yes, by design.** Strong *is* the dispute path for an
out-of-band height: the claimant supplies a `LightBlock` (spec §18.4)
and the dispute verifier re-runs `VerifyLightBlock` against the pinned
validator set. `INVALID(strong_required)` is the refusal that creates
the proof obligation; the dispute is what adjudicates it. This is the
canonical "dispute, not only a log line" case — it is just not wired
yet.

## The `MARK(height_unbacked)` outcome

This is not one of the five `INVALID` errors, but it is the new
log-plane outcome introduced with the §10.3.1 rule, and it is the one
most directly shaped as a future dispute.

**Fires in:** `checkL0`, when a **sequencer-composed** stamp
(`MsgHeartbeat` or `MsgStartInference`) carries `H > F(m)`. The user
holds no protocol oracle, so its only truthful stamp is exactly `F(m)`
— the floor a host already seeded. Anything above that is a number the
user made up.

**Why a mark, not an `INVALID`.** The claim is already inert: rule 3
keeps it out of `F`, and the turn clock reads host stamps only. An
`INVALID` on a floor read would let one lagging verifier refuse a diff
the rest accept, splitting the escrow over a claim that changes
nothing. The mark cannot fire on honest lag, because the producer had
the log in front of it and `F` is *in* the log.

**Logged at:** error level, via `logLogPlaneMisbehaviour` — the one
path that logs a `MARK` at error rather than debug, on the grounds
that ordinary marks (L5a, L6) fire on honest lag while this one
cannot. Fields: `escrow`, `detail`, `leg`, `height`, `floor`,
`producing_nonce`.

**Counted at:** `MarkLog.Append` → `IncMarks("height_unbacked")`;
metric `devshard_heightsync_marks_total{kind="height_unbacked"}`. It
is **not** in `ObserveLogPlaneReject`'s allowlist, because it is not a
rejection.

**Documented at:** spec §10.3.1 (the receiving-verifier table and the
"Planned: dispute signal" paragraph); spec §14, L0 row; plan §8.7, L0
row; catalog H13h; PR review [`height-sync-pr-1584-review.md`](./height-sync-pr-1584-review.md) P1.

**Dispute?** **Yes, planned (⏳ §18).** `height_unbacked` names
authored misbehaviour with the signature attached, so it belongs on the
same dispute path as L6 `DEFERRED_FAIL` rather than in an operator's
log grep. The signal is deliberately deferred until the Strong phase
(spec §15) provides the artifact a dispute is judged against. Today
the error-level log line is the whole operator signal.

## The other marks (for completeness)

These are not `INVALID` errors and are not new, but they round out the
`MARK` column. All are recorded by `checkL4`/`checkL5a`/`checkL6`/`checkL7`
and counted by `IncMarks`; the dispute story for each is in spec §18.

| `MarkKind` | Fires in | Dispute? |
| ---------- | -------- | --------- |
| `dispute_originator` | L4 (ack height ≠ `max(anchor, F(m))`), L6 (hash mismatch with verified originator) | **Yes** — the canonical `DISPUTE_ORIGINATOR` of §18; same-exchange self-contradiction, no oracle lookup needed |
| `dispute_carrier` | L4 (heartbeat height ≠ request-leg section the user signed) | **Yes** — `DISPUTE_CARRIER` of §18; carrier became the signer |
| `vector_contradiction` | L7 (`sync_vector` says `ACKED(j, h, n)` but `Diff[n]` has no ack) | **Yes** — user-attributable, diff already signed |
| `deferred_fail` | L6 (oracle reconciles `(H, hash)` once the follower reaches `H`) | **Yes** — `DISPUTE_ORIGINATOR` once the origin blob is fetched; blame does not rewind `F` (Phase F cleans it, H109) |
| `l5a_admission` | L5a (`\|observed_height − local_aligned\| > D` at admission) | **Yes, via Strong** — the refusal becomes `INVALID(strong_required)` and the `LightBlock` dispute of §18.4 |

## Summary: which outcomes are meant to become disputes

| Outcome | Today | After the dispute layer (§18) |
| -------- | ----- | --------------------------- |
| `INVALID(height_regression)` | reject, no dispute | reject; off-chain pattern aggregation only |
| `INVALID(bad_framing)` | reject, no dispute | reject, no dispute (no signed claim) |
| `INVALID(ack_sig_invalid)` | reject, no dispute | reject **+** `DISPUTE_ORIGINATOR` (host slash) |
| `INVALID(ack_causality)` | reject, no dispute | reject **+** cPoC C3′ dispute (user slash) |
| `INVALID(strong_required)` | not emitted | transport refusal **+** `LightBlock` dispute (§18.4) |
| `MARK(height_unbacked)` | mark, error log | `DISPUTE_ORIGINATOR` on the sequencer (§18, planned ⏳) |
| `MARK(deferred_fail)` | mark | `DISPUTE_ORIGINATOR` (§18) |
| `MARK(dispute_originator)` / `dispute_carrier` | mark | already the dispute verdict |
| `MARK(l5a_admission)` | mark | Strong dispute (§18.4) |

The pattern: an `INVALID` that rejects a diff never needs a dispute on
its own, because the diff did not apply — there is nothing to
re-adjudicate. A `MARK` that lets the diff apply always needs a dispute,
because the escrow moved on the claim and only a later verifier vote can
un-move it. The two exceptions are `bad_framing` (no signed claim to
attribute) and `height_regression` (the rejection is already terminal;
a dispute would add nothing). Everything else — `ack_sig_invalid`,
`ack_causality`, `strong_required`, and every `MARK` — is on the
dispute path the moment the dispute layer lands.
