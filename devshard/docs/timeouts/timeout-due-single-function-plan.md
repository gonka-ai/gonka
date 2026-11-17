# One timeout due function

**Status:** Done  
**Related:** [timeout-clock-dual-mode-plan.md](timeout-clock-dual-mode-plan.md), [timeout-clock-dual-mode-implementation-review.md](timeout-clock-dual-mode-implementation-review.md) (issue 3)  
**Branch:** this async-header-time line. No `inference-chain/` edits.

## Goal

Every path that asks "is this refusal or execution timeout due?" calls one function. The escrow flag picks the clock. Callers differ only by which anchor they already have, and by whether they add the gateway buffer or the sweep grace.

| `heightsync_timeout_clock` | Active clock |
|---|---|
| **false** (default) | Wall. Refusal from the caller's wall anchor (`sendTime` while the attempt is alive, otherwise record `StartedAt`). Execution from `outcome.confirmedAt` while that confirm is queued, otherwise record `ConfirmedAt`. |
| **true** | Stamp. Carried `*TimeMs`, otherwise the height to time map, then `headertime.Decide`. A missing row does not open the challenge. |

Legacy mode does not read the header map to decide. Header mode does not open because the wall time is past.

Do the steps in order. Each step's tests gate the next.

## Why

Today the live waiter and the verifier branch on the flag. The background sweep, the escrow hold, completion retry, and `timeoutUnarmed` do not. They call `StateMachine.TimeoutRemaining`, which ages the stamp floor and treats a missing height as "not armed". A legacy escrow then depends on the header feed for janitor work, and a sweep that passes `time.Time{}` into `HandleTimeout` treats the Unix epoch as the send time, so the wall deadline is already past.

## Callers

| Caller | What it decides | Legacy anchor | Header |
|---|---|---|---|
| `TimeoutDeadline` / `WaitTimeoutDeadline` / `VoteTimeout` | sleep and vote for the live attempt | refusal: `sendTime`; execution: queued confirm, else `ConfirmedAt` | stamp `Decide` |
| `awaitVerifierDue` | accept or reject the challenge | record `StartedAt` / `ConfirmedAt`, no buffer | stamp `Decide` / AnswerWait |
| `overdueTimeoutInferences` | which orphans to vote | record `StartedAt` / `ConfirmedAt`, plus buffer and sweep grace | same stamp arm |
| `summarizeEscrowHoldInFlight` | whether pending reserved cost is overdue | record `StartedAt`, plus buffer | same stamp arm |
| completion retry (`user/completion.go`) | vote after process restart | record `StartedAt` / `ConfirmedAt`, plus buffer | same stamp arm |
| `timeoutUnarmed` | drop a completion that can never time out | no wall anchor on the record for that reason | no stamp height and no carried time |

`TimeoutDue` / `TimeoutRemaining` stay the header floor helper used by drain and seal. Apply stays votes-only and does not call them. `winnerHardTimeoutDeadline` is the streaming attempt cap, not the protocol timeout.

## Shared function

```text
Due(input) (deadline time.Time, kind string, armed bool)
```

`input`:

- `HeightsyncTimeoutClock`
- `Reason` — refused or execution
- `WallAnchor` — Unix seconds. `0` means the caller has no wall instant. It is not the Unix epoch and it is not "immediately due".
- `StampTimeMs`, `StampMissing`, `StampHeight` — filled by the caller before the call. The function does not take the session lock and does not call `StoredAt`.
- `Interval` — `RefusalTimeout` or `ExecutionTimeout` as a duration.
- `Extra` — `TimeoutBuffer` for gateway vote, hold, completion, and sweep. Verifier passes `0`. Sweep adds its grace on top of the buffer. Header mode ignores `Extra`.

Rules:

