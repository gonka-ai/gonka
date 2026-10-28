# Block-change feed: implementation plan

**Status:** Plan
**Design:** [proposals/block-change-feed.md](proposals/block-change-feed.md)
**Concerns adopted there:** [proposals/block-change-feed-concerns.md](proposals/block-change-feed-concerns.md)

Implements the shared long-poll. One `GetHostEvents` loop on devshardd and the gateway. dapi writes the feed from one ordered loop, one height at a time, with `BLOCK(H)` last. The store is the catch-up window. On both binaries the existing `headertime.Source` is the only block map, and the poller is its primary writer.

Do the steps in order. The feed writer needs the `BLOCK` kind. The store needs the writer's per-height transaction. The RPC read needs the store. The block map needs its writer and filler hooks before the poller binds to it. Gateway verification sits on top of the poller.

Out of this plan: refusal from `started_at`, execution from `time(confirm stamp)`, and the verifier's 60 second wait. Those stay in [async-header-time-timeout.md](proposals/async-header-time-timeout.md). This plan only fills the map those checks will read.

## Steps

Update the status column as a step lands. Each row links to its step, and each step links back here.

| Step | Delivers | Status |
|---|---|---|
| [1. Wire contract and ring capacity](#step-1-wire-contract-and-ring-capacity) | `BLOCK`, `BlockPayload`, `after_height`, `supported_kinds`, ring of 40960, page limit 1000 | Not started |
| [2. One ordered feed writer](#step-2-one-ordered-feed-writer) | `HostEventFeed`, its own `emitted_height`, epoch labels, bounded membership lookups | Not started |
| [3. Persist the feed](#step-3-persist-the-feed) | `HostEventStore` with a `writer` column, durable commits, head marker, one process per writer | Not started |
| [4. `GetHostEvents` catch-up, paging, and stall](#step-4-gethostevents-catch-up-paging-and-stall) | Pages of 1000, the cursor rule, `needs_reset` with `next_cursor = head`, `Unavailable`, `EscrowLoad` for escrow subscribers | Not started |
| [5. `headertime.Source` as the single block map](#step-5-headertimesource-as-the-single-block-map) | One map per binary, source and verified flags, `Filler`, floor window, `HighWater`, `BlockInterval` | Not started |
| [6. One poller on devshardd and the gateway](#step-6-one-poller-on-devshardd-and-the-gateway) | Shared poller, one sink per kind, `after_height` once per process, trusted-dapi rule, `LoadMap` | Not started |
| [7. Commit-signature checks for dapi blocks on the gateway](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway) | Self-check, pending buffer for the map window, link down from the trusted tip through dapi's headers | Not started |

## Key decisions

These came from checking the first design against the code. The design doc now includes them.

- **The feed is its own ordered loop, not part of `BlockObserver`'s handler walk.** It has its own cursor, `emitted_height`. Handlers keep `lastProcessedHeight` and the 500-block jump. A stalled store, a slow escrow lookup, or a long dapi outage then affects only the feed, never dapi's own handlers. The feed resumes at `emitted_height + 1`, and handlers resume at `lastProcessedHeight + 1`.
- **The store is written before the ring, and every commit is durable.** The ring only holds committed rows, and a crash cannot drop a commit, so a restart never serves different events under a sequence number a client already saw. SQLite runs with `synchronous=FULL`, and every Postgres transaction of the feed sets `synchronous_commit = on`.
- **Store rows carry a `writer` column** (the participant address). Every read, write, prune, and lock is scoped to it, so dapis of different participants can share one database.
- **A stalled feed answers `UNAVAILABLE`.** Clients keep their cursor and use their fallbacks until the feed recovers in the same generation.
- **Pages hold at most 1000 events**, from the ring and from the store.
- **`after_height` is read only when `cursor` is 0, and the client sends it only once per process.** A `needs_reset` answer carries `next_cursor = head`, and the client continues from it with `after_height = 0`. A reset can therefore never repeat on the same request.
- **The gateway trusts the dapi it points at for warm-key events.** `NODE_MANAGER_ADDR` on a gateway must name a dapi the gateway operator trusts. Blocks are still signature-checked ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). Warm-key events are applied as received.
- **`EscrowLoad` is attached only for escrow subscribers.** The poller applies it only when it subscribes to escrow kinds.
- **Escrow membership lookups take a context.** The feed cancels them at the per-height deadline and runs at most 8 at a time.
- **`BlockPayload` carries the full Comet header.** Time is in milliseconds and the hash is raw bytes. The gateway verifies every dapi block before it reaches the map: a block at or above the trusted tip by its commit signatures, and a catch-up by linking down from that tip through dapi's headers.
- **The block map is the existing `headertime.Source`.** It is the only header-time store, the poller is its primary writer, and its miss path goes through the poller's filler.
- **Epoch labels come from height.** The feed caches each epoch's `poc_start_block_height` from `EpochGroupData`, so a catch-up across several epochs is labeled correctly.

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

Raise `DefaultHostEventRingCapacity` from 4096 to 40960. With full headers that is about 25 MB of memory. `NewHostEventRing` still takes an explicit capacity so tests can use a small ring.

Add `MaxHostEventsPage = 1000`. Step 4 enforces it.

Regenerate the nodemanager stubs. Do not switch any writer or client in this step. Existing subscribe sets keep working. An old client that does not know `BLOCK` still receives only the kinds it subscribed to.

### Test plan

- [ ] `host_events_wire_test.go` still pins fields 1–4 of the request and 1–7 of the response. New assertions pin `after_height = 5`, `supported_kinds = 8`, `BLOCK = 9`, and the `BlockPayload` field numbers.
- [ ] A `BlockPayload` built from a real Comet header round-trips. `cmtproto.Header` decoded from `header` hashes to `hash`, and its time in milliseconds equals `header_time_unix_ms`.
- [ ] A ring constructed with the default capacity holds 40960 events and drops the oldest on the next append.
- [ ] A ring constructed with a small capacity still wraps at that capacity.
- [ ] `Since` with a subscribe set that does not include `BLOCK` does not return a `BLOCK` event appended to the same ring.
- [ ] An unset `after_height` and an empty `supported_kinds` decode as zero values, matching a peer built before this step.

---

## Step 2. One ordered feed writer

[↑ Steps](#steps)

### Description

Add `HostEventFeed` in `decentralized-api/internal/event_listener`. It becomes the only writer of the host-event ring. Today the ring is filled from two unordered paths:

- the `NewBlock` WebSocket workers (`handleMaintenanceLifecycleEvents`, `handleWarmKeyExpiryEvents`)
- ten concurrent `BlockObserver` handler workers (`escrow_events.go`, `maintenance_events.go`, `warm_key_events.go`)

The feed is a single goroutine with its own cursor, `emitted_height`. `BlockObserver.updateStatus` wakes it through a second notify channel. Like the handler walk, it runs only while `caughtUp` is set. It does not use `lastQueriedBlockHeight`, `lastProcessedBlockHeight`, or the 500-block jump. Those stay with the handlers.

Add `Header(ctx, *int64)` to `TmHTTPClient`. The cosmos RPC client already implements it. Add a small cache of recent `BlockResults` keyed by height, shared by the feed and the handler walk. When the two are at the same height, which is the normal case, the node is read once.

For each height H, the feed:

1. Reads `BlockResults(H)` from the shared cache or the node. `FinalizeBlockEvents` carries BeginBlock `maintenance_canceled` and EndBlock warm-key expiry. `TxsResults` carries escrow, maintenance, and warm-key transaction events.
2. Reads `Header(H)` (Comet `/header`, block meta only).
3. Resolves slot membership for every escrow id in H, created and settled alike, concurrently with a 2 second budget for the whole height. An id still unresolved at the deadline is appended, matching today's "membership unresolved" branch. See "Escrow membership lookups" below.
4. Builds the events of H in this order: `FinalizeBlockEvents` with mode `BeginBlock`, then `TxsResults` in transaction order, then `FinalizeBlockEvents` with mode `EndBlock`, then `BLOCK(H)` last.
5. Appends that list to the memory ring and wakes waiters.
6. Sets `emitted_height = H`.

**Epoch label.** `epoch_index` comes from H, not from the phase tracker's current value. The feed keeps a small cache of epoch index → `poc_start_block_height`. The current epoch's entry comes from `LatestEpoch`, and older entries come from `EpochGroupData(epoch_index, model_id = "")`. H gets the largest index whose start is at or below H. This is correct across any number of epoch boundaries, so a long catch-up is labeled right.

**Escrow membership lookups.** `escrowQuerier.GetEscrow(escrowID)` becomes `GetEscrow(ctx, escrowID)`. `ChainEscrowQuerier` passes that context to the `DevshardEscrow` query instead of `context.Background()`.
- The feed builds one context per height with the 2 second deadline and cancels it once the height's list is built. No lookup outlives its height.
- At most 8 lookups run at once, across the whole feed. An id that cannot start before the deadline counts as unresolved and is appended.
- The handlers' remaining callers pass their own request context.

**Failures.** A failed `BlockResults`, `Header`, or epoch lookup stops the feed at H. The next wake retries H, and nothing after H is appended. The handler walk is not affected.

**Start height and pruned heights.**
- A fresh generation starts at the tip at its first wake. There is no backfill.
- If the node no longer has `emitted_height + 1` (`EarliestBlockHeight` is above it), the feed starts a new generation at the tip. This is the only jump on the feed path, and clients see it as `needs_reset`.

**Handler changes.**
- Payload parsing in the handler files becomes pure functions the feed calls.
- Handlers keep their other side effects and stop calling `hostEvents.Append`. They also stop calling `localHoldsEscrowSlot` for the ring.
- `NewBlock` still calls `updateStatus`, `ObserveHex`, and the dispatcher. It no longer writes the ring.

Persistence lands in [step 3](#step-3-persist-the-feed). In this step a process restart still starts a new generation at the tip.

### Test plan

- [ ] For a fixture height with a BeginBlock cancel, an escrow tx, a maintenance tx, a warm-key tx, and an EndBlock expiry, the appended sequence is: BeginBlock events, transactions in order, EndBlock events, then `BLOCK(H)`.
- [ ] `BLOCK(H)` carries `hash`, `header_time_unix_ms`, and `header` from `Header(H)`.
- [ ] `epoch_index`: a height one below a start is labeled with the earlier epoch, a height at the start with the new one. A catch-up that crosses two boundaries labels each height by the `EpochGroupData` starts. A failed epoch lookup stops the feed at that height.
- [ ] A failed `Header(H)` or `BlockResults(H)` leaves the ring without `BLOCK(H)` and without any event of H+1. The next wake appends H, then H+1. The handler walk keeps advancing during the failure.
- [ ] A height with five escrow events whose membership lookups each take 10 s is emitted within about 2 s, and all five events are appended.
- [ ] After that height is emitted, every lookup goroutine has returned: the stub sees its context canceled, and the goroutine count is back to its baseline.
- [ ] A height with 20 escrow ids never has more than 8 lookups in flight. Ids that could not start before the deadline are appended as unresolved.
- [ ] `ChainEscrowQuerier.GetEscrow` passes its context to `DevshardEscrow`. A canceled context returns at once with an error.
- [ ] Settled escrows go through the same membership check as created ones. An id that resolves to "not ours" is not appended.
- [ ] The feed does not use the 500-block jump. With the handlers jumped and the feed 800 blocks behind, the feed appends every one of those heights in order. Only `EarliestBlockHeight > emitted_height + 1` changes `Generation()`.
- [ ] With the feed and the handlers at the same height, `BlockResults(H)` is read from the node once.
- [ ] The `NewBlock` path and the handler workers no longer call `Append`. A test that drives only those paths appends nothing.
- [ ] Existing handler side effects (escrow state, maintenance state) still run, with handlers alone advancing `lastProcessedBlockHeight`.
- [ ] The feed does not walk while `caughtUp` is false.

---

## Step 3. Persist the feed

[↑ Steps](#steps)

### Description

Add `HostEventStore`. It is the catch-up window behind the 40960-slot memory ring, because three epochs of blocks do not fit in the ring.

Interface. The store is opened for one `writer`, and every operation is scoped to it:

- Append one height's rows together with `emitted_height` and `next_seq`, in one transaction.
- Read the subscribed kinds after a sequence number, up to a page limit.
- Resolve `BLOCK(height)` to its sequence number.
- Prune rows whose `epoch_index` is older than `current_epoch − 4`.
- Load the hot tail (the last 40960 rows), the saved generation, `next_seq`, `emitted_height`, and the oldest retained sequence number.

Two implementations, SQLite and Postgres. The process picks one through the same factory configuration `statsstorage` already uses.

**Schema and writer.** Every table has a `writer` column, set to the participant address. Every statement filters on it.

```text
host_events      (writer, seq, height, kind, epoch_index, observed_at_unix, payload)
                 primary key (writer, seq)
                 index (writer, kind, seq)            -- filtered pages
                 index (writer, height) where kind = BLOCK  -- after_height lookup
host_event_feed  (writer, generation, next_seq, emitted_height)
                 primary key (writer)
```

The in-memory ring keeps no `writer` field: one dapi process serves one writer. Persistence adds it on write and filters by it on load, read, resolve, and prune. Two dapis of different participants on one database therefore have separate sequences, generations, retention, and locks.

**Write order.** The feed from [step 2](#step-2-one-ordered-feed-writer) does these in order for H:
1. Commits H to the store.
2. Writes the head marker (below).
3. Appends the same rows to the memory ring.

The ring therefore only ever holds rows that are committed and recorded in the marker. On restart the hot tail matches exactly what clients were served, so the generation can be kept.

**Durable commits.** A commit the feed has acknowledged must survive a crash or power loss. Otherwise a restart could rebuild a lost height with different events, for example because a membership lookup resolves differently, under sequence numbers a client already holds.
- SQLite: the store is its own database file in the dapi data directory, opened in WAL mode with `PRAGMA synchronous=FULL` on every connection.
- Postgres: every feed transaction runs `SET LOCAL synchronous_commit = on`, whatever the server or role default is.

**Head marker.** Durable commits do not cover a Postgres failover to an asynchronous standby, or a restore from an older backup. Either can roll the store back without dapi noticing. After each commit, the feed writes `(writer, generation, seq, height)` of the last committed row to `host_events.head` in the dapi data directory: write a temp file, fsync it, rename it over the old one, then fsync the directory. A failed marker write is handled like a failed store write: the feed stalls at H, and the ring does not get H.

**Restart.**
1. On startup the store's head for this writer is compared with the marker:
   - Store head at or above the marker: the store has everything that was served. A marker lagging by one height after a crash between commit and marker write is expected.
   - Store head below the marker, a marker for another generation or writer, a missing marker with a non-empty store, or a missing or unreadable store: start a new generation at the tip.
2. If the store passes, the hot tail is loaded into the ring, and the generation, `next_seq`, and `emitted_height` are restored.
3. The feed resumes at `emitted_height + 1` from the node's `BlockResults`. This is correct because the feed has its own cursor, while handlers resume at `lastProcessedHeight + 1` as today.

A pruned next height starts a new generation at the tip, as in [step 2](#step-2-one-ordered-feed-writer).

**Store write failure.** The feed stops at H and enters the stalled state. It does not append to the ring, does not skip H, and does not change the generation. dapi's handlers keep running. The feed retries H with backoff. When the store accepts the write, it catches up from `emitted_height + 1` in the same generation. Step 4 turns the stalled state into `UNAVAILABLE` for clients. If the node prunes `emitted_height + 1` while the feed is stalled, recovery starts a new generation at the tip.

**One process per writer.**
- On Postgres, the feed opens one dedicated connection (`sql.Conn`, not a pooled statement) and takes a session advisory lock on it, keyed by a hash of `writer`. It keeps that connection for its lifetime.
- If the connection drops, the lock is gone. The feed stalls, opens a new connection, takes the lock again, and reloads `host_event_feed` before writing.
- A second dapi with the same `writer` that cannot take the lock does not run the feed and reports it as stalled. A dapi with a different `writer` takes a different lock and is unaffected.
- On SQLite, the database is a local file under the dapi data directory and is not shared.

**Retention.** Every kind is stored: block, warm key, escrow, and maintenance. Prune runs against `current_epoch − 4`. The extra epoch is the margin for a boundary block labeled one epoch early.

`GetHostEvents` does not read the store yet. That is [step 4](#step-4-gethostevents-catch-up-paging-and-stall).

### Test plan

- [ ] SQLite and Postgres: appending H writes its rows, `emitted_height = H`, and `next_seq` together. Inject a failure on the last statement and assert the rows are rolled back.
- [ ] Rows for H are in the store before they are in the ring. A store failure on H leaves the ring without H, and later appends do not happen.
- [ ] After a store failure, the handler walk still advances `lastProcessedBlockHeight`.
- [ ] When the store accepts writes again, the feed appends every missed height in order, in the same generation, with consecutive sequence numbers.
- [ ] A stalled feed whose next height has been pruned by the node recovers with a new generation at the tip.
- [ ] `BLOCK(height)` resolves to the sequence number of that height's block row. A missing height resolves to not found.
- [ ] A read after a sequence number returns only subscribed kinds, in sequence order, and stops at the page limit.
- [ ] Rows with `epoch_index` older than `current_epoch − 4` are deleted. A row at `current_epoch − 4` remains. Escrow and maintenance rows follow the same rule.
- [ ] Load restores the last 40960 rows, the generation, `next_seq`, and `emitted_height`. The feed's next height is `emitted_height + 1`, and the next append uses `next_seq`.
- [ ] After a restart where `lastProcessedHeight < emitted_height`, handlers replay from `lastProcessedHeight + 1`, and the ring gets no duplicate rows.
- [ ] A missing store, or one that cannot be read, starts a new generation and an empty ring.
- [ ] SQLite: every connection of the store reports `PRAGMA synchronous` = FULL (2) and `journal_mode` = wal.
- [ ] Postgres: with the role default set to `synchronous_commit = off`, a feed transaction still runs with `synchronous_commit = on`. Read the setting inside the transaction.
- [ ] Order: a failed marker write leaves H committed in the store, absent from the ring, and the feed stalled. A retry appends H once.
- [ ] Restart with the store head below the marker, which simulates a rolled-back Postgres, starts a new generation. A cursor from the old generation gets `needs_reset`.
- [ ] Restart with the marker one height behind the store (crash between commit and marker write) keeps the generation.
- [ ] Restart with a missing marker and a non-empty store starts a new generation. With an empty store and no marker, the first boot starts a generation normally.
- [ ] Two writers in one database: each sees only its own rows on load, read, `BLOCK(height)` resolve, and prune. Their sequences, generations, and `emitted_height` are independent.
- [ ] Postgres: a second feed with the same `writer` cannot take the lock and reports stalled. A feed with a different `writer` takes its own lock and runs.
- [ ] Postgres: killing the lock connection stalls the feed. It takes the lock again on a new connection, reloads its state, and continues in the same generation.
- [ ] The factory selects SQLite or Postgres the same way `statsstorage` does. An unknown backend fails startup.

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
| `cursor = 0`, `after_height = X > 0`, `X < emitted_height`, `BLOCK(X)` stored | the sequence number of `BLOCK(X)` |
| `cursor = 0`, `after_height = X > 0`, `X < emitted_height`, no `BLOCK(X)` row (pruned, or below this generation's first height) | nothing: `needs_reset` with `next_cursor = head` |
| `cursor = 0`, `after_height = X ≥ emitted_height` | head. The client already has those heights, and new ones arrive live. |
| `cursor = 0`, `after_height = 0` | the oldest retained event. This is today's behavior, kept for old clients. It is paged like any other read. |

Write this rule in the RPC comment. Because `after_height` is ignored once there is a cursor, the runner's dispatch retry (`cursor = seq − 1`) keeps working.

**Paging.** At most `MaxHostEventsPage` (1000) events per response, from the ring and from the store alike. The ring's `Since` gets a limit and finds its start position by sequence offset rather than scanning the whole ring. `next_cursor` is:

- the sequence number of the last returned event, when the page is full
- the head sequence number captured before the read, when it is not full

A filtered client therefore never re-scans a range it has already passed. A page may end mid-height. The height is complete when its `BLOCK` event arrives.

**Read path.** The waiter is registered before the read (`longpoll.Wait`, same as today).

- Start inside the memory ring: answer from the ring.
- Start older than the ring and inside the store: answer from the store, with sequence numbers up to the captured head. The store holds every row the ring holds ([step 3](#step-3-persist-the-feed)), so one store read covers the gap between the store and the ring. Do not wait.
- Caught up: hold until the next appended event or `max_wait_seconds`.

**`needs_reset`** is returned for:
- a generation mismatch
- a cursor above head
- a cursor older than the store's oldest row
- an `after_height` with no `BLOCK` row, because it was pruned or is below this generation's first height

Every `needs_reset` response has no events, the current `generation`, and `next_cursor` = the head captured before the read. A client that continues from that cursor, with `after_height = 0`, is inside the window by construction, so the next request cannot be reset for the same reason. This is what stops a reset loop. A client that sends the same `after_height` again would be reset again, which is why [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) sends it only once per process.

A client inside the hot tail does not see `needs_reset` across a dapi restart, because [step 3](#step-3-persist-the-feed) restored the generation and the tail.

**`EscrowLoad`.** The response carries the `EscrowLoad` snapshot only when the request's subscribe set includes `ESCROW_CREATED` or `ESCROW_SETTLED`. Other requests get an empty field. This includes the gateway's, and every page of a catch-up for a non-escrow subscriber. The field is not attached to `Unavailable` errors.

**Stall.** While the feed is stalled (the store is failing, or another dapi holds the lock), or while `emitted_height` is more than 3 blocks behind the node tip:

- A request with committed events after its cursor gets them, paged as usual.
- A request that would otherwise wait gets `codes.Unavailable` with the stalled height in the message.
- A store read that fails returns `codes.Unavailable`.

Old clients already treat `Unavailable` as a transient error and retry with backoff. New clients keep their cursor and switch on the fallbacks in [step 6](#step-6-one-poller-on-devshardd-and-the-gateway).

### Test plan

- [ ] A response from this dapi lists `BLOCK`, both warm-key kinds, both escrow kinds, and both maintenance kinds in `supported_kinds`.
- [ ] `cursor = 0, after_height = X` with the tip above X+2000 returns 1000 events after `BLOCK(X)` without blocking. The following call with the returned cursor continues without a gap or a duplicate.
- [ ] `cursor > 0` with a non-zero `after_height` reads from the cursor. A rewind to `seq − 1` returns that event again.
- [ ] `cursor = 0, after_height ≥ emitted_height` starts at head and waits.
- [ ] `cursor = 0, after_height = 0` against a full 40960-row ring returns 1000 events and a `next_cursor` that continues the window.
- [ ] No response holds more than 1000 events, whether from the ring, from the store, or across the boundary between them.
- [ ] A client subscribed only to escrow, with 3 escrow rows among 50000 store rows, gets all 3 in one response, and `next_cursor` equals the captured head.
- [ ] A page that ends before `BLOCK(H)` does not include `BLOCK(H)`. The next page starts with the remaining events of H and then `BLOCK(H)`.
- [ ] A cursor inside the restored hot tail, using the restored generation, returns events and `needs_reset = false`.
- [ ] A cursor older than the pruned store, a cursor above head, a generation mismatch, and an `after_height` whose row was pruned each return `needs_reset`, no events, the current generation, and `next_cursor` = head.
- [ ] Fresh generation started at tip T0: `cursor = 0, after_height = T0 − 5` returns `needs_reset` with `next_cursor = head`. A follow-up with that cursor and `after_height = 0` returns events or waits, and is not reset.
- [ ] A request subscribed to an escrow kind gets `EscrowLoad`. A request subscribed only to `BLOCK` and warm-key kinds gets none, on live responses and on store pages alike.
- [ ] A client at the tip holds until an append or `max_wait_seconds`, then returns `unchanged` on timeout. An append during the wait is not lost.
- [ ] Stalled feed: a client behind head still drains committed events, and a client at head gets `Unavailable` instead of waiting. After recovery, the same cursor and generation continue without `needs_reset`.
- [ ] A failing store read returns `Unavailable`, not `needs_reset`.

---

## Step 5. `headertime.Source` as the single block map

[↑ Steps](#steps)

### Description

Both binaries already create `headertime.Source`, a map of height → (hash, time in ms):
- devshardd: `devshardd/session/heightsync.go`, `m.headerTimes = headertime.New(oracle)`
- gateway: `devshardctl/heightsync_env.go`, `gatewayHeaderTimeSource`

Today it is filled only by fetch-on-miss through a block oracle. `Observe` is never called in production code. Make it the only header-time store, with the poller from [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) bound to it as the primary writer.

**Entries.** Each entry gains a `source` (dapi, comet, chain) and a `verified` flag. `Observe(h, source, verified)` replaces `Observe(h)`.

**Conflict rule** for a height that is already present:

- Same hash: keep the entry, and upgrade `source` and `verified` if the new write ranks higher.
- Different hash on devshardd: a comet or chain write replaces a dapi entry, logged at warn and counted in a metric. Otherwise the existing entry stays, and the conflict is logged and counted.
- Different hash on the gateway: every entry there is verified ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)), so a conflict means two verified headers disagree. Keep the existing entry, log at error, and count it.

**Miss path.** `Source` no longer holds a `blocks.BlockOracle`. It takes a `Filler` interface, and a miss asks the filler for that height with today's 250 ms wait. The poller from [step 6](#step-6-one-poller-on-devshardd-and-the-gateway) implements `Filler` with header-only reads. On the gateway those reads are verified ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). Lookups keep returning `ErrHeaderPending` and `ErrHeaderMismatch` as today.

**Window.**
- `Source` takes a floor function. The floor is the lower of the oldest open stamp's height and `tip − (longest timeout / block interval) − margin`.
- Entries below the floor are dropped.
- `DefaultCapacity` (200000) stays as a hard cap.

**Read helpers.**
- `HighWater()` returns the highest height with no gap below it, down to the floor.
- `BlockInterval()` returns the median of the last 20 header-time gaps, or a configured default until 20 entries exist.

**Wiring.** Each binary creates one `Source` at startup and passes it to:
- the header-time readers (`state.WithHeaderTimes`, `gatewayHeaderTimes`)
- the poller ([step 6](#step-6-one-poller-on-devshardd-and-the-gateway))
- the Comet socket handlers

The `headertime.New(oracle)` calls are removed. `tipcache` and `failover.Oracle` stay as the height-sync tip. They are not a header-time store.

### Test plan

- [ ] All header-time readers in both binaries receive the same `Source` instance. A test that builds each binary's wiring finds no second `headertime.New`.
- [ ] `Observe` with the same hash from dapi, then comet, keeps one entry with `source = comet`.
- [ ] devshardd: dapi hash A then comet hash B leaves B and counts one conflict. Comet A then dapi B leaves A and counts one conflict.
- [ ] Gateway: two verified writes with different hashes keep the first and log at error.
- [ ] A miss calls the `Filler` once per height, with concurrent lookups sharing one call. The filler's result is visible to the next lookup. With no filler set, a miss returns `ErrHeaderPending`.
- [ ] A dummy header, a zero time, or an empty hash is never stored.
- [ ] Entries below the floor function's value are dropped. The hard cap still applies when the floor function returns 0.
- [ ] `HighWater()` stops below a gap. Filling the gap moves it up.
- [ ] `BlockInterval()` returns the median of the last 20 gaps, and the configured default before 20 entries exist.

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

**Sinks.** `hostevents.Run` becomes the escrow sink and no longer opens its own long-poll. The warm-key sink moves into the poller, so both binaries apply the same events to the same copy. The block sink writes `Source` with `source = dapi`. On devshardd it marks entries verified, because dapi is the host's own. The gateway goes through [step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway) first.

**Trust in the gateway's dapi.** The gateway applies warm-key events from dapi as received, with no chain check. A grant from dapi makes `VerifyWarmKey` accept that key. `NODE_MANAGER_ADDR` on a gateway must therefore name a dapi the gateway operator trusts: its own, or one run by a party it trusts with authorization data. Document this next to `NODE_MANAGER_ADDR` in the gateway's configuration reference and its startup log line. Blocks are still signature-checked in [step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway), because header times feed timeout decisions that a counterparty can dispute.

**`EscrowLoad`.** The poller keeps the `LoadMap` hook from `hostevents.Config`. It calls `LoadMap.Replace` only when its subscribe set includes the escrow kinds, so a gateway or a devshardd with `DEVSHARD_HOST_EVENTS_ENABLED=false` never blanks the map with an empty field. While dapi answers `Unavailable` the map is not replaced. Readers see how old the snapshot is from `Snapshot()`'s `deliveredAt`. No production code wires `LoadMap` today. This step keeps the hook working and does not add a consumer.

**`DEVSHARD_HOST_EVENTS_ENABLED`.** On devshardd this flag only removes the escrow kinds from the subscribe set. The poller, the block sink, and the warm-key sink run regardless. The poller is started next to `startHostEventsWarm`, outside its early return.

**Cursor policy.** `after_height` is sent once per process, never after a reset:
- **Process start:** the first request sends `cursor = 0`, `generation = 0`, and `after_height = max(Source.HighWater(), floor)`. `floor` comes from [step 5](#step-5-headertimesource-as-the-single-block-map)'s floor function, using the tip from the process's Comet or chain.
- **Normal response:** the next request sends the returned `cursor` and `generation`, with `after_height = 0`.
- **`needs_reset`:** the poller takes `next_cursor` and `generation` from the response, and the next request sends them with `after_height = 0`. Then it runs the fallbacks for what the reset skipped:
  - The escrow sink runs `RehydrateOpenEscrows`.
  - The warm-key copy reloads from `WarmKeyLog` on next use.
  - The `Filler` fills block holes between `Source.HighWater()` and the first `BLOCK` that arrives.

  Resending `after_height` here would hit the same missing row and be reset again, forever. That happens for example on a fresh dapi generation that starts above the client's high-water mark.
- **Old dapi:** it ignores `after_height` and answers the same as for cursor 0.

Before the first poll on devshardd, the escrow sink runs `RehydrateOpenEscrows` once. That covers escrows created before `after_height`.

**Idempotency.** Every sink accepts a replay:
- `WarmEscrow` on a warm escrow is a no-op.
- `OnEscrowSettled` on a settled escrow is a no-op.
- A warm-key event already in the copy is a no-op.

**When dapi has no live block feed.** The poller treats dapi this way, and writes blocks from the fallback, when any of these is true:

- `NODE_MANAGER_ADDR` is unset, or the RPC is `Unimplemented`
- `supported_kinds` is empty or does not contain `BLOCK`
- the RPC returns `Unavailable` ([step 4](#step-4-gethostevents-catch-up-paging-and-stall) stall)
- the poll succeeds but no `BLOCK` event arrives for three `Source.BlockInterval()`

On `Unavailable` the poller keeps its cursor and generation and retries with the existing backoff. Warm-key and escrow events on any successful response are still applied. When `BLOCK` events resume, dapi is the primary writer again. Comet keeps writing in both states.

**Comet sockets.** The two existing Comet sockets stay: the gateway's `tipcache.StartCometWithHooks`, and devshardd's `chainEvents.OnNewBlock` → `ObserveChainHeader`. Each also writes `Source` with `source = comet`. On the gateway those headers go through the verifier first ([step 7](#step-7-commit-signature-checks-for-dapi-blocks-on-the-gateway)). This plan does not open a third subscription.

**Holes.** A hole between the floor and the tip is filled by the `Filler` with header-only reads: Comet `/header`, or `BlockchainInfo` in batches of 20. `GetBlockByHeight` and `GetBlockHeader` are not used.

**Startup.** The gateway starts the poller from `initGatewayHeightSync`, even when `NODE_MANAGER_ADDR` is empty.

### Test plan

- [ ] devshardd opens one `GetHostEvents` loop. A test that counts RPCs fails if escrow and the new poller each poll.
- [ ] The gateway's subscribe set is `BLOCK` plus the two warm-key kinds. devshardd's set adds the two escrow kinds, and drops them when `DEVSHARD_HOST_EVENTS_ENABLED=false`. With the flag off, devshardd still polls and still fills `Source`.
- [ ] The first request sends `cursor = 0` and `after_height = max(HighWater, floor)`, and later requests send `after_height = 0`.
- [ ] After `needs_reset` the next request sends the response's `next_cursor` and `generation` with `after_height = 0`.
- [ ] Reset loop: dapi on a fresh generation at T0, with the client's `HighWater` at T0 − 5, causes exactly one `needs_reset`. The client then continues from head, fills T0 − 4 … T0 with the `Filler`, and makes no second reset request.
- [ ] The gateway never calls `LoadMap.Replace`. devshardd with escrow kinds calls it on every successful response. With `DEVSHARD_HOST_EVENTS_ENABLED=false` it does not call it, and a non-empty map stays as it was.
- [ ] `Unavailable` does not replace the load map. `deliveredAt` keeps the time of the last successful response.
- [ ] The gateway startup log names `NODE_MANAGER_ADDR` as the trusted source of warm-key events.
- [ ] devshardd calls `RehydrateOpenEscrows` once before the first poll. Escrow events replayed after it change nothing.
- [ ] Replaying a page of escrow and warm-key events twice leaves the same state as applying it once.
- [ ] With dapi serving `BLOCK`, a height reaches `Source` from dapi before any header read for that height. `GetBlockHeader` is not called from the poller or from a lookup of a height already in `Source`.
- [ ] An old dapi (`supported_kinds` empty) does not stall the map. Comet or a header read fills the height. Warm-key events on that same response are still applied.
- [ ] `Unavailable` keeps the cursor and switches blocks to the fallback. When the feed recovers, the poller continues from the same cursor without `needs_reset`, and dapi is the primary writer again.
- [ ] A dapi that stays up but sends no `BLOCK` for three block intervals is treated as having no feed until a `BLOCK` arrives.
- [ ] `Unimplemented` does not stop the loop. Escrow falls back to rehydrate and lazy create. Warm keys fall back to `WarmKeyLog`. Blocks fall back to Comet or header reads.
- [ ] With `NODE_MANAGER_ADDR` unset, both binaries still fill `Source` from Comet or header reads, and the warm-key copy from `WarmKeyLog`.
- [ ] A hole is filled with `/header` or `BlockchainInfo` batches of 20. The backfill test asserts `GetBlockByHeight` and `GetBlockHeader` are not called.
- [ ] `needs_reset` triggers the fallback for the gap, then the loop continues on the new generation. Escrow rehydrates. The warm-key copy reloads from `WarmKeyLog` rather than caching the miss as "not a grant".
- [ ] A failed `WarmKeyLog` query does not store "not a grant".
- [ ] No call path used by signing or by the timeout decision invokes `GetBlockHeader` or blocks on `GetHostEvents`.

---

## Step 7. Commit-signature checks for dapi blocks on the gateway

[↑ Steps](#steps)

### Description

When the gateway has a Comet RPC, its header times are commit-verified today: `gatewayHeaderTimeSource` builds `observer.NewTendermint` with `VerifyCommits: true`. A block from dapi must not lower that bar. A dapi block reaches `Source` on the gateway only after it is verified.

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

When the buffer is contiguous from the floor up to T, the whole window is verified in that one downward pass, and `BlockchainInfo` is not called. A height the buffer does not have is the only reason to read the RPC, and only that height is read, through `/header` or one `BlockchainInfo` batch that covers it. Until the buffer connects to the trusted tip, those heights stay pending and a lookup returns `ErrHeaderPending`.

**Failures.**
- A self-check failure drops that event.
- A broken back-link, or an anchor whose hash differs from dapi's header at the same height, drops that header and every pending header below the break. The RPC fills the hole. Headers above the break that already link to the anchor stay verified.
- The failure is logged at error and counted in a metric.
- The poller treats dapi as having no live block feed for 5 minutes. Warm-key events from dapi are still applied.

A forged header at H whose `LastBlockID` links to a real H−1 is never verified, because the walk only trusts a header from the one above it.

**Cost.** After a restart, heights below the tip are not usable until the pages that connect them to the tip have arrived. That wait is the catch-up, a few seconds, instead of about one `BlockchainInfo` call per 20 heights of the window. The buffer holds one header per height in the window.

**No RPC to verify against.** If the gateway has neither a Comet RPC nor a chain RPC, which happens only when `loadHeightSyncProcessState` has no `rpc`, dapi blocks cannot be verified. They are not written, startup logs an error, and header-time lookups stay pending.

devshardd does not run this step. Its dapi is the same host's, and its block sink writes `verified = true` ([step 6](#step-6-one-poller-on-devshardd-and-the-gateway)).

### Test plan

- [ ] Catch-up: the trusted header is the tip T, and dapi delivers every height from the floor to T. No `verifiedAt` runs below T, and `BlockchainInfo` is not called. None of those heights is in `Source` before the buffer connects to T. After that one downward pass, all of them are, with `verified = true`. A lookup before the pass returns `ErrHeaderPending`.
- [ ] A live page whose newest height is above the trusted height costs one `verifiedAt`. The rest of the page links down from it with no `BlockchainInfo` call.
- [ ] Each self-check failure rejects the block, and nothing from it reaches `Source`: a header that does not hash to `hash`, a mismatched height, a mismatched time, a foreign chain id.
- [ ] A forged header at H whose `LastBlockID` links to a real H−1 is not verified. A verified anchor at H with a different hash drops dapi's H, logs the conflict, and switches blocks to the fallback for 5 minutes.
- [ ] A broken link at H inside the buffer keeps the verified heights above H and drops H and everything pending below it. Only the missing heights are read from the RPC.
- [ ] One height missing from an otherwise contiguous buffer causes a read of that height only. Every other height in the window comes from the buffer.
- [ ] Entries do not reach `Source` before their anchor is verified. A timeout lookup for a pending height returns `ErrHeaderPending`.
- [ ] A Comet tip and a dapi `BLOCK` for the same height cause one commit check, not two.
- [ ] The buffer holds the heights from the floor to the tip and nothing below the floor. When the floor moves up, headers below the new floor are dropped.
- [ ] With no Comet RPC and no chain RPC, dapi blocks never reach `Source`, and startup logs an error.
- [ ] devshardd's block sink does not call the verifier.
