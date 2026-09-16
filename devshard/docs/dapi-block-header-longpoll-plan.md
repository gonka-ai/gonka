# Dapi block-header long-poll — implementation plan

**Design:** [`proposals/dapi-block-header-longpoll.md`](./proposals/dapi-block-header-longpoll.md)  
**Spec constants:** `HistoryWindow = 100_000`, `MaxHeadersPerPoll = 1_000`, exclusive `from_height`  
**Status:** Phase A in progress — **A1–A5** done; Phase B not started

Height-sync keeps calling `blocks.BlockOracle` (`Latest` / `At` / `Subscribe` / `Stale`). This plan adds the **wire** catch-up+live path on dapi (Phase A) and then a **composite producer** that hides three backends from the protocol (Phase B).

| Phase | Who uses it | Primary tip | Fallback |
| ----- | ----------- | ----------- | -------- |
| **A** | tests + mock-dapi + production dapi mount only | n/a (devshardd unchanged) | host still Comet → `ObserveChainHeader` |
| **B** | `devshardd` height-sync | dapi `GetBlockHeaders` long-poll | Comet `NewBlock`, then periodic chain `GetLatestBlock` (10s) |

Do not start Phase B until every **LP-A\*** row is green. Each step below is one PR-sized slice: **summary** (what lands) + **test plan** (named scenarios).

---

## 0. Interface height-sync sees

Unchanged:

```go
// common/chainoracle/blocks.BlockOracle
Latest(ctx) (*Header, error)
At(ctx, height) (*Header, error)
Prove(...)  // still Unimplemented (hash-only)
Subscribe(ctx, fromHeight) (<-chan *Header, error)
```

`heightsync.LocalOracleSource.LatestSection` keeps calling `Latest()`. It must not import NodeManager, Comet, or chain clients.

Phase A: existing `failover.Oracle` + host Comet `tipcache` (no long-poll client).

Phase B: new composite (name TBD, e.g. `tipfeed.Feed`) that **implements `BlockOracle`**:

1. **Primary** — dapi long-poll `GetBlockHeaders` → `Observe` into a local `tipcache`
2. **Fallback** — host Comet `tm.event='NewBlock'` → same cache, only while dapi is down / `Unimplemented` / stale
3. **Last resort** — every **10s**, `direct.Oracle` `GetLatestBlock` / `GetBlockByHeight` → `Observe` / `Remember`

`Subscribe` on the composite is the local cache’s subscribe (catch-up via `At` over the 100k window, live via `Observe` fan-out). Callers never see which backend filled the cache.

```
heightsync / host.latestHeader
        └── BlockOracle  (tipfeed.Feed)
                └── local tipcache  (Latest / At / Subscribe)
                        ▲ Observe
                        │
            ┌───────────┼──────────────┐
            │           │              │
     GetBlockHeaders   Comet WS    chain gRPC
     long-poll (1°)   NewBlock (2°)  10s poll (3°)
```

---

## Phase A — produce on dapi and mock; do not consume in devshardd

**Exit criterion:** mock-dapi and (when mounted) production dapi serve `GetBlockHeaders` + shared-cache `GET /block/:height` / `GetBlockHeader`. `devshardd` / `devshardctl` **do not** call the new RPC. `SetHeightSyncFromEnv` and `ObserveChainHeader` stay as they are.

### A1 — Cache window 100k ✅

**Summary:** Raise `tipcache` retention from 100 to **100_000** heights. `oldest = max(1, tip − HistoryWindow)`. `Observe` / `Remember` evict below that floor. Dummy headers are never stored.

**Code:** `common/chainoracle/blocks` (`HistoryWindow`, `OldestHeight`, `MaxHeadersPerPoll`); `devshard/chainoracle/blocks/tipcache`

**Test plan** (`tipcache`):

| ID | Scenario | Status |
| -- | -------- | ------ |
| **LP-A1a** | After `Observe(H)`, `At(H-100_000)` hits and `At(H-100_001)` misses | ✅ |
| **LP-A1b** | Advancing tip evicts the old floor; `At` of the evicted height misses | ✅ |
| **LP-A1c** | `Remember` of a height inside the window does not move `Latest()` or freshness | ✅ |
| **LP-A1d** | Dummy headers are not stored | ✅ |

**Done when:** `GOMODCACHE=… GOCACHE=… go test ./chainoracle/blocks/tipcache/` — **done** (`devshard/`)

### A2 — Wire: proto + `nmrpc.GetBlockHeaders` ✅

**Summary:** Add unary `GetBlockHeaders`. Handler clamps the exclusive cursor, returns at most 1000 headers via `At` catch-up, long-polls via `Subscribe`, and maps with `HeaderToProto`. `from_height < 0` → `InvalidArgument`. Nil oracle → `FailedPrecondition`. Existing NodeManager field numbers stay unchanged.

