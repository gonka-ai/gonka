# Async header time for timeout and challenge

**Status:** Draft / proposal  
**Related:** height-sync floor `F`, `DeltaBlocks` (default 2), legacy `started_at` / `confirmed_at`  
**Scope:** Where a block time is learned, which messages may wait for it, and how a gateway triggers a timeout challenge.

This is a design note only. It does not change code by itself.

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

The diff contains no header time. `started_at` and `confirmed_at` stay as they are today: the local wall clock at the moment the message is created. They are not a chain time and they are not filled from the map.

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

Header time is used in two local decisions: the gateway deciding to open a timeout challenge, and a verifier deciding whether to answer one. Both read the map. Neither reads a time out of the diff.

`event_height` is the height stamp on the message being judged: the start stamp for a refusal, the confirm stamp for an execution timeout. `timeout_interval` is the existing second bound (`RefusalTimeout` or `ExecutionTimeout`).

### Gateway trigger

**Refusal.** The gateway created the `MsgStartInference`, so it already knows when it sent it. It opens the refusal challenge on its own wall clock:

```text
gateway_wallclock - started_at > RefusalTimeout
```

It does not need the map for this trigger.

**Execution.** The confirm stamp was written by the executor. The gateway opens the challenge when the map has a time for the confirm stamp and

```text
gateway_wallclock - time(confirm stamp) > ExecutionTimeout
```

When the legacy wall clock already says the interval has elapsed,

```text
gateway_wallclock - confirmed_at > ExecutionTimeout
```

and the map has no time for the confirm stamp, the gateway does not open the challenge. It logs that fact. The log means the gateway's block feed is behind the chain by more than the timeout: the wall clock of the message is already due, and the block that would prove it has not been seen.

### Verifier, answering a challenge

The check is asynchronous and uses the verifier's own wall clock and its own map:

```text
verifier_wallclock - time(event_height) > timeout_interval
```

- The inequality holds: answer the challenge now.
- The map has the time and the inequality does not hold. Let

  ```text
  delta = timeout_interval - (verifier_wallclock - time(event_height))
  ```

  - `delta < 60s`: schedule the same check once, after `delta`, then answer.
  - `delta >= 60s`: ignore this challenge message. Do not sign a vote and do not hold the request open.
- The map has no entry for `event_height`. Wait up to 60 seconds for the map to catch up from the block feed, without calling `GetBlockHeader` and without blocking other work. When the entry arrives, apply the two rules above. When it still has not arrived after 60 seconds, do not sign on this round, and log that this verifier's block feed is behind the chain.

The 60 second bounds are these waits only. They are not a period during which an executor can suppress the vote by answering `header_pending`.

### Why a stamp cannot point past the chain

A stamp that names a height no process will ever see would leave the map empty forever. Height sync is optimistic, but it has disputes: a floor is raised only by host-signed stamps, and a raise whose `(height, hash)` does not exist on chain is disputed and attributed to the signer that set it. A floor therefore cannot sit in the far future without proof that the claimed height exists on chain. A stamp beyond what the chain has produced is a dispute, not a deadline that never arrives.

---

## What stays off the sign path

Applying a diff, signing a diff, and returning ConfirmStart or Finish use the floor index and the height-sync stamp rules. They succeed or fail from the stamp and the log. A missing header time is not an apply error, not `ErrHeaderPending` on that path, and not a reason to retry the apply for seconds before answering.

The state root keeps the legacy wall-clock fields. It does not gain a header-time field that every replica must resolve before the root will match.
