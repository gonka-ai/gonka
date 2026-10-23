# Async header time for timeout and challenge

**Status:** Draft / proposal  
**Related:** height-sync floor `F`, `DeltaBlocks` (default 2), legacy `started_at` / `confirmed_at`  
**Scope:** Where a block time is learned, which messages may wait for it, and how a gateway triggers a timeout challenge.

This is a design note only. It does not change code by itself.

---

## Goal

A height stamp is `(height, block hash)`. Block time for that pair comes from the block-change feed the process already receives. Start, ConfirmStart, and Finish carry the stamp and return immediately. Header time is consulted only when deciding to trigger or answer a timeout challenge, and that decision is local and asynchronous.

---

## Where height, hash, and time already arrive

Comet `NewBlock` already delivers one event per committed block. Each event carries the height, the block hash, and the header time.

- dapi's event listener subscribes to `tm.event='NewBlock'` and stores `BlockInfo{Height, Hash, Time, ChainID}`.
- A host's chain listener parses the same event into `NewBlockEvent{BlockHeight, BlockHash, Time, ChainID}` and records it on the height-sync tip cache.

That feed is the source of the local map. Learning `time(height, hash)` does not send another `GetBlockHeader`.

`GetBlockHeader` with `wait_ms`, the light client (`light.Verify`), and the SQLite `height → (hash, time)` store are a separate lookup path. They are not required for this clock.

---

## Local map

Every gateway and every host keeps

```text
(height, block hash) → header time
```

filled only by those block-change events (dapi NewBlock, or the process's own Comet subscription).

A lookup reads the map. It does not call out, and it does not wait for a later block. A missing entry means "this process has not seen that block yet."

---

## Messages that carry a stamp and do not wait

The diff contains no header time. `started_at` and `confirmed_at` stay as they are today: the local wall clock at the moment the message is created. They are not a chain time and they are not filled from the map.

### Gateway, `MsgStartInference`

The gateway writes the current floor into the start stamp: the height and hash of `F` at that nonce. It does this from the floor index it already has. It does not look up `time(F)` and it does not delay the diff until that time exists.

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

The gateway opens a challenge when the map has a time for `event_height` and

```text
gateway_wallclock - time(event_height) > timeout_interval
```

The legacy wall clock on the message is the other signal. Each start still has `started_at` and each confirm still has `confirmed_at`. When that wall clock says the interval has already elapsed:

```text
gateway_wallclock - started_at  > timeout_interval    (refusal)
gateway_wallclock - confirmed_at > timeout_interval   (execution)
```

and the map has no time for `event_height`, the gateway does not open the challenge. It logs that fact. The log means the gateway's block feed is behind the chain by more than the timeout: the wall clock of the message is already due, and the block that would prove it has not been seen.

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

A verifier with no map entry for `event_height` does not call `GetBlockHeader` and does not block the reply. It applies the same behind-chain log when the message wall clock is already past `timeout_interval`, and it does not sign on this round.

The 60 second bound is this schedule only. It is not a period during which an executor can suppress the vote by answering `header_pending`.

---

## What stays off the sign path

Applying a diff, signing a diff, and returning ConfirmStart or Finish use the floor index and the height-sync stamp rules. They succeed or fail from the stamp and the log. A missing header time is not an apply error, not `ErrHeaderPending` on that path, and not a reason to retry the apply for seconds before answering.

The state root keeps the legacy wall-clock fields. It does not gain a header-time field that every replica must resolve before the root will match.