Cursor math (exclusive `from_height`):

- Tip `H`, `from = H-2000`, limit 1000 → headers `(H-2000, H-1000]`, `next_from_height = H-1000`
- `from = H-100_001` → first height `H-100_000` (oldest)
- `from >= tip` and `max_wait=0` → `unchanged`, empty headers
- `from >= tip` and `max_wait>0` → hold; wake on next `Observe`

**Code:** `common/nodemanager/nodemanager.proto`, `common/chainoracle/blocks/nmrpc`

Wired in A4 (mock-dapi) and A5 (production dapi). `recordingClient` forwards it so `NodeManagerClient` still compiles.

**Test plan** (`nmrpc`):

| ID | Scenario | Status |
| -- | -------- | ------ |
| **LP-A2a** | `from=H-2000` → 1000 headers ending at `H-1000` | ✅ |
| **LP-A2b** | Follow-up `from=H-1000` continues contiguously (no gap, no overlap) | ✅ |
| **LP-A2c** | `from=H-100_001` starts at `oldest` | ✅ |
| **LP-A2d** | `from=0` starts at `oldest` (not height 1 unless the window includes it) | ✅ |
| **LP-A2e** | `max_headers=0` and `>1000` both clamp to 1000 | ✅ |
| **LP-A2f** | Caught up + `max_wait=0` → immediate `unchanged` | ✅ |
| **LP-A2g** | Long-poll wakes on `Observe(tip+1)` before `max_wait` | ✅ |
| **LP-A2h** | Timeout → `unchanged`, cursor unchanged | ✅ |
| **LP-A2i** | Subscribe-before-read: `Observe` between register and `At` is not lost | ✅ |
| **LP-A2j** | Hole in `At` range: batch stops before the hole; `next_from_height` is last contiguous | ✅ |
| **LP-A2k** | Field numbers of existing NodeManager messages unchanged (wire) | ✅ |

**Done when:** proto regenerated; `go test ./common/chainoracle/blocks/nmrpc/` — **done** (`common/`)

### A3 — Mock observer matches the window ✅

**Summary:** `observer.Mock` evicts history below `oldest` like tipcache. In-process `Subscribe` replays only heights still in the window (not unbounded). `At` below `oldest` errors the same way. `AdvanceTo` jumps the tip without filling the gap so tests can hit the 100k floor without signing 100k blocks.

**Code:** `devshard/chainoracle/blocks/observer.Mock`

**Test plan** (`observer`):

| ID | Scenario | Status |
| -- | -------- | ------ |
| **LP-A3a** | Advance past 100k+N; `At(oldest-1)` fails; `Subscribe(oldest)` replays the window then live | ✅ |
| **LP-A3b** | `Subscribe(1)` after eviction does not emit evicted heights | ✅ |
| **LP-A3c** | Existing fan-out / slow-consumer drop tests still pass | ✅ |

**Done when:** `go test ./chainoracle/blocks/observer/` — **done** (`devshard/`)

### A4 — Mock-dapi serves the RPC; host unused ✅

**Summary:** Mock NodeManager `GetBlockHeaders` calls `nmrpc` against the same `blockMock` as `GetBlockHeader` and HTTP `GET /block/:height`. `OmitBlockRoutes` is **Unimplemented** (old-dapi stand-in; not an empty success). Nil oracle remains `FailedPrecondition` from nmrpc. **devshardd does not call the new RPC.**

**Code:** `devshard/testenv/mockdapi`

**Test plan:**

| ID | Scenario | Where | Status |
| -- | -------- | ----- | ------ |
| **LP-A4a** | Live mock-dapi: catch-up `H-2000` and clamp `H-100_001` | `mockdapi` | ✅ |
| **LP-A4b** | HTTP `GET /block/:h` hash equals `GetBlockHeader` and the long-poll header for that `h` | `mockdapi` | ✅ |
| **LP-A4c** | Mock `AdvanceOne` (NewBlock) appears on the next long-poll | `mockdapi` | ✅ |
| **LP-A4d** | `OmitBlockRoutes`: not a silent empty success | `mockdapi` | ✅ |
| **LP-A4e** | Host without the RPC still enables height-sync via Comet | `session/heightsync_test.go` | ✅ |

**Done when:** `go test ./testenv/mockdapi/ ./cmd/devshardd/session/ -run HeightSync` — **done** (`devshard/`)

### A5 — Production dapi: EventListener → cache → all three surfaces ✅

**Summary:** One 100k `tipcache` (in `common/chainoracle/blocks/tipcache`) is the NodeManager `BlockOracle`. After parse, `Observe` **every** NewBlock in `processEvent` (catch-up and params-unchanged included). `GetBlockHeader`, `GET /block/:height`, and `GetBlockHeaders` share that pointer (no second map). Do not append headers to `HostEventRing`. Do not change `devshardd` env or `SetHeightSyncFromEnv`. `DAPI_API__CHAINORACLE_DISABLED=true` skips the cache and both mounts.

