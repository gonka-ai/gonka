# Header-time poll mocks

**Status:** Done
**Timeout checks that read the map:** [async-header-time-timeout-plan.md](async-header-time-timeout-plan.md)
**Production poll:** [block-change-feed-plan.md](../block-change-feed-plan.md)

Session tests today stamp a height inside the test and hand that stamp to `HeaderTimeMs`. Production does not do that. The gateway and devshardd each long-poll dapi `GetHostEvents`. A `BLOCK` carries `(height, hash, header_time_unix_ms)`. If that poll fails, the process reads the same triple from its chain subscription. The map is `headertime.Source`. The timestamp is the wall clock at the moment the chain progressed to that height.

This plan builds the mocks those tests should drive, and switches the timeout tests onto the production poll. It does not change the timeout inequalities.

## Steps

Update the status column as a step lands. Each row links to its step, and each step links back here.

| Step | Delivers | Status |
|---|---|---|
| [1. Mock chain](#step-1-mock-chain) | Progressing a height records `(height, hash, wall clock now)` once | Done |
| [2. Mock dapi long-poll](#step-2-mock-dapi-long-poll) | `GetHostEvents` serves that triple. The production poller writes the map | Done |
| [3. Failover to the mock chain](#step-3-failover-to-the-mock-chain) | A failed dapi poll still delivers the next progressed height from the chain subscription | Done |
| [4. Timeout tests use the poll](#step-4-timeout-tests-use-the-poll) | Gateway and host maps are filled only by their pollers. The side clock is gone | Done |

## Key decisions

- **The test does not write the map.** `floorRig.set` progresses the mock chain. It does not call `HeaderTimeMs`, `Source.Observe`, or `wallHeightClock`.
- **One stamp per height.** The wall clock is read when that height is first progressed. A later height does not change it. A jump from 100000 to 200000 does not invent 100000 seconds.
- **Same poller as production.** The test runs `hostevents.Poller`. dapi blocks go through `applyBlock`. Chain blocks go through the chain observer that already calls `Source.Observe`.
- **Two maps.** The gateway poller and the devshardd poller each have their own `Source`. A test can advance one feed and leave the other behind.
- **`height × 1000` is not a block time.** Helpers that still put `HeaderTimeUnixMs: height * 1_000` on a `BLOCK` are updated in [step 2](#step-2-mock-dapi-long-poll) so they stop teaching that stamp.

---

## Step 1. Mock chain

[↑ Steps](#steps)

### Description

Add one in-process chain the tests can progress. Progressing to height `H` records:

```text
(H, hash, timestamp = wall clock now)
```

The hash is the block hash the floor rig already carries (`[]byte{0xaa}` unless a test sets another). A second progress to the same height keeps the first timestamp. A progress to a lower height starts a new series and drops the old stamps, so one test does not keep another's clock.

This step does not start a poll and does not touch `headertime.Source`.

### Test plan

Gate for [step 2](#step-2-mock-dapi-long-poll). Step 2 starts when every box below is checked.

- [x] Progress to 100, then to 101. Each record's timestamp is the wall clock at that call, within a few milliseconds. The two timestamps differ by the time between the calls, not by 1000 ms.
- [x] Progress to 100000 after 100. `time(100)` is unchanged. `time(100000) - time(100)` is the real gap between the two calls.
- [x] A second progress to 100000 keeps the first timestamp.
- [x] Progress to 50 after 100000 clears the series. The next read of 100000 is absent. 50 has a new wall-clock stamp.

---

## Step 2. Mock dapi long-poll

[↑ Steps](#steps)

### Description

Serve [step 1](#step-1-mock-chain) on the existing `GetHostEvents` feed. Reuse the host-event ring and `BLOCK` payload. When the mock chain progresses, the ring appends one `BLOCK` whose `header_time_unix_ms` is that record's timestamp.

A test builds the production `hostevents.Poller` with `BlocksVerified` set, points it at an in-process `GetHostEvents` client, and gives it its own `headertime.Source`. The poller is the only writer. `applyBlock` stores `time.UnixMilli(header_time_unix_ms)`.

Update the in-memory fixtures that still stamp `height * 1_000` (`appendBlock` in `devshard/testenv/mockdapi/hostevents_test.go` and the block helper in `devshard/hostevents/poller_test.go`) so a progressed height carries the wall clock from [step 1](#step-1-mock-chain). `appendHostEventBlock` already copies the chain event time. Leave that path.

### Test plan

Gate for [step 3](#step-3-failover-to-the-mock-chain). Step 3 starts when every box below is checked.

- [x] Progress the mock chain to 100, then 101. After the poller catches up, `HeaderTimeMs(100)` and `HeaderTimeMs(101)` equal the timestamps from [step 1](#step-1-mock-chain). Origin is dapi.
- [x] The test process never calls `Source.Observe` or `SetHeaderTimes`.
- [x] A height the chain has not progressed is absent from the map. The poller does not call `GetBlockHeader`.
- [x] The fixtures that used `height * 1_000` now store the wall clock of the progress. A test that asserted the old product is updated to the wall clock.

---

## Step 3. Failover to the mock chain

[↑ Steps](#steps)

### Description

When `GetHostEvents` fails, or the feed is down, the process still learns the next height from the mock chain. Use the production chain subscription shape: a `NewBlock` delivers `(height, hash, timestamp)` and the existing observer writes `Source` with origin chain. The timestamp is the one [step 1](#step-1-mock-chain) recorded. The test does not write the map on the failure path either.

Stopping the dapi client must not stop the chain. Starting dapi again makes the next block arrive with origin dapi.

### Test plan

Gate for [step 4](#step-4-timeout-tests-use-the-poll). Step 4 starts when every box below is checked.

- [x] With the dapi client returning an error, progress the mock chain. The map receives that height from the chain subscription. Origin is chain. The timestamp is the wall clock from [step 1](#step-1-mock-chain).
- [x] While dapi is failing, a height already stored from an earlier dapi block keeps its timestamp.
- [x] Restore the dapi client and progress once more. That height is stored with origin dapi.
- [x] No test on this path calls `HeaderTimeMs` to insert the stamp.

---

## Step 4. Timeout tests use the poll

[↑ Steps](#steps)

### Description

The gateway session and each host in the timeout tests get their own poller and their own `Source` from [step 2](#step-2-mock-dapi-long-poll). `setupArmedSession` starts those pollers. `floorRig.set` only progresses the mock chain. Remove `wallHeightClock` from this path (`devshard/user/wall_height_clock_test.go` and the fallback installed in `devshard/user/headertime_fallback_test.go`).

A height jump is not a way to age a block. Tests that used to raise the floor so `height × 1000` would pass `ExecutionTimeout` or `RefusalTimeout` must wait on the wall clock, or assert that the challenge stays closed. The chain's `TimeoutDue` reads the floor's header time from the same map, so a jump that used to make `time(200000) - time(100000)` equal 100000 seconds no longer makes a timeout due.

`ConfirmedAtTimeMs` and `StartedAtTimeMs` stay whatever production stores at apply. For these tests that value is the poll's timestamp of the floor height at apply, because apply reads the map. The gateway execution check remains `wall clock - time(ConfirmedAtHeight)`.

### Test plan

The mock plan is done when every box below is checked.

- [x] `TestExecutionOpensOnTheConfirmedHeight` still passes, and the header time it reads was written by the gateway poller.
- [x] Raising the floor from 100000 to 200000 does not open an execution challenge. `time(ConfirmedAtHeight)` is unchanged. The wait's remaining time stays about `ExecutionTimeout`.
- [x] A refusal whose `sendTime` is 1970 still opens on the gateway wall clock. The chain accepts it only after the floor's polled header time is past `StartedAtTimeMs` by `RefusalTimeout`. A test that needs the chain to accept advances the wall clock, or stops asserting `Applied` while the stamps are a few milliseconds apart.
- [x] With dapi stopped, the next progressed height still appears in the gateway map and the host map via [step 3](#step-3-failover-to-the-mock-chain).
- [x] `go test` of the user timeout suite finishes without sleeping out `ExecutionTimeout`. Tests that need the full interval use a short timeout in the session config, or a clock the poll records, and they do not block the package for 1200 seconds.
