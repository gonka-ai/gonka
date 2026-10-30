# One long-poll client for blocks and warm keys

**Status:** Draft / plan
**Related:** [async-header-time-timeout.md](async-header-time-timeout.md), [warm-key-grant-cache.md](warm-key-grant-cache.md), [block-change-feed-concerns.md](block-change-feed-concerns.md) (fixes adopted here), [block-change-feed-plan.md](../block-change-feed-plan.md) (implementation steps), `GetHostEvents` ring, `BlockObserver`, `headertime.Source`, `WarmKeyLogCopy`
**Scope:** One async library on devshardd and the gateway. It long-polls the host-event ring for block changes and warm-key events, writes blocks into the existing `headertime.Source`, catches up from a height through that ring's store, and falls back to the chain when dapi is absent or stalled. dapi fills the ring from one ordered feed loop, one height at a time. `NODE_MANAGER_ADDR` on the gateway is optional and trusted when set. Comet headers are still commit-checked. The header-time map is optional.

This is a plan. It does not change code by itself.

---

## What already exists

There is no client that reads memory, then dapi, then the chain for a block stream.

Two separate clients each do part of it:

| Client | Where it runs | What it does |
|---|---|---|
| `failover.Oracle` | devshardd always (when a NodeManager or chain client exists). Gateway only when `DEVSHARD_GATEWAY_CHAIN_ORACLE` is on. | Synchronous `Latest` / `At`. Order is the Comet tip cache, then dapi `GetBlockHeader` (one height), then chain `GetLatestBlock` / `GetBlockByHeight`. A miss outside the last 100 heights is a dummy header. `Subscribe` on the NodeManager client is refused. |
| `hostevents.Run` / `Poller` | devshardd and the gateway. `DEVSHARD_HOST_EVENTS_ENABLED` only removes the escrow kinds on devshardd. | One async `GetHostEvents` loop for blocks, warm keys, and, on devshardd, escrow. `Unimplemented` keeps the loop and uses the fallbacks. `needs_reset` reloads open escrows when they are subscribed and resets warm-key completeness. The Comet socket applies warm keys whether or not dapi is up. The next read of a granter loads that granter. The first request sends `after_height` once. |

The gateway starts that poller from `initGatewayHeightSync`, including when `NODE_MANAGER_ADDR` is empty. When it has a Comet URL, its live blocks come from `tipcache.StartCometWithHooks`. devshardd's live blocks come from `chainEvents.OnNewBlock` → `ObserveChainHeader`. Both sockets stay. The poller does not open another Comet subscription.

Both binaries have one block map, `headertime.Source`, which maps height → (hash, time in ms, source, verified):
- devshardd builds it with `headertime.New` in `devshardd/session/heightsync.go` and passes that instance to session state and `ObserveChainHeader`.
- The gateway builds it in `gatewayHeaderTimeSource` and passes that instance to `gatewayHeaderTimes`. The map is optional. With a Comet RPC and a successful seed, one `observer.NewTendermint` with `VerifyCommits: true` is the filler and the verifier for the Comet `OnHeader` hook and live dapi `BLOCK` events. If Comet seed failed and `NODE_MANAGER_ADDR` is set, dapi latest is trusted instead.

`Source` does not hold a block oracle. On devshardd the Comet socket writes it, and when a Comet RPC is set the poller installs a header-only filler. On the gateway the verifier writes it. Otherwise a miss still uses `OracleFiller`. That filler reports `dapi` when the oracle stored the height from a dapi BLOCK, and on the gateway a verified dapi origin is not relabeled `chain` for the same hash.

On dapi, the ring is written today from two unordered paths:

- `NewBlock` over the WebSocket, handled by two concurrent workers. That path appends BeginBlock `maintenance_canceled` and EndBlock warm-key expiry. It is live only: a dropped socket or a restart loses those heights.
- `BlockObserver`, which walks heights in order from a persisted last processed height, reads `BlockResults(H)`, and hands each transaction's events to ten concurrent workers. Those workers append escrow, maintenance, and warm-key events. When the walk is more than 500 blocks behind, it jumps forward.

