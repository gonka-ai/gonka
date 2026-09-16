# Proposal: Dapi block-header long-poll (shared 100k cache)

**Status:** Draft / proposal  
**Related:** chainoracle `BlockOracle.Subscribe` / `tipcache`, NodeManager long-poll (`GetRuntimeConfig`, `GetHostEvents`), PRs [#1622](https://github.com/gonka-ai/gonka/pull/1622) and [#1738](https://github.com/gonka-ai/gonka/pull/1738)  
**Implementation plan:** [`../dapi-block-header-longpoll-plan.md`](../dapi-block-header-longpoll-plan.md) (Phase A = produce only; Phase B = height-sync primary feed)  
**Scope:** One dapi `(height, hash)` cache, fed by the existing Comet `NewBlock` EventListener, reused by HTTP `/block/*`, unary `GetBlockHeader`, and a new unary long-poll. Same catch-up contract on the testenv mock oracle.

This is a design note only; it does not change code by itself.

---

## Problem

Height-sync needs hash-only headers `(Height, Time, ChainID, BlockHash)`.

Today those come from **two independent Comet WebSockets**:

- dapi `EventListener` — epoch, PoC, params, host-event ring
- host `events.Listener` — `ObserveChainHeader` → host `tipcache`

Dapi already exposes unary lookup (`GET /block/:height`, `GetBlockHeader`) after #1622 / #1738. There is **no** NodeManager long-poll that replays a height range and then waits for the next tip.

`BlockOracle.Subscribe(fromHeight)` is the in-process form of that (catch-up, then live). It is not on the wire. `nmclient.Subscribe` errors. Production `tipcache.Subscribe` only emits the **current tip**, not `fromHeight…tip`. The mock observer *does* replay the full range.

`GetRuntimeConfig` and `GetHostEvents` must not carry per-block headers: they wake on params/escrow, use the wrong cursor, and cannot hold 100k heights.

---

## Goals

1. **One Comet ingress on dapi.** EventListener already has `tm.event='NewBlock'`. Feed the header cache from that parse, not a second WS.
2. **One cache** for HTTP `/block/:height`, gRPC `GetBlockHeader` / `ProveBlockPath`, and the new long-poll.
3. **Catch-up + live** over unary long-poll (same `max_wait_seconds` contract as the other NodeManager polls). Not a gRPC stream.
4. **Bounded replay:** retain **100_000** heights; return **at most 1_000** headers per RPC.
5. **Same contract** on mock-dapi / `observer.Mock` so citest and testermint exercise the real cursor math.
6. **Phase B only:** height-sync sees one `BlockOracle`. Dapi long-poll is primary; Comet `NewBlock` and a 10s chain `GetLatestBlock` are fallbacks. Phase A must not change `devshardd` tip wiring.

Non-goals: Strong / `LightBlock` / prove; folding headers into `GetHostEvents` or `GetRuntimeConfig`.

---

## Architecture

```
CometBFT  tm.event='NewBlock'          (live only; no from-height)
    └── dapi EventListener
            ├── ProcessNewBlock (epoch, PoC, params notify)
            ├── host-event ring (escrow / maintenance)
            └── tipcache.Observe(HashOnlyHeader)     ← new hook

tipcache  (last 100_000 heights + current tip)
    ├── HTTP  GET /block/:height           At()
    ├── gRPC  GetBlockHeader               Latest() / At()
    └── gRPC  GetBlockHeaders (new)        At() catch-up + Subscribe wait
```

Do **not** write back into Comet. Comet is the source on dapi. Dapi listens and feeds a local cache. NodeManager clients are the subscribers.

Phase A: host tip stays Comet `ObserveChainHeader`. Phase B: host `tipfeed` implements `BlockOracle` — long-poll primary, Comet then 10s chain poll as fallbacks (see the plan).

---

## Shared cache

Reuse `tipcache.Cache` (or a dapi-local twin with the same API). Raise `HistoryWindow` from **100** to **100_000**.

| Constant | Value | Meaning |
|---|---|---|
| `HistoryWindow` | `100_000` | retained committed heights |
| `oldest` | `max(1, tip − HistoryWindow)` | lowest height still in the map |
| `MaxHeadersPerPoll` | `1_000` | max headers in one long-poll reply |

At tip `H` (when `H > 100_000`), the closed interval is **`[H − 100_000, H]`**.

`Observe(h)` on each EventListener NewBlock: advances `Latest()`, stores `h` in the window, evicts below `oldest`, fans out to in-process `Subscribe` waiters.

`Remember(h)` on unary `At` cache-fill (Comet `/header` miss): does **not** move `Latest()` or freshness.

`Subscribe(fromHeight)` stays in-process: live fan-out of new tips with `height >= fromHeight`. **Catch-up for the wire RPC must use `At`, not `Subscribe`**, because today’s tipcache `Subscribe` only replays the current tip.

Memory: ~100k × (32-byte hash + height + time + chain id) is on the order of 10–15 MiB. Acceptable for dapi.

---

## Long-poll wire

New unary RPC next to `GetBlockHeader` in `common/nodemanager/nodemanager.proto`. Handlers live in `common/chainoracle/blocks/nmrpc` (same package as the unary twins). Live tip is still Comet → `Observe`; this RPC does not become a gRPC stream.

```protobuf
rpc GetBlockHeaders(GetBlockHeadersRequest) returns (GetBlockHeadersResponse);

message GetBlockHeadersRequest {
  // Exclusive cursor: return heights > from_height.
  // 0 = start at oldest retained (or height 1 if the window includes genesis).
  int64 from_height = 1;
  // Same contract as GetRuntimeConfig / GetHostEvents:
  //   0  = immediate (catch-up only, no hold)
  //   >0 = long-poll up to N seconds (server-capped)
  //   <0 = reserved; treat as immediate
  int32 max_wait_seconds = 2;
  // Optional; 0 = MaxHeadersPerPoll (1000). Server clamps to 1000.
  uint32 max_headers = 3;
}

message GetBlockHeadersResponse {
  bool unchanged = 1;
  repeated BlockHeader headers = 2;  // existing hash-only message
  int64 next_from_height = 3;        // last returned height; = from_height if unchanged
  int64 oldest_height = 4;           // cache floor (tip − 100_000, or 1)
  int64 tip_height = 5;
}
```

`BlockHeader` already has `height`, `time_unix_nano`, `chain_id`, `block_hash` (`nmrpc.HeaderToProto`).

`max_wait_seconds` uses `longpoll.ClampMaxWait` (default cap 60s).

---

## Cursor and clamp rules

`from_height` is exclusive (same idea as `GetHostEvents.cursor`: “I already have this”).

Let `tip` be `Latest().Height`, `oldest = max(1, tip − HistoryWindow)`, `limit = min(requested max_headers, 1000)`.

**Clamp below the window.** If `from_height < oldest − 1` (requested start is not in cache and is not the cursor just below `oldest`):

- Effective cursor becomes `oldest − 1`.
- First header returned is `oldest`.
- Example: tip `H`, request `from_height = H − 100_001`. Height `H − 100_001` is not retained. Start at minimal **`H − 100_000`**.

**Catch-up batch.** Return the next at most `limit` heights strictly after the effective cursor, not past `tip`:

```
start = effective_from + 1
end   = min(tip, start + limit - 1)
```

**Example (batch cap).** Tip `H`, client `from_height = H − 2000`, `limit = 1000`:

- Interval **`(H − 2000, H − 1000]`**
- Headers for `H − 1999 … H − 1000` (1000 heights)
- `next_from_height = H − 1000`

Next poll with `from_height = H − 1000` returns `(H − 1000, H]` if `H − 1000 + 1000 >= H`, else another 1000-high slice.

**Caught up.** If `effective_from >= tip`:

- `max_wait <= 0` → `unchanged=true`, `next_from_height = from_height` (or `tip` if clamped), empty `headers`
- `max_wait > 0` → register `Subscribe(tip + 1)` **before** re-reading `Latest()` (no lost wake), then `longpoll.Wait`. On notify, return the new tip (one header, or a small drain up to `limit`). On timeout, `unchanged=true`

**Holes.** If `At(h)` misses inside `[start, end]`, stop the batch before the hole and set `next_from_height` to the last contiguous height. Do not skip a missing height and continue (that would look like a reorg-free chain). Optionally fill once from Comet `/header` + `Remember`, then retry that height.

**Dummy headers** are not stored and not returned.

---

## Server loop (same shape as `GetHostEvents`)

```
effective_from = clamp(from_height, oldest)
ch, _ = oracle.Subscribe(ctx, effective_from+1)   // wake first
batch = AtRange(effective_from+1, min(tip, effective_from+limit))
if len(batch) > 0:
    return headers, next = batch[last].Height
if max_wait <= 0:
    return unchanged
wait(ch, max_wait)
re-read Latest / drain ch; return or unchanged
```

`Subscribe` is the waiter. `At` is catch-up. That is the mock `Subscribe` contract, expressed as unary long-poll.

---

## EventListener hook

In `processEvent` after `parseNewBlockInfo` (today `{Height, Hash}` only):

1. Read `Time` and `ChainID` from the same NewBlock JSON (or `HeaderFromNewBlock` if the typed Comet event is available).
2. `cache.Observe(blocks.HashOnlyHeader(height, time, chainID, blockHash))`.
3. Do this even when `ApplyRuntimeConfigBlockIfChanged` does **not** notify (params unchanged). Every committed block must enter the window.

Do not append these headers to `HostEventRing`.

---

## Mock / testenv oracle

`devshard/chainoracle/blocks/observer.Mock` and mock-dapi must implement the **same** numbers and cursor rules, not “replay everything”.

| Today (mock) | Required |
|---|---|
| Unbounded `history` | Evict below `oldest = tip − 100_000` (same as tipcache) |
| `Subscribe` replays `fromHeight…tip` in one channel | Keep in-process `Subscribe` for tests, but **evict** and **cap replay** to the window |
| mock-dapi `GetBlockHeader` only | Also serve `GetBlockHeaders` via `nmrpc` |

Long-poll on mock-dapi uses the same `from_height` / clamp / 1000-batch math so citest can:

- Advance the mock 2_500 heights, poll `from = tip-2000`, assert 1000 headers ending at `tip-1000`, then poll again to the tip.
- Poll `from = tip-100_001`, assert first header is `oldest` (`tip-100_000`), not a `NotFound`.

`OmitBlockRoutes` (old-dapi stand-in) leaves `GetBlockHeaders` `Unimplemented` so host failover stays testable.

---

## Host client

New loop next to `devshard/hostevents` / `common/runtimeconfig/client`:

```
from := 0   // or last applied height
loop:
  resp = GetBlockHeaders(from, max_wait=30, max_headers=1000)
  apply resp.headers to host tipcache.Observe / Remember
  from = resp.next_from_height
```

Optional: keep host Comet as primary (`failover` already prefers it). Long-poll fills the 100k window and covers dapi-only deployments.

`from = 0` means “oldest retained,” not genesis of the chain.

---

## Why not the existing long-polls

| RPC | Why it cannot carry this |
|---|---|
| `GetRuntimeConfig` | Wakes only when params/epoch change; payload is `RuntimeConfig` |
| `GetHostEvents` | Seq cursor, ~4096 ring, escrow/maintenance kinds; kinds 1–2 reserved for epoch/params, not per-block tips |
| `GetBlockHeader` | Unary one height; no `max_wait`, no range |

---

## Acceptance sketch

- EventListener NewBlock updates the cache; `GET /block/:tip` and `GetBlockHeader(0)` see the same hash.
- Long-poll from `H-2000` at tip `H` returns exactly 1000 headers `(H-2000, H-1000]`.
- Long-poll from `H-100_001` starts at `H-100_000`.
- Second poll after a 1000-batch continues contiguously to the tip (or another 1000).
- Caught-up client with `max_wait > 0` returns the next NewBlock without a lost wake.
- Timeout with no new block returns `unchanged=true` and an unchanged cursor.
- Mock observer evicts below the 100k floor and mock-dapi serves the same RPC.
- Old dapi / `OmitBlockRoutes`: `Unimplemented`; host failover unchanged.

---

## Implementation

Step-by-step, test-covered phases: [`../dapi-block-header-longpoll-plan.md`](../dapi-block-header-longpoll-plan.md).
