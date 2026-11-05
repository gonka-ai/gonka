# Async header time for timeout and challenge

**Status:** Implemented (timeout trigger/answer). Seal/drain clock path superseded by [stamped-block-time-plan.md](stamped-block-time-plan.md).  
**Implementation:** [async-header-time-timeout-plan.md](async-header-time-timeout-plan.md)  
**Related:** height-sync floor `F`, `DeltaBlocks` (default 2), legacy `started_at` / `confirmed_at`, stamped `observed_time_ms`  
**Scope:** Where a block time is learned, which messages may wait for it, and how a gateway triggers a timeout challenge.

Historical design note. Timeout trigger/answer rules below still match the shipped plan. Sections that say seal/drain wait on the map, or that the diff carries no header time, are superseded by stamped block time — see callouts.

---

## Goal

A height stamp is `(height, block hash)`. Block time for that pair comes from a block-change feed: the dapi block long-poll, or the process's own Comet subscription. Start, ConfirmStart, and Finish carry the stamp and return immediately. Header time is consulted only when deciding to trigger or answer a timeout challenge, and that decision is local and asynchronous.

---

## Where height, hash, and time arrive

Comet `NewBlock` delivers one event per committed block. Each event carries the height, the block hash, and the header time.

Today:

- dapi's event listener subscribes to `tm.event='NewBlock'` and keeps `BlockInfo{Height, Hash, Time, ChainID}` inside the dapi process.
- A host's own chain listener parses the same event into `NewBlockEvent{BlockHeight, BlockHash, Time, ChainID}` and records it on the height-sync tip cache.
- dapi's NodeManager long-poll (`GetHostEvents`) carries escrow and maintenance events. It does not carry blocks.

### dapi block long-poll

dapi provides a long-poll for new blocks. Each item carries `(height, block hash, header time)` from its `NewBlock` listener. A caller passes the last height it has, and the call returns every newer block dapi holds, or waits until the next one is committed.

`devshardd` and the gateway subscribe to it and fill their local map from it. A process's own Comet `NewBlock` subscription is the fallback when dapi is unreachable.

Learning `time(height, hash)` this way does not send a per-height `GetBlockHeader`. `GetBlockHeader` with `wait_ms`, the light client (`light.Verify`), and the SQLite `height → (hash, time)` store are a separate lookup path. They are not required for this clock.

---

## Local map

Every gateway and every host keeps

```text
(height, block hash) → header time
```

filled by the dapi block long-poll, or by the process's own Comet subscription as fallback.

A lookup reads the map. It does not call out. A missing entry means "this process has not seen that block yet."

---

## Messages that carry a stamp and do not wait

> **Superseded for stamp times:** every height stamp now also carries `observed_time_ms` ([stamped-block-time-plan.md](stamped-block-time-plan.md)). Compose fills it from the map or an optimistic candidate (floor/tip); apply copies it onto the record. Wire wall-clock `started_at` / `confirmed_at` stay reserved/absent as before.

### Gateway, `MsgStartInference`

The gateway writes the current floor into the start stamp: the height and hash of `F` at that nonce. It does this from the floor index it already has. It does not look up `time(F)` and it does not delay the diff until that time exists. Heartbeats keep `F` close to the chain tip in a quiet session.

`started_at` is the gateway's wall clock at send.

### Executor, `MsgConfirmStart` and `MsgFinishInference`

The executor writes a height stamp on the receipt and on the finish, then responds. It does not look up `time(height)` and it does not wait for the block that stamp names.

When the stamp is a floor carried by an earlier diff, the executor's own tip can sit behind that floor. The height-sync rule already allows this when the gap is inside the optimistic delta (`DeltaBlocks`, default 2):

```text
height_local < F
F - height_local <= delta
```

In that window the executor trusts the carried `F` and puts that same `(height, hash)` on ConfirmStart and Finish. L0b already treats start-versus-confirm as a cross-signer comparison and does not call an executor behind the carried start a regression. The response path follows that rule and still does not fetch the header.

`confirmed_at` is the executor's wall clock when it signs the receipt.

---

## Where header time is used

