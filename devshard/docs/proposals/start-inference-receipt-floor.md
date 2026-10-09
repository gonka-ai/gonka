# Proposal: StartInference height as a receipt floor

**Status:** Draft / proposal  
**Related:** [HEIGHT_SYNC_PROTOCOL_PROPOSAL.md](HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §14 producer rule, L0 / L0b  
**Scope:** Honest `MsgConfirmStart` / executor receipt (and `MsgFinishInference` using the same lift) must never stamp below the height already on that nonce’s `MsgStartInference`.

This is a design note only; it does not change code by itself.

---

## Goal

A receipt that is produced at all must **land**. Its reference height must be **at least** the height stamp already on the corresponding nonce (`MsgStartInference` at `inference_id`), when that start is stamped.

Without that, a lagging executor can sign a real receipt at its own tip `H` while the start already carries `H+1` (or `F(m)` the sequencer carried onto the start). L0 then drops `MsgConfirmStart` (`INVALID(height_regression)`), the inference stays `Pending`, and work that happened never settles.

---

## Problem

L0 for confirm/finish uses **`F(m)` at the producing nonce** `m = inference_id`: the last host-attested floor from diffs that **landed before** `m`. Floor entries are indexed by **landing** nonce, so:

- Raises that land **after** this inference (other receipts returning first, later acks) do **not** bind this receipt. Pipelined / async receipt order is already safe.
- The start stamp **lives in Diff `m`**. It is therefore **not** part of `F(m)`. Sequencer stamps also **do not raise** `F` (rule 3).

The sequencer already stamps start as `max(seq_tip, F(m))` or omits (`user/heartbeat.go` `referenceStampLocked`). When present, that pair is a lower bound this nonce already advertised.

The executor does **not** use it. `host.referenceStamp` only lifts to `HeightSyncFloorAsOf(inference_id)`. L0b does **not** compare start vs confirm: start is user-signed; an executor behind a sequencer carry that is *above* `F(m)` is not a regression on the log plane.

So a host that applied **this** start (required to have the inference at all) but whose `FloorIndex` is empty or stale — never synced earlier diffs, vote/challenge catch-up incomplete, store-dry `ChallengeReceipt` — can stamp own tip `H` while start already has `H' > H` and `H' ≥ F(m)`. Apply drops ConfirmStart. The receipt does not land.

Vote path already sends missed diffs (`diffsForHost` on `VerifyTimeout`; verifiers then forward stored `1..LatestNonce` on challenge). That is necessary for the inference record. It is **not** sufficient for the stamp if the executor still signs from an empty floor after applying only this start.

---

## Proposed producer rule

Extend the executor producer rule (same call sites as today: `signReceipt`, challenge receipt, finish):

```text
bar     = max(F(m), start.observed_height)   # start only if StampPresent
stamp   = max(own_tip, bar)                  # or omit
```

Omit when `bar − own_tip > W_conf` (poisoned / unreachable branch), same as today’s floor escape. A host with **no** own tip (`t = 0`) still **carries** (cannot use omit).

Lift with the **pair**: height **and** `observed_block_hash` of whichever bound won. Never mix start height with the host’s own hash. The live record stores `StartedAtHeight` only; implementation must take the hash from the applied start tx (or persist the pair on the record).

When start is **unstamped**, `bar = F(m)` and behaviour is unchanged. Catch-up / floor remain required for L0 in that case.

`MsgFinishInference` uses the same `referenceStamp(inference_id, …)` path. After confirm has lifted to the start bar, finish must still be `≥` confirm (existing L0b). Using the same bar on finish keeps that automatic.

---

## What this is not

**Not an L0b verdict `confirm ≥ start`.** That would `INVALID` an honest executor whose tip is below a sequencer carry *above* `F(m)` if they omitted or if an old receipt is replayed. L0 stays exact: `≥ F(m)` only. Start is a **local source** of that bar (and of the sequencer’s carry on this nonce), not a stricter consensus check.

**Not a ±1 L0 band, and not “judge against the landing Diff’s live floor.”** Later receipts at `H+1` still must not drop this inference’s `H` when `H ≥ F(inference_id)`.

**Not a substitute for catch-up.** The host still has to apply the start Diff to have the record. Vote/challenge must keep sending missed diffs; if the verifier store is dry, challenge should forward the in-memory prefix just applied, not an empty `GetDiffs`.

---

## Why the receipt then lands

Honest compose never writes a confirm height below `F(m)`. If start is stamped, that stamp is already `≥ F(m)`, and the executor now writes `≥` that stamp (or omits). L0 cannot drop the tx for height. Omission still lands: absence skips L0; ConfirmStart applies unstamped.

The nonce’s start height is the floor this receipt will not undercut.

---

## Call sites

| Path | Today | Change |
| --- | --- | --- |
| HTTP `HandleRequest` → `signReceipt` | `referenceStamp(id, tip)` vs `F(id)` only | Also lift to applied start pair |
| `ChallengeReceipt` (refused-timeout vote) | same, after applying challenge diffs | same; start is in that prefix |
| Finish after execute | same `referenceStamp` | inherits the bar |

Sequencer `MsgStartInference` compose is unchanged: still `max(seq_tip, F(m))` or omit.

---

## Acceptance sketch

- Start stamped at `H_s`, executor tip `H < H_s` but within `W_conf` of `H_s` (or `t = 0`): receipt / ConfirmStart / Finish use `(H_s, start hash)`, not `H`. Diff apply succeeds; inference leaves `Pending`.
- Start stamped, `H_s − t > W_conf`: stamp omitted; ConfirmStart still applies.
- Start unstamped: still lift to `F(m)` only (existing tests).
- Two in-flight inferences, later nonce’s receipt at `H+1` lands first: earlier nonce’s receipt at `H ≥ F(m)` still lands (producing nonce; no live-floor compare).
- Unit: `referenceStamp` with empty `FloorIndex` but applied stamped start still lifts; mixed height/hash rejected; L0b start-vs-confirm still not an `INVALID`.