So ring sequence order is not height order, and a dapi restart leaves gaps. The host-event ring is memory only:
- It holds 4096 events, and its generation is a boot nonce, so a restart makes every cursor `needs_reset`.
- `Since` returns every matching event after the cursor in one response, with no page limit.

dapi's chainoracle SQLite table is not this feed. Its `epoch` column is the light-client trust generation, and it prunes by a height window (`top - 131072`). `GetBlockHeader` stays a one-height lookup. The timeout design does not use it to learn `time(height)`.

---

## Fit with the warm-key grant cache

[warm-key-grant-cache.md](warm-key-grant-cache.md) wants one long-poll that wakes on every new block and carries that height. A host could then drop a sticky `false` from `GranteesByMessageType` without subscribing to the chain itself. In that note, dapi re-queries the miss set on every block and rechecks a few accepted entries every 10th block.

This branch already fixed the sticky `false`, by a different mechanism:
- `VerifyWarmKey` no longer reads that boolean cache. It reads `WarmKeyLogCopy`.
- dapi appends `WARM_KEY_GRANTED` / `WARM_KEY_REVOKED` to `GetHostEvents` from the chain events `warm_key_granted` and `warm_key_revoked`, and devshardd applies them to the copy. An old dapi ignores those kinds and still serves escrow events.
- The copy loads `WarmKeyLog` on first use. It reloads a granter at most every 30 seconds, and once per 5 seconds after a miss.
- A query error is not stored as "not a grant". The copy does not write escrow `WarmKeys`.

That covers the grant-cache note's acceptance criteria: a grant that was invisible at H−1 is visible once the feed has H, and a failed query does not stick. It does not implement the note's machinery, and this plan does not add it:

- dapi does not re-query `GranteesByMessageType` for a miss set.
- There is no every-10th-block recheck of accepted booleans. An end is a `WARM_KEY_REVOKED` event, or a later `WarmKeyLog` read.
- The gateway runs the poller without the escrow sink. It applies warm-key events to its bridge's copy. The process's Comet subscription applies as well, including while dapi is up. A gap resets the copy, and the next read of a granter loads that granter.

The piece the grant-cache note still asks for, and this library provides, is the shared wake: block height and warm-key events on the same long-poll, on both binaries, with the Comet subscription as the live fallback when that poll is down.

## Fit with async header time

[async-header-time-timeout.md](async-header-time-timeout.md) needs:
- each item of the long-poll to carry `(height, block hash, header time)`
- the caller to pass the last height it has and receive newer blocks, or wait
- both processes to fill a local map, with the process's own Comet subscription as the fallback when dapi is unreachable
- a lookup to read the map without calling `GetBlockHeader`

The verifier's 60 second wait is a wait for that map.

A `BLOCK` kind on `GetHostEvents` is that poll, and the map is the existing `headertime.Source`. A separate `GetBlockChanges` RPC is not required, and this plan does not add one. The chainoracle SQLite store stays unused for this clock.

The block event reaches clients one `BlockResults` call and one `Header` call after `NewBlock`. On the gateway it also waits for one coalesced commit check. The map stores header time, not arrival time, so that delay does not move any timeout.

Chain reads write the map only when dapi and Comet are both absent, or when they leave a hole. The timeout decision still only reads the map. Refusal, execution, and the 60 second wait stay a later change.

---

## dapi: one ordered feed loop

`HostEventFeed` is a new single goroutine on dapi, and the only writer of the host-event ring. It has its own cursor, `emitted_height`, separate from `BlockObserver`'s `lastProcessedHeight`.

`BlockObserver.updateStatus` wakes both loops. Like the handler walk, the feed runs only while the node is caught up. A small cache of recent `BlockResults`, keyed by height, is shared by the two loops, so in the normal case each height is read from the node once.

The feed is separate from the handler walk so that its failures stay its own:
- a store outage, a slow escrow lookup, or a long catch-up never holds back dapi's handlers
- the handlers' 500-block jump never cuts a hole in the feed

After a restart the feed resumes at `emitted_height + 1`, and the handlers resume at `lastProcessedHeight + 1`.

