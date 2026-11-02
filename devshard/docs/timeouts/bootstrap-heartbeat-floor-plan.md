# Bootstrap heartbeat: a floor before the first inference

**Status:** Done. Steps 1–7 are in. The quiet-session cadence is always on. Empty starts stay legal on `v6`. `v7` and later reject a start while `F` is empty. The gateway does not call the height-sync seed RPC.
**Timeouts that read the start time:** [async-header-time-timeout-plan.md](async-header-time-timeout-plan.md) step 4
**Map this plan reads:** [block-change-feed-plan.md](../block-change-feed-plan.md)

The first `MsgStartInference` on a fresh escrow carries no height. `F` is empty, a user-signed start may only copy `F`, and `StartedAtTimeMs` is stored as 0. A refusal uses that field, so `timeoutWindowLocked` returns `ErrTimeoutNotArmed` for that inference for the rest of its life. A later floor does not rewrite a start that is already applied.

The height seed does not fix this. It asks each host for an Anchor, and half the roster answering opens chat. Those Anchors are not diffs, so they never raise `F`. The ordinary cadence still refuses a height of 0. [Step 1](#step-1-unstamped-bootstrap-span) is the one span allowed to go out with an empty stamp, and [step 2](#step-2-ack-label-for-a-heartbeat-with-no-height) labels a host that has a header `SYNCED`.

This plan replaces that seed with one unstamped heartbeat span. The hosts' acks raise `F`. Chat opens only once that floor has a header time and half the slots have acked a block. The first start then copies `F`, so its refusal can be armed.

Do the steps in order. [Step 1](#step-1-unstamped-bootstrap-span) is the only new message. The chat gate, the empty-floor start rejection, and the freshness check all read the floor that span produces. The quiet-session cadence is always on, so later heartbeats keep moving `F`. Retiring the seed is last, after a test shows the bootstrap reply fills the peer-tip cache the seed fills today.

## Steps

Update the status column as a step lands. Each row links to its step, and each step links back here.


| Step                                                                                            | Delivers                                                                        | Status      |
| ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ----------- |
| [1. Unstamped bootstrap span](#step-1-unstamped-bootstrap-span)                                 | While `F` is empty, one heartbeat per slot goes out with no height              | Done        |
| [2. Ack label for a heartbeat with no height](#step-2-ack-label-for-a-heartbeat-with-no-height) | Comparing a tip against 0 reports `SYNCED`, not `CATCHING_UP`                   | Done        |
| [3. Chat gate on the floor](#step-3-chat-gate-on-the-floor)                                     | Chat opens when `F` has a map time and half the slots have acked a block        | Done        |
| [4. A start requires a floor](#step-4-a-start-requires-a-floor)                                 | A start while `F` is empty is rejected. Older sessions still replay             | Done        |
| [5. The floor must be fresh](#step-5-the-floor-must-be-fresh)                                   | Chat waits until `time(F)` is within two cadence intervals of now               | Done        |
| [6. No gateway without a header-time source](#step-6-no-gateway-without-a-header-time-source)   | A process with no map does not serve chat                                       | Done        |
| [7. Retire the seed](#step-7-retire-the-seed)                                                   | The bootstrap reply fills the peer-tip cache. The seed RPC leaves the chat path | Done        |


## Key decisions

These are the design, restated as the rules the steps must not drift from.

- **The opening heartbeat carries no height.** The gateway is not a height source. `MsgHeartbeat` may only copy `F`, and there is nothing to copy yet. The span is `slots_num` heartbeats, one per slot, each with an empty stamp. A gateway-chosen tip on that heartbeat is a user-made height and must not be sent.
- **One applied ack raises `F`.** Half the roster is not the raise rule. It is the chat gate, the same bar the seed uses (`seeded * 2 >= slots`). An `ORACLE_UNAVAILABLE` ack has no height. `FloorIndex.Observe` ignores it, the same way the seed ignores an omitted Anchor.
- **The start copies `F` exactly.** `checkFloorStampLocked` stays the accept rule. `|height − local tip| ≤ D` stays a mark on a heartbeat or an ack (`checkL5a`). It does not accept or reject a start. Replicas would otherwise disagree, because each host's tip is its own.
- **The acks are already applied before the start.** They land on the bootstrap nonces, not inside the start diff. The host's proof is the exact-`F` check. A rule that demands half the acks sit in the start diff would reject a start that already has a floor.
- **Two maps.** The host holding a pair in its own map does not fill the gateway's map. The gateway looks up `time(F)` on its own poller before it copies `F` onto a start. An ack whose polled row is still missing is applied; the chat gate waits for `time(F)`. An ack whose hash disagrees with a row already stored, or with a host ack already accepted at that height, stays out of the log and is logged at warn. A later poll that disagrees with an accepted host hash is logged the same way. A later version should open a dispute from either disagreement.
- **A refusal has two halves.** [Step 4](#step-4-a-start-requires-a-floor) arms it: `StartedAtTimeMs > 0`. The chain already makes it due when a later block in `F` is at least `StartedAtTimeMs + RefusalTimeout`. The always-on cadence is what moves `F` on a quiet session. The gateway's own wait stays `gateway_wallclock − started_at > RefusalTimeout` and does not read the map.
- **Freshness is part of the gate.** A start copies `F`, which can lag the real start by up to two cadence intervals. [Step 5](#step-5-the-floor-must-be-fresh) bounds that lag. The cadence has no off switch, so a quiet session keeps sending spans; freshness is what stops one stale floor from opening chat. Verifying hosts still see a live executor's receipt directly, so this lag does not by itself vote a live executor out.
- **The cadence is always on.** There is no `DEVSHARD_HEIGHTSYNC_HEARTBEAT`. The gateway starts `StartHeartbeatLoop` on every session. A test may still pass `WithDisableHeightSyncHeartbeat` to hold one session still. That option is not a production setting.

## What this does to the first inference

After [step 3](#step-3-chat-gate-on-the-floor) and [step 4](#step-4-a-start-requires-a-floor):

1. The poller is running on the gateway and on every host.
2. The gateway sends the unstamped span. Nonces `1..slots_num` are those heartbeats. The first inference is no longer nonce 1.
3. Each host acks `max(its tip, F)`. While `F` is empty, that is its tip.
4. The first ack that carries a block raises `F`. Chat stays closed until half the slots have such an ack and the gateway map has `time(F)`.
5. `MsgStartInference` carries that floor. `applyStartInference` stores `StartedAtTimeMs` from the map. It is not 0.
6. `timeoutWindowLocked` does not return `ErrTimeoutNotArmed` for that inference.

A missing header time does not hold the ack. Compose applies it, and chat stays closed until the gateway map has a fresh `time(F)`. A hash that disagrees with the stored row or an accepted host ack is left out and logged at warn. The bootstrap does not call `GetBlockHeader` and does not call `AwaitHeaderTimeMs`.

---

## Step 1. Unstamped bootstrap span

[↑ Steps](#steps)

### Description

While `HeightSyncFloorAsOf(next nonce)` is empty, `composeSpan` sends one heartbeat per slot with `ObservedHeight = 0` and no hash. `SpanTxs` today returns nil for that input (`devshard/heightsync/heartbeat.go`). `composeSpan` returns before any host is contacted (`devshard/user/heartbeat.go`, log `heartbeat skipped` / `no_height`). Both of those stops are lifted for this one span.

The span is sent by the loop that already starts when the seed gate is on. It waits for router catalog admission first, as the seed loop does. While `F` is empty, it retries once per heartbeat interval. A still-open bootstrap turn gets its unacked heartbeats resent on their original nonces, and a closed one gets a new span. It does not run again once `F` is non-empty; the ordinary cadence owns later spans and still requires a height. The cadence is always on.

Log plane for this span is already legal and must stay legal:

- `checkL0` skips a stamp when `F` as of the producing nonce is 0.
- `checkL4` skips a heartbeat with no hash.
- `TurnTracker.windowClosed` stays false while the heartbeat's requested height is 0, so the turn is not closed as degraded for having no `HReq`.

The heartbeat must not be filled with the gateway's polled tip. That tip is not `F`.

### Test plan

Gate for [step 2](#step-2-ack-label-for-a-heartbeat-with-no-height). Step 2 starts when every box below is checked.

- [x] A fresh session with the seed gate on and the cadence off sends `slots_num` heartbeats, each with no height and no hash, on nonces `1..slots_num`.
- [x] The same session does not send a second unstamped span after one ack has raised `F`.
- [x] A span whose sends all fail is resent one heartbeat interval later, on the same nonces, and `F` rises once the hosts answer. A session restarted with that turn open resends at once.
- [x] A session whose floor is already non-empty never emits an unstamped heartbeat. The ordinary cadence still refuses `hNow == 0`.
- [x] Applying the span does not raise `F`. A user-signed heartbeat is not a floor claim.
- [x] A host acks its slot with its tip. After that ack is in a diff, `F` as of the next nonce is that tip.
- [x] `checkL0`, `checkL3`, and `checkL4` accept the span and the ack. No mark is required for the empty heartbeat.

---

## Step 2. Ack label for a heartbeat with no height

[↑ Steps](#steps)

### Description

`EvaluateSyncStateFromHeader` compares the host's tip with the heartbeat's requested height. A requested height of 0 makes every real tip look more than `DeltaBlocks` ahead, so the ack is labeled `CATCHING_UP`. For a heartbeat with no stamp, a host that has a header reports `SYNCED`. `ORACLE_UNAVAILABLE` and `ORACLE_STALE` are unchanged.

The stamp on the ack is unchanged: `max(tip, F)` from `referenceStamp`. This step does not hold the ack back until the host map has the pair. Compose already keeps an ack out of the log until the gateway map has its time, and the host retries a missing time for 15 seconds.

### Test plan

Gate for [step 3](#step-3-chat-gate-on-the-floor). Step 3 starts when every box below is checked.

- [x] Heartbeat height 0, host tip 100, header present: `SyncState` is `SYNCED` and the ack stamp is `(100, hash)`.
- [x] Heartbeat height 0, oracle missing: `SyncState` is `ORACLE_UNAVAILABLE` and the ack has no height. Applying it does not raise `F`.
- [x] Heartbeat height 100, host tip 100 + `DeltaBlocks` + 1: `SyncState` stays `CATCHING_UP`.
- [x] An ack whose `(height, hash)` is missing from the gateway map is not part of the composed diff. It is still queued for a later diff.

---

## Step 3. Chat gate on the floor

[↑ Steps](#steps)

### Description

Replace the chat wait in `Proxy.waitHeightSeed` (`devshard/cmd/devshardctl/proxy.go`). The session is ready for a first inference when all three hold:

- `F` as of the next nonce is non-empty.
- The gateway map returns a positive time for that `(height, hash)`. `LookupHeaderTimeMs` is the read. It does not call out.
- At least half the slots have an applied ack that carries a block. The count is the seed's `heightSeedQuorum`: `acked * 2 >= slots`. One slot still needs that one ack. Three slots need two.

The wait is the seed's 30 second gate (`heightSeedGate`), and it is a bootstrap budget only. On expiry, while `F` has no map time or fewer than half the slots have acked, the chat response is the same 503, `Retry-After: 5`, and `height_seed_incomplete`. Once those three hold, that clock no longer returns `missed`. Catalog admission is still required before the span, and the status label `catalog_pending` stays. The gate applies to the first inference. After that start commits, a chat request does not consult it.

`/v1/status` reports this gate with the same states the seed reports (`ok`, `pending`, `missed`, `catalog_pending`), counted from applied acks rather than from seed RPCs. The gateway does not call the seed RPC. The host still serves it.

This step does not reject a start. A caller that skips the gate can still compose an empty start. [Step 4](#step-4-a-start-requires-a-floor) closes that.

### Test plan

Gate for [step 4](#step-4-a-start-requires-a-floor). Step 4 starts when every box below is checked.

- [x] Three hosts, one applied ack with a block, map has its time: chat is not ready.
- [x] A second slot acks a block and the map has `time(F)`: chat is ready. The first `MsgStartInference` carries that floor, and `StartedAtTimeMs` equals the map time.
- [x] Half the slots ack with `ORACLE_UNAVAILABLE`: chat is not ready and `F` is empty.
- [x] The map has no time for `F`: chat is not ready, even though half the slots have acked.
- [x] The gate expires: the chat handler returns 503 with `Retry-After: 5` and `height_seed_incomplete`.
- [x] The gate does not call `GetBlockHeader` or `AwaitHeaderTimeMs`.

---

## Step 4. A start requires a floor

[↑ Steps](#steps)

### Description

`checkFloorStampLocked` for a start today allows an empty stamp when `F` is empty. On the protocol version this plan ships, that allowance is removed: an empty floor rejects the start, and a start must carry exactly `F`. `referenceStampLocked` is unchanged. It already returns absent when `F` is empty, so the gateway's compose path fails closed with the same error instead of emitting an empty start.

Sessions already created on the previous protocol version keep the old rule, so a stored empty first start still replays. The version is the state-root protocol tag (`StateRootAndProtocolVersion`), not the devshardd build name. That tag is `v7` (`StartRequiresFloorProtocolVersion`) and later. The default tag stays `v6`, so a session created on `v6` still applies an empty first start. A tag that is not `vN` keeps the `v6` rule.

`applyStartInference` is unchanged in how it stores the time: `StartedAtTimeMs` is `floorTimeMsLocked` of the stamp. A missing map entry is still `ErrHeaderPending` and the diff is not applied.

### Test plan

Gate for [step 5](#step-5-the-floor-must-be-fresh). Step 5 starts when every box below is checked.

- [x] New protocol version, empty floor, empty start: `ErrStampNotFloor`. The same start with a height is also `ErrStampNotFloor`.
- [x] New protocol version, floor `(100, hash)`, start carrying that pair, map has the time: `StartedAtHeight` is 100 and `StartedAtTimeMs` is that time. `TimeoutRemaining` for refusal does not return `ErrTimeoutNotArmed`.
- [x] New protocol version, floor set, start carrying a different height or a different hash: `ErrStampNotFloor`.
- [x] Previous protocol version, empty floor, empty start: still applies, `StartedAtTimeMs` is 0, refusal is `ErrTimeoutNotArmed`.
- [x] A replica that applies the same new-version start without the floor rejects it. The rejection does not read the replica's oracle tip and does not use `DeltaBlocks`.

---

## Step 5. The floor must be fresh

[↑ Steps](#steps)

### Description

The chat gate from [step 3](#step-3-chat-gate-on-the-floor) gains one more condition. `time(F)` must be within two cadence intervals of the gateway clock. One interval is 24 seconds unless overridden, so the window is 48 seconds. A floor older than that does not open chat. The next span is one interval after the last turnover, and `time(F)` is already behind that turnover by the ack round trip, so a window of one interval would be stale at the moment the replacement span is sent.

When the floor is stale, or empty, the gateway sends a span and waits for a new ack. Chats of the session share that span: a second request inside the same interval sends nothing.

- `F` empty: the unstamped span from [step 1](#step-1-unstamped-bootstrap-span).
- `F` stale: an ordinary span that copies `F`. Hosts ack `max(tip, F)`, which raises `F` when their tip is newer.

The 30 second cutoff stays on bootstrap. It does not apply once `F` has a map time and half the slots have acked, so a stale floor on the first inference stays pending and this span is sent. A floor that is fresh but held by fewer than half the slots does not open chat. After the first inference commits, the gate is not consulted.

This bounds how far `StartedAtTimeMs` can sit behind the real start. The on-chain refusal window is shortened by at most about two intervals.

### Test plan

Gate for [step 6](#step-6-no-gateway-without-a-header-time-source). Step 6 starts when every box below is checked.

- [x] `time(F)` is now minus half an interval, half the slots have acked, the map has the time: chat is ready.
- [x] `time(F)` is now minus one interval plus the ack lag: chat is ready.
- [x] `time(F)` is now minus more than two intervals: chat is not ready, and a stamped span is sent. A second chat inside that interval does not send another span.
- [x] That span's ack raises `F` to a block whose map time is within one interval: chat becomes ready, and the start carries the new floor.
- [x] A fresh floor held by one slot of three: chat is not ready.
- [x] The gateway clock is not written into `StartedAtTimeMs`. The stored value is the map time of `F`.

---

## Step 6. No gateway without a header-time source

[↑ Steps](#steps)

### Description

`mustBuildGateway` fails when `gatewayHeaderTimes()` is nil (`devshard/cmd/devshardctl`). The error is `header_times_unwired: no chain source for block times`. `listenGateway` returns that error and does not start the HTTP server. A gateway with a source starts, and the chat gate reads that source through `LookupHeaderTimeMs`.

A host process is unchanged here. `SetHeightSyncFromEnv` staying a no-op when both chain clients are nil is a separate decision and is not part of this step.

### Test plan

Gate for [step 7](#step-7-retire-the-seed). Step 7 starts when every box below is checked.

- [x] Gateway build with a nil header-time source returns an error and does not start the HTTP server.
- [x] Gateway build with a source starts, and the chat gate reads that source.
- [x] The error text names the missing block-time source.

---

## Step 7. Retire the seed

[↑ Steps](#steps)

### Description

The seed RPC (`POST /sessions/:id/height-sync`, `ServeSeedHeightSync`) stays on the host for operators. The gateway does not call it during startup or during a chat request. [Step 3](#step-3-chat-gate-on-the-floor) is the proof that a host can see a block. The peer-tip cache is filled from the HeightSync section on the bootstrap heartbeat response, ingested by `ingestResponseHeightSync`, the same function `SeedHeightSync` uses for its Anchor.

`/v1/status` keeps the field name `height_seed`. Its count is the applied-ack count from [step 3](#step-3-chat-gate-on-the-floor), and `state` is `ok` when that gate and [step 5](#step-5-the-floor-must-be-fresh) would open chat.

### Test plan

- [x] After the bootstrap span, the gateway peer-tip cache has an entry for every host that acked with a block, taken from that response's HeightSync section.
- [x] A chat request does not call `SeedHeightSync`. A test host that fails the RPC still lets chat open once the floor gate is met.
- [x] `/v1/status` `height_seed.state` is `ok` exactly when [step 3](#step-3-chat-gate-on-the-floor) and [step 5](#step-5-the-floor-must-be-fresh) would open chat.
- [x] A host with the seed RPC disabled still acks the bootstrap heartbeat and still raises `F`.