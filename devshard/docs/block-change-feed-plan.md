# Block-change feed: implementation plan

**Status:** Plan
**Design:** [proposals/block-change-feed.md](proposals/block-change-feed.md)
**Concerns adopted there:** [proposals/block-change-feed-concerns.md](proposals/block-change-feed-concerns.md)
**Review of steps 1–3:** [block-change-feed-plan-review.md](block-change-feed-plan-review.md)
**Implementation review of steps 1–7:** [block-change-feed-implementation-review.md](block-change-feed-implementation-review.md)

Implements the shared long-poll. One `GetHostEvents` loop on devshardd and the gateway. dapi writes the feed from one ordered loop, one height at a time, with `BLOCK(H)` last. The store is the catch-up window. On both binaries the existing `headertime.Source` is the only block map, and the poller is its primary writer.

Do the steps in order. The feed writer needs the `BLOCK` kind. The store needs the writer's per-height transaction. The RPC read needs the store. The block map needs its writer and filler hooks before the poller binds to it. Gateway verification sits on top of the poller. Testenv speaks the same feed before the stack scenarios run.

Out of this plan: refusal from `started_at`, execution from `time(confirm stamp)`, and the verifier's 60 second wait. Those stay in [async-header-time-timeout.md](proposals/async-header-time-timeout.md). This plan only fills the map those checks will read.

## Steps

Update the status column as a step lands. Each row links to its step, and each step links back here.