For each height H the feed:

1. Reads `BlockResults(H)`. `FinalizeBlockEvents` carries BeginBlock `maintenance_canceled` and EndBlock warm-key expiry. `TxsResults` carries escrow, maintenance, and warm-key transaction events.
2. Reads `Header(H)` (Comet `/header`, block meta only). `TmHTTPClient` gains a `Header` method.
3. Resolves slot membership for every escrow id in H, created and settled alike, concurrently with a 2 second budget for the whole height. An id still unresolved at the deadline is appended, matching today's "membership unresolved" branch.
   - `GetEscrow` takes a context, and the feed cancels it at the deadline, so no lookup outlives its height.
   - At most 8 lookups run at once across the feed.
4. Builds the events of H in this order: BeginBlock `FinalizeBlockEvents`, then `TxsResults` in transaction order, then EndBlock `FinalizeBlockEvents`, then `BLOCK(H)` last.
5. Commits those rows, `emitted_height = H`, and `next_seq` to the store in one transaction.
6. Appends the same rows to the memory ring and wakes waiters.

`BLOCK(H)` therefore means "every host event at H is already in the ring and in the store." A client that has applied `BLOCK(X)` has every subscribed event up to X.

`BLOCK(H)` carries:
- `height`
- `hash`, as raw bytes
- `header_time_unix_ms`
- `epoch_index`
- `header`, the protobuf-encoded `cmtproto.Header`

devshardd uses `hash` and the time directly. The gateway verifies `header`, as described below.

**Epoch label.** `epoch_index` comes from H, not from the phase tracker's current value. The feed caches each epoch's `poc_start_block_height`. The current epoch's entry comes from `LatestEpoch`, and older entries come from `EpochGroupData(epoch_index, model_id = "")`. H gets the largest index whose start is at or below H, so a catch-up across several epochs is labeled correctly.

What moves:

- `hostEvents.Append` leaves the handler workers (`escrow_events.go`, `maintenance_events.go`, `warm_key_events.go`) and the `NewBlock` path (`handleMaintenanceLifecycleEvents`, `handleWarmKeyExpiryEvents`). The payload parsing in those files becomes pure functions the feed calls. Handlers keep their other side effects.
- `NewBlock` over the WebSocket stays, as the trigger. It still calls `updateStatus`, `ObserveHex`, and the dispatcher. It no longer writes to the ring.

Rules:

- **The store is written before the ring, and commits are durable.** For each height, the feed commits to the store, then writes a head marker, then appends to the ring. The ring therefore only holds rows that are committed and recorded. A crash must not drop a commit: SQLite runs with `synchronous=FULL` in WAL mode, and every Postgres feed transaction sets `SET LOCAL synchronous_commit = on`.
- **The head marker catches a rolled-back store.** After each commit the feed fsyncs `(writer, generation, seq, height)` to `host_events.head` in the dapi data directory: temp file, rename, directory fsync. A failed marker write stalls the feed like a failed store write.
  - At boot, if the store's head is below the marker, the store lost rows clients may have seen, so dapi starts a new generation. This covers a Postgres failover to an asynchronous standby and a restore from an older backup.
  - A missing marker with a non-empty store also starts a new generation.
  - A marker one height behind the store is normal.

  Without the marker, a restart could rebuild a lost height with different events under sequence numbers a client already holds.
- **A failure stops the feed at H.** A failed `BlockResults`, `Header`, epoch lookup, or store write stops the feed at H. Nothing after H is appended, H is not skipped, and the generation does not change. The handler walk keeps running. The feed retries H with backoff and catches up in order when the read or the store recovers.
- **Only pruning starts a new generation.** The feed does not use the 500-block jump. It starts a new generation at the tip in two cases: the node no longer has `emitted_height + 1` (`EarliestBlockHeight` is above it), or the store is missing or unreadable at boot. A fresh generation starts at the tip with no backfill.
- **Rows carry a writer.** Every store table has a `writer` column set to the participant address. Every append, read, `BLOCK(height)` resolve, load, and prune filters on it. Dapis of different participants can share one database with independent sequences, generations, and retention.
- **One process per writer.** On Postgres, the feed holds a session advisory lock, keyed by a hash of `writer`, on one dedicated connection kept for its lifetime. Reads use the pool at `REPEATABLE READ` so `Load` and `Head` see one commit. If that connection drops, the feed stalls, takes the lock again on a new connection, and reloads its state. Reload continues in the same generation only when the store matches the ring. A height the store already emitted is returned only when that generation and the first stored sequence match the ring; otherwise the feed starts a new generation. A second dapi with the same `writer` cannot take the lock, does not run the feed, and reports it as stalled. On SQLite, writes use a dedicated `sql.Conn` and reads use a pool of 4. The database is a local file and is not shared.