- **Legacy, armed.** Deadline is `WallAnchor + Interval + Extra`. Due when `!now.Before(deadline)`. A zero `WallAnchor` is unarmed (`armed == false`), not due.
- **Legacy execution preference.** The live waiter still prefers a queued confirm's `confirmedAt`, then a started record's `ConfirmedAt`. That choice happens in the caller, which passes the chosen instant as `WallAnchor`. A `Pending` record whose confirm was dropped passes `sendTime` and kind `refused` (existing issue 9 rule).
- **Header.** Active result is today's `refusalDeadlineLocked` / `executionDeadlineLocked` / verifier stamp loop: carried time, else map, else not yet. `Extra` is not added. `WallAnchor` is not the open condition.
- **Shadow.** Only the live gateway vote and the verifier record a mismatch, and only in legacy mode, once per nonce and kind. Sweep selection, escrow hold, and completion selection are read-only. They must not call `RecordClockMismatch` on every 30 second tick.

The map read for a legacy shadow stays outside `s.mu`, as it does now. Header `StoredAt` for the active deadline stays where it is today.

## Steps

| Step | Delivers | Status |
|---|---|---|
| [1. Due helper](#step-1-due-helper) | Pure function, both arms, no I/O | Done |
| [2. Live gateway](#step-2-live-gateway) | `TimeoutDeadline` calls `Due` | Done |
| [3. Verifier](#step-3-verifier) | `awaitVerifierDue` calls `Due` for the active bit | Done |
| [4. Sweep and hold](#step-4-sweep-and-hold) | Selection uses `Due`, then the same `HandleTimeout` | Done |
| [5. Restart and unarmed](#step-5-restart-and-unarmed) | Completion retry and `timeoutUnarmed` use `Due` | Done |

### Step 1. Due helper

Put `Due` next to the existing clock code (`user` for the gateway formula, or `headertime` if both gateway and host can import it without a cycle). `headertime` already has `Decide`. Production active decisions use `Due`. The legacy sample is `CompareLegacyShadow`.

#### Test plan

- [x] Legacy: anchor `T`, interval 5s, extra 5s → deadline `T+10s`. Due at `T+10s`, not before.
- [x] Legacy: `WallAnchor == 0` → `armed == false`, deadline is not the Unix epoch.
- [x] Legacy: extra `0` (verifier) → deadline `T+interval`, due when `now.Unix()-T >= interval` seconds for the verifier caller. Gateway due stays `!now.Before(deadline)` so the buffer path matches v6.
- [x] Header: stamp missing → not due, deadline in the future (feed poll). Wall anchor in the past does not flip it.
- [x] Header: stamp due → deadline in the past. `Extra` does not move it.
- [x] No sleep, no `StoredAt`, no clock read inside `Due`. Callers pass `now`.

### Step 2. Live gateway

`timeoutDeadlineLocked` builds the anchor and calls `Due`.

- Legacy refusal: `WallAnchor = sendTime.Unix()` when `sendTime` is set. When `sendTime` is zero, `WallAnchor = record.StartedAt` if it is positive, otherwise unarmed. Delete the "zero sendTime is the epoch" behavior.
- Legacy execution: unchanged preference (queued `confirmedAt`, else `ConfirmedAt`). `Extra = TimeoutBuffer`.
- Header: keep the current stamp deadline. Pass `Extra = 0`.
- `sleepRefusal`, `deadlineNotReached`, and `VoteTimeout` keep using the deadline `Due` returned. They do not grow a second formula.
- Shadow stays in `observeLegacyClockShadow` after the lock drops, and only when this call is a vote or a wait, not a label check from `inferenceConfirmed`.

#### Test plan

- [x] Existing legacy and header `TimeoutDeadline` tests pass, including the saturated-duration test and the dropped-receipt refusal test.
- [x] `TimeoutDeadline(nonce, time.Time{})` on a legacy pending record with `StartedAt` in the future waits until `StartedAt + RefusalTimeout + TimeoutBuffer`.
- [x] The same call with `StartedAt == 0` does not return a deadline in 1970.
- [x] Header refusal still does not open on wall alone.

### Step 3. Verifier

`awaitVerifierLegacyDue` becomes "fill `Due` input from the record, `Extra = 0`, due when the integer-second check says so". Stamp `Decide` remains the shadow. Header mode stays `awaitVerifierStampDue`.

The verifier and the gateway share the anchor selection and the mode switch. They do not share `Extra`. That buffer difference is v6 and stays.

#### Test plan

- [x] Legacy wall due and stamp not due → accept, mismatch once, no AnswerWait.
- [x] Header map miss still waits / `feed_behind`.
- [x] `StartedAt == 0` on legacy is due, matching v6 (`now.Unix() - 0 >= timeout`). Document this next to the gateway rule so the two zeros are not "fixed" into one behavior by accident: the verifier anchor is the signed field, and zero means epoch; the gateway treats a missing caller anchor as unarmed and only then falls back to `StartedAt`.

### Step 4. Sweep and hold

`overdueTimeoutInferences` and `summarizeEscrowHoldInFlight` take a `Due` result instead of `TimeoutRemaining`.

Sweep, per nonce:

1. Pick the reason the same way (`Started` → execution, `Pending` with a stored refusal payload → refusal).
2. Call `Due` with the record wall anchor in legacy mode, or the stamp inputs in header mode. Sweep `Extra` is `TimeoutBuffer` plus the v6 grace (2 minutes), legacy only.
3. Skip when `!armed` or the deadline is still ahead.
4. Call `HandleTimeout` with the same anchor the selection used. Do not pass `time.Time{}` to force the deadline into the past. If the live `HandleTimeout` needs a `sendTime`, pass `time.Unix(StartedAt, 0)` for a legacy refusal so the vote waits on the same instant the sweep already checked.

Hold:

- Pending rows call the same `Due` with refusal, record `StartedAt`, and `Extra = TimeoutBuffer` (no sweep grace).
- `deadline = Due`'s deadline. Overdue means `!now.Before(deadline)`.
- A stamp error in legacy mode is not possible, because legacy does not consult the feed. In header mode a missing stamp is not overdue.

Neither path records a clock mismatch.

#### Test plan

- [x] Legacy: wall past `StartedAt + refusal + buffer + grace`, stamp floor not due → sweep selects the nonce.
- [x] Legacy: stamp floor due, wall not yet past → sweep does not select.
- [x] Legacy: `StartedAt == 0` → not selected.
- [x] Header: sweep still waits on the stamp and does not select on wall alone.
- [x] After selection, `HandleTimeout` does not sleep for a full extra interval when the sweep already found the deadline past.
- [x] Hold: legacy pending cost is overdue from `StartedAt`, not from the floor. Header pending cost follows the stamp.

### Step 5. Restart and unarmed

`user/completion.go` restarts a vote with `HandleTimeout(..., time.Time{}, payload)`. Route that through step 2's zero-`sendTime` rule so the anchor is the record, not the epoch.

`timeoutUnarmed`:

- Legacy: unarmed when the wall anchor for the record's reason is missing (`StartedAt <= 0` while `Pending`, `ConfirmedAt <= 0` while `Started`). A missing `StartedAtHeight` does not drop the completion.
- Header: unarmed when there is no stamp height and no carried `*TimeMs` (today's `ErrTimeoutNotArmed`). A missing map row stays armed and retries.

`timeoutDueAtFloor` and `awaitingFloor` have no production callers. Delete them in this step if they still have no callers, so the floor helper cannot be wired back in by mistake.

#### Test plan

- [x] Legacy completion retry with `StartedAt` still inside the window does not vote immediately.
- [x] Legacy completion with a wall anchor and `StartedAtHeight == 0` is kept and can vote.
- [x] Header completion with no height and no carried time is still dropped as unarmed.
- [x] Header completion with a height and an empty map is kept.

## Out of scope

- Changing apply to re-check `Due`.
- Pointing drain and seal `TimeoutDue` at the wall clock. That remains the carried floor.
- `StreamingAttemptHardTimeout` / `winnerHardTimeoutDeadline`.
- Flipping `heightsync_timeout_clock` on a live escrow.
- Registering the mismatch counter on the served `/metrics` registry (review issue 1). This plan only stops the sweep and the hold from sampling it.
- E2E host clock env (review issue 2).

## Done when

- Sweep, hold, live vote, completion retry, and `timeoutUnarmed` all branch through `Due`.
- Legacy tests pass with the header map empty.
- Header tests pass with the wall anchors long past and the stamp not due.
- `TimeoutRemaining` / `TimeoutDue` stay the header floor. Drain and seal still use that floor. One verifier fallback also calls it: `awaitVerifierDue` when the evidence has no header feed. A verifier that has the feed uses `Due` (legacy) or stamp `Decide` (header).
