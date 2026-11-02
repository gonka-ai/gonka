# Async header time: timeout implementation plan

**Status:** Done
**Design:** [async-header-time-timeout.md](async-header-time-timeout.md)
**Map this plan reads:** [block-change-feed-plan.md](../block-change-feed-plan.md)
**Step 4 review:** [step-4-implementation-review.md](step-4-implementation-review.md)
**Floor before the first start:** [bootstrap-heartbeat-floor-plan.md](bootstrap-heartbeat-floor-plan.md)

Implements the timeout decisions in the design. The block-change feed already fills `headertime.Source` with `(height, hash) → header time`. This plan only decides when a gateway opens a timeout challenge and when a verifier answers one.

Do the steps in order. The shared decision is what the gateway execution trigger and the verifier both call. The gateway acts on `due`, `not_yet`, and `missing`. The 60 second schedule and ignore results are the verifier's. The refusal trigger does not use the function. Before the verifier runs, the bootstrap heartbeat gives the first start a floor, so that start has a header time a refusal can be checked against. After that wait exists, a second challenge for the same inference and reason is refused from host memory. The sign path loses its header-time wait only after both. The state-root check is last, so a root change cannot land inside an earlier step.

Out of this plan: writing the map, the dapi long-poll, and the chain fallback. Those are the block-change feed. The test mocks that drive that poll are [header-time-poll-mock-plan.md](header-time-poll-mock-plan.md). This plan does not call `GetBlockHeader` and does not call `AwaitHeaderTimeMs`. A missing entry means this process has not seen that block yet.

## Steps

Update the status column as a step lands. Each row links to its step, and each step links back here.

