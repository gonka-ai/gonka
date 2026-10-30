# Block-change feed: open concerns

**Status:** Adopted into [block-change-feed.md](block-change-feed.md)
**Related:** [block-change-feed.md](block-change-feed.md)

Ring order and the restart gap are closed in the design by one ordered feed loop on dapi (`HostEventFeed`). It writes `BLOCK(H)` last per height, commits each height to the store before the ring, and resumes from its own `emitted_height`. The fixes below are adopted there as well. This file keeps the reasoning. The memory ring is 40960 slots, ten times the current 4096.

A second review changed the fixes for 4 and 7. The notes under each say how. It also added these:
- the feed is a loop separate from the handlers
- a stalled feed answers `Unavailable`
- pages hold at most 1000 events
- `after_height` is read only at cursor 0
- the gateway checks commit signatures on dapi blocks

All of these are in [block-change-feed.md](block-change-feed.md).

---

## 1. An old dapi looks healthy and sends no blocks

An old dapi casts unknown subscribe kinds through (`hostEventKindsFromProto`), and `Since` matches nothing for them. It also drops the unknown `after_height` field. There is no `Unimplemented` and no error. A client that waits for dapi to fail before using Comet keeps an empty block map forever. A dapi whose node is syncing looks the same, because the observer does not walk while the node is behind.

**Proposed fix.** Add `supported_kinds` to `GetHostEventsResponse`. An old dapi leaves it empty. If `BLOCK` is missing, the client treats dapi as having no block feed and writes the map from step 3, while still taking warm-key events from dapi. Also, if no `BLOCK` event arrives for three block intervals while the poll succeeds, the client writes from step 3 until `BLOCK` events resume.

## 2. Blocks shrink the escrow window in the memory ring

One `BLOCK` per height fills 4096 slots in a few hours. Escrow events used to share that window only with maintenance and warm-key events. An escrow consumer that disconnects for longer gets `needs_reset` and a full rehydrate sweep.

**Proposed fix.** The store already keeps every ring row. Keep escrow and maintenance rows for the same three-epoch window, and let `Since` read the store when a cursor is older than the memory ring, for every kind. Escrow and maintenance events are rare, so the extra rows are small. `needs_reset` then only happens outside three epochs or across generations.

## 3. devshardd would run two `GetHostEvents` loops

The escrow loop and the new library would each hold their own cursor and long-poll against the same dapi.

**Proposed fix.** One poller with a sink per kind, each with its own fallback:

- escrow: rehydrate open escrows, lazy create (today's behavior)
- warm key: `WarmKeyLog`
- block: Comet, then header reads

`Unimplemented` becomes a per-sink fallback instead of a loop exit. The gateway subscribes to warm-key and block kinds only. devshardd adds the escrow kinds. `hostevents.Run` becomes that poller's escrow sink.

## 4. Merging sources into the block map

The plan says Comet is not a writer while the long-poll is healthy. That leaves gaps when the client switches source, and it needs a definition of "healthy". "First hash wins" also means a wrong hash from one source blocks the right one from another.

**Proposed fix.** Every source writes. The map dedupes by height and keeps a contiguous high-water mark. A hole below the tip is filled by header reads. On a hash conflict for one height, the process's own Comet or chain read wins over dapi. The conflict is logged, and the dapi entry is replaced.

**Second review.** The map is the existing `headertime.Source`, and the poller is its primary writer. The rule above holds on devshardd. On the gateway, every entry is commit-verified before it is written, so a conflict there is between two verified headers. The existing entry stays and the conflict is logged at error.

## 5. Client start height and retention

The plan keeps three epochs in the client map and does not say where a fresh client starts. Without dapi or Comet, three epochs of backfill is many chain calls. `direct.NewFromChain` uses `GetBlockByHeight`, which returns whole blocks with transactions.

**Proposed fix.** The timeout clock only needs heights from the oldest open stamp up to the tip. Start at the lower of that stamp's height and `tip − (longest timeout / block time) − margin`. Keep that window and drop the rest. Fill holes with header-only reads: Comet `/header`, or `BlockchainInfo` for a range (20 per call). Keep three epochs only on dapi.

## 6. Store backend

The plan stores the ring in dapi's config SQLite. The requirement was Postgres or SQLite depending on the backend. dapi already chooses a backend through a factory for `statsstorage` and `payloadstorage`.

**Proposed fix.** Define a `HostEventStore` interface (append a height's rows with `emitted_height` in one transaction, read from a sequence number, resolve `BLOCK(height)` to a sequence number, prune by epoch, load the hot tail). Implement SQLite and Postgres. Choose through the same factory configuration as `statsstorage`.

## 7. A block's epoch index can be off by one

The phase tracker is updated by the dispatcher on the `NewBlock` path. The observer runs on a different goroutine and may read the tracker before or after it moves, so a block at an epoch boundary can carry the previous epoch.

**Proposed fix.** Label by height, not by the tracker's current value. The epoch state already carries `LatestEpoch.PocStartBlockHeight`. A height at or above it gets `LatestEpoch.EpochIndex`; a height below it gets the previous index. Prune with a margin of one epoch (`current_epoch − 4`), so a mislabeled boundary block is never deleted early.

**Second review.** The `PocStartBlockHeight` rule is only correct within one boundary. A catch-up can cross several epochs. The feed caches each epoch's `poc_start_block_height`, taking older ones from `EpochGroupData(epoch_index, model_id = "")`, and labels H with the largest index whose start is at or below H. The `− 4` margin stays.