**Code:** `decentralized-api` EventListener / nodemanager / public server / `main.go`; cache lives in `common` so dapi does not import `devshard`.

**Test plan:**

| ID | Scenario | Where | Status |
| -- | -------- | ----- | ------ |
| **LP-A5a** | Parse helper returns height, hash, time, chain id | `event_listener` | ✅ |
| **LP-A5b** | Listener `Observe`s; `GetBlockHeader(0)` and HTTP `GET /block/:h` match | dapi `nodemanager` + listener + public | ✅ |
| **LP-A5c** | Params-unchanged NewBlock still `Observe`s (runtime-config `unchanged`, cache tip moves) | dapi unit | ✅ |
| **LP-A5d** | `GetBlockHeaders` long-poll wakes on EventListener `Observe` | dapi `nodemanager` + listener | ✅ |
| **LP-A5e** | `DAPI_API__CHAINORACLE_DISABLED` / nil oracle: RPCs `FailedPrecondition`; `/block` not mounted; `/v1/versions` unchanged | dapi | ✅ |

**Done when:** `go test` under `decentralized-api/nodemanager`, `internal/event_listener`, `internal/server/public`, `apiconfig` — **done**. Testermint optional smoke: `GET /block/<h>` on a live node after A5 lands.

### Phase A freeze

- [x] All **LP-A1**–**LP-A5** green
- [x] `devshardd` still tips from Comet only (`ObserveChainHeader`)
- [x] `rg GetBlockHeaders devshard/cmd/devshardd` is empty (except comments / tests that assert absence)

---

## Phase B — height-sync consumes the feed; Comet and chain are fallbacks

**Exit criterion:** `devshardd` implements `BlockOracle` as `tipfeed.Feed`. Height-sync and host stamps use only that interface. Dapi long-poll is **primary**. Comet WS runs only when the dapi feed is down. Chain `GetLatestBlock` runs on a **10s** ticker only when both dapi and Comet fail.

### B1 — Composite `BlockOracle` (`tipfeed`) ⏳

**Summary:** Local `tipcache` is the only state `Latest` / `At` / `Subscribe` read. A supervisor `Observe`s from (1) dapi long-poll while fresh, (2) Comet `NewBlock` while dapi is `Unimplemented` / transport-error / stale, (3) chain `GetLatestBlock` every **10s** only if both fail. On dapi recovery, stop Comet and resume long-poll from `local.Latest().Height`. Hash conflict at a height: **last-wins from the current active backend** (lock in tests). `Stale()` stays the existing tipcache `staleAfter` (default 10s). `Decide` omit/stale unchanged.

**Code:** new package under `devshard/chainoracle/blocks/tipfeed` (or `common/chainoracle/blocks/tipfeed` if dapi-free)

| Priority | Backend | When it `Observe`s |
| -------- | ------- | ------------------- |
| 1 | `GetBlockHeaders` client (`max_wait>0`, cursor = last applied) | while last success is fresh |
| 2 | Comet `tm.event='NewBlock'` | dapi `Unimplemented` / transport error / `Stale()` |
| 3 | `direct.Oracle.Latest` every **10s** | dapi and Comet both failing |

**Test plan** (`tipfeed`):

| ID | Scenario |
| -- | -------- |
| **LP-B1a** | Dapi up: `Subscribe` / `Latest` move only from long-poll headers; Comet stub not started |
| **LP-B1b** | Dapi `Unimplemented`: Comet `Observe`s; `Latest` matches Comet |
| **LP-B1c** | Dapi transport error → Comet; dapi returns → Comet stopped; next tip from long-poll |
| **LP-B1d** | Dapi + Comet down: after 10s, chain `Latest` `Observe`s |
| **LP-B1e** | Chain ticker does **not** fire while dapi is healthy |
| **LP-B1f** | `Subscribe(from)` catch-up uses local `At` (100k window), then live `Observe` |
| **LP-B1g** | Fake clock: chain interval is 10s ± test slack |
| **LP-B1h** | `Stale()` true after silence; `Decide` omit/stale still holds |

**Done when:** `go test ./chainoracle/blocks/tipfeed/`

### B2 — Long-poll client (nmclient) ⏳

**Summary:** Add `PollHeaders` / runner with the same backoff / `Unimplemented` stop-or-fallback as `runtimeconfig/client`. Map proto → `Header`. Advance `from_height = next_from_height`. Clamp (`oldest_height`) is accepted so the client does not retry a missing floor forever.

**Code:** `common/chainoracle/blocks/nmclient`

**Test plan** (`nmclient`):