| Step | Delivers | Status |
|---|---|---|
| [1. Shared header-time decision](#step-1-shared-header-time-decision) | One function: due, not yet, schedule once, ignore, or still waiting on the feed | Done |
| [2. Gateway refusal on the wall clock](#step-2-gateway-refusal-on-the-wall-clock) | Refusal opens from `started_at`. The map is not read | Done |
| [3. Gateway execution on the confirm stamp](#step-3-gateway-execution-on-the-confirm-stamp) | Execution opens from `gateway_wallclock - time(ConfirmedAtHeight)`. A missing time does not open the challenge | Done |
| [4. A floor before the first inference](#step-4-a-floor-before-the-first-inference) | The bootstrap heartbeat raises `F`. The first start copies it, so its refusal can be armed | Done |
| [5. Verifier answers from its own map](#step-5-verifier-answers-from-its-own-map) | The verifier's wall clock against the stamp on the signed message, with one 60 second bound | Done |
| [6. Refuse a duplicate challenge](#step-6-refuse-a-duplicate-challenge) | A second `VerifyTimeout` for the same inference and reason is refused from host memory | Done |
| [7. The sign path does not wait for header time](#step-7-the-sign-path-does-not-wait-for-header-time) | Apply, sign, ConfirmStart, and Finish return without `ErrHeaderPending` for a missing header time | Done |
| [8. The state root keeps the wall-clock fields](#step-8-the-state-root-keeps-the-wall-clock-fields) | `started_at` and `confirmed_at` stay wall-clock fields. The root has no header time | Done |

## Key decisions

These are the design, restated as the rules the steps must not drift from.

- **One feed, already built.** `HeaderTimeMs(height, hash)` is the only read. It does not call out. The timeout path does not start a second subscriber.
- **Refusal is the gateway's own clock.** `gateway_wallclock - started_at > RefusalTimeout`. `TimeoutBuffer` is not added. The map is not consulted.
- **Execution is confirmed-at time, from the map.** The timestamp is `time(ConfirmedAtHeight)`, not the stored `ConfirmedAtTimeMs` and not the confirm stamp. The challenge opens only when the map has that time and `gateway_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout`. The host uses its own wall clock against the same height on its own map. When `gateway_wallclock - confirmed_at > ExecutionTimeout` and the map has no time for `ConfirmedAtHeight`, the gateway does not open the challenge and logs that its block feed is behind the chain.
- **A verifier uses its own wall clock and its own map.** A refusal checks `verifier_wallclock - time(start stamp) > RefusalTimeout`. An execution timeout checks `verifier_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout`.
- **The 60 second bound is only this answer.** It is not a period an executor can stretch by returning `header_pending`. `header_pending` means the host cannot serve the response. It does not open a deferral window. A refusal whose executor answers `header_pending` votes yes.
  - The inequality holds: answer now.
  - The map has the time and the remaining time is under 60 seconds: schedule the same check once, after that remainder, then answer.
  - The remaining time is 60 seconds or more: do not sign and do not hold the request.
  - The map has no entry: wait up to 60 seconds for the feed, without `GetBlockHeader` and without blocking other work. Then apply the rules above. If it is still missing, do not sign, and log that this verifier's block feed is behind the chain.
- **One challenge per inference and reason on a verifier.** The key is `(inference_id, reason)`. While [step 5](#step-5-verifier-answers-from-its-own-map) holds a check, and after that check has returned a vote, a later `VerifyTimeout` for the same pair is refused from host memory: a warning and `accept=false` with an empty body. The held check is left in place and is the call that returns the vote. An answer that carried no vote is not remembered, so a later sweep can ask again. The memory is not in the state machine, the diff, or the hashed record. [Step 6](#step-6-refuse-a-duplicate-challenge).
- **A missing header time is not an apply error.** Signing a diff and returning ConfirmStart or Finish use the floor index and the height-sync stamp rules. They do not retry for seconds because the map has no time.
- **The first start has a floor.** [Step 4](#step-4-a-floor-before-the-first-inference) is [bootstrap-heartbeat-floor-plan.md](bootstrap-heartbeat-floor-plan.md). One unstamped heartbeat span, then the hosts' acks, raise `F` before chat opens. The first `MsgStartInference` copies that floor, so `StartedAtTimeMs` is the map time and a refusal can be armed. The gateway's own refusal wait stays the wall clock from [step 2](#step-2-gateway-refusal-on-the-wall-clock) and still does not read the map.

---

## Step 1. Shared header-time decision

[↑ Steps](#steps)

### Description

Add one pure function next to `headertime.Source`, in `devshard/chainoracle/blocks/headertime`. The gateway execution trigger ([step 3](#step-3-gateway-execution-on-the-confirm-stamp)) and the verifier ([step 5](#step-5-verifier-answers-from-its-own-map)) both call it. Refusal does not.

Inputs: wall-clock `now`, the optional header time of the stamp in milliseconds, `timeout_interval`, how long this waiter has already been waiting for a missing entry, and whether this caller is a verifier. No context, no oracle, no sleep.

Output, exactly one of:

- `due`, when the map has the time and `now - time(stamp) > timeout_interval`
- `not_yet`, when the map has the time, the inequality does not hold, and the caller is the gateway. `delta` is returned so the gateway can sleep until the header time is due, including when `delta` is longer than 60 seconds
- `schedule`, with `delta = timeout_interval - (now - time(stamp))`, when the caller is a verifier, the map has the time, and `delta < 60s`
- `ignore`, when the caller is a verifier, the map has the time, and `delta >= 60s`
- `missing`, when the map has no time and the caller is the gateway. The gateway decides the log itself in [step 3](#step-3-gateway-execution-on-the-confirm-stamp)
- `waiting`, when the caller is a verifier, the map has no time, and the waiter has been waiting under 60 seconds
- `feed_behind`, when the caller is a verifier, the map has no time, and the waiter has already waited 60 seconds

`60s` is one named constant in this package. Callers must not pick their own. The gateway never receives `schedule`, `ignore`, `waiting`, or `feed_behind`. The verifier never receives `not_yet` or `missing`.

This step does not change `Session.TimeoutDeadline`, `VerifyRefusedTimeout`, or `VerifyExecutionTimeout`.

### Test plan

Gate for [step 2](#step-2-gateway-refusal-on-the-wall-clock). Step 2 starts when every box below is checked.

- [x] A time 1 millisecond past `timeout_interval` returns `due` for both callers. A time exactly at the interval does not.
- [x] Gateway, remaining time of 10 minutes: `not_yet` with that delta. The result is not `ignore`.
- [x] Verifier, remaining time of 59 seconds: `schedule` with that delta. Remaining time of 60 seconds: `ignore`.
- [x] Gateway, missing time: `missing`, for `waited = 0` and for `waited = 60s`.
- [x] Verifier, missing time with `waited = 0`: `waiting`. With `waited` just under 60 seconds: `waiting`. With `waited = 60s`: `feed_behind`.
- [x] The function does not call `HeaderTimeMs`, `AwaitHeaderTimeMs`, or `GetBlockHeader`. A test builds it with a plain `int64` and a missing flag.

---

## Step 2. Gateway refusal on the wall clock

[↑ Steps](#steps)

### Description

The gateway opens a refusal from its own wall clock:

```text
gateway_wallclock - started_at > RefusalTimeout
```

`started_at` is the wall clock already stored when `MsgStartInference` is sent. This step does not look up `time(F)` and does not read `headertime.Source`.

Today `Session.TimeoutDeadline` in `devshard/user/session.go` can replace that deadline with `TimeoutRemaining`, which compares the floor's header time with `StartedAtTimeMs`, and it adds `TimeoutBuffer`. For a refusal, stop both. The sleep in the refusal branch waits until `started_at + RefusalTimeout` and then opens the challenge.

`TimeoutBuffer` stays on paths this step does not own. Execution still uses its current deadline until [step 3](#step-3-gateway-execution-on-the-confirm-stamp).

### Test plan

Gate for [step 3](#step-3-gateway-execution-on-the-confirm-stamp). Step 3 starts when every box below is checked.

- [x] A pending inference opens a refusal when the gateway wall clock is past `started_at + RefusalTimeout`, with an empty header-time map.
- [x] The same inference does not open a refusal one millisecond earlier.
- [x] Filling the map with a header time for the start stamp does not move the refusal deadline.
- [x] `TimeoutBuffer` is not added to the refusal deadline. A test that used to pass only because of that buffer now fails if the buffer is added back.
- [x] An execution timeout in the same test still uses the pre-step-3 deadline. This step does not change it.

---

## Step 3. Gateway execution on the confirm stamp

[↑ Steps](#steps)

### Description

The gateway opens an execution challenge from confirmed-at time. That timestamp is the header time of `ConfirmedAtHeight`, read from the map. It is not the stored `ConfirmedAtTimeMs`, and it is not the confirm stamp. Open only when the map has that time and

```text
gateway_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout
```

Read it with `HeaderTimeMs`. Do not call `AwaitHeaderTimeMs`. The host's execution check in [step 5](#step-5-verifier-answers-from-its-own-map) is the same inequality: that host's wall clock against `time(ConfirmedAtHeight)` on that host's map.

When the executor's wall clock already says the interval has elapsed,

```text
gateway_wallclock - confirmed_at > ExecutionTimeout
```

and `HeaderTimeMs` has no entry for `ConfirmedAtHeight`, do not open the challenge. Log that the gateway's block feed is behind the chain. Keep waiting for the feed. A later pass opens the challenge once the entry arrives and the inequality holds.

`confirmed_at` stays the executor's wall clock. It is the signal for the log, not the deadline.

Use the decision from [step 1](#step-1-shared-header-time-decision) as the gateway caller, with the gateway wall clock as `now` and `time(ConfirmedAtHeight)` as the stamp. Open on `due`. On `not_yet`, sleep until `time(ConfirmedAtHeight) + ExecutionTimeout` and check again. That sleep may be longer than 60 seconds. On `missing`, do not open. Log the feed-behind line only when `confirmed_at` is already past `ExecutionTimeout`. A later pass opens the challenge once the entry arrives and the result is `due`.

### Test plan

Gate for [step 4](#step-4-a-floor-before-the-first-inference). Step 4 starts when every box below is checked.

- [x] The map has `time(ConfirmedAtHeight)`, and the gateway wall clock is past that time by `ExecutionTimeout`: the challenge opens.
- [x] The map has the time and the wall clock is one millisecond inside the interval: the challenge does not open.
- [x] `confirmed_at` is already past `ExecutionTimeout` and the map has no entry for `ConfirmedAtHeight`: the challenge does not open, and the log says the block feed is behind.
- [x] The same case, after the test inserts the header time and the inequality holds: the next pass opens the challenge. No `GetBlockHeader` call was made during the gap.
- [x] A refusal in the same test still follows [step 2](#step-2-gateway-refusal-on-the-wall-clock). An empty map does not block it.

---

## Step 4. A floor before the first inference

[↑ Steps](#steps)

### Description

Implement [bootstrap-heartbeat-floor-plan.md](bootstrap-heartbeat-floor-plan.md) in the order written there. This step does not restate those rules. It places them here because [step 5](#step-5-verifier-answers-from-its-own-map) checks a refusal against `time(start stamp)`, and a fresh escrow's first start has no stamp until this lands.

The bootstrap plan's seven steps are the work:

1. While `F` is empty, send one heartbeat per slot with no height.
2. An ack to that heartbeat reports `SYNCED` when the host has a header.
3. Chat opens when `F` has a map time and half the slots have acked a block.
4. A start while `F` is empty is rejected. Older sessions still replay.
5. `time(F)` must be within two cadence intervals of now.
6. A gateway with no header-time source does not serve.
7. The bootstrap reply fills the peer-tip cache, and the seed RPC leaves the chat path.

The quiet-session cadence is always on. There is no `DEVSHARD_HEIGHTSYNC_HEARTBEAT`. The chain already requires a later floor time before a refusal is due, and that cadence is what moves `F`.

One applied ack raises `F`. Half the roster is only the chat gate. The start copies `F` exactly. `|height − local tip| ≤ D` stays a mark on a heartbeat or an ack, not the rule that accepts a start.

This step does not change the gateway refusal inequality from [step 2](#step-2-gateway-refusal-on-the-wall-clock) or the execution inequality from [step 3](#step-3-gateway-execution-on-the-confirm-stamp).

### Test plan

Gate for [step 5](#step-5-verifier-answers-from-its-own-map). Step 5 starts when every box in the bootstrap plan is checked. The boxes below are that plan's steps, in its order.

- [x] [Unstamped bootstrap span](bootstrap-heartbeat-floor-plan.md#step-1-unstamped-bootstrap-span)
- [x] [Ack label for a heartbeat with no height](bootstrap-heartbeat-floor-plan.md#step-2-ack-label-for-a-heartbeat-with-no-height)
- [x] [Chat gate on the floor](bootstrap-heartbeat-floor-plan.md#step-3-chat-gate-on-the-floor)
- [x] [A start requires a floor](bootstrap-heartbeat-floor-plan.md#step-4-a-start-requires-a-floor)
- [x] [The floor must be fresh](bootstrap-heartbeat-floor-plan.md#step-5-the-floor-must-be-fresh)
- [x] [No gateway without a header-time source](bootstrap-heartbeat-floor-plan.md#step-6-no-gateway-without-a-header-time-source)
- [x] [Retire the seed](bootstrap-heartbeat-floor-plan.md#step-7-retire-the-seed)

---

## Step 5. Verifier answers from its own map

[↑ Steps](#steps)

### Description

`VerifyRefusedTimeout` and `VerifyExecutionTimeout` in `devshard/host/timeout.go` decide whether the challenge is due before they contact the executor. The decision uses this verifier's wall clock and this verifier's `headertime.Source`, through [step 1](#step-1-shared-header-time-decision).

A refusal uses this verifier's wall clock and its own map. `event_height` is the start stamp and `timeout_interval` is `RefusalTimeout`. An execution timeout uses the [step 3](#step-3-gateway-execution-on-the-confirm-stamp) inequality on this verifier's wall clock and map: `verifier_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout`. The results below apply to whichever check that reason uses. The time left for `schedule` and `ignore` is that check's remainder.

- `due`: run the challenge that already exists (receipt check, executor call, vote).
- `schedule`: do not call the executor yet. Register one timer for `delta`, then run the same check once. One timer per inference and reason. A second call before it fires does not arm another timer. [Step 6](#step-6-refuse-a-duplicate-challenge) is the response that second call gets, and it leaves this timer in place.
- `ignore`: return without a vote and without holding the request. Do not call the executor.
- `waiting`: do not call the executor and do not call `GetBlockHeader`. Rearm this check for the time left in the 60 seconds. Other inferences on this host keep running.
- `feed_behind`: return without a vote. Log that this verifier's block feed is behind the chain.

`timeoutDue` today asks `StateMachine.TimeoutDue`, which compares the floor header time with `StartedAtTimeMs` or `ConfirmedAtTimeMs`. That comparison is no longer the gate for these two functions. Leave `TimeoutDue` in place for callers this step does not own. [Step 7](#step-7-the-sign-path-does-not-wait-for-header-time) removes the header-time wait on the sign path.

The 60 second wait is a timer on this challenge. It is not `headerPendingMaxWait` and it is not a sleep inside `ChallengeReceipt`.

### Test plan

Gate for [step 6](#step-6-refuse-a-duplicate-challenge). Step 6 starts when every box below is checked.

- [x] Refusal: the map has the start stamp, and this host's wall clock is past that time by `RefusalTimeout`. `VerifyRefusedTimeout` calls the executor once.
- [x] Execution: the map has `time(ConfirmedAtHeight)`, and this host's wall clock is past that time by `ExecutionTimeout`. `VerifyExecutionTimeout` calls the executor once.
- [x] The map has the time and 30 seconds remain. The executor is not called. One timer is armed for 30 seconds. When it fires, the executor is called once. A second evaluation before the timer fires does not arm a second timer. The response of that second call is [step 6](#step-6-refuse-a-duplicate-challenge).
- [x] The map has the time and 60 seconds remain. The function returns without a vote. The executor is not called. No timer is armed.
- [x] The map has no entry. For 60 seconds the executor is not called and `GetBlockHeader` is not called. Another inference on the same host still completes a confirm during that wait.
- [x] The entry arrives at 10 seconds and the inequality holds: the executor is called once, and the remaining wait is canceled.
- [x] The entry is still missing at 60 seconds: no vote, and the log says this verifier's block feed is behind the chain.

---

## Step 6. Refuse a duplicate challenge

[↑ Steps](#steps)

### Description

[Step 5](#step-5-verifier-answers-from-its-own-map) holds at most one check per inference and reason: the schedule timer, the feed wait, or the executor call that follows `due`. This step is what a second `VerifyTimeout` does with that check. The memory sits on the host process. It is not in the state machine, the diff, or the hashed record. A restart drops it. After a restart the record status is what refuses a challenge whose timeout has already been applied.

The key is `(inference_id, reason)`. A refusal entry does not cover an execution challenge. An execution entry does not cover a refusal.

- A check is already held. Return `accept=false` and an empty body. Log a warning. Leave the held timer where it is. Do not arm another timer, do not call the executor, and do not hand this caller the vote or any recovered transactions. The held call is the one that answers.
- The held call has returned a vote for this pair. Remember the pair. A later `VerifyTimeout` returns `accept=false`, logs a warning, and does not call the executor. The body stays empty. This caller does not receive a copy of the vote.
- The answer carried no vote. `ignore`, `feed_behind`, and `accept=false` with an empty body are not remembered. A later sweep can ask again. Remembering those would leave the inference pending while every later challenge on this host is refused.
- Drop a remembered pair when the local record leaves the status that reason requires. A refusal entry drops when the record is no longer `Pending`. An execution entry drops when the record is no longer `Started`. From then on the status check already in `VerifyRefusedTimeout` and `VerifyExecutionTimeout` is what refuses the call.

A vote whose response never reached the gateway stays remembered while the record is still `Pending` or `Started`. This host does not sign that pair again until the record leaves that status or the process restarts. The gateway keeps the response from the call that was held.

### Test plan

Gate for [step 7](#step-7-the-sign-path-does-not-wait-for-header-time). Step 7 starts when every box below is checked.

- [x] One inference, refusal, 30 seconds remaining. The first call arms the [step 5](#step-5-verifier-answers-from-its-own-map) timer. A second call before it fires returns `accept=false` with an empty body and a warning, and does not arm a second timer. When the first timer fires, the executor is called once, and that first call returns the vote.
- [x] The same case for an execution challenge. A refusal hold on that inference does not refuse the execution call, and an execution hold does not refuse the refusal call.
- [x] The first call has returned a vote. A later call for the same pair returns `accept=false` with an empty body and a warning. The executor is not called. The record is unchanged by the second call.
- [x] The first call returns `ignore` because 60 seconds or more remain. Nothing is remembered. A later call is evaluated again by [step 5](#step-5-verifier-answers-from-its-own-map).
- [x] The feed has no entry and the first call is inside the 60 second wait. A second call is refused with a warning. The first wait still runs. Another inference on the same host still completes a confirm during that wait.
- [x] After the vote, the timeout is applied and the record leaves `Pending` for a refusal, or `Started` for an execution. The memory entry is gone. A later call is refused by the status check.

---

## Step 7. The sign path does not wait for header time

[↑ Steps](#steps)

### Description

Applying a diff, signing a diff, and returning ConfirmStart or Finish succeed or fail from the stamp and the log. A missing header time is not an apply error, not `ErrHeaderPending` on that path, and not a reason to retry the apply.

`diff_writer.go` used to retry `ErrHeaderPending` for up to `headerPendingMaxWait` (15 seconds). That retry is gone. A test that withholds the map still gets ConfirmStart and Finish on the first attempt.

`header_pending` means the host cannot serve the response. There is no `PendingEvidenceDeferWindow`. An executor answer of `header_pending` does not withhold a refusal or execution vote.

The verifier's 60 second behavior from [step 5](#step-5-verifier-answers-from-its-own-map) stays, and so does the duplicate refusal from [step 6](#step-6-refuse-a-duplicate-challenge). An executor cannot move the 60 second bound by answering `header_pending`.

### Test plan

Gate for [step 8](#step-8-the-state-root-keeps-the-wall-clock-fields). Step 8 starts when every box below is checked.

- [x] A host whose map has no entry for the carried floor still applies the diff and returns ConfirmStart. The call does not return `ErrHeaderPending` and does not sleep.
- [x] The same host returns Finish for a started inference with the map still empty. One attempt, no retry loop.
- [x] Signing the diff does not read `HeaderTimeMs`.
- [x] A verifier from [step 5](#step-5-verifier-answers-from-its-own-map) still waits up to 60 seconds when its own map misses, including when the executor's challenge reply is `ErrHeaderPending`. The reply does not extend that wait past 60 seconds.

---

## Step 8. The state root keeps the wall-clock fields

[↑ Steps](#steps)

### Description

`started_at` and `confirmed_at` stay the local wall clock at the moment the message is created (gateway `sendTime`, executor receipt wall clock). The diff does not carry a header time. The hashed inference record does not gain a header-time field: `StartedAtTimeMs` and `ConfirmedAtTimeMs` may still be written on the live record from this process's map for `TimeoutDue`, but `marshalInferenceEntry` omits them so they cannot move the state root.

Two replicas that apply the same diffs must produce the same root when their header-time maps differ, including when one map is empty. A missing header time must not make one replica refuse the apply that the other replica accepted. That refusal is what [step 7](#step-7-the-sign-path-does-not-wait-for-header-time) already removed. This step locks it with the root.

### Test plan

The plan is done when every box below is checked.

- [x] After Start, ConfirmStart, and Finish, gateway refusal still uses the injected wall-clock `started_at` (`sendTime`), not the map (`TestRefusalOpensOnTheGatewayWallClock`). Live `StartedAtTimeMs` / `ConfirmedAtTimeMs` may cache map times for `TimeoutDue`, but those fields are omitted from the canonical inference bytes so they cannot move the root (`TestCanonicalInferenceEntryOmitsHeaderTimes`).
- [x] Two machines apply the same three messages. One map has the stamps' header times. The other map is empty. Both applies succeed, and the state roots are equal. (`TestStateRootIgnoresHeaderMapDifferences`)
- [x] The canonical record bytes contain no header-time field. A hash of the record is unchanged by inserting a header time into the map after the apply. (`TestStateRootUnchangedWhenHeaderMapFillsAfterApply`)