| Step | Delivers | Status |
|---|---|---|
| [1. Wire contract and ring capacity](#step-1-wire-contract-and-ring-capacity) | `BLOCK`, `BlockPayload`, `after_height`, `supported_kinds`, ring of 40960, page limit 1000 | Done |
| [2. One ordered feed writer](#step-2-one-ordered-feed-writer) | `HostEventFeed`, its own `emitted_height`, epoch labels, bounded membership lookups | Done |
| [3. Persist the feed](#step-3-persist-the-feed) | `HostEventStore` with a `writer` column, durable commits, head marker, one process per writer | Done |
| [4. `GetHostEvents` catch-up, paging, and stall](#step-4-gethostevents-catch-up-paging-and-stall) | Pages of 1000, the cursor rule, `needs_reset` with `next_cursor = head`, `Unavailable`, `EscrowLoad` for escrow subscribers | Done |
| [5. `headertime.Source` as the single block map](#step-5-headertimesource-as-the-single-block-map) | One map per binary, source and verified flags, `Filler`, floor window, `HighWater`, `BlockInterval` | Done |
| [6. One poller on devshardd and the gateway](#step-6-one-poller-on-devshardd-and-the-gateway) | Shared poller, one sink per kind, `after_height` once per process, trusted-dapi rule, `LoadMap` | Done |
| [7. Commit-signature checks for dapi blocks on the gateway](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway) | Self-check, pending buffer for the map window, link down from the trusted tip through dapi's headers | Done |
| [8. Testenv speaks the feed](#step-8-testenv-speaks-the-feed) | mock-dapi `GetHostEvents` with `BLOCK`, the cursor rule, and a commit the gateway can anchor | Done |
| [9. Testenv block-feed scenarios](#step-9-testenv-block-feed-scenarios) | Stack scenarios in `testenv/docs/scenarios.md`, suite `citest-block-feed` | Done |

## Key decisions

These came from checking the first design against the code. The design doc now includes them.

- **The feed is its own ordered loop, not part of `BlockObserver`'s handler walk.** It has its own cursor, `emitted_height`. Handlers keep `lastProcessedHeight` and the 500-block jump. A stalled store, a slow escrow lookup, or a long dapi outage then affects only the feed, never dapi's own handlers. The feed resumes at `emitted_height + 1` when that height is in the current epoch or the previous one. An older gap starts a new generation at the tip. Handlers resume at `lastProcessedHeight + 1`.
- **The store is written before the ring, and every commit is durable.** The ring only holds committed rows, and a crash cannot drop a commit, so a restart never serves different events under a sequence number a client already saw. SQLite runs with `synchronous=FULL`, and every Postgres transaction of the feed sets `synchronous_commit = on`.
- **Store rows carry a `writer` column** (the participant address). Every read, write, prune, and lock is scoped to it, so dapis of different participants can share one database.
- **A stalled feed answers `UNAVAILABLE`.** Clients keep their cursor and use their fallbacks until the feed recovers in the same generation.
- **Pages hold at most 1000 events**, from the ring and from the store.
- **`after_height` is read only when `cursor` is 0, and the client sends it only once per process.** A `needs_reset` answer carries `next_cursor = head`, and the client continues from it with `after_height = 0`. A reset can therefore never repeat on the same request.
- **The gateway trusts the dapi it points at.** `NODE_MANAGER_ADDR` is optional. Unset means dapi is never used and the header-time map is not filled from it. When set, that dapi is trusted for warm-key events and, if Comet seed failed, for block times. Comet headers are still signature-checked ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). The map itself is optional.
- **`EscrowLoad` is attached only for escrow subscribers.** The poller applies it only when it subscribes to escrow kinds.
- **Escrow membership lookups take a context.** The feed cancels them at the per-height deadline and runs at most 8 at a time.
- **`BlockPayload` carries the full Comet header.** Time is in milliseconds and the hash is raw bytes. When Comet seed succeeded, the gateway verifies dapi blocks before they reach the map. When Comet seed failed, it trusts dapi's latest height and writes further dapi blocks as verified.
- **The block map is the existing `headertime.Source`.** It is the only header-time store, the poller is its primary writer, and its miss path goes through the poller's filler.
- **Epoch labels come from height.** The feed caches each epoch's `poc_start_block_height` from `EpochGroupData`. Catch-up walks the current epoch and the previous one. An older gap starts a new generation at the tip.

---

## Step 1. Wire contract and ring capacity

[↑ Steps](#steps)

### Description

Add the fields the later steps read, without changing existing field numbers.

In `common/nodemanager/nodemanager.proto`:

- `HOST_EVENT_KIND_BLOCK = 9`. Values 1 and 2 stay reserved.
- `BlockPayload` on `HostEvent`, with a new field number. Do not reuse `escrow`, `maintenance`, or `warm_key`. Fields:
  - `height`
  - `hash`: raw 32 bytes, not hex
  - `header_time_unix_ms`
  - `epoch_index`
  - `header`: the protobuf-encoded `cmtproto.Header`
- `GetHostEventsRequest.after_height` (field 5). `0` means the client did not pass a height. The server ignores it until [step 4](#step-4-gethostevents-catch-up-paging-and-stall).
- `GetHostEventsResponse.supported_kinds` (field 8). Empty until [step 4](#step-4-gethostevents-catch-up-paging-and-stall) fills it. An old dapi omits it.

`hash` and `header_time_unix_ms` duplicate data inside `header`. They are there so devshardd can use a block without decoding the header. The gateway decodes `header` and checks that the two copies agree ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)).

Raise `DefaultHostEventRingCapacity` from 4096 to 40960. With full headers that is about 25 MB of memory. `NewHostEventRing` still takes an explicit capacity so tests can use a small ring. `Since` binary-searches the first retained slot with `seq` greater than the cursor, then walks only that suffix.

Add `MaxHostEventsPage = 1000`. Step 4 enforces it.

Regenerate the nodemanager stubs. Do not switch any writer or client in this step. Existing subscribe sets keep working. An old client that does not know `BLOCK` still receives only the kinds it subscribed to.

### Test plan

- [x] `host_events_wire_test.go` still pins fields 1–4 of the request and 1–7 of the response. New assertions pin `after_height = 5`, `supported_kinds = 8`, `BLOCK = 9`, and the `BlockPayload` field numbers.
- [x] A `BlockPayload` built from a real Comet header round-trips. `cmtproto.Header` decoded from `header` hashes to `hash`, and its time in milliseconds equals `header_time_unix_ms`.
- [x] A ring constructed with the default capacity holds 40960 events and drops the oldest on the next append.
- [x] A ring constructed with a small capacity still wraps at that capacity.
- [x] `Since` with a subscribe set that does not include `BLOCK` does not return a `BLOCK` event appended to the same ring.
- [x] `Since` on a full wrapped ring returns the first event past the cursor without reading every older slot.
- [x] An unset `after_height` and an empty `supported_kinds` decode as zero values, matching a peer built before this step.

---

## Step 2. One ordered feed writer

[↑ Steps](#steps)

### Description

Add `HostEventFeed` in `decentralized-api/internal/event_listener`. It becomes the only writer of the host-event ring. Today the ring is filled from two unordered paths:

- the `NewBlock` WebSocket workers (`handleMaintenanceLifecycleEvents`, `handleWarmKeyExpiryEvents`)
- ten concurrent `BlockObserver` handler workers (`escrow_events.go`, `maintenance_events.go`, `warm_key_events.go`)

The feed is a single goroutine with its own cursor, `emitted_height`. `BlockObserver.updateStatus` wakes it through a second notify channel. Like the handler walk, it runs only while `caughtUp` is set. It does not use `lastQueriedBlockHeight`, `lastProcessedBlockHeight`, or the 500-block jump. Those stay with the handlers.

Add `Header(ctx, *int64)` and `BlockchainInfo(ctx, minHeight, maxHeight)` to `TmHTTPClient`. The cosmos RPC client already implements both. Add a small cache of recent `BlockResults` keyed by height, shared by the feed and the handler walk. When the two are at the same height, which is the normal case, the node is read once.

For each height H, the feed:

1. Reads `BlockResults(H)` from the shared cache or the node. `FinalizeBlockEvents` carries BeginBlock `maintenance_canceled` and EndBlock warm-key expiry. `TxsResults` carries escrow, maintenance, and warm-key transaction events.
2. Reads the header. A single live height uses `Header(H)`. A catch-up reads `BlockchainInfo` in batches of 20.
3. Resolves slot membership for every escrow id in H, created and settled alike, concurrently with a 2 second budget for the whole height. An id still unresolved at the deadline is appended, matching today's "membership unresolved" branch. See "Escrow membership lookups" below.
4. Builds the events of H in this order: `FinalizeBlockEvents` with mode `BeginBlock`, then `TxsResults` in transaction order, then `FinalizeBlockEvents` with mode `EndBlock`, then `BLOCK(H)` last.
5. Appends that list to the memory ring and wakes waiters.
6. Sets `emitted_height = H`.

**Epoch label.** `epoch_index` comes from H, not from the phase tracker's current value. The feed keeps a small cache of epoch index → `poc_start_block_height`. The current epoch's entry comes from `LatestEpoch`, and older entries come from `EpochGroupData(epoch_index, model_id = "")`. H gets the largest index whose start is at or below H. Catch-up walks only the current epoch and the previous one, so a gap that starts before the previous epoch is not labeled height by height.

**Escrow membership lookups.** `escrowQuerier.GetEscrow(escrowID)` becomes `GetEscrow(ctx, escrowID)`. `ChainEscrowQuerier` passes that context to the `DevshardEscrow` query instead of `context.Background()`.
- The feed builds one context per height with the 2 second deadline and cancels it once the height's list is built. No lookup outlives its height.
- At most 8 lookups run at once, across the whole feed. An id that cannot start before the deadline counts as unresolved and is appended.
- The handlers' remaining callers pass their own request context.

**Earliest height.** The feed uses the observer's last `EarliestBlockHeight`. The sync checker records it every 5 seconds, and the feed refreshes it when the record is older than 30 seconds. The same checker stores `LatestBlockHeight` into the observer tip when it is higher, so a quiet WebSocket still moves the tip used by the 3-block stall.

**Failures.** A failed `Status`, `BlockResults`, `Header`, or epoch lookup stops the feed at H. The next wake retries H, and nothing after H is appended. The handler walk is not affected. The third failure of that same height marks the feed stalled. A later success clears the stall and appends H. A height whose chain reads succeeded and whose commit failed keeps those rows, and the next wake retries the commit only. The writer lock is checked before any chain read.

**Start height and pruned heights.**
- A fresh generation starts at the tip at its first wake. There is no backfill.
- If the node no longer has `emitted_height + 1` (`EarliestBlockHeight` is above it), the feed starts a new generation at the tip. Clients see that as `needs_reset`.
- If `emitted_height + 1` is older than the previous epoch (`epoch < current_epoch − 1`), the feed does the same. Catch-up does not walk pruned heights, or any height before the previous epoch.

**Handler changes.**
- Payload parsing in the handler files becomes pure functions the feed calls.
- Handlers keep their other side effects and stop calling `hostEvents.Append`. They also stop calling `localHoldsEscrowSlot` for the ring.
- `NewBlock` still calls `updateStatus`, `ObserveHex`, and the dispatcher. It no longer writes the ring.

Persistence lands in [step 3](#step-3-persist-the-feed). In this step a process restart still starts a new generation at the tip.

### Test plan

- [x] For a fixture height with a BeginBlock cancel, an escrow tx, a maintenance tx, a warm-key tx, and an EndBlock expiry, the appended sequence is: BeginBlock events, transactions in order, EndBlock events, then `BLOCK(H)`.
- [x] `BLOCK(H)` carries `hash`, `header_time_unix_ms`, and `header` from `Header(H)`.
- [x] `epoch_index`: a height one below a start is labeled with the earlier epoch, a height at the start with the new one. A catch-up that starts in the previous epoch labels each height through the current epoch. A next height older than the previous epoch starts a new generation at the tip and does not read the skipped heights. A failed epoch lookup stops the feed at that height.
- [x] A failed `Header(H)` or `BlockResults(H)` leaves the ring without `BLOCK(H)` and without any event of H+1. The next wake appends H, then H+1. The handler walk keeps advancing during the failure.
- [x] One failed `Status`, `Header`, `BlockResults`, or epoch lookup does not mark the feed stalled. The third failure of that height does. The next successful walk clears the stall and appends the height.
- [x] Two walks within 30 seconds call `Status` once. After that record expires, the next walk calls `Status` again.
- [x] A catch-up reads headers with `BlockchainInfo` in batches of 20, and does not call `Header` for those heights.
- [x] A failed commit retries the commit and does not read `BlockResults` or `Header` again.
- [x] A feed that does not hold the writer lock does not read the chain. `Status` and `EpochGroupData` are not called while another process holds the lock.
- [x] A height with five escrow events whose membership lookups each take 10 s is emitted within about 2 s, and all five events are appended.
- [x] After that height is emitted, every lookup goroutine has returned: the stub sees its context canceled, and the goroutine count is back to its baseline.
- [x] A height with 20 escrow ids never has more than 8 lookups in flight. Ids that could not start before the deadline are appended as unresolved.
- [x] `ChainEscrowQuerier.GetEscrow` passes its context to `DevshardEscrow`. A canceled context returns at once with an error.
- [x] Settled escrows go through the same membership check as created ones. An id that resolves to "not ours" is not appended.
- [x] The feed does not use the 500-block jump. With the handlers jumped and the feed 800 blocks behind inside the current epoch, the feed appends every one of those heights in order. `Generation()` changes when the node pruned the next height, or when that height is older than the previous epoch.
- [x] With the feed and the handlers at the same height, `BlockResults(H)` is read from the node once.
- [x] The `NewBlock` path and the handler workers no longer call `Append`. A test that drives only those paths appends nothing.
- [x] Existing handler side effects (escrow state, maintenance state) still run, with handlers alone advancing `lastProcessedBlockHeight`.
- [x] The feed does not walk while `caughtUp` is false.

---

## Step 3. Persist the feed

[↑ Steps](#steps)

### Description

Add `HostEventStore`. It is the catch-up window behind the 40960-slot memory ring, because three epochs of blocks do not fit in the ring.

Interface. The store is opened for one `writer`, and every operation is scoped to it:

- Append one height's rows together with `emitted_height` and `next_seq`, in one transaction. The rows go in one multi-row insert.
- Read the subscribed kinds after a sequence number, up to a page limit.
- Resolve `BLOCK(height)` to its sequence number.
- Prune rows whose `epoch_index` is older than `current_epoch − 4`.
- Load the hot tail (the last 40960 rows), the saved generation, `next_seq`, `emitted_height`, and the oldest retained sequence number.

Two implementations, SQLite and Postgres. The process picks SQLite or Postgres from `DAPI_HOST_EVENTS_STORE` (empty is SQLite). An unknown backend fails startup.

**Schema and writer.** Every table has a `writer` column, set to the participant address. Every statement filters on it.

```text
host_events      (writer, seq, height, kind, epoch_index, observed_at_unix, payload)
                 primary key (writer, seq)
                 index (writer, kind, seq)            -- filtered pages
                 index (writer, epoch_index)          -- prune
                 index (writer, height) where kind = BLOCK  -- after_height lookup
                 index (writer, height)                     -- eventsForHeight
host_event_feed  (writer, generation, next_seq, emitted_height)
                 primary key (writer)
```

The in-memory ring keeps no `writer` field: one dapi process serves one writer. Persistence adds it on write and filters by it on load, read, resolve, and prune. Two dapis of different participants on one database therefore have separate sequences, generations, retention, and locks.

`payload` is `proto.Marshal` of `HostEvent`. Field numbers stay stable when a Go field is renamed. Header bytes are stored as bytes.

**Write order.** The feed from [step 2](#step-2-one-ordered-feed-writer) does these in order for H:
1. Commits H to the store.
2. Writes the head marker (below).
3. Stores `emitted_height = H`, then appends the same rows to the memory ring in one `AppendBatch`.

The ring therefore only ever holds rows that are committed and recorded in the marker. On restart the hot tail matches exactly what clients were served, so the generation can be kept.

**Durable commits.** A commit the feed has acknowledged must survive a crash or power loss. Otherwise a restart could rebuild a lost height with different events, for example because a membership lookup resolves differently, under sequence numbers a client already holds.
- SQLite: the store is its own database file in the dapi data directory, opened in WAL mode with `PRAGMA synchronous=FULL` on every connection. A file that is not a database, or a corrupt image (`SQLITE_NOTADB`, `SQLITE_CORRUPT`), is renamed aside and the store opens a new file. The `-wal` and `-shm` files next to it are removed. Any other open error is returned and the file stays.
- Postgres: every feed transaction runs `SET LOCAL synchronous_commit = on`, whatever the server or role default is.

**Head marker.** Durable commits do not cover a Postgres failover to an asynchronous standby, or a restore from an older backup. Either can roll the store back without dapi noticing. After each commit, the feed writes `(writer, generation, seq, height)` of the last committed row to `host_events.head` in the dapi data directory: write a temp file, fsync it, rename it over the old one, then fsync the directory. A failed marker write is handled like a failed store write: the feed stalls at H, and the ring does not get H.

**Restart.**
1. On startup the store's head for this writer is compared with the marker:
   - Store head at or above the marker: the store has everything that was served. A marker lagging by one height after a crash between commit and marker write is expected.
   - Store head below the marker, a marker for another generation or writer, a missing marker with a non-empty store, or a marker file whose JSON does not parse: start a new generation at the tip.
2. If the store passes, the hot tail is loaded into the ring, and the generation, `next_seq`, and `emitted_height` are restored.
3. The feed resumes at `emitted_height + 1` from the node's `BlockResults`. This is correct because the feed has its own cursor, while handlers resume at `lastProcessedHeight + 1` as today.

If that comparison does not finish because the writer lock is held, the store cannot be read, or the marker file cannot be read, the feed stays stalled and does not reset. It runs the comparison again before the next append, with the same backoff as a stalled walk. It does not start at the tip on top of the previous generation.

Startup runs the comparison with a 30 second deadline. If it times out, the feed stays stalled and retries from its run loop. A timeout does not reset the store.

A pruned next height, or a next height older than the previous epoch, starts a new generation at the tip, as in [step 2](#step-2-one-ordered-feed-writer).

**Store write failure.** The feed stops at H and enters the stalled state. It does not append to the ring, does not skip H, and does not change the generation. dapi's handlers keep running. The feed retries H with backoff. When the store accepts the write, it catches up from `emitted_height + 1` in the same generation. Step 4 turns the stalled state into `UNAVAILABLE` for clients. If the node prunes `emitted_height + 1` while the feed is stalled, recovery starts a new generation at the tip.

**One process per writer.**
- On Postgres, the feed opens one dedicated connection (`sql.Conn`, not a pooled statement) and takes a session advisory lock on it, keyed by a hash of `writer`. It keeps that connection for its writes. Reads (`Read`, `ResolveBlock`, `Head`, `Load`, `Oldest`) use the pool and do not take the lock. Those reads run at `REPEATABLE READ` so a multi-statement transaction sees one commit. A statement that fails because the session is gone closes that session. The connection is not returned to the pool while it still holds the lock.
- If the connection drops, the lock is gone. The feed stalls, opens a new connection, takes the lock again, and reloads `host_event_feed` before writing. Reload continues in the same generation only when the reloaded row matches the ring: the same generation, `next_seq` equal to the ring head plus one, and `emitted_height` equal to the previous height. A height the store already emitted is the crash between commit and marker write: it is returned unchanged only when that generation and the first stored sequence still match the ring. Any other mismatch, including an already-emitted height with no matching rows, starts a new generation at the tip.
- A second dapi with the same `writer` that cannot take the lock does not run the feed and reports it as stalled. A dapi with a different `writer` takes a different lock and is unaffected.
- On SQLite, the database is a local file under the dapi data directory and is not shared. Writes use a dedicated `sql.Conn`. Reads use a pool of 4.

**Retention.** Every kind is stored: block, warm key, escrow, and maintenance. Prune runs when the current epoch changes, against `current_epoch − 4`, using the `(writer, epoch_index)` index. The extra epoch is the margin for a boundary block labeled one epoch early.

`GetHostEvents` does not read the store yet. That is [step 4](#step-4-gethostevents-catch-up-paging-and-stall).

### Test plan

- [x] SQLite and Postgres: appending H writes its rows, `emitted_height = H`, and `next_seq` together. Inject a failure on the last statement and assert the rows are rolled back.
- [x] One height's rows are inserted in one statement.
- [x] Rows for H are in the store before they are in the ring. A store failure on H leaves the ring without H, and later appends do not happen.
- [x] After a store failure, the handler walk still advances `lastProcessedBlockHeight`.
- [x] When the store accepts writes again, the feed appends every missed height in order, in the same generation, with consecutive sequence numbers.
- [x] A stalled feed whose next height has been pruned by the node recovers with a new generation at the tip.
- [x] `BLOCK(height)` resolves to the sequence number of that height's block row. A missing height resolves to not found.
- [x] A read after a sequence number returns only subscribed kinds, in sequence order, and stops at the page limit.
- [x] A stored payload is `proto.Marshal` of `HostEvent`. A round trip restores escrow, maintenance, warm key, and block bytes. The blob is not JSON.
- [x] Rows with `epoch_index` older than `current_epoch − 4` are deleted. A row at `current_epoch − 4` remains. Escrow and maintenance rows follow the same rule.
- [x] Two heights in one epoch prune once. The next epoch prunes again. The `(writer, epoch_index)` index exists.
- [x] Load restores the last 40960 rows, the generation, `next_seq`, and `emitted_height`. The feed's next height is `emitted_height + 1`, and the next append uses `next_seq`.
- [x] After a restart where `lastProcessedHeight < emitted_height`, handlers replay from `lastProcessedHeight + 1`, and the ring gets no duplicate rows.
- [x] A transient `Head` failure or a marker I/O error stays stalled and keeps the stored rows. A later success restores that generation. A corrupt marker, or a confirmed mismatch, starts a new generation.
- [x] A boot that does not finish stays stalled and runs the comparison again before appending. A later success restores a matching store.
- [x] Startup boot times out instead of blocking. The rows stay, and the run loop retries the comparison.
- [x] SQLite: every connection of the store reports `PRAGMA synchronous` = FULL (2) and `journal_mode` = wal.
- [x] SQLite: a file that is not a database is renamed aside, its `-wal` and `-shm` are removed, and a new store opens. A different open error leaves the file in place.
- [x] Postgres: with the role default set to `synchronous_commit = off`, a feed transaction still runs with `synchronous_commit = on`. Read the setting inside the transaction.
- [x] Order: a failed marker write leaves H committed in the store, absent from the ring, and the feed stalled. A retry appends H once.
- [x] Restart with the store head below the marker, which simulates a rolled-back Postgres, starts a new generation. A cursor from the old generation gets `needs_reset`.
- [x] Restart with the marker one height behind the store (crash between commit and marker write) keeps the generation.
- [x] Restart with a missing marker and a non-empty store starts a new generation. With an empty store and no marker, the first boot starts a generation normally.
- [x] Two writers in one database: each sees only its own rows on load, read, `BLOCK(height)` resolve, and prune. Their sequences, generations, and `emitted_height` are independent.
- [x] Postgres: a second feed with the same `writer` cannot take the lock and reports stalled. A feed with a different `writer` takes its own lock and runs. That second feed can still read, resolve, and load the writer's rows.
- [x] Postgres: a lost lock session is closed. Another connection can take the writer lock, and the feed's next append does too.
- [x] A read, resolve, head, and load do not wait on the writer mutex.
- [x] SQLite: with 4 read rows held open, `AppendHeight` returns without waiting for the reads.
- [x] Postgres: killing the lock connection stalls the feed. It takes the lock again on a new connection and reloads its state. Reload continues in the same generation only when the reloaded row matches the ring. An externally changed `next_seq` starts a new generation at the tip.
- [x] Postgres: `Load` and `Head` run at `REPEATABLE READ`. A concurrent append between the two statements leaves `Load`'s `next_seq` equal to the tail's last sequence + 1.
- [x] SQLite: append heights 1–2 at G. Set `host_event_feed` to `(G+1, next_seq, emitted_height = 10)`, with and without a row at height 3. `AppendHeight(3, {G, 3, 2})` returns `ErrFeedDiverged` in both cases. The next walk starts a new generation and is not stalled. A crash between commit and marker still returns the stored rows.
- [x] The process picks SQLite or Postgres from `DAPI_HOST_EVENTS_STORE` (empty is SQLite). An unknown backend fails startup.

---

## Step 4. `GetHostEvents` catch-up, paging, and stall

[↑ Steps](#steps)

### Description

Teach the existing RPC to read the store, to page, and to report a stalled feed.

**`supported_kinds`.** The response lists every kind this process emits, including `BLOCK`. An old server leaves it empty.

**Where the read starts:**

| Request | Read starts after |
|---|---|
| `cursor > 0` | `cursor`. `after_height` is ignored. |
| `cursor = 0`, `after_height = X > 0`, `emitted_height = 0` | nothing: `Unavailable`. `next_cursor` is unset. The client retries this request. |
| `cursor = 0`, `after_height = X > 0`, `X < emitted_height`, `BLOCK(X)` stored | the sequence number of `BLOCK(X)` |
| `cursor = 0`, `after_height = X > 0`, `X + 1` is this generation's first height | the oldest retained event of this generation |
| `cursor = 0`, `after_height = X > 0`, `X < emitted_height`, no `BLOCK(X)` row (pruned, or below this generation's first height − 1) | nothing: `needs_reset` with `next_cursor = head` |
| `cursor = 0`, `after_height = X = emitted_height`, and a height has been committed | the sequence number of `BLOCK(X)` when that row is stored, otherwise head |
| `cursor = 0`, `after_height = X > emitted_height`, and a height has been committed | head. The client is ahead of this feed. |
| `cursor = 0`, `after_height = 0` | the oldest retained event. This is today's behavior, kept for old clients. It is paged like any other read. |

Write this rule in the RPC comment. Because `after_height` is ignored once there is a cursor, the runner's dispatch retry (`cursor = seq − 1`) keeps working.

**Paging.** At most `MaxHostEventsPage` (1000) events per response, from the ring and from the store alike. `Since` already finds the first slot past the cursor by binary search. This step adds the page limit. `next_cursor` is:

- the sequence number of the last returned event, when the page is full
- the head sequence number captured before the read, when it is not full

A filtered client therefore never re-scans a range it has already passed. A page may end mid-height. The height is complete when its `BLOCK` event arrives.

**Read path.** The waiter is registered before the read (`longpoll.Wait`, same as today).

- The ring has not been restored (boot still pending): head and generation come from the store. Pages use the store path up to that head. The ring's boot nonce is not returned. A client at that head gets `Unavailable` while the feed is stalled. A failed store head read returns `Unavailable`.
- Start inside the memory ring: answer from the ring. If the ring wraps between `Bounds` and `SincePage`, answer from the store when the generation still matches.
- Start older than the ring and inside the store: answer from the store, with sequence numbers up to the captured head. The store holds every row the ring holds ([step 3](#step-3-persist-the-feed)), so one store read covers the gap between the store and the ring. Do not wait.
- Caught up: hold until the next appended event or `max_wait_seconds`.

**`needs_reset`** is returned for:
- a generation mismatch
- a cursor above head
- a cursor older than the store's oldest row
- an `after_height` with no `BLOCK` row, because it was pruned or is below this generation's first height − 1

Start is resolved once per request. A wake or timeout re-read uses that sequence and ignores `after_height`.

Every `needs_reset` response has no events, the current `generation`, and `next_cursor` = the head captured before the read. A client that continues from that cursor, with `after_height = 0`, is inside the window by construction, so the next request cannot be reset for the same reason. This is what stops a reset loop. A client that sends the same `after_height` again would be reset again, which is why [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) sends it only once per process.

A client inside the hot tail does not see `needs_reset` across a dapi restart, because [step 3](#step-3-persist-the-feed) restored the generation and the tail.

**`EscrowLoad`.** The response carries the `EscrowLoad` snapshot only when the request's subscribe set includes `ESCROW_CREATED` or `ESCROW_SETTLED`. Other requests get an empty field. This includes the gateway's, and every page of a catch-up for a non-escrow subscriber. The field is not attached to `Unavailable` errors.

**Stall.** While the feed is stalled (the store is failing, or another dapi holds the lock), or while `emitted_height` is more than 3 blocks behind the node tip (`NewBlock`, or the sync checker's `LatestBlockHeight` when that is higher):

- A request with committed events after its cursor gets them, paged as usual.
- A request that would otherwise wait gets `codes.Unavailable` with the stalled height in the message. A long-poll that times out still at that cursor does too.
- A store read that fails returns `codes.Unavailable`. A client cancel or deadline on that read returns that status instead.

Old clients already treat `Unavailable` as a transient error and retry with backoff. New clients keep their cursor and switch on the fallbacks in [step 6](#step-6-one-poller-on-devshardd-and-the-gateway).

### Test plan

- [x] A response from this dapi lists `BLOCK`, both warm-key kinds, both escrow kinds, and both maintenance kinds in `supported_kinds`.
- [x] `cursor = 0, after_height = X` with the tip above X+2000 returns 1000 events after `BLOCK(X)` without blocking. The following call with the returned cursor continues without a gap or a duplicate.
- [x] `cursor > 0` with a non-zero `after_height` reads from the cursor. A rewind to `seq − 1` returns that event again.
- [x] `cursor = 0, after_height = emitted_height` starts after `BLOCK(emitted)`. A ring that already holds the next height returns those events.
- [x] `cursor = 0, after_height > emitted_height` starts at head.
- [x] `cursor = 0, after_height > 0` while `emitted_height` is 0 returns `Unavailable` with no `next_cursor`, on a restored ring and an unrestored one. `after_height = 0` is unchanged. After a height is committed, the same `after_height` starts after that block (or at head when there is no store).
- [x] `cursor = 0, after_height = 0` against a full 40960-row ring returns 1000 events and a `next_cursor` that continues the window.
- [x] No response holds more than 1000 events, whether from the ring, from the store, or across the boundary between them.
- [x] A client subscribed only to escrow, with 3 escrow rows among 50000 store rows, gets all 3 in one response, and `next_cursor` equals the captured head.
- [x] A page that ends before `BLOCK(H)` does not include `BLOCK(H)`. The next page starts with the remaining events of H and then `BLOCK(H)`.
- [x] A cursor inside the restored hot tail, using the restored generation, returns events and `needs_reset = false`.
- [x] A cursor older than the pruned store, a cursor above head, a generation mismatch, and an `after_height` whose row was pruned each return `needs_reset`, no events, the current generation, and `next_cursor` = head.
- [x] A prune between `Oldest` and `Read` that moves the store past the cursor returns `needs_reset`, no events, and `next_cursor` = head.
- [x] Fresh generation started at tip T0: `cursor = 0, after_height = T0 − 5` returns `needs_reset` with `next_cursor = head`. A follow-up with that cursor and `after_height = 0` returns events or waits, and is not reset.
- [x] A ring of capacity 4 at the tail, with an append between `Bounds` and `SincePage`, is answered from the store with no `needs_reset`.
- [x] `after_height = tip − 1` waiting on a fresh generation returns `BLOCK(tip)` after the first commit. `after_height` equal to this generation's first height − 1 starts at the oldest retained event.
- [x] A request subscribed to an escrow kind gets `EscrowLoad`. A request subscribed only to `BLOCK` and warm-key kinds gets none, on live responses and on store pages alike.
- [x] A client at the tip holds until an append or `max_wait_seconds`, then returns `unchanged` on timeout. An append during the wait is not lost.
- [x] Stalled feed: a client behind head still drains committed events, and a client at head gets `Unavailable` instead of waiting. After recovery, the same cursor and generation continue without `needs_reset`.
- [x] A `GetHostEvents` `BLOCK` has the same `Hash` and `Header` bytes as the ring event.
- [x] With no `NewBlock`, a sync status 10 blocks above `emitted` makes a waiting request return `Unavailable`.
- [x] A long-poll that times out still at its cursor returns `Unavailable` when the feed stalled, or the tip moved more than 3 blocks ahead, during the wait. An event appended during that wait is returned.
- [x] A failing store read returns `Unavailable`, not `needs_reset`. A canceled read and a deadline return those statuses.
- [x] An unrestored ring (boot nonce, head 0) with a readable store answers from the store's generation and head. A cursor inside that window returns events and `needs_reset = false`. A cursor at that head gets `Unavailable`. A different generation gets `needs_reset` with the store generation and the store head. A failed store head read returns `Unavailable`.

---

## Step 5. `headertime.Source` as the single block map

[↑ Steps](#steps)

### Description

Both binaries create one `headertime.Source` and pass that instance to their header-time readers and Comet handlers:
- devshardd: `devshardd/session/heightsync.go`, then `sessionSMOpts` and `ObserveChainHeader`
- gateway: `devshardctl/heightsync_env.go`, `gatewayHeaderTimes` and the Comet `OnHeader` hook

The poller from [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) writes this map. When a Comet RPC is set, a miss uses header-only reads. Otherwise the existing `OracleFiller` stays. `WithFloor` is the window hook; the poller passes `EpochFloor` from BLOCK `epoch_index` (keep current and previous epoch).

**Entries.** Each entry gains a `source` (dapi, comet, chain) and a `verified` flag. `Observe(h, source, verified)` replaces `Observe(h)`.

**Conflict rule** for a height that is already present:

- Same hash: keep the entry, and upgrade `source` and `verified` if the new write ranks higher. On the gateway a verified dapi origin ranks with chain, so a filler read of the same hash does not relabel it. The gateway filler reports the origin the oracle stored (`fromDAPI`).
- Different hash on devshardd: a comet or chain write replaces a dapi entry, logged at warn and counted in a metric. Otherwise the existing entry stays, and the conflict is logged and counted.
- Different hash on the gateway: every entry there is verified ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)), so a conflict means two verified headers disagree. Keep the existing entry, log at error, and count it.

**Miss path.** `Source` no longer holds a `blocks.BlockOracle`. It takes a `Filler` interface, and a miss asks the filler for that height with today's 250 ms wait. The poller from [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) implements `Filler` with header-only reads. On the gateway those reads are verified ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). Lookups keep returning `ErrHeaderPending` and `ErrHeaderMismatch` as today.

**Startup.** Each binary reads the latest header and stores it, then subscribes to Comet `NewBlock`. Later blocks extend that tip. A lookup of a height H below the tip fetches H immediately. The missing heights between H and the live run are filled by one background job. The job reads them in batches of 20 into a cache and stores each batch in one apply. Each batch has its own deadline (`DefaultFetchTimeout × 4`). A timed-out batch is a miss and is retried by the existing rule, so a hung RPC cannot hold `catchOn` forever. The observer RPC client uses `rpchttp.NewWithTimeout` (10 s). That apply moves the contiguous run base down through the heights the batch connected. A second lookup H2 is queued behind the running job as H2 up to the catch floor. The catch floor stays put until a job is applied and connects, then it moves down to that job's anchor. A miss is retried a few times behind later ranges, and a retry whose anchor is above the catch floor is dropped. One job runs at a time.

**Window.**
- `Source` takes a floor function. The floor is the first height of epoch `current−1` (`EpochFloor`). Epochs older than that are dropped. A zero previous-epoch start keeps the whole map. The poller writes that height into the map on BLOCK applies (`SetFloorHeight`). Lookups use the cached value and do not recompute it.
- A lookup below the floor asks the filler, returns the time, and does not store the header.
- `DefaultCapacity` (200000) stays as a hard cap. It drops the lowest heights below the contiguous run before any height inside the run.
- The already-caught-up run is persisted under `headertime.sqlite` or Postgres (`DEVSHARD_STORAGE_MODE` / `PGHOST`, same as the other planes). Restore fills the map before the first poll. The same epoch floor prunes the cache.

**Read helpers.**
- `HighWater()` walks up from the base of the contiguous tip run and stops at the first gap. A stored height below that base does not move it. A missing floor height does not return 0; the walk stays on the stored run.
- `BlockInterval()` returns the median of the last 20 header-time gaps at the top of the run. It returns the configured default until 20 entries exist, when no two heights are consecutive, or when that median is 0. The gaps are kept incrementally on writes.

**Wiring.** Each binary creates one `Source` at startup and passes it to:
- the header-time readers (`state.WithHeaderTimes`, `gatewayHeaderTimes`)
- the poller ([step 6](#step-6-one-poller-on-devshardd-and-the-gateway))
- the Comet socket handlers

The `headertime.New(oracle)` calls are removed. `tipcache` and `failover.Oracle` stay as the height-sync tip. They are not a header-time store.

### Test plan

- [x] All header-time readers in both binaries receive the same `Source` instance. A test that builds each binary's wiring finds no second `headertime.New`.
- [x] `Observe` with the same hash from dapi, then comet, keeps one entry with `source = comet`.
- [x] devshardd: dapi hash A then comet hash B leaves B and counts one conflict. Comet A then dapi B leaves A and counts one conflict.
- [x] Gateway: two verified writes with different hashes keep the first and log at error.
- [x] A miss calls the `Filler` once per height, with concurrent lookups sharing one call. The filler's result is visible to the next lookup. With no filler set, a miss returns `ErrHeaderPending`.
- [x] A dummy header, a zero time, or an empty hash is never stored.
- [x] Entries below the floor function's value are dropped. The hard cap still applies when the floor function returns 0. A lookup below the floor is a pass-through filler read and is not stored.
- [x] `HighWater()` stops below a gap. Filling the gap moves it up. A height below the contiguous run does not move `HighWater`. A missing floor height does not zero `HighWater`. The hard cap removes that height before a height inside the run.
- [x] Restart restores the caught-up run from SQLite. An epoch floor prune drops those rows from the cache too.
- [x] `BlockInterval()` returns the median of the last 20 gaps, and the configured default before 20 entries exist or when that median is 0.
- [x] `HeaderTimeMs` of a stored height does not allocate more in a map of 20 000 than in a map of 1 000. `Observe` of the next tip has the same per-call allocs at those sizes.
- [x] Startup stores the latest header before the Comet subscription. A lookup below that run fetches the exact height immediately and fills the gap in one background job, in batches of 20 stored together. A lower lookup queues behind that job as a fill up to the catch floor. The catch floor moves when a job connects. A miss is retried a few times, and a retry above the new floor is dropped. A forward hole between two observed heights is its own batch.
- [x] A `BatchFiller` that blocks forever on the first call: that job ends after the batch deadline, and a second queued job runs and stores its heights.
- [x] `NewTendermint` against an `httptest` server that accepts and never answers: `BlockchainInfo` returns an error within the client timeout.
- [x] `TestGatewayHeaderTimes_OneSource` waits on the stub commit RPC for the queued height, then asserts the map does not store it.

---

## Step 6. One poller on devshardd and the gateway

[↑ Steps](#steps)

### Description

Replace the devshardd-only `hostevents.Run` loop with one poller used by both binaries. One cursor, one `GetHostEvents` call, and a sink per kind, each with its own fallback. Callers read the caches. Nothing on the sign path or in a timeout check dials from this loop.

```text
1. memory    headertime.Source ([step 5](#step-5-headertimesource-as-the-single-block-map)), and the warm-key log copy
2. dapi      GetHostEvents, when NODE_MANAGER_ADDR is set and the feed is live
3. chain     per kind, below
```

| Kind | Who subscribes | Fallback when dapi has no live feed |
|---|---|---|
| `BLOCK` | both | the process's Comet `NewBlock`, then header-only reads for holes |
| `WARM_KEY_GRANTED`, `WARM_KEY_REVOKED` | both | `WarmKeyLog` (the reload the copy already does) |
| escrow created / settled | devshardd only | rehydrate open escrows, lazy create |
| maintenance | neither | stays the gateway's own concern |

**Sinks.** `hostevents.Run` becomes the escrow sink and no longer opens its own long-poll. The warm-key sink moves into the poller, so both binaries apply the same events to the same copy. The block sink writes `Source` with `source = dapi`. On devshardd it marks entries verified, because dapi is the host's own. On the gateway, dapi blocks are trusted when Comet seed failed (`BlocksVerified`); when Comet seed succeeded they go through [step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway).

**Trust in the gateway's dapi.** `NODE_MANAGER_ADDR` is optional. When unset, the gateway never dials dapi and does not fill the header-time map from it; warm keys fall back to `WarmKeyLog`. When set, that dapi is trusted: a grant makes `VerifyWarmKey` accept the key, and if Comet seed failed the gateway asks dapi for the latest height (`GetBlockHeader(0)`) and stores it as verified. Document this next to `NODE_MANAGER_ADDR` in the gateway's configuration reference and its startup log line. The map is optional: timeouts can fill a missing height on demand.

**`EscrowLoad`.** The poller keeps the `LoadMap` hook from `hostevents.Config`. It calls `LoadMap.Replace` only when its subscribe set includes the escrow kinds, so a gateway or a devshardd with `DEVSHARD_HOST_EVENTS_ENABLED=false` never blanks the map with an empty field. While dapi answers `Unavailable` the map is not replaced. Readers see how old the snapshot is from `Snapshot()`'s `deliveredAt`. No production code wires `LoadMap` today. This step keeps the hook working and does not add a consumer.

**`DEVSHARD_HOST_EVENTS_ENABLED`.** On devshardd this flag only removes the escrow kinds from the subscribe set. The poller, the block sink, and the warm-key sink run regardless. The chain warm handler is registered in `startHostEventsWarm`, and the poller starts after `SeedHeaderTimes` so the first `after_height` sees the seeded tip.

**Cursor policy.** `after_height` is sent once per process, never after a reset:
- **Process start:** the first request sends `cursor = 0`, `generation = 0`, and `after_height = max(Source.HighWater(), floor)` when that value is positive. On the gateway, if dapi is configured and the map is empty, `after_height` is start-at-head (`AfterHeightHead`), never `0`. `0` on dapi replays the oldest retained events (the old-client path).
- **Normal response:** the next request sends the returned `cursor` and `generation`, with `after_height = 0`.
- **`needs_reset`:** the poller takes `next_cursor` and `generation` from the response, and the next request sends them with `after_height = 0`. Then it runs the fallbacks for what the reset skipped:
  - The escrow sink runs `RehydrateOpenEscrows`.
  - The warm-key copy reloads from `WarmKeyLog` on next use.
  - `Observe` of that `BLOCK` queues the map's forward catch-up job. It fills missing heights in the background, in batches of 20, and does not run inside the poll loop.

  Resending `after_height` here would hit the same missing row and be reset again, forever. That happens for example on a fresh dapi generation that starts above the client's high-water mark.
- **Old dapi:** it ignores `after_height` and answers the same as for cursor 0.

Before the first poll on devshardd, the escrow sink runs `RehydrateOpenEscrows` once. That covers escrows created before `after_height`.

**Idempotency.** Every sink accepts a replay:
- `WarmEscrow` on a warm escrow is a no-op.
- `OnEscrowSettled` on a settled escrow is a no-op.
- A warm-key event already in the copy is a no-op.

**When dapi has no live block feed.** `blockLive` is whether dapi is the primary `BLOCK` writer into `Source`. Comet and the filler always run; this flag does not start them. Warm-key and escrow events on a successful response are still applied. The poller reports not-live, and does not consume dapi `BLOCK`s, when any of these is true:

- `NODE_MANAGER_ADDR` is unset, or the RPC is `Unimplemented`
- `supported_kinds` is empty or does not contain `BLOCK`
- the RPC returns `Unavailable` ([step 4](#step-4-gethostevents-catch-up-paging-and-stall) stall)
- the poll succeeds but no `BLOCK` event arrives for three `Source.BlockInterval()`
- on the gateway, a dapi header failed verification within the last 5 minutes ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)) — this is the only case that still delivers `BLOCK`s, so `applyBlock` and `offerBlocks` both skip `Source` for that window

On `Unavailable` the poller keeps its cursor and generation and retries with the existing backoff. When `BLOCK` events resume and suppress has expired, dapi is the primary writer again. Operators read `block_feed_live` on gateway `GET /v1/debug/heightsync` (`header_time`) and on gauge `devshard_hostevents_block_feed_live`; the poller logs each flip.

**Comet sockets.** The two existing Comet sockets stay: the gateway's `tipcache.StartCometWithHooks`, and devshardd's `chainEvents.OnNewBlock` → `ObserveChainHeader`. Each also writes `Source` with `source = comet`. On the gateway those headers go through the verifier first ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). This plan does not open a third subscription.

**Holes.** A hole between the floor and the tip is filled by the `Filler` with header-only reads: Comet `/header`, or `BlockchainInfo` in batches of 20. `GetBlockByHeight` is not used. `GetBlockHeader(0)` is the gateway's trusted dapi seed when Comet failed; hole fills do not call it.

**Startup.** The gateway starts the poller from `initGatewayHeightSync`, even when `NODE_MANAGER_ADDR` is empty. An empty address does not dial. Seed is Comet `Trusted()` when that works, otherwise dapi `GetBlockHeader(0)` when the URL is set. A failed seed leaves the map empty.

### Test plan

- [x] devshardd opens one `GetHostEvents` loop. A test that counts RPCs fails if escrow and the new poller each poll.
- [x] The gateway's subscribe set is `BLOCK` plus the two warm-key kinds. devshardd's set adds the two escrow kinds, and drops them when `DEVSHARD_HOST_EVENTS_ENABLED=false`. With the flag off, devshardd still polls and still fills `Source`.
- [x] The first request sends `cursor = 0` and `after_height = max(HighWater, floor)`, and later requests send `after_height = 0`.
- [x] After `needs_reset` the next request sends the response's `next_cursor` and `generation` with `after_height = 0`.
- [x] Reset loop: dapi on a fresh generation at T0, with the client's `HighWater` at T0 − 5, causes exactly one `needs_reset`. The client then continues from head, the map's catch-up job fills the missing heights below that `BLOCK`, stores T0 from the `BLOCK`, and makes no second reset request. A production-shaped floor (seed 10 000) never issues `BlockchainInfo` with `from` below the floor, covers only the hole, and the next poll returns within one poll interval even when the hole reader blocks.
- [x] `TestPoller_ResetOnceThenFillsTheGapAtHeight10000`: seed 10 000, 1 s interval, epoch floor 9390, `needs_reset`, then `BLOCK(10 005)`. First `after_height` is 10 000, the hole is 10 001..10 004, nothing below the floor is read, and the follow-up poll sends `after_height = 0`.
- [x] The gateway never calls `LoadMap.Replace`. devshardd with escrow kinds calls it on every successful response. With `DEVSHARD_HOST_EVENTS_ENABLED=false` it does not call it, and a non-empty map stays as it was.
- [x] `Unavailable` does not replace the load map. `deliveredAt` keeps the time of the last successful response.
- [x] The gateway startup log names `NODE_MANAGER_ADDR` as the trusted source of warm-key events and block times, and says dapi is never used when the URL is unset.
- [x] With `NODE_MANAGER_ADDR` unset, seed does not write the map from dapi.
- [x] Comet seed failed and dapi `Latest()` succeeded: the map stores that height as `origin=dapi`, `verified=true`, and the first `after_height` is that height.
- [x] Empty map and `StartAtHead`: the first `after_height` is start-at-head, not `0`.
- [x] devshardd calls `RehydrateOpenEscrows` once before the first poll. Escrow events replayed after it change nothing.
- [x] Replaying a page of escrow and warm-key events twice leaves the same state as applying it once.
- [x] With dapi serving `BLOCK`, a height reaches `Source` from dapi before any header read for that height. `GetBlockHeader` is not called from the poller or from a lookup of a height already in `Source`.
- [x] An old dapi (`supported_kinds` empty) does not stall the map. Comet or a header read fills the height. Warm-key events on that same response are still applied.
- [x] A verifier whose `OfferDAPI` sleeps 2 s. A warm-key event in the same response is applied after the dispatch, no later than the offer plus 10 ms.
- [x] `Unavailable` keeps the cursor and switches blocks to the fallback. When the feed recovers, the poller continues from the same cursor without `needs_reset`, and dapi is the primary writer again.
- [x] A dapi that stays up but sends no `BLOCK` for three block intervals is treated as having no feed until a `BLOCK` arrives.
- [x] `BlockFeedLive` is false after three quiet intervals and true after the next `BLOCK`. `applyBlock` does not store a `BLOCK` while suppressed. Gateway `header_time.block_feed_live` and gauge `devshard_hostevents_block_feed_live` follow that bit, and the log contains the transitions.
- [x] `Unimplemented` does not stop the loop. Escrow falls back to rehydrate and lazy create. Warm keys fall back to `WarmKeyLog`. Blocks fall back to Comet or header reads.
- [x] With `NODE_MANAGER_ADDR` unset, both binaries still fill `Source` from Comet or header reads, and the warm-key copy from `WarmKeyLog`.
- [x] A hole is filled with `/header` or `BlockchainInfo` batches of 20. The backfill test asserts `GetBlockByHeight` and `GetBlockHeader` are not called.
- [x] `needs_reset` triggers the fallback for the gap, then the loop continues on the new generation. Escrow rehydrates. The warm-key copy reloads from `WarmKeyLog` rather than caching the miss as "not a grant".
- [x] A failed `WarmKeyLog` query does not store "not a grant".
- [x] No call path used by signing or by the timeout decision invokes `GetBlockHeader` or blocks on `GetHostEvents`.

---

## Step 7. Commit-signature checks for dapi blocks on the gateway

[↑ Steps](#steps)

### Description

When the gateway has a Comet RPC and Comet seed succeeded, its header times are commit-verified: `gatewayHeaderTimeSource` builds `observer.NewTendermint` with `VerifyCommits: true`. Live dapi `BLOCK` events then go through that verifier. If Comet seed failed, the gateway does not wait on that bar: it trusts the dapi named by `NODE_MANAGER_ADDR`, seeds `GetBlockHeader(0)`, and writes further dapi blocks as verified. `NODE_MANAGER_ADDR` is optional; unset means dapi is never used.

The gateway's `observer.Oracle` becomes the verifier for every block source: dapi `BLOCK` events, the Comet socket, and `Filler` reads. It publishes verified headers into `Source`. Its internal history stays, because it holds the trust chain for back-links, but readers never use it directly.

**Why a catch-up is not a forward check.** The gateway passes the verifier no store, so on every boot `ensureTrusted` takes the latest header from the gateway's own RPC. The trusted block is the tip T. A light client can check commit signatures only forward from a trusted block. Heights below T have nothing to check that way: `verifiedAt` returns `errBelowTrusted` and today's `fillDown` reads `BlockchainInfo` from the gateway's RPC, 20 headers at a time, without looking at dapi's headers. A link can be trusted only from a verified header above it.

So one commit check covers a page only when that page's newest height is at or above the trusted height. That is the live tip. A restart catch-up is below T, and it is verified by walking down from T through the headers dapi already sent.

For each dapi `BLOCK(H)`:

1. **Decode and self-check.** Decode `header` as `cmtproto.Header`. Reject the event unless all of these hold:
   - the header hashes to `hash`
   - `header.Height == height`
   - the header time in milliseconds equals `header_time_unix_ms`
   - `header.ChainID` is the gateway's chain id
   A rejected event is not buffered.
2. **Hold as pending.** Keep every self-checked header from `floor` to the tip. `floor` is the same function [step 5](#step-5-headertimesource-as-the-single-block-map) uses for the map window. A header below the floor is not stored, and headers below a floor that has moved up are dropped. There is no fixed cap of 2000. Until the trusted tip is known, headers are held, and the floor is applied once it is.
3. **Pick the anchor.**
   - If the newest pending height is at or above the trusted height, queue it on the verifier's existing coalescing queue (`runVerifier`). `verifiedAt` checks its commit signatures. A Comet tip on the same height uses that same queue, so dapi does not add a second check.
   - If every pending height is below the trusted tip, the tip is already the anchor. Do not call `verifiedAt` on those heights.
4. **Link down.** Walk down from the anchor. `fillDown` takes the header for a height from this buffer before it calls `BlockchainInfo`. Header H is verified when `header(H+1).LastBlockID.Hash` equals the hash of header H. Each verified header goes to `Source` with `verified = true`.

When the buffer is contiguous from the floor up to T, the whole window is verified in that one downward pass, and `BlockchainInfo` is not called. A height the buffer does not have is the only reason to read the RPC, and only that height is read, through `/header` or one `BlockchainInfo` batch that covers it. Until the buffer connects to the trusted tip, those heights stay pending and a lookup returns `ErrHeaderPending`. After 2 × the block interval (minimum 5 s), `At` fills the hole from the RPC. `SuppressBlocks` and `needs_reset` call `LinkBuffered`, which fills from the trusted header on the verifier goroutine.

**Failures.**
- A self-check failure drops that event.
- A broken back-link, or an anchor whose hash differs from dapi's header at the same height, drops that header and every pending header below the break. The RPC fills the hole. Headers above the break that already link to the anchor stay verified.
- The failure is logged at error and counted in `devshard_hostevents_dapi_blocks_rejected_total`.
- The poller treats dapi as having no live block feed for 5 minutes. Warm-key events from dapi are still applied.

A forged header at H whose `LastBlockID` links to a real H−1 is never verified, because the walk only trusts a header from the one above it.

**Cost.** After a restart, heights below the tip are not usable until the pages that connect them to the tip have arrived. That wait is the catch-up, a few seconds, instead of about one `BlockchainInfo` call per 20 heights of the window. The buffer holds one header per height in the window.

**No Comet seed.** If Comet `Trusted()` fails or there is no Comet RPC, and `NODE_MANAGER_ADDR` is set, the gateway asks dapi for the latest height and treats that header as trusted. The poller then writes further dapi `BLOCK` events as verified. If `NODE_MANAGER_ADDR` is unset, dapi is never used and the map is not filled from it.

devshardd does not run this step. Its dapi is the same host's, and its block sink writes `verified = true` ([step 6](#step-6-one-poller-on-devshardd-and-the-gateway)).

**Where it lives.**
- `observer.Oracle.OfferDAPI` takes one response's `BLOCK` events as a page. It self-checks each header, buffers it as pending, and returns. The newest height at or above the trusted header goes on the verifier queue; heights below it send a coalescing link signal. `linkPending` runs only on the verifier goroutine. `remember` signals that goroutine when it stores a row directly above a pending height. The poller dispatches warm-key and escrow events, then offers the page.
- `SetPublish` writes each verified header into `Source`. A dapi header keeps `source = dapi`, and the rest use `source = chain`.
- `SetPendingFloor` reads `Source.Floor()`.
- `SetOnDAPIFailure` calls `Poller.DAPIBlockRejected`, which counts the failure and suppresses blocks for `hostevents.DAPIBlockPenalty`.
- A lookup of a pending height returns `observer.ErrHeaderPendingLink`, which `Source` reports as `ErrHeaderPending`, until the link budget expires. Then `At` fills through the RPC. `Poller.SuppressBlocks` and `needs_reset` call `Oracle.LinkBuffered`.
- A dapi height above the RPC tip logs "dapi ahead of the RPC" once, at debug.
- On a verifying oracle, `BlockchainInfo` rows are not stored as verified. Only `fillDown` stores them, after each back-link checks out.
- The gateway seeds the map from `Oracle.Trusted` when Comet works, otherwise from dapi `Latest()` when `NODE_MANAGER_ADDR` is set. It hands Comet `NewBlock` headers to `Oracle.Observe`. Hole reads go through the map's filler. The map is optional and may stay empty.

### Test plan

- [x] Catch-up: the trusted header is the tip T, and dapi delivers every height from the floor to T. No `verifiedAt` runs below T, and `BlockchainInfo` is not called. None of those heights is in `Source` before the buffer connects to T. After that one downward pass, all of them are, with `verified = true`. A lookup before the pass returns `ErrHeaderPending`.
- [x] A live page whose newest height is above the trusted height costs one `verifiedAt`. The rest of the page links down from it with no `BlockchainInfo` call.
- [x] Each self-check failure rejects the block, and nothing from it reaches `Source`: a header that does not hash to `hash`, a mismatched height, a mismatched time, a foreign chain id.
- [x] A forged header at H whose `LastBlockID` links to a real H−1 is not verified. A verified anchor at H with a different hash drops dapi's H, logs the conflict, and switches blocks to the fallback for 5 minutes.
- [x] A broken link at H inside the buffer keeps the verified heights above H and drops H and everything pending below it. Only the missing heights are read from the RPC.
- [x] One height missing from an otherwise contiguous buffer causes a read of that height only. Every other height in the window comes from the buffer.
- [x] Entries do not reach `Source` before their anchor is verified. A timeout lookup for a pending height returns `ErrHeaderPending`.
- [x] A Comet tip and a dapi `BLOCK` for the same height cause one commit check, not two.
- [x] The buffer holds the heights from the floor to the tip and nothing below the floor. When the floor moves up, headers below the new floor are dropped.
- [x] With no Comet RPC and `NODE_MANAGER_ADDR` set, dapi `Latest()` is stored as trusted and further dapi `BLOCK` events are written as verified. Unset `NODE_MANAGER_ADDR` never fills the map from dapi.
- [x] devshardd's block sink does not call the verifier.
- [x] `OfferDAPI` returns within 10 ms while `Commit` is held.
- [x] `OfferDAPI` for a page below the trusted header returns at once while `fillDown` holds `fillMu`, and the page links after the fill releases.
- [x] Trusted T = 200 and pages 150..189, with no page above. `At(160)` returns `ErrHeaderPendingLink` before the budget. After the budget it returns the verified header with one `BlockchainInfo` for 190..199, and 150..189 are published from the buffer.
- [x] A page above the RPC tip logs at most one line per height.
- [x] `needs_reset` and `SuppressBlocks` call `LinkBuffered`.
- [x] 100 `At` calls for stored heights publish the trusted header once.
- [x] Gateway `SetOnDAPIFailure(poller.DAPIBlockRejected)` through the poller and a signed-chain Comet RPC: a forged page suppresses blocks and increments `devshard_hostevents_dapi_blocks_rejected_total`.
- [x] One gateway run of poller + verifier + `Source`, with fake `GetHostEvents` and a stub Comet RPC, checks catch-up below T, a live page, and a forged header.
- [x] Gateway: a height published from dapi and then read by the filler keeps `OriginDAPI`. `OracleFiller` reports `OriginDAPI` when the oracle stored that height from a dapi BLOCK.

---

## Step 8. Testenv speaks the feed

[↑ Steps](#steps)

### Description

mock-dapi's `GetHostEvents` is the old server from [step 6](#step-6-one-poller-on-devshardd-and-the-gateway): no `supported_kinds`, no `BLOCK`, `after_height` ignored, escrow-created only. The poller then treats the stack as having no live block feed. This step makes mock-dapi a live feed, and points the gateway at it.

**Address.** devshardd already has `NODE_MANAGER_ADDR` set to mock-dapi. The gateway service sets `DEVSHARD_NODE_MANAGER_ADDR` and not `NODE_MANAGER_ADDR`, so the poller never dials. `gencompose` sets `NODE_MANAGER_ADDR` on `devshardctl` to the same mock-dapi address. `DEVSHARD_NODE_MANAGER_ADDR` stays for the runtime-params client.

**Commit the mock can anchor.** mock-chain blocks are `MakeBlock` with a placeholder validators hash and no commit. [Step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway) bootstraps trust with `VerifyCommitLight`, so those headers never become the gateway's tip. mock-chain signs each block with one testenv validator. `NewBlock`, `/header`, `BlockchainInfo`, and the validators RPC all return that header, that commit, and that validator set. `ValidatorsHash` matches the set.

**`BLOCK` on the ring.** The Comet listener mock-dapi already runs (`runBlockSync`) appends `BLOCK(H)` after each NewBlock, with `BlockPayload` taken from that same header: height, raw hash, time in milliseconds, epoch, and the protobuf header bytes. `supported_kinds` lists `BLOCK`, both warm-key kinds, and both escrow kinds. Maintenance stays off.

**Cursor.** The mock answers the [step 4](#step-4-gethostevents-catch-up-paging-and-stall) table. It keeps `emitted_height` at the last appended `BLOCK`. A page holds at most 1000 events. The ring stays in memory. `POST /testenv/host-events/escrow-created` stays.

**Admin, for the scenarios in [step 9](#step-9-testenv-block-feed-scenarios):**

- `POST /testenv/host-events/stall` makes `GetHostEvents` return `Unavailable` with no body. Clearing it resumes the same generation.
- `POST /testenv/host-events/generation` starts a new generation at the current tip, with no rows below it.
- `POST /testenv/host-events/warm-key` appends a grant or a revoke.
- `POST /testenv/host-events/block` appends one `BLOCK` as given, so a scenario can send a header that does not hash to `hash`.

**What the stack reads.** Gateway `GET /v1/debug/heightsync` includes `header_time`: `high_water`, `tip_height`, `tip_origin`, `tip_verified`, `block_feed_live`. A stored dapi block logs `header_time: applied` with `height`, `origin`, `verified`, plus those map tip fields. Citest already fetches the gateway debug route and already tails compose logs.

### Test plan

- [x] `gencompose` puts `NODE_MANAGER_ADDR` on `devshardctl`, equal to mock-dapi. `DEVSHARD_NODE_MANAGER_ADDR` is unchanged.
- [x] A mock block's commit verifies under `VerifyCommitLight` for the testenv validator. The header bytes in `NewBlock`, `/header`, and `BLOCK` are equal, and the hash matches.
- [x] `GetHostEvents` lists `BLOCK`, both warm-key kinds, and both escrow kinds in `supported_kinds`.
- [x] `cursor = 0` and `after_height` below `emitted_height` starts at that `BLOCK`. A missing row returns `needs_reset` with `next_cursor = head`. `after_height` equal to the emitted height starts after `BLOCK(emitted)`. `after_height` above the emitted height starts at head. `after_height` before the first block returns `Unavailable`. `cursor > 0` ignores `after_height`.
- [x] More than 1000 events returns a page of 1000 and a cursor that continues with no gap.
- [x] Stall returns `Unavailable`. The following call after the stall is cleared returns events in the same generation.
- [x] A generation bump then `after_height` below the tip returns one `needs_reset`. The follow-up at `next_cursor` with `after_height = 0` is not reset.
- [x] Escrow-created, warm-key, and a raw block append wake a waiter. The existing escrow-created route still appends `ESCROW_CREATED`.

---

## Step 9. Testenv block-feed scenarios

[↑ Steps](#steps)

### Description

Stack scenarios, written in [testenv/docs/scenarios.md](../testenv/docs/scenarios.md). They boot the standard stack and use the admin routes from [step 8](#step-8-testenv-speaks-the-feed). Unit coverage of the poller and the verifier stays in [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) and [step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway).

The suite is `make citest-block-feed` (`-run '^TestBlockFeed_'`), its own `citest-*` target so `list-citest-targets` gives it a runner. It does not chat, so it does not share a stack with escrow long-poll warm. `TestEscrowLongPollWarmWithoutInferenceNode` stays in `citest-escrow-longpoll` and must still pass against this mock.

**Live feed.** `TestBlockFeed_LiveHeights`. Advance mock-chain. Gateway `header_time.high_water` reaches that tip, with `tip_origin = dapi` and `tip_verified = true`. versiond-0 and versiond-1 each log `header_time: applied` for that height with `origin = dapi` and `verified = true`.

**Catch-up.** `TestBlockFeed_CatchupAfterRestart`. With the tip already above the floor, restart `devshardctl`. The new process reaches the same tip. Its log shows one request with a non-zero `after_height` and no `needs_reset`.

**Stall.** `TestBlockFeed_StallUsesComet`. Stall the feed, then advance one block. Gateway `tip_origin` is `comet` and `high_water` includes that height. Clear the stall, advance one more block, and `tip_origin` is `dapi` again. Warm-key events posted during the stall are still applied after the next successful poll.

**One reset.** `TestBlockFeed_ResetOnce`. Bump the generation, wait for one `needs_reset`, then advance one block. The gateway log contains one `needs_reset` and then a poll at the returned cursor with `after_height = 0`. A second `needs_reset` fails the test. `high_water` reaches the new tip.

**Warm key.** `TestBlockFeed_WarmKeyFromDapi`. Post a grant. The gateway log records that grantee with no chain warm-key read. Post the revoke. The grant is gone. devshardd logs the same two events.

**Bad header.** `TestBlockFeed_GatewayDropsBadHeader`. Post a `BLOCK` whose header does not hash to `hash`. The gateway `high_water` does not move to that height, and the log contains the self-check failure. devshardd logs `header_time: applied` for that height: its dapi is its own, and it does not run [step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway).

### Test plan

- [x] `testenv/docs/scenarios.md` lists these six scenarios, the admin routes they call, and `make citest-block-feed`.
- [x] `make list-citest-targets` prints `citest-block-feed`.
- [x] `TestBlockFeed_LiveHeights` sees the same tip on the gateway and on both versiond hosts, origin `dapi`, verified.
- [x] `TestBlockFeed_CatchupAfterRestart` sends `after_height` once and does not log `needs_reset`.
- [x] `TestBlockFeed_StallUsesComet` records the stalled height as `comet`, then the next height as `dapi`. A warm-key grant posted while stalled is applied after the feed returns.
- [x] `TestBlockFeed_ResetOnce` logs one `needs_reset` and then catches the new tip.
- [x] `TestBlockFeed_WarmKeyFromDapi` applies a grant and a revoke on the gateway and on devshardd.
- [x] `TestBlockFeed_GatewayDropsBadHeader` leaves the gateway map unchanged and lets devshardd store the height.
- [x] `make citest-escrow-longpoll` still passes.