Header time is used for the gateway deciding to open a timeout challenge and for a verifier deciding whether to answer one; both read the local map. Seal and drain use **carried** stamp / floor times ([stamped-block-time-plan.md](stamped-block-time-plan.md)); the pre-apply map wait in [Seal and drain wait for the map](#seal-and-drain-wait-for-the-map) is historical. Timeout apply does not read the map: it counts votes only.

`event_height` is the height stamp on the message being judged: the start stamp for a refusal, the confirm stamp for an execution timeout. `timeout_interval` is the existing second bound (`RefusalTimeout` or `ExecutionTimeout`).

### Gateway trigger

**Refusal.** The gateway created the `MsgStartInference`, so it already knows when it sent it. It opens the refusal challenge on its own wall clock:

```text
gateway_wallclock - started_at > RefusalTimeout
```

It does not need the map for this trigger.

**Execution.** The anchor is confirmed-at time. That timestamp is the header time of `ConfirmedAtHeight`, read from the map. It is not the stored `ConfirmedAtTimeMs`, and it is not the confirm stamp. The gateway opens the challenge when the map has that time and

```text
gateway_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout
```

The host uses its own wall clock against `time(ConfirmedAtHeight)` from its own map. Both sides share that timestamp.

When the executor's wall clock already says the interval has elapsed,

```text
gateway_wallclock - confirmed_at > ExecutionTimeout
```

and the map has no time for `ConfirmedAtHeight`, the gateway does not open the challenge. It logs that fact. The log means the gateway's block feed is behind the chain: the wall clock of the message is already due, and the block that would prove it has not been seen.

### Verifier, answering a challenge

A refusal uses the verifier's own wall clock and its own map, with `event_height` the start stamp:

```text
verifier_wallclock - time(event_height) > RefusalTimeout
```

An execution timeout uses the same check as the gateway, on this verifier's wall clock and its own map:

```text
verifier_wallclock - time(ConfirmedAtHeight) > ExecutionTimeout
```

- The inequality holds: answer the challenge now.
- The map has the times and the inequality does not hold. `delta` is the time left on that check.

  - `delta < 60s`: schedule the same check once, after `delta`, then answer.
  - `delta >= 60s`: ignore this challenge message. Do not sign a vote and do not hold the request open.
- The map has no entry the check needs. Wait up to 60 seconds for the map to catch up from the block feed, without calling `GetBlockHeader` and without blocking other work. When the entry arrives, apply the two rules above. When it still has not arrived after 60 seconds, do not sign on this round, and log that this verifier's block feed is behind the chain.

The 60 second bounds are these waits only. They are not a period during which an executor can suppress the vote by answering `header_pending`.

A second `VerifyTimeout` for the same inference and the same reason is refused on the host, from memory, not from the state machine. While the check above is held, the later call returns `accept=false` with an empty body and a warning, and the held check is the one that answers. After that check has returned a vote, the pair stays remembered and a later call is refused the same way. An answer that carried no vote is not remembered. A refusal and an execution timeout are separate pairs. The remembered pair is dropped when the local record leaves `Pending` for a refusal or `Started` for an execution, and a restart drops it. The implementation step is [step 6](async-header-time-timeout-plan.md#step-6-refuse-a-duplicate-challenge) of the timeout plan.

### Why a stamp cannot point past the chain

A stamp that names a height no process will ever see would leave the map empty forever. Height sync is optimistic, but it has disputes: a floor is raised only by host-signed stamps, and a raise whose `(height, hash)` does not exist on chain is disputed and attributed to the signer that set it. A floor therefore cannot sit in the far future without proof that the claimed height exists on chain. A stamp beyond what the chain has produced is a dispute, not a deadline that never arrives.

---

## Seal and drain wait for the map

> **Superseded.** Seal and drain now read carried `StartedAtTimeMs` / `ConfirmedAtTimeMs` and floor `TimeMs` ([stamped-block-time-plan.md](stamped-block-time-plan.md) steps 4–6). There is no pre-apply map snapshot and no `DiffRecord.HeaderTimes` write. The only residual wait is settlement drain when carried floor time is still `0` (`waitFloorTimeIfMissing`). Map prefer when the map already has `time(F)`: use the map and log carried≠map. Historical map-wait text below is kept for context only.

Seal and drain write `SealedAcc`, balances, and host stats. Those values are in the state root, so both replicas have to use the same clocks. Carried stamp times make that deterministic without a map wait. Timeout apply no longer uses those rows.

---

## What stays off the sign path

Signing a diff, and returning ConfirmStart or Finish, use the floor index and the height-sync stamp rules. They succeed or fail from the stamp and the log. A missing header-map row on that path is not an apply error and not a reason to hold the answer; compose fills `observed_time_ms` from the map or an optimistic candidate.

The state root omits stamp times from the canonical inference entry (heights/hashes only). Seal/drain read carried fields from the live record / floor.