| ID | Scenario |
| -- | -------- |
| **LP-B2a** | Two 1000-batches stitch to a contiguous `H-2000…H` |
| **LP-B2b** | `Unimplemented` is surfaced so tipfeed can switch to Comet |
| **LP-B2c** | `unchanged` does not rewind the cursor |
| **LP-B2d** | Clamp response (`oldest_height`) accepted; no infinite retry of the missing floor |

**Done when:** `go test ./common/chainoracle/blocks/nmclient/`

### B3 — Wire `tipfeed` into `devshardd`; Comet becomes fallback ⏳

**Summary:** `SetHeightSyncFromEnv` builds `tipfeed.Feed{ Cache, NM, CometURL, Chain }` instead of `failover.New` + always-on `OnNewBlock(ObserveChainHeader)`. Comet start/stop for the **tip** is owned by tipfeed. Escrow Tx queries on the same `events.Listener` stay. Protocol scheduler unchanged. `devshardctl` courier stays `PeerTipOracleSource` (gateway-as-follower later, out of B3). Unary `GetBlockHeader` is not required for tip motion.

**Code:** `devshard/cmd/devshardd/session/heightsync.go`, `app.go`

**Test plan** (`session/heightsync_test.go`):

| ID | Scenario |
| -- | -------- |
| **LP-B3a** | `SetHeightSyncFromEnv` with NM: `Latest` comes from injected long-poll stub, not Comet |
| **LP-B3b** | NM `Unimplemented`: `Latest` comes from `Observe` of a Comet-like stub |
| **LP-B3c** | No NM, no Comet, chain stub: after 10s (fake clock) `Latest` is chain |
| **LP-B3d** | Existing `TestSetHeightSyncFromEnv_WiresGetBlockHeader` updated: long-poll, not unary `GetBlockHeader`, moves the tip |

**Done when:** `go test ./cmd/devshardd/session/ -run HeightSync`

### B4 — In-process + citest ⏳

**Summary:** Prove the three backends under a real session: mock-dapi long-poll as the only `Observe` source when healthy; Comet when the RPC is `Unimplemented`/stopped; 10s chain poll when both are down; citest survives killing mock-dapi gRPC mid-run.

**Code:** `devshard/testenv/scenarios`, `devshard/testenv/citest`

**Test plan:**

| ID | Scenario |
| -- | -------- |
| **LP-B4a** | In-process e2e: mock-dapi long-poll is the only `Observe` source; Anchors match mock tip |
| **LP-B4b** | Mock-dapi `GetBlockHeaders` `Unimplemented` / stopped: session still Anchors from mock-chain Comet (or mock RPC NewBlock) |
| **LP-B4c** | Both dapi and Comet down: Anchors advance on the 10s chain poll (shorten interval in test via knob) |
| **LP-B4d** | Citest stack: `citest-height-sync` (or sibling) with mock-dapi serving `GetBlockHeaders`; kill mock-dapi gRPC mid-run; height-sync continues |

**Done when:** `go test ./testenv/scenarios/ -run HeightSync` and the named citest.

### B5 — Testermint / live probe (optional, same phase) ⏳

**Summary:** Optional live check that host `Latest` tracks chain via long-poll while dapi is up, then via Comet after NodeManager is stopped. Can reuse / extend `devshard/chainoracle/blocks/direct/live_testermint_probe_test.go`.

**Test plan:**

| ID | Scenario |
| -- | -------- |
| **LP-B5a** | Live testermint: host `Latest` tracks chain while dapi is up (long-poll) |
| **LP-B5b** | Stop dapi NodeManager; host `Latest` still tracks via Comet |

### Phase B freeze

- [ ] All **LP-B1**–**LP-B4** green (B5 if testermint is in CI)
- [ ] Height-sync packages import only `blocks.BlockOracle` for tip motion
- [ ] Comet height-sync subscriber is idle while dapi long-poll is healthy
- [ ] Chain 10s poll idle while dapi or Comet is healthy

---

## What must not change

- `GetRuntimeConfig` / `GetHostEvents` payloads and cursors
- Height-sync cadence, L0–L6, dummy header on `At` miss for L6
- `Prove` remains Unimplemented until Strong
- Host escrow Comet **Tx** subscriptions (`devshard_escrow_*`) — not the tip feed

---

## Suggested PR split

1. A1 + A3 (cache + mock window)
2. A2 (proto + nmrpc)
3. A4 (mock-dapi)
4. A5 (production dapi EventListener)
5. B1 + B2 (tipfeed + nmclient)
6. B3 (devshardd wiring)
7. B4 (e2e / citest)

---

## Catalog legend

⏳ planned · 🚧 in progress · ✅ done

Flip a row when the named test lands. IDs are unique to this plan (`LP-A*`, `LP-B*`) so they do not collide with height-sync `D*` / `H*`.