---

## Ring and store

The memory ring grows from 4096 slots to 40960, ten times larger. With full headers that is about 25 MB. It is the hot tail and the live wake. Three epochs of blocks still do not fit in it, so the store is the catch-up window.

`HostEventStore` is the persistence interface. It is opened for one `writer`, and every operation is scoped to it:

- append one height's rows with `emitted_height` and `next_seq`, in one transaction
- read the subscribed kinds after a sequence number, up to a page limit
- resolve `BLOCK(height)` to its sequence number
- prune by epoch
- load the hot tail (the last 40960 rows), the saved generation, `next_seq`, `emitted_height`, and the oldest retained sequence number

Two implementations, SQLite and Postgres. The process picks SQLite or Postgres from `DAPI_HOST_EVENTS_STORE` (empty is SQLite). An unknown backend fails startup.

Every kind is stored: block, warm key, escrow, and maintenance. Rows whose `epoch_index` is older than `current_epoch − 4` are deleted. The extra epoch is a margin for a boundary block. On startup the store's head is checked against the head marker. If it passes, the hot tail is loaded into the memory ring, the generation, `next_seq`, and `emitted_height` are restored, and the feed resumes at `emitted_height + 1`. Heights missed while dapi was down are therefore appended in order, and a client inside the hot tail does not see `needs_reset`.

### `GetHostEvents`

`GetHostEvents` gains `after_height` and `supported_kinds`. `supported_kinds` lists every kind this dapi emits. An old dapi leaves the field empty.

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
| `cursor = 0`, `after_height = 0` | the oldest retained event (today's behavior, kept for old clients) |

Because `after_height` is ignored once there is a cursor, the runner's dispatch retry (`cursor = seq − 1`) keeps working.

**Paging.** A response holds at most 1000 events, from the ring and from the store alike. `next_cursor` is the sequence number of the last returned event when the page is full. Otherwise it is the head captured before the read, so a client subscribed to rare kinds never re-scans a range it has passed. A page may end mid-height. The height is complete when its `BLOCK` event arrives.

**Read path.** The waiter is registered before the read, with the same `longpoll.Wait` as today:

- Start inside the memory ring: answer from the ring. If the ring wraps between `Bounds` and `SincePage`, answer from the store when the generation still matches.
- Start older than the ring, inside the store: answer from the store, up to the captured head. The store holds every row the ring holds, so one store read covers the gap between the store and the ring. Do not wait.
- Caught up: hold until the next appended event or `max_wait_seconds`. Start is resolved once per request. A wake or timeout re-read uses that sequence and ignores `after_height`.

**`needs_reset`** is returned for:
- a generation change
- a cursor above head
- a cursor older than the store
- an `after_height` with no `BLOCK` row, because it was pruned or is below this generation's first height − 1

Every `needs_reset` has no events, the current `generation`, and `next_cursor` = the head captured before the read. The client continues from that cursor with `after_height = 0` and fills what it skipped from the chain. Its next request is inside the window by construction, so a reset cannot repeat.

A client that resent the same `after_height` would loop. That happens for example when a fresh dapi generation starts above the client's high-water mark. So the client sends `after_height` only once per process.

`needs_reset` is no longer what a client sees after a few hours offline, or after a dapi restart.

**`EscrowLoad`.** The snapshot is attached only when the subscribe set includes an escrow kind, so the gateway and non-escrow catch-up pages do not carry it. It is not attached to `Unavailable` errors.

**Stall.** While the feed is stalled (a failing store, or another dapi holds the lock), or `emitted_height` is more than 3 blocks behind the node tip (`NewBlock`, or the sync checker's `LatestBlockHeight` when that is higher):

- A request with committed events after its cursor gets them.
- A request that would otherwise wait gets `codes.Unavailable`. A long-poll that times out still at that cursor does too.
- A failing store read also returns `Unavailable`. A client cancel or deadline on that read returns that status instead.

The client keeps its cursor and generation, and uses its fallbacks until the feed recovers in the same generation. Stopping only the feed, rather than dapi or the ring's content, keeps clients from rebuilding state when the store comes back.

---

## The block map

`headertime.Source` is the only header-time store on both binaries. Each binary creates one at startup and passes it to:
- the header-time readers (`state.WithHeaderTimes`, `gatewayHeaderTimes`)
- the poller
- the Comet socket handlers

The `headertime.New(oracle)` calls go away. `tipcache` and `failover.Oracle` stay as the height-sync tip. They are not a header-time store.

**Writers.**
- The poller's block sink is the primary writer when dapi has a live feed.
- The process's Comet socket also writes, in every state.
- A `Filler` fills holes and misses with header-only reads: Comet `/header`, or `BlockchainInfo` in batches of 20. Each batch has its own deadline (`DefaultFetchTimeout × 4`). The observer RPC client uses `rpchttp.NewWithTimeout` (10 s). `GetBlockByHeight` (whole blocks with transactions) and `GetBlockHeader` are not used.

A miss asks the `Filler` with today's 250 ms wait. `Source` no longer holds a `blocks.BlockOracle`. A dummy header is never stored.

**Entries** record a `source` (dapi, comet, chain) and a `verified` flag.

For a height that is already present:
- Same hash: the entry stays, and its `source` and `verified` are upgraded if the new write ranks higher. On the gateway a verified dapi origin ranks with chain, so a filler read of the same hash does not relabel it.
- Different hash on devshardd: a Comet or chain write replaces a dapi entry, and the replacement is logged and counted. Otherwise the existing entry stays.
- Different hash on the gateway: every entry there is verified, so this means two verified headers disagree. The existing entry stays, and the conflict is logged at error.

**Window.** The map does not keep three epochs. Its floor is the lower of the oldest open stamp's height and `tip − (longest timeout / block interval) − margin`, and entries below the floor are dropped. A hard cap of 200000 entries stays.

Three-epoch retention is dapi's store only.

**Read helpers.**
- `HighWater()` is the highest height with no gap below it, down to the floor.
- `BlockInterval()` is the median of the last 20 header-time gaps, or a configured default until 20 entries exist, when no two heights are consecutive, or when that median is 0.

---

## The library

One poller on devshardd and on the gateway. One cursor, one `GetHostEvents` loop, and a sink per kind, each with its own fallback. Callers read the caches. They do not dial from the sign path or from a timeout check.

```text
1. memory    headertime.Source, and the warm-key log copy
2. dapi      GetHostEvents, when NODE_MANAGER_ADDR is set and the feed is live
3. chain     per kind, below
```

| Kind | Who subscribes | Fallback when dapi has no live feed |
|---|---|---|
| `BLOCK` | both | the process's Comet `NewBlock`, then header-only reads for holes |
| `WARM_KEY_GRANTED`, `WARM_KEY_REVOKED` | both | the process's Comet subscription always applies; `WarmKeyLog` loads one granter on the next read after a gap |
| escrow created / settled | devshardd only | rehydrate open escrows, lazy create |
| maintenance | neither in this library | stays the gateway's own concern, unchanged |

`hostevents.Run` becomes the escrow sink of this poller. It no longer opens its own long-poll, and `Unimplemented` is no longer a loop exit. The warm-key sink moves into the poller, so the gateway and the host apply the same events to the same copy.

On devshardd, `DEVSHARD_HOST_EVENTS_ENABLED` only removes the escrow kinds from the subscribe set. The poller, the block sink, and the warm-key sink run regardless.

`failover.Oracle` stays the synchronous height-sync lookup. This library does not replace it and does not depend on `DEVSHARD_GATEWAY_CHAIN_ORACLE`.

**Cursor.**
- At process start, the poller sends `cursor = 0`, `generation = 0`, and `after_height = max(Source.HighWater(), floor)` when that is positive. On the gateway, an empty map with dapi configured sends start-at-head, never `after_height = 0`. This is the only request that carries `after_height`.
- After a normal response it sends the returned cursor and generation.
- After `needs_reset` it sends the returned `next_cursor` and generation, with `after_height = 0`. It then runs the fallbacks for the skipped range:
  - escrow rehydrate
  - a warm-key catch-up: reset completeness, then one `WarmKeyLog` read for the granter the next check asks for. The same reset runs on Comet connect, on `warmLive` flipping to false, and when a NewBlock is more than one height above the last one seen on that socket. The first NewBlock is not a gap. The Comet subscription keeps applying
  - the `Filler` for block holes between `HighWater()` and the first `BLOCK` that arrives
- Before the first poll, devshardd runs `RehydrateOpenEscrows` once, which covers escrows created before `after_height`.
- Every sink accepts a replay as a no-op: warming a warm escrow, settling a settled one, applying a warm-key event already in the copy.

**Trust in the gateway's dapi.** `NODE_MANAGER_ADDR` is optional. Unset means dapi is never used and the header-time map is not filled from it. When set, that dapi is trusted: a grant makes `VerifyWarmKey` accept the key, and if Comet seed failed the gateway asks dapi for the latest height and stores it as verified. This is documented with the gateway's configuration and logged at startup. Comet headers are still signature-checked. The map is optional: a lookup can fill a missing height on demand.

**`EscrowLoad`.**
- The poller keeps the `LoadMap` hook and replaces the map only when it subscribes to escrow kinds, so the gateway, or devshardd with the flag off, never blanks it.
- `Unavailable` leaves the last snapshot in place. Its `deliveredAt` shows its age.
- No production code wires `LoadMap` today.

**When dapi has no live block feed.** `blockLive` is whether dapi is the primary `BLOCK` writer into `Source`. Comet and the filler always run. The poller reports not-live, and does not consume dapi `BLOCK`s, when any of these is true:

- `NODE_MANAGER_ADDR` is unset, or the RPC is `Unimplemented`
- `supported_kinds` is empty or does not contain `BLOCK` (an old dapi)
- the RPC returns `Unavailable` (a stalled feed)
- the poll succeeds but no `BLOCK` event arrives for three `BlockInterval()` (a syncing node, or a dapi that silently drops the kind)
- on the gateway, a dapi header failed verification within the last 5 minutes (the only case that still delivers `BLOCK`s)

Warm-key and escrow events from a successful poll are still applied. When `BLOCK` events resume and suppress has expired, dapi is the primary writer again. Operators read `block_feed_live` on the gateway debug route and on `devshard_hostevents_block_feed_live`.

**Startup.** The gateway starts the poller from `initGatewayHeightSync`, even when `NODE_MANAGER_ADDR` is empty. An empty address never dials dapi. Seed is Comet `Trusted()` when that works, otherwise dapi `GetBlockHeader(0)`. devshardd starts the poller next to `startHostEventsWarm`, outside that function's early return for a disabled flag.

### Gateway: commit-signature checks on dapi blocks

When Comet seed succeeded, live dapi `BLOCK` events go through the gateway's `observer.Oracle` so they do not lower the Comet verification bar. Comet `NewBlock` and filler reads use the same verifier. If Comet seed failed, this check is skipped and dapi is trusted (see **No Comet seed** below).

For each dapi `BLOCK(H)`:

1. **Self-check.** Decode `header`. The event is rejected, and not buffered, unless all of these hold:
   - the header hashes to `hash`
   - its height is H
   - its time in milliseconds equals `header_time_unix_ms`
   - its chain id is the gateway's chain id
2. **Pending.** Keep every self-checked header from `floor` to the tip. `floor` is the map window from above. A header below the floor is not stored, and headers below a floor that has moved up are dropped. There is no fixed cap. The poller does not offer pages until a trusted tip exists (Comet seed, or dapi latest).
3. **Anchor.** The gateway's verifier has no saved trust point, so on boot the trusted block is the tip from its own RPC. Commit signatures can be checked only forward from that block.
   - A pending height at or above the trusted tip goes on the verifier's coalescing queue, and `verifiedAt` checks its commit. A Comet tip on the same height uses that queue, so dapi does not add a second check.
   - Heights below the tip are not sent to `verifiedAt`. The tip is already their anchor.
4. **Link down.** Walk down from the anchor. `fillDown` takes a height from this buffer before it calls `BlockchainInfo`. Header H is verified when `header(H+1).LastBlockID.Hash` equals the hash of header H.

When the buffer is contiguous from the floor up to the tip, the whole window is verified in that one downward pass, with no `BlockchainInfo` reads. A missing height is the only reason to read the RPC, and only that height is read. Until the buffer connects to the tip, those heights stay pending. A link is trusted only from above: a forged H that links to a real H−1 is never verified.

One commit check covers a page only at the live tip, where the page's newest height is at or above the trusted block. A restart catch-up is below that block.

After a restart, heights below the tip are not usable until the pages that connect them to the tip have arrived. That wait replaces about one `BlockchainInfo` call per 20 heights.

**Failures.** A self-check failure drops that event. A broken link, or an anchor whose hash differs from dapi's header at the same height, drops that header and every pending header below the break. The RPC fills the hole. The failure is logged at error and counted in `devshard_hostevents_dapi_blocks_rejected_total`. The client treats dapi as having no live block feed for 5 minutes, and still applies dapi's warm-key events.

**No Comet seed.** If Comet `Trusted()` fails and `NODE_MANAGER_ADDR` is set, the gateway asks dapi for the latest height and treats it as trusted. If the URL is unset, dapi is never used.

devshardd skips this check. Its dapi is the same host's, so its block sink writes entries as verified.

---

## Work split

[block-change-feed-plan.md](../block-change-feed-plan.md) has the per-step description and test plan.

1. **Wire contract.**
   - `BLOCK` kind and `BlockPayload` with the full header.
   - `after_height` and `supported_kinds`.
   - Ring capacity 40960, page limit 1000.
2. **Ordered feed.**
   - `HostEventFeed` with its own `emitted_height`, a shared `BlockResults` cache, and `Header(H)`.
   - Epoch label from `EpochGroupData` starts.
   - Membership checks with a budget per height, a context on `GetEscrow`, and at most 8 at once.
   - Appends removed from the handlers and the `NewBlock` path.
3. **Persist.**
   - `HostEventStore` (SQLite, Postgres) with a `writer` column, committed durably before the ring.
   - A head marker on local disk, checked at boot.
   - Restore and resume from `emitted_height + 1`.
   - A stall on store failure, one process per writer on a dedicated lock connection, and retention through `current_epoch − 4`.
4. **RPC.**
   - `supported_kinds`, the cursor and `after_height` rule, and pages of 1000 from the ring and the store.
   - `needs_reset` with `next_cursor = head`, and `Unavailable` while stalled.
   - `EscrowLoad` only for escrow subscribers.
5. **Block map.**
   - `headertime.Source` as the single store, with entry source and verified flag, the conflict rule, a `Filler` miss path, the epoch floor (keep current and previous epoch), persisted catch-up, `HighWater`, and `BlockInterval`.
6. **Shared client.**
   - One poller with a sink per kind, and the cursor policy, with `after_height` sent once per process.
   - The `LoadMap` hook, and the trusted-dapi rule for the gateway.
   - The no-feed rules, including `Unavailable`, and idempotent sinks.
   - Run on devshardd, independent of the host-events flag, and on the gateway.
7. **Gateway verification.** Self-check, a pending buffer for the map window, a downward link from the trusted tip through dapi's headers, and one commit check only for a block at or above that tip.
8. **Testenv feed.** mock-dapi serves `BLOCK` and the cursor rule, mock-chain signs a commit the gateway can anchor, and the gateway's `NODE_MANAGER_ADDR` is that mock.
9. **Testenv scenarios.** The block-feed stack scenarios in `testenv/docs/scenarios.md`, suite `citest-block-feed`: live heights, catch-up after restart, stall uses Comet, one reset, warm key from dapi, gateway drops a bad header.
10. **Warm-key client.** One client on both binaries, the same shape as height listening: the process's existing Comet socket always applies, and dapi `GetHostEvents` applies too when a host is set and the feed lists both warm-key kinds. A gap resets the copy. `WarmKeyLog` loads the granter the next check asks for. No second WebSocket and no `block_results` walk on the host or the gateway. devshardd subscribes NewBlock and escrow before the warm-key queries. A refused warm-key `Subscribe` leaves that listener up. A Comet apply logs at debug. The dapi apply stays at info. devshardd parses each NewBlock once and passes that event, including its height, to every handler.

Out of this plan: refusal from `started_at`, execution from `time(confirm stamp)`, and the verifier's 60 second map wait.

---

## Acceptance

**Ordering and restart**
- For every height H, the BeginBlock events of H, then its transactions, then its EndBlock events, all have lower sequence numbers than `BLOCK(H)`, and `BLOCK(H)` comes before every event of H+1.
- The memory ring holds 40960 events and never holds a row that is not in the store.
- A dapi restart restores the ring and appends the heights it missed, in order and in the same generation, however long dapi was down, as long as the node still has those heights. A cursor inside the hot tail does not get `needs_reset`. Re-walked heights do not produce duplicate rows.
- The handlers' 500-block jump does not affect the feed. Only a height the node has pruned starts a new generation.
- A failed store write stops the feed, not dapi's handlers. Clients waiting at head get `Unavailable`, and after the store recovers they continue on the same cursor.
- Five slow escrow membership lookups in one height delay that height by about 2 seconds, not 10. No lookup outlives its height, and at most 8 run at once.
- A store rolled back below the head marker, by a failover or a restore, starts a new generation instead of reusing sequence numbers. A store at or above the marker keeps the generation.
- Two writers on one database see only their own rows, sequences, and generations.
- A client resets at most once for one cause. A fresh dapi generation above the client's high-water mark produces one `needs_reset`, after which the client continues from head.
- `EscrowLoad` reaches only escrow subscribers, and the gateway never replaces its load map.

**Catch-up and paging**
- A client offline for longer than the memory ring, but inside `current_epoch − 4`, receives escrow, maintenance, warm-key, and block events from the store. `needs_reset` is for a generation change, a cursor above head, or a cursor older than the store.
- No response holds more than 1000 events. A client that passes height X with cursor 0, with the tip far above X, receives 1000 events without waiting, and the next call continues on the returned cursor without a gap or a duplicate.
- A client at the tip holds until the next block or warm-key event.

**Block map and fallbacks**
- On each binary, `headertime.Source` is the only header-time store, and the poller writes it before any header read for a height dapi served.
- An old dapi (empty `supported_kinds`, or no `BLOCK` for three block intervals), and a stalled one (`Unavailable`), do not block the block map. Comet or header reads fill it. Warm-key events from that dapi are still applied.
- On devshardd, a Comet or chain hash replaces a different dapi hash for the same height, and the replacement is logged.
- On the gateway, no dapi block reaches the map before it links down from a trusted header. A catch-up below the tip uses dapi's headers for that walk and does not read `BlockchainInfo` for heights dapi supplied. A forged dapi header is rejected, logged, and switches blocks to the fallback.
- With no dapi, devshardd and the gateway still fill the block map from Comet or header reads, and the warm-key copy from the Comet subscription, with `WarmKeyLog` as catch-up. Backfill does not call `GetBlockByHeight`.

**Client**
- devshardd opens one `GetHostEvents` loop, also when `DEVSHARD_HOST_EVENTS_ENABLED` is off. The gateway opens the same loop without the escrow kinds.
- A grant missed by the ring is visible after the chain read. A failed query is not cached as "not a grant". Escrow `WarmKeys` is unchanged.
- Nothing on the sign path, and nothing in the timeout decision, calls `GetBlockHeader` or waits on this RPC.
