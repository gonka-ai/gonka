# devshard testenv v2 — step-by-step plan (audited)

Status: **proposal** for `merge/pixelplex-refactoring-into-r2` (and follow-on PRs).
Last audited against `merge/pixelplex-refactoring-into-r2` + `devshard-testenv`.

This plan **reuses the testenv *idea*** from branch `devshard-testenv` (local protocol
lab, Docker Compose, Go-controlled integration tests) but **rewrites the topology**
to match production: **versiond → devshardd**, **versiond-router**, and the **current
gateway devshardctl**. It **drops** the old testenv height-sync / gossip-anchor /
`host.Host` BlockOracle seam work.

> ⚠️ **Read §0 (Audit findings) first.** The audit changed four things materially vs
> the original draft: (1) the "mock-chain" is **not** a small custom-proto gRPC stub —
> production `devshardd` is hard-wired to the **real Cosmos gRPC + CometBFT RPC** node
> surface; (2) the long-poll **server** is coupled to dapi internals, so the extraction
> is interface-shaped, not a copy; (3) the old `mockdapi` is an **in-process client noop
> with no `GetRuntimeConfig`** — the params/epoch oracle is net-new; (4) the **chain query
> transport is already half-consolidated** in `common/chain` (edge-api + devshardd bridge)
> but devshardd-params and the gateway still use private/REST transports — unify on
> `common/chain` so every consumer (incl. mock-dapi) fetches the *same* way (§0.9, Phase 2b).
> **D1 is decided** (Option A — faithful fake mock-chain; see Phase 3).

Canonical references on `devshard-testenv` (read for patterns, do **not** port
verbatim):

| Doc / dir | Reuse |
|-----------|--------|
| `devshard/docs/testenv.md` | Compose + config model, citest harness, observability |
| `devshard/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md` | Scenario style, fault injection, multi-process E2E |
| `devshard/docs/proposals/TESTENV_PROPOSAL.md` | High-level testenv intent (note: written with `subnet/` naming) |
| `devshard/testenv/config/` | config.yaml model (strip height-sync) |
| `devshard/testenv/citest/`, `scenarios/container/` | Go-controlled Docker harness pattern |
| `devshard/testenv/Dockerfile.dev`, `docker-compose.dev.yml`, `DEVELOPMENT-MODE.md` | Dev overlay: air live-reload + dlv (Phase 4 ✅) |
| `devshard/testenv/cmd/gencompose/` + `config/` key fields | Config-driven compose + deterministic keys (Phase 4 ✅ stub; Phase 6 extends) |

**Ignore** on that branch: `height-sync`, `devshard/heightsync`, `devshardd-testenv`
direct binary, slim devshardctl proxy, `host.BlockOracle` seam, height-sync transport
changes, `heightsyncd`.

---

## 0. Audit findings (what the code actually is, June 2026)

Verified against the merge branch. Each finding has a ✅ (plan was right), ⚠️ (plan
under-specified), or ❌ (plan was wrong) marker.

### 0.1 Chain surface — the crux ❌

The original draft assumed a single **custom-proto gRPC "mock-chain"** ported from
`devshard/testenv/proto/mockchain.proto` + `testenv/bridge/grpc.go`. That is **not**
how production `devshardd` talks to the chain anymore.

Production `devshardd` (`devshard/cmd/devshardd`) uses:

| Caller | Transport | Endpoint | What it needs |
|--------|-----------|----------|----------------|
| `bridge/chain.go` `ChainBridge` | **Cosmos gRPC** | `NODE_GRPC_URL` (`node:9090`) | `inference.Query/{DevshardEscrow, Participant, EpochGroupData, GranteesByMessageType, GetCurrentEpoch}` |
| `chain_events.go` bootstrap | **Cosmos gRPC** | `node:9090` | `cosmos.base.tendermint.v1beta1.Service/GetLatestBlock` (cmtservice) |
| `chain_events.go` `events.Listener` | **CometBFT RPC (websocket)** | `http://node:26657` | `subscribe` for `tm.event='Tx' AND devshard_escrow_created…`, `devshard_escrow_settled…`, and `NewBlock` |
| tx signing (`SubmitDisputeState`) | **Cosmos keyring + gRPC/RPC broadcast** | keyring + node | file keyring, `KEY_NAME`, `CHAIN_ID` |

So `common/chain.Client` (`common/chain/client.go`) wraps the **generated
`inferencetypes.QueryClient`** and `cmtservice`. The old `mockchain.proto` and the old
`testenv/bridge/grpc.go` served the **old injected gRPC `MainnetBridge`** that only
`devshardd-testenv` used — that injection point **does not exist** in production
`devshardd`, which dials URLs. **Conclusion: the old mock-chain is essentially not
reusable.** A v2 mock-chain must speak the real Cosmos query gRPC + cmtservice +
a minimal CometBFT RPC (subscribe/status/block). See Phase 3 (D1 = Option A).

### 0.2 Gateway uses Cosmos REST (LCD), not gRPC ⚠️

`devshard/cmd/devshardctl` (the current gateway) talks to the chain over **REST/LCD**
(`chain_tx_rest.go`, default `http://localhost:1317`) for:

- `MsgCreateDevshardEscrow` / `MsgSettleDevshardEscrow` broadcast (tx),
- account number/sequence lookup, tx polling.

So the gateway needs a **third** chain face (REST + tx broadcast/query) that the
original mock-chain (gRPC-only) does not provide. The escrow it creates must become
visible to `devshardd` over gRPC + emit a CometBFT event — i.e. the mock-chain must
keep a shared escrow store behind all three faces.

### 0.3 Long-poll server is coupled to dapi internals ⚠️

`decentralized-api/nodemanager/server.go` `GetRuntimeConfig` is a clean long-poll loop,
**but** it reads from `apiconfig.ConfigManager` (`RuntimeConfigSnapshot`,
`RuntimeConfigNotifier`, `RuntimeParamsBlockHeight`) and `chainphase.ChainPhaseTracker`.
Extraction to `common/runtimeconfig/server` is therefore **interface extraction**, not a
copy: define `SnapshotSource`, `Notifier`, `EpochSource` ports + a common `Snapshot`
type + `Snapshot → *gen.RuntimeConfig` mapping. dapi adapts its ConfigManager to the
ports; mock-dapi implements them over YAML. The proto already carries all fields
(`common/nodemanager/nodemanager.proto` `RuntimeConfig`).

### 0.4 Client long-poll already exists in `devshard/runtimeconfig` ⚠️

`devshard/runtimeconfig/grpc_runner.go` is a working client long-poll loop wrapped by an
**adaptive provider** (chain fallback, `proto_mapping.go`, `runner_events.go`). devshardd
already consumes it via `NODE_MANAGER_ADDR`. The original plan's step "refactor
devshard/runtimeconfig to import common client" is **high-risk and not required for
testenv** (mock-dapi is a *server*, not a client). Recommend: extract **server** to
common in Phase 1; treat **client** consolidation as Phase 12 follow-up.

### 0.5 Old `mockdapi` is an in-process client noop ❌ (re: reuse)

`devshard/testenv/mockdapi/` on the branch is an **in-process library** linked into
`devshardd-testenv`. It exposes `Oracle` (a blockoracle **client** in host-trust mode
consuming `heightsyncd`) and `NodeManager` (a `NoopNodeManager` implementing the gRPC
**client** interface). It has **no `GetRuntimeConfig`** at all (it predates the proto;
imports the deleted `devshard/mlnode/gen`). For v2 the `mock-dapi` container must be a
real gRPC **server** (`gen.NodeManagerServer`) + HTTP — built on Phase 1, not ported.

### 0.6 devshardd needs a real Cosmos keyring ⚠️

`devshard/cmd/devshardd/config.go` loads a **file keyring** (`KEYRING_BACKEND=file`,
`KEY_NAME`, `KEYRING_DIR=/root/.inference`, optional `ACCOUNT_PUBKEY`). Each devshardd
slot host signs session responses and dispute txs. The testenv must **provision a
keyring per host** (deterministic test keys) and seed matching participant addresses +
host URLs into the mock-chain. Phase **4** `gencompose` pins host/user keys and syncs
mock-chain seed; Phase **6b** materializes Cosmos file keyrings for devshardd. The gateway
uses a hex `TESTENV_PRIVATE_KEY` (filled from `user.private_key_hex` in Phase 4 ✅).

### 0.7 versiond / router env vars ✅

Confirmed in `versioned/internal/config/config.go` and `versiond-router/`:

- `VERSIOND_ORACLE_URL` (**required**), `VERSIOND_BINARY_NAME` (default `devshard`),
  `VERSIOND_OVERRIDE_<v2>` (local binary), `VERSIOND_FORCE`, `VERSIOND_BIN_DIR`,
  `VERSIOND_DATA_DIR`, `VERSIOND_POLL_INTERVAL`. versiond listens on `:8080`.
- versiond-router renders `VERSIOND_HOSTS` + `VERSIOND_PORT`, sticky `hash $sticky_key
  consistent` on escrow id from `/<version>/sessions/<id>/…`.
- versiond polls `VERSIOND_ORACLE_URL` for the **approved-versions list** (which devshard
  versions to spawn). In testenv this oracle can be served by **mock-dapi** (`/versions`)
  or a tiny static stub. Note: this is a *separate* surface from the gRPC
  `GetRuntimeConfig` long-poll; do not conflate them.

### 0.8 Reuse inventory (corrected)

| From `devshard-testenv` | Reuse % | Action |
|-------------------------|--------:|--------|
| `testenv/config/config.go` | ~60% | Port, strip `HeightSyncCfg`, add versiond/router/mock-dapi |
| `testenv/citest/harness/` | ~80% | Port harness (compose up/down, log/metric parse) |
| `testenv/cmd/gencompose/` | ~70% | ✅ **Phase 4 done** — `fillConfig` + keygen + `config.Save`; mock-chain compose stub. **Phase 6:** extend template for versiond×N + router + mock-dapi |
| `testenv/observability/` | ~90% | Port as-is (optional overlay) |
| `Dockerfile.dev`, `docker-compose.dev.yml`, `.air.*.toml`, `DEVELOPMENT-MODE.md` | ~85% | ✅ **Phase 4 done** — dev overlay for mock-chain; service matrix grows in Phases 5–6 |
| `devshard/blockoracle/**` (→ `chainoracle`) | ~90% | Port + **rename** to `devshard/chainoracle`; add params/epoch gRPC surface |
| `testenv/cmd/mockchain` + `proto/` + `bridge/grpc.go` | ~10% | **Do not port**; build Phase 3 mock-chain (real Cosmos surface) |
| `testenv/mockdapi/` | ~5% | Concept only; container is net-new |
| `testenv/engine/` in-process stub engines | 0% | **Drop** — prod devshardd calls a real HTTP ML endpoint; replaced by the `mock-openai` network service (D2) |
| `heightsync/`, `heightsyncd`, `devshardd-testenv` | 0% | Drop |

### 0.9 Chain query transport is already half-consolidated ⚠️ (architectural opportunity)

The brief asks to **merge the chain-fetch transport into `common/` and reuse it at
edge-api, devshardd, and devshardctl** (and let the testenv chain oracle simulate
chain reads through the *same* transport). The audit shows this is already half-done and
fragmented:

| Consumer | Chain **query** transport (today) | Params fetch | On `common/chain`? |
|----------|-----------------------------------|--------------|:--:|
| `edge-api/queryapi` | `common/chain.Client` gRPC (`h.chain.InferenceQueryClient()`) | n/a (read APIs) | ✅ |
| `devshardd` bridge (`bridge/chain.go`) | `common/chain.Client` gRPC | — | ✅ |
| `devshardd` params (`runtimeparams`) | **private** `QueryClientProvider` → `inferencetypes.QueryClient` gRPC | gRPC fetcher (`chain_fetcher_grpc.go`) | ❌ duplicate abstraction |
| `devshardctl` gateway (`runtime_params.go`) | **REST/LCD** (`NewRESTChainFetcher`) | REST fetcher (`chain_fetcher_rest.go`) | ❌ REST-only outlier |
| `dapi` | `decentralized-api/cosmosclient` + `apiconfig.ConfigManager` | publishes from chain | ❌ separate (out of testenv scope) |

So `devshardd` carries **two** chain transports (common/chain for bridge, a private
`QueryClientProvider` for params) and the gateway carries a **third** (REST). The
`runtimeparams.GRPCChainFetcher` and `RESTChainFetcher` both produce the *same*
`runtimeconfig.Snapshot` from the same two queries (`Params`, `EpochInfo`).

**Recommendation (folded into Phase 2b):** make `common/chain.Client` the single chain
**query** transport and add a `common/runtimeconfig` **gRPC chain fetcher** (move
`GRPCChainFetcher` logic, build it on `common/chain.InferenceClient`). Then:

- `devshardd` params uses `common/chain` (drop the private `QueryClientProvider`);
- `devshardctl` gains a gRPC query path (keep REST only for **tx broadcast** of
  create/settle — queries move to gRPC), so it fetches params the same way;
- `mock-dapi`'s params/epoch oracle **reads from mock-chain via `common/chain`** (exactly
  "simulate the requests to mock-chain using the same transport"), instead of inventing a
  YAML-only snapshot truth;
- a **single mock-chain gRPC face** then satisfies edge-api, devshardd, the
  gateway's queries, *and* mock-dapi — shrinking D1's surface (see §11 D1 note).

This aligns with the existing `merge-plan.md` follow-up "Cosmos chain clients → `common/`".
The dapi/cosmosclient consolidation stays **out of scope** (tx + publisher); only the
**query/params read transport** is unified here.

---

## 1. Goals and non-goals

### Goals

1. **Production-shaped runtime:** `versiond` spawns the **real `devshardd`** binary;
   multiple versiond instances behind **`versiond-router`** (sticky on escrow/session id).
2. **No live chain / no real dapi / no real ML:** a **mock-chain** serving the *real*
   Cosmos query gRPC + cmtservice + minimal CometBFT RPC (+ LCD REST for the gateway);
   **`mock-dapi`** replacing dapi for the NodeManager surfaces; **`mock-openai`** as the
   OpenAI-compatible ML endpoint `AcquireMLNode` hands out (D2).
3. **Current gateway:** the **`devshardctl` gateway variant** (not the slim testenv proxy).
4. **Params + epoch oracle via long-poll:** `mock-dapi` serves `GetRuntimeConfig`
   long-poll (params + epoch) using a **shared `common/runtimeconfig/server`** core, so the
   same code backs dapi today and edge-api later.
5. **Chain oracle (`devshard/chainoracle`):** port the old `devshard/blockoracle` block-header
   code **renamed and extended** into a unified **chain oracle** module that exposes **both**
   authenticated block headers (HTTP/SSE) **and** params + epoch (`GetRuntimeConfig`
   long-poll via `common/runtimeconfig/server`). The testenv `mock-dapi` container mounts
   `chainoracle` — not two separate oracles.
6. **Go-controlled Docker tests:** port the citest / container scenario pattern
   (`go test -tags=testenvci`, compose up/down from tests) minus height-sync scenarios.
7. **Dev overlay (live reload):** ✅ **DONE (Phase 4)** — `Dockerfile.dev` + `docker-compose.dev.yml` +
   `air` configs from `devshard-testenv`; mock-chain rebuilds on save without rebaking images.
   Phase 5–6 extend the overlay to mock-dapi / versiond services.
8. **One chain query transport:** `common/chain` is the single gRPC chain-read transport
   for edge-api, devshardd (bridge + params), and devshardctl (queries); `mock-dapi`
   reads mock-chain through it too (§0.9, Phase 2b).

### Non-goals

1. **Height-sync protocol** — no `heightsync`, no anchor gossip, no
   `ForceHeightSyncAnchor`, no `host.LatestHeight`/BlockOracle on `Host`.
2. **In-process mock-dapi inside devshardd** — devshardd stays the production binary;
   dapi-facing deps are **network services**.
3. **Embedded `devshardd-testenv`** — versiond supervises the prod `devshardd`.
4. **Full Testermint / a real `inferenced` validator set** — out of scope (D1 = faithful
   fake mock-chain, not a live node).
5. **Replacing the gateway** with the testenv slim proxy.

---

## 2. Target architecture (corrected)

```text
┌────────────────────────────────────────────────────────────────────────────┐
│ mock-chain  (one process, shared escrow/participant store, 3 faces)         │
│   :9090  Cosmos gRPC   inference.Query/* + cmtservice/GetLatestBlock        │
│   :26657 CometBFT RPC  /status /block + websocket subscribe (Tx, NewBlock)  │
│   :1317  Cosmos LCD    tx broadcast/query for gateway (Create/Settle escrow)│
└───┬───────────────────────────────┬───────────────────────────────┬────────┘
    │ gRPC + RPC (events)            │ gRPC queries                  │ REST tx
    ▼                                ▼                               ▼
┌─────────────────────────┐   (escrow/host/epoch)          ┌──────────────────┐
│ mock-dapi  (×1)         │                                │ devshardctl      │
│  mounts chainoracle:    │                                │ (gateway)        │
│  :9400 params gRPC LP   │◄── long-poll ── devshardd      │ OpenAI proxy,    │
│  :9100 blocks HTTP/SSE  │                                │ capacity, store  │
│  /versions /testenv/*   │                                └────────┬─────────┘
│  AcquireMLNode→mock-oai │                                         │ /devshard/<v>/…
└─────────────────────────┘                                         ▼
                                                            ┌──────────────────┐
                                                            │ versiond-router  │
                                                            │  :8080 sticky    │
                                                            └───┬───┬───┬──────┘
                                                                ▼   ▼   ▼
                                                        versiond-0/1/2  (:8080)
                                                          │   each spawns
                                                          └─ devshardd (prod) child
                                                             SQLite/PG, /devshard/<v>/sessions/…
                                                             → mock-chain (gRPC+RPC)
                                                             → mock-dapi (NODE_MANAGER_ADDR)
                                                             → mock-openai /v1/chat/completions
┌─────────────────────────┐
│ mock-openai  (×1)       │  OpenAI-compatible: POST /v1/chat/completions (+ SSE)
│  :8088 chat completions │  deterministic replies + latency/chunk-drop/5xx knobs (D2)
└─────────────────────────┘  (devshardd execute + validate both call this URL)
```

**Traffic / dependency paths:**

| Path | Wiring |
|------|--------|
| User chat | `devshardctl` → host session URLs (from mock-chain escrow slots) → `versiond-router` → `versiond-N` → `devshardd` |
| Escrow create/settle | `devshardctl` → mock-chain **LCD :1317** (tx) → escrow store → CometBFT **event** |
| devshardd escrow/epoch queries | `devshardd` `common/chain` → mock-chain **gRPC :9090** |
| devshardd chain events | `devshardd` `events.Listener` → mock-chain **RPC :26657** (ws) |
| Params + epoch (long-poll) | `devshardd` → `NODE_MANAGER_ADDR=mock-dapi:9400` → **chainoracle params** `GetRuntimeConfig` |
| chainoracle params source | `mock-dapi` **chainoracle** → `common/chain` → mock-chain **gRPC :9090** (`Params`/`EpochInfo`) |
| ML inference (D2) | `devshardd` → `AcquireMLNode` (mock-dapi) → `mock-openai` URL → `POST /v1/chat/completions` |
| Block proofs | proof consumers → mock-dapi `:9100` **chainoracle blocks** HTTP/SSE |
| versiond version list | `versiond` → `VERSIOND_ORACLE_URL=http://mock-dapi:9100/versions` |

**Single chain query transport (§0.9):** every chain *read* arrow above (devshardd
bridge + params, gateway queries, mock-dapi params) goes through `common/chain.Client`
gRPC to mock-chain `:9090`. Only **tx broadcast** (gateway create/settle) uses LCD REST.

---

## 3. What to port vs rewrite (corrected from §0.8)

| From old testenv | Action | Note |
|------------------|--------|------|
| `testenv/config/` + `config.yaml` | **Port + trim** | drop `HeightSyncCfg`; add `versiond`, `router`, `mock_dapi`, `keyring` |
| `testenv/citest/` + `scenarios/container/` | **Port harness, rewrite scenarios** | drop all height-sync cases |
| `testenv/observability/` | **Port** | optional overlay |
| `Dockerfile.dev`, `docker-compose.dev.yml`, `.air.*.toml`, `DEVELOPMENT-MODE.md` | **Port + adapt** ✅ Phase 4 done | mock-chain overlay live; grows in P5–P6 |
| `testenv/cmd/gencompose/` | **Port stub (P4) ✅ + extend (P6)** | P4 done: keys + mock-chain compose; P6: versiond×N + router + mock-dapi + gateway |
| `testenv/cmd/mockchain`, `proto/`, `bridge/grpc.go` | **Do not port** | replaced by Phase 3 mock-chain (3a/3b/3c) |
| `testenv/mockdapi/` | **Do not port** | container is net-new (built on Phase 1) |
| `cmd/devshardd-testenv/`, `cmd/heightsyncd/` | **Drop** | |
| `devshard/heightsync/`, `host` BlockOracle, height-sync transport | **Do not port** | |
| `devshard/blockoracle/` (→ **`chainoracle`**) | **Port + rename + extend** | blocks HTTP/SSE + params gRPC; see Phase 2 |
| Old slim `devshardctl` | **Do not port** | use `cmd/devshardctl` gateway |

---

## 4. New module layout

```text
common/
  chain/                    # EXISTING transport — extend to be the single chain-read client
    client.go               #   (already used by edge-api + devshardd bridge)
  runtimeconfig/            # NEW — long-poll SERVER core (extracted from dapi nodemanager)
    server.go               # generic long-poll loop over ports (SnapshotSource/Notifier/EpochSource)
    snapshot.go             # common Snapshot type + Snapshot↔*gen.RuntimeConfig mapping
    maxwait.go              # clampMaxWait (moved from dapi runtime_config_max_wait.go)
    chain_fetcher.go        # NEW (Phase 2b) — gRPC Params/EpochInfo → Snapshot, built on common/chain
    server_test.go          # ported backcompat + long-poll cases
    # (client extraction is Phase 12, not here)

devshard/
  chainoracle/              # PORT+RENAME from devshard-testenv/blockoracle; unified chain oracle
    blocks/                 #   authenticated headers: observer, server, client, verifier (old blockoracle)
    params/                 #   GetRuntimeConfig gRPC server wiring (uses common/runtimeconfig)
    standalone/             #   test/dev helpers
  testenv/
    cmd/
      mockchain/            # mock-chain lib: store + grpc + rpc + rest + seed (Phase 3)
      mockdapi/             # NEW container — mounts chainoracle (params gRPC + blocks HTTP) + /versions + /testenv
      mockopenai/           # NEW container (D2) — OpenAI-compatible /v1/chat/completions + fault knobs
      gencompose/           # ✅ Phase 4 stub (keys + mock-chain); Phase 6 extends template
    config/                 # ✅ Phase 4: host/user keys + chain seed; grows with compose
    keyring/                # Phase 6b — deterministic file keyring from host keys (not Phase 4)
    citest/                 # ported harness + rewritten scenarios
    scenarios/
    Makefile
    docker-compose.yml      # generated
  docs/
    Dockerfile.dev          # ✅ shared Go toolchain + air + dlv (Phase 4)
    docker-compose.dev.yml  # ✅ live-reload overlay on generated compose (Phase 4)
    .air.*.toml             # ✅ per-service air configs (Phase 4: mock-chain)
    DEVELOPMENT-MODE.md     # ✅ dev overlay runbook (Phase 4)
    vscode-launch.json      # ✅ remote attach profiles (Phase 4: mock-chain)
    testenv-v2.md           # operator runbook (Phase 11 ✅)
```

**Dependency rules:**

- `common/runtimeconfig` MUST NOT import `devshard/` or `decentralized-api/`.
- `devshard/chainoracle` MUST NOT import `devshard/testenv`, `devshard/host`,
  `devshard/heightsync` (the latter no longer exists).
- `devshard/chainoracle/params` MUST use `common/runtimeconfig` for the long-poll loop
  (Phase 1) and `common/chain` for snapshot fetch (Phase 2b) — no duplicate params logic.
- `devshard/testenv` MAY import `devshard/chainoracle`, `common/runtimeconfig`,
  `common/nodemanager/gen`, `common/chain`, `inference-chain` types.
- dapi later delegates its nodemanager server to `common/runtimeconfig/server`.

---

## 5. Phase-by-phase implementation

> Phases 2–2b are independent of mock-chain and can proceed in parallel with Phase 3.
> Phase 3 is **staged** (3a → 3b → 3c) per D1 Option A. Phase **4** (dev overlay) can
> start as soon as **3a** lands and grows with later phases.

### Phase 0 — Docs + sign-off

- This file.
- `devshard/docs/testenv-v2.md` stub (architecture + env-var tables).
- Link from `devshard/docs/merge-plan.md` "Follow-up todos" as a tracked follow-up.

**Exit:** D1–D3 decided; height-sync drop + versiond-first topology agreed. (D1 ✅ Option A;
D2 ✅ mock-openai; D3 ✅ Phase 1 done.)

---

### Phase 1 — Extract long-poll **server** to `common/runtimeconfig` ✅ **DONE**

**Goal:** one server-side `GetRuntimeConfig` long-poll loop usable by mock-dapi
(testenv), dapi (production), and later edge-api.

**Delivered (D3 = server-only extraction):**

| Path | Role |
|------|------|
| `common/runtimeconfig/snapshot.go` | `Snapshot`, `ApprovedVersion`, `ToProto()` |
| `common/runtimeconfig/maxwait.go` | `ClampMaxWait`, `DefaultMaxWaitCap` |
| `common/runtimeconfig/server.go` | `SnapshotSource` / `Notifier` / `EpochSource` ports + `Server.Handle` |
| `common/runtimeconfig/server_test.go` | long-poll + backcompat unit tests (fake ports) |
| `decentralized-api/nodemanager/runtime_config_adapter.go` | dapi adapters → common ports |
| `decentralized-api/nodemanager/server.go` | `GetRuntimeConfig` delegates to `common/runtimeconfig.Server` |
| `decentralized-api/nodemanager/runtime_config_max_wait.go` | delegates cap/clamp to `common/runtimeconfig` |

**Not moved (Phase 12):** `devshard/runtimeconfig` client long-poll loop + adaptive
provider — devshardd keeps using it unchanged.

**Validated:**

```bash
cd common && go test ./runtimeconfig/... -count=1          # ok
cd decentralized-api && go test ./nodemanager/... -count=1 # ok
```

---

### Phase 2 — Port `devshard/chainoracle` (blocks + params) ✅ **DONE** (2b deferred)

**Naming:** the old `devshard-testenv` branch called this **`blockoracle`** (block headers
only). Phase 2 **renames it to `chainoracle`** because the module is the unified
**chain-facing oracle** for testenv and (later) edge-api: **both** authenticated block
headers **and** params + epoch (`GetRuntimeConfig` long-poll). Do **not** keep a separate
`blockoracle` package name.

**Layering:**

| Layer | Package | Role |
|-------|---------|------|
| Long-poll loop core | `common/runtimeconfig` (Phase 1 ✅) | transport-agnostic `Server.Handle` |
| Chain read transport | `common/chain` + `common/runtimeconfig/chain_fetcher` (Phase 2b) | `Params`/`EpochInfo` → `Snapshot` |
| Unified oracle module | `devshard/chainoracle` (Phase 2 ✅) | mounts **both** surfaces for consumers |

**Goal:** one reusable module; **no** host / height-sync wiring.

**Delivered:**

| Path | Role |
|------|------|
| `devshard/chainoracle/blocks/**` | ported from `blockoracle` (observer, server, client, verifier, standalone) |
| `devshard/chainoracle/params/` | gRPC `NodeManager` server: `GetRuntimeConfig` long-poll + ML stubs |
| `devshard/chainoracle/server/` | HTTP mux: blocks SSE + `/versions` |
| `devshard/chainoracle/import_gate_test.go` | no `testenv` / `host` / `heightsync` imports |

Params `SnapshotFetcher` is injectable (static fetcher for now); Phase 2b wires it to
`common/chain` gRPC. Testenv fault routes on `/testenv/*` deferred to Phase 5.

**Validated:**

```bash
cd devshard && go test ./chainoracle/... -count=1   # ok
```

**Exit:** ✅ chainoracle builds + tests pass; params long-poll integration test against fake
ports; zero testenv/host imports.

---

### Phase 2b — Consolidate chain query transport into `common` ✅ **DONE**

**Goal:** one chain **read** transport (`common/chain`) reused by edge-api, devshardd
(bridge + params), devshardctl (queries), and mock-dapi — so the testenv exercises the
*same* code path production uses (§0.9). Tx broadcast stays REST for now.

**Delivered:**

| Path | Role |
|------|------|
| `common/runtimeconfig/chain_fetcher.go` | `Params` + `EpochInfo` → `Snapshot` via `common/chain.InferenceClient` |
| `common/chain/client.go` | added `Params` to `InferenceClient` interface |
| `devshard/runtimeconfig/provider.go` | `Snapshot` / `ApprovedVersion` alias `common/runtimeconfig` types |
| `devshard/runtimeparams/chain_fetcher_grpc.go` | thin wrapper over `common/runtimeconfig.ChainFetcher` |
| `devshard/runtimeparams/query.go` | **removed** — single `*chain.Client` dial for bridge + params |
| `devshard/cmd/devshardd/params_provider.go` | passes `*chain.Client` (not `QueryClientProvider`) |
| `devshard/cmd/devshardctl/runtime_params.go` | gRPC fetcher primary; REST legacy fallback |
| `devshard/cmd/devshardctl/main.go` | `--chain-grpc` / `DEVSHARD_CHAIN_GRPC` / `NODE_GRPC_URL` |
| `devshard/chainoracle/params/fetcher.go` | `ChainClientFetcher` on `common/chain` |

**Validated:**

```bash
cd common && go test ./runtimeconfig/... -count=1
cd devshard && go test ./runtimeparams/... ./runtimeconfig/... ./chainoracle/... -count=1
```

**Exit:** ✅ devshardd and devshardctl fetch params over `common/chain` gRPC; REST retained
as fallback when gRPC URL unset.

---

### Phase 3 — `mock-chain` (faithful fake, staged) ✅ D1 decided — Option A

**D1 decision:** **Option A — faithful fake, staged.** One Go process (`mock-chain`) with a
shared in-memory store and **three thin protocol faces**. Do **not** run a real
`inferenced` node in testenv. Implement **only endpoints callers actually use**; add more
as edge-api / gateway scenarios need them.

**Why staged:** unblocks devshardd + chainoracle (S1–S4) before gateway tx work (S5).

| Stage | Face | Port | Unblocks | Scenarios |
|-------|------|------|----------|-----------|
| **3a** | Cosmos gRPC + cmtservice | `:9090` | devshardd bridge, params fetch, chainoracle | S1–S4 |
| **3b** | CometBFT RPC (ws subscribe) | `:26657` | devshardd `events.Listener`, phase bootstrap | S1–S4 |
| **3c** | Cosmos LCD REST (tx) | `:1317` | devshardctl create/settle escrow | S5 |

**Layout**

```text
devshard/testenv/
  cmd/mockchain/           # single binary, three listeners
  mockchain/
    store/                 # escrows, participants, epochs, params, accounts (shared state)
    grpc/                  # inference.Query subset + cmtservice.GetLatestBlock
    rpc/                   # CometBFT /status, /block, /websocket subscribe
    rest/                  # LCD: account seq, tx broadcast/query (2 msg types)
    seed/                  # load testenv/config.yaml → store
    testenv/               # POST /testenv/* mutation API (fault injection)
```

**Shared store (one source of truth):**

- **Escrows** — `DevshardEscrow` records (string id, slots → host URLs, fees, epoch, …)
- **Participants** — address → `InferenceUrl` (points at `versiond-router` paths)
- **Epoch / params** — current epoch index, `Params` + `EpochInfo` bodies for gRPC;
  dedicated `params_block_height` on the store (stamped at chain tip on param/epoch publish);
  mock-dapi subscribes to CometBFT `NewBlock` and queries Params via gRPC per block (dapi-style)
- **Accounts** — account number + sequence for gateway tx signing
- **Blocks** — monotonic height with optional interval jitter (`MOCK_CHAIN_BLOCK_INTERVAL_DELTA`)
  for CometBFT RPC ticker + websocket `NewBlock` events

---

#### Phase 3a — gRPC face (`:9090`) — **first** ✅ **DONE**

**Goal:** `common/chain.Client` and chainoracle params fetcher work against mock-chain with
**zero** devshardd code changes.

**Implement (minimum for testenv):**

| RPC | Consumer |
|-----|----------|
| `Params` | chainoracle / `runtimeparams` (Phase 2b) |
| `EpochInfo` | chainoracle / `runtimeparams` |
| `GetCurrentEpoch` | devshardd phase bootstrap |
| `DevshardEscrow` | devshardd bridge |
| `Participant` | devshardd bridge (`GetHostInfo`) |
| `EpochGroupData` | devshardd bridge (`GetValidationThreshold`) |
| `GranteesByMessageType` | devshardd bridge (`VerifyWarmKey`) |
| `cmtservice.GetLatestBlock` | devshardd `bootstrapPhase` |

**Defer until needed:** remaining `common/chain.InferenceClient` methods (edge-api parity:
`ModelsAll`, `BridgeAddressesByChain`, …) — add when citest hits them.

**Steps**

1. `mockchain/store` — thread-safe in-memory models + seed from `testenv/config.yaml`.
2. `mockchain/grpcface` — register generated `QueryServer` stub implementing the table above;
   delegate reads/writes to `store`.
3. `cmd/mockchain` — listen `:9090`; env `MOCK_CHAIN_GRPC_ADDR`, `MOCK_CHAIN_CONFIG`.
4. Optional `POST /testenv/escrow`, `/testenv/epoch`, `/testenv/params` on a side HTTP port
   (or reuse 3c REST) to mutate store without full tx decode — citest fault injection.

**Delivered:**

| Path | Role |
|------|------|
| `testenv/config/config.go` + `config.yaml` | YAML seed schema + default fixture |
| `testenv/mockchain/store/` | thread-safe in-memory chain state |
| `testenv/mockchain/seed/` | defaults + YAML loader |
| `testenv/mockchain/grpcface/` | inference Query + cmtservice gRPC |
| `testenv/cmd/mockchain/` | binary entrypoint |

**Validated:**

```bash
cd devshard && go test ./testenv/mockchain/... -count=1
```

**Exit (3a):** `devshardd` boots with `NODE_GRPC_URL=mock-chain:9090`; bridge queries
succeed; chainoracle reads params via `common/chain`.

---

#### Phase 3b — CometBFT RPC face (`:26657`) — **second** ✅ **DONE**

**Goal:** devshardd event listener and phase updates without a real CometBFT node.

**Implement:**

| Endpoint | Behavior |
|----------|----------|
| `/status` | chain id, latest block height |
| `/block` | block at height (minimal header) |
| `/websocket` `subscribe` | replay events for queries in `events/events.go`: |
| | `tm.event='NewBlock'` — block ticker (configurable interval) |
| | `tm.event='Tx' AND devshard_escrow_created.escrow_id EXISTS` |
| | `tm.event='Tx' AND devshard_escrow_settled.escrow_id EXISTS` |

When store mutates an escrow (seed or `/testenv/*`), emit matching `Tx` event attributes
(`escrow_id`, `creator`, `amount`, `epoch_index`, …) so `events.Listener` dispatches to
`ChainBridge`.

**Tests:** start mock-chain RPC; wire `events.NewListener`; create escrow in store →
handler fires; `NewBlock` advances phase block height.

**Delivered:**

| Path | Role |
|------|------|
| `testenv/mockchain/rpcface/` | CometBFT JSON-RPC + `/websocket` subscribe via real `EventBus` |
| `testenv/mockchain/store/` | `AdvanceBlock`, `PutEscrow` for event injection |
| `testenv/cmd/mockchain/` | dual listeners: gRPC `:9090` + RPC `:26657` |

Env: `MOCK_CHAIN_RPC_ADDR`, `MOCK_CHAIN_BLOCK_INTERVAL` (default `1s`).

**Validated:**

```bash
cd devshard && go test ./testenv/mockchain/... -count=1
```

**Exit (3b):** `devshardd` with `NODE_HOST=mock-chain` subscribes; `bootstrapPhase` +
escrow-created handler work end-to-end with 3a gRPC.

---

#### Phase 3c — LCD REST face (`:1317`) — **third (gateway / S5)** ✅ **DONE**

**Goal:** `devshardctl` creates/settles escrows without code changes (`chain_tx_rest.go`).

**Implement (narrow — two msg types only):**

| REST path | Purpose |
|-----------|---------|
| Account query | account number + sequence for signing |
| `POST /cosmos/tx/v1beta1/txs` | broadcast `MsgCreateDevshardEscrow`, `MsgSettleDevshardEscrow` |
| `GET …/txs/{hash}` | tx poll for gateway |

Decode protobuf tx body → mutate `store` → emit CometBFT `Tx` event (3b) → visible on gRPC
(3a). Gateway REST is the **only** tx path; all **queries** stay on gRPC (`common/chain`).

**Tests:** gateway REST create escrow round-trip; devshardd sees escrow via gRPC + event.

**Delivered:**

| Path | Role |
|------|------|
| `testenv/mockchain/restface/` | LCD REST: accounts, broadcast, tx query, node_info |
| `testenv/mockchain/store/accounts.go` | account sequence + escrow id allocation |
| `testenv/cmd/mockchain/` | triple listener: gRPC `:9090`, RPC `:26657`, REST `:1317` |
| `cmd/devshardctl/chain_tx_rest_mockchain_test.go` | production `RESTChainTxClient` against mock-chain |

Env: `MOCK_CHAIN_REST_ADDR` (default `:1317`).

**Validated:**

```bash
cd devshard && go test ./testenv/mockchain/... ./cmd/devshardctl/ -run 'MockChain|REST' -count=1
```

**Exit (3c):** S5 gateway chat path can create an escrow in testenv; full three-face
mock-chain ready for compose (Phase 6).

---

**Phase 3 overall exit:** mock-chain container healthy; `docker compose up mock-chain` serves
9090/26657/1317; integration tests for 3a+3b green before 3c; S5 blocked only until 3c
lands.

**Explicitly rejected (D1):** Option B (real `inferenced` node), Option C (hybrid). Testenv
stays a **pure mock** with production wire shapes.

---

### Phase 4 — Dev overlay + config-driven compose (keys + live reload) ✅ **DONE**

**Goal:** two deliverables ported from `devshard-testenv`, shipped together:

1. **Config-driven build** — one `config.yaml` is the source of truth; `gencompose` fills
   missing keys, derives bech32 addresses, syncs mock-chain seed fields (participants,
   escrows, grantees), writes back `config.yaml`, and renders `docker-compose.yml`.
2. **Development overlay** — Go services rebuild inside containers when source changes on the
   host (`air` + optional `dlv`) without rebaking production images.

The overlay is **additive**: generated `docker-compose.yml` (prod-shape images) stays
unchanged; dev mode stacks `docker-compose.dev.yml` on top.

**Depends on:** Phase **3a** minimum (`cmd/mockchain` + gRPC face). The overlay and compose
template **grow** incrementally as Phases 5–6 add mock-dapi, mock-openai, and versiond×N —
do not block Phase 4 on the full versiond stack.

**Port from `devshard-testenv`:**

| Artifact | Reuse | Status |
|----------|------:|--------|
| `cmd/gencompose/` (`fillConfig`, keygen, `config.Save`) | ~70% | ✅ mock-chain stub; Phase 6 extends template |
| `config/config.go` host/user key fields | ~60% | ✅ `hosts[]`, `user`, `warm_grantee`, `network` + chain seed |
| `Dockerfile.dev` | ~85% | ✅ `golang` + `air` + `dlv`; no baked binaries |
| `docker-compose.dev.yml` | ~85% | ✅ mock-chain overlay; `-f docker-compose.yml -f docker-compose.dev.yml` |
| `.air.mock-chain.toml` (+ `.debug`) | ~85% | ✅ checked in; more services in Phases 5–6 |
| `DEVELOPMENT-MODE.md` | ~85% | ✅ `gen-compose` → `dev-up`, dlv `:2345` |
| Makefile `gen-compose`, `dev-*` / `hot-up` | ~85% | ✅ `testenv/Makefile` |
| `devoverlay_test.go`, `citest/compose_config_test.go` | ~80% | ✅ air contract + `docker compose config` |
| `vscode-launch.json` | optional | ✅ mock-chain attach `:2345`; extend in Phases 5–6 |

**Drop from old branch:** `.air.height-sync.*`, `.air.devshardd-testenv` configs (height-sync and
in-process `devshardd-testenv` are gone); `height_sync` section in config.

**Config + keys contract (from `devshard-testenv`, adapted for v2):**

Operators edit a **skeleton** `testenv/config.yaml` (or let gencompose bootstrap defaults).
Re-running `make gen-compose` is idempotent:

| Field | Who fills it | Used by |
|-------|--------------|---------|
| `hosts[].private_key_hex` | gencompose when empty/`TODO` | Phase 6b file keyring; today: mock-chain participant addresses |
| `hosts[].address` | derived from key | `participants[]`, grantee granter, escrow slot owner |
| `hosts[].id`, `port`, `ip`, `url` | gencompose defaults | Phase 6 versiond service names + router backends |
| `user.private_key_hex` / `user.address` | gencompose | Phase 7 gateway `TESTENV_PRIVATE_KEY`; today: escrow `creator` |
| `escrow.creator` | defaults to `user.address` | mock-chain seed |
| `participants[]`, `escrows[]`, `grantees[]` | gencompose **syncs from hosts** | mock-chain store via `MOCK_CHAIN_CONFIG` mount |

Key generation uses `devshard/signing` (same as old branch). Placeholder keys (`""`, `TODO…`,
`CHANGEME…`) are overwritten; pinned keys round-trip with derived addresses.

**Phase 4 gencompose scope (stub)** ✅ **done:**

- Input: `-config config.yaml` (create from defaults if missing — e.g. 2 hosts for laptop dev).
- `fillConfig`: generate host + user keys, derive addresses, assign escrow slots round-robin
  across hosts (slot URL → `versiond-router` placeholder until Phase 6), set `network` IPs.
- `syncChainSeed`: rewrite `participants`, primary `escrows[0].creator` / slot URLs, and
  grantee bindings so mock-chain and compose agree on identities without manual duplication.
- Output: `docker-compose.yml` with **mock-chain only** (prod-shape `Dockerfile.mock-chain`,
  bind-mount `./config.yaml`, ports 9090/26657/1317).
- `config.Save`: stamp auto-generated banner; operators re-run gencompose instead of hand-editing
  filled keys (same discipline as old branch).

Phase 6 **extends** the same binary and template — no second compose generator.

**How it works (unchanged pattern):**

- Shared `Dockerfile.dev` image for every Go service in the overlay.
- Repo root bind-mounted at `/workspace`; `air` watches `/workspace/devshard/**/*.go` (debounced
  ~500 ms) and rebuilds + restarts the owning service.
- Named volumes `gomodcache` / `gobuildcache` keep rebuilds warm across restarts.
- Debug variants wrap the binary in `dlv exec --headless --accept-multiclient --continue`; publish
  host ports for IDE attach (`SYS_PTRACE` + `seccomp:unconfined` on Docker Desktop).

**v2 service matrix (extend as phases land):**

| When | `air` services | `dlv` listener (host) | Unblocks |
|------|----------------|----------------------|----------|
| **Phase 4 (initial)** ✅ | `mock-chain` | `:2345` → mock-chain | 3b/3c iteration without image rebuilds |
| **Phase 5** ✅ | + `mock-dapi`, `mock-openai` | `:2346` mock-dapi; `:2347` mock-openai | chainoracle + ML stub hacking |
| **Phase 6** | + `devshardctl` | — | gateway REST/LCD work |
| **Phase 6+** | `versiond-0` child `devshardd` | `:2348` → devshardd (host-0 only) | see note below |

**versiond / devshardd note:** `versiond` is a binary supervisor, not an `air` target. For
devshardd iteration use one of:

1. **Recommended:** `air` builds `devshardd` to a bind-mounted path
   (`VERSIOND_OVERRIDE_<ver>=/workspace/devshard/build/devshardd`); save triggers rebuild;
   restart the versiond child (or rely on versiond's poll interval) to pick up the new binary.
2. **Fast path (mock-chain-only dev):** ✅ available now — skip versiond in the dev overlay until
   Phase 6 compose exists; iterate mock-chain with `go test` + `make gen-compose && make dev-up`.

Hosts `versiond-1..N-1` run live-reload only (no `dlv`) so multi-host scenarios stay responsive
on a laptop — same rule as the old branch.

**Steps** ✅

1. ✅ **Extend `testenv/config/`** — `hosts[]`, `user`, `warm_grantee`, `network`, escrow meta +
   chain-seed fields; `Load`/`Validate`/`ApplyDefaults`/`Save`.
2. ✅ **Add `testenv/cmd/gencompose/` stub** — `fillConfig`, `syncChainSeed`, mock-chain compose
   template; `make gen-compose` writes `docker-compose.yml` + updates `config.yaml`.
3. ✅ `Dockerfile.dev` + `Dockerfile.mock-chain` (repo-root build context).
4. ✅ `docker-compose.dev.yml` — mock-chain live reload + dlv `:2345`.
5. ✅ `.air.mock-chain.toml` + `.air.mock-chain.debug.toml`.
6. ✅ Makefile targets: `gen-compose`, `dev-build`, `dev-up`, `hot-up`, `dev-down`, `dev-clean`,
   `dev-logs`, `dev-restart-mock-chain`.
7. ✅ `DEVELOPMENT-MODE.md`.
8. ✅ Tests: `gencompose/main_test.go` (config → seed → gRPC round-trip),
   `devoverlay_test.go`, `citest/compose_config_test.go`, `CONFIG_PATH` alias test.
9. ✅ `vscode-launch.json` — **Attach: mock-chain** on `:2345` (merge into `.vscode/launch.json`).

**Validated:**

```bash
cd devshard && go test ./testenv/... -count=1
cd devshard/testenv && make gen-compose   # fills keys, writes docker-compose.yml
```

**Exit (4):** ✅ `make gen-compose` on a fresh clone produces pinned keys + `docker-compose.yml`;
`make dev-up` runs mock-chain with live reload; IDE attaches to `:2345`; mock-chain seed matches
generated host/user addresses without hand-editing bech32 placeholders. `CONFIG_PATH` aliases
`MOCK_CHAIN_CONFIG`.

---

### Phase 5 — `mock-dapi` + `mock-openai` containers ✅ **DONE**

**Goal:** single container replacing dapi. Uses **`devshard/chainoracle`** for both params
and blocks — not separate oracles.

**Depends on:** Phase 1–2 (+ 2b for params source); **mock-chain Phase 3a** (gRPC) for
chainoracle params fetch. Phase 3b required before full devshardd escrow-event tests.

**Delivered (Phase 5 + 5b):**

1. New `devshard/testenv/cmd/mockdapi/` (net-new, built on Phase 1 + 2 + 2b):
   - **gRPC :9400** — mount **`chainoracle/params`** (`NodeManagerServer`):
     - `GetRuntimeConfig` → `common/runtimeconfig.Server` + chain fetcher reading mock-chain
       via `common/chain` gRPC;
     - `AcquireMLNode` → returns **`mock-openai`** endpoint (D2); `ReleaseMLNode` → noop.
   - **HTTP :9100** — mount **`chainoracle`** HTTP mux:
     - blocks HTTP/SSE (mock observer, validator set from config),
     - `/versions` for `VERSIOND_ORACLE_URL`,
     - `/testenv/epoch` + `/testenv/params` fault API (mutates mock-chain store).
2. **Fault-injection wiring:** `/testenv/*` mutates **mock-chain's** store (preferred —
   keeps mock-chain the single source of truth); mock-dapi's poll then observes the change
   and `Notifier.Notify()`s. (Fallback if a direct mock-chain mutation is not yet wired:
   mock-dapi overrides its cached snapshot directly.)
3. `Dockerfile.mockdapi` ✅; gencompose emits `mock-dapi` + mock-chain admin `:9191`.

**Phase 5b — `mock-openai` service (D2).** ✅ **DONE**

1. `devshard/testenv/cmd/mockopenai/` — OpenAI-compatible `POST /v1/chat/completions` (JSON +
   SSE `text/event-stream`).
2. Deterministic assistant text: `mock-openai:` + SHA-256 prefix of the raw request body
   (same prompt → same content for validation re-runs).
3. Fault knobs: env (`MOCK_OPENAI_LATENCY_MS`, `MOCK_OPENAI_HTTP_STATUS`,
   `MOCK_OPENAI_DROP_FIRST_CHUNK`, `MOCK_OPENAI_PARTIAL_STREAM`) and runtime
   `POST /testenv/fault`.
4. `Dockerfile.mockopenai` ✅; gencompose emits `mock-openai` at `:8088`; dev overlay air/dlv
   on `:2347`.

**Wire contract for devshardd (prod):** `NODE_MANAGER_ADDR=mock-dapi:9400`;
mock-dapi env `MOCK_ML_ENDPOINT=http://mock-openai:8088`.

**Validated:**

```bash
cd devshard && go test ./testenv/mockdapi/... ./testenv/mockopenai/... ./testenv/mockchain/adminface/... -count=1
```

**Exit (5):** ✅ mock-dapi healthy; `GetRuntimeConfig` long-poll wakes on `/testenv/params`;
`/versions` JSON valid; blocks SSE monotonic; `AcquireMLNode` returns `MOCK_ML_ENDPOINT`.

**Phase 5b exit:** ✅ `POST /v1/chat/completions` returns deterministic JSON + SSE; fault
injection via env and `/testenv/fault`.

---

### Phase 6 — Compose: versiond × N + versiond-router + devshardd ✅ **DONE**

**Goal:** production-shaped supervision in the testenv.

**Depends on:** Phase **4** ✅ gencompose stub; Phase **5** ✅ mock-dapi; mock-chain **3a+3b** for devshardd-only stack.

**Delivered:**

1. ✅ **Extended** `cmd/gencompose` (`compose.go`) to emit `versiond-0..N-1`, `versiond-router`,
   `devshardctl`, optional `devshard-postgres` (`postgres.enabled`), plus Phase 4/5 services.
   Also writes `.env` with `TESTENV_USER_PRIVATE_KEY` / `TESTENV_KEYRING_PASSWORD`.
2. ✅ Each `versiond-i` env:
   - `VERSIOND_ORACLE_URL=http://mock-dapi:9100/versions`
   - `VERSIOND_BINARY_NAME=devshardd`, `VERSIOND_OVERRIDE_v2=/opt/devshard/devshardd`,
     `VERSIOND_FORCE=v2` (mount `../../build/devshardd`)
   - devshardd child env via versiond inheritance: `NODE_GRPC_URL`, `NODE_HOST=mock-chain`,
     `NODE_MANAGER_ADDR=mock-dapi:9400`, `CHAIN_ID`, `KEYRING_BACKEND=file`, `KEYRING_DIR`,
     `KEY_NAME`
3. ✅ **Phase 6b keyring provisioning:** `testenv/keyring/materialize.go` — `gencompose` writes
   `./keyring/<host-id>/` Cosmos file keyrings from `hosts[].private_key_hex`; address checked
   against `hosts[].address`.
4. ✅ `versiond-router`: `VERSIOND_HOSTS` from `hosts[].id`, `VERSIOND_PORT=8080`, sticky nginx image.
5. ✅ Separate per-host `./data/<id>` dirs; router + versiond patterns from `local-test-net` /
   `deploy/join` overlays. **No `api` (dapi) service.**

**Config:** `versiond`, `versiond_router`, `devshardctl`, `postgres` sections in `config/config.yaml`.
Default skeleton: **3** versiond hosts (`versiond-0..2`).

**Docs:** `testenv/README.md` — build, test, start 3× versiond + router.

**Build:** `make build-devshardd` (testenv Makefile) → repo-root
`make devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2`.

Protocol slot name (`DEVSHARD_VERSION` / `VERSIOND_FORCE` / `/versions` name) is **`v2`**.
Binary log prefix (`DEVSHARD_BINARY_VERSION` / devshardd log lines) is **`0.2.13-v2-r2`**.

**Tests:**

```bash
cd devshard && go test ./testenv/cmd/gencompose/... ./testenv/citest/... ./testenv/keyring/... -count=1
```

**Exit:** ✅ `make gen-compose && docker compose config` renders; `make gen-compose && make build-devshardd && make up`
starts stack with materialized keyrings (devshardd child healthy when binary + arch match).

---

### Phase 7 — Gateway `devshardctl` integration ✅ **DONE**

**Goal:** use the current gateway, not the slim proxy.

**Depends on:** mock-chain **Phase 3c** (LCD REST tx) + Phase 6 compose.

**Delivered:**

1. ✅ `devshardctl` compose service with full gateway env (`DEVSHARD_ESCROW_ID`, `DEVSHARD_MODEL`,
   `DEVSHARD_CHAIN_GRPC`, `DEVSHARD_PUBLIC_API` → mock-dapi, `DEVSHARD_TX_QUERY_REST`, build arg
   `DEVSHARD_VERSION=v2`, healthcheck on `/v1/status`).
2. ✅ `syncChainSeed` fixes: escrow **slots** = host validator addresses; participant
   `inference_url` = versiond-router origin (no duplicate `/devshard/<v>` path).
3. ✅ `gatewayphase` HTTP stubs on mock-dapi (`/v1/epochs/latest`, `/v1/epochs/current/participants`).
4. ✅ Docs + `make test-gateway-smoke` runbook.

**Tests:**

```bash
cd devshard && go test ./testenv/gatewayphase/... ./testenv/citest/... -count=1
TESTENV_GATEWAY_SMOKE=1 go test ./testenv/citest/ -run TestGatewayPhase7_Smoke -count=1
```

**Exit:** ✅ gateway status + chat completion through router to devshardd; create escrow via
`POST /v1/admin/escrows` (smoke test).

---

### Phase 8 — Go citest harness (Docker controlled by Go)

**Goal:** port the old `citest/`/`scenarios/container/` *pattern*, no height-sync cases.

**Steps**

1. ✅ Port `citest/harness/{stack.go, output.go, http.go, config.go}` (compose up/down,
   health polling, config skeletons). Build tag `testenvci` for Docker stack scenarios.
2. Initial scenarios:

   | ID | Scenario | Asserts | Status |
   |----|----------|---------|--------|
   | S1 | Stack smoke | mock-chain + mock-dapi + router + 2 versiond + gateway healthy | ✅ **DONE** |
   | S2 | Router stickiness | same escrow/session → same versiond backend across retries | ✅ **DONE** |
   | S3 | Params long-poll | `/testenv/params` change → devshardd session bind uses new timeouts | ✅ **DONE** |
   | S4 | Epoch switch | `/testenv/epoch` advance → fast-forward blocks to `next_poc_start`, runtimeparams epoch bump | ✅ **DONE** |
   | S5 | Gateway chat | devshardctl → router → devshardd → `mock-openai` `/v1/chat/completions` → 200 (stream + non-stream) | ✅ **DONE** |
   | S6 | Fault: versiond stop | router retries another instance or fails as designed | ✅ **DONE** |

   **S4 implementation:** `citest/s4_epoch_switch_test.go` posts `POST /testenv/epoch`
   `{advance:true}` on mock-dapi; mock-chain `rpcface.AdvanceEpoch` publishes CometBFT
   `NewBlock` for each height up to `next_poc_start`, commits epoch index+2 with updated
   `next_poc_start`, and mock-dapi `RefreshRuntimeConfig` wakes `GetRuntimeConfig`
   long-poll with higher `current_epoch_id`. Included in `make citest-stack` (rebuild
   `mock-chain` image when epoch admin changes).

   **S5 implementation:** `citest/s5_gateway_chat_test.go` boots the S1 stack and posts
   pooled `POST /v1/chat/completions` on devshardctl for non-stream and SSE stream;
   asserts HTTP 200, non-empty assistant content with `mock-openai:` prefix, and
   `data: [DONE]` on the stream path. Uses admin API key (`TESTENV_ADMIN_API_KEY`),
   waits for gateway runtime + `/{version}/healthz` via router. Helpers in
   `citest/harness/gateway_chat.go`. Compose/gencompose fixes: mock-chain REST escrow
   query, router `/devshard/` rewrite, `NODE_RPC_URL`, internal router URL `:8080`.
   Included in `make citest-stack`.

   **S6 implementation:** `citest/s6_versiond_stop_test.go` finds two session ids
   routed to different versiond upstreams (sticky hash), stops the compose service for
   one upstream, asserts the pinned session either gets 502/503 (unavailable peer) or
   re-hashes to the surviving upstream (consistent-hash ring shrink when Docker DNS drops
   the peer), and the session on the live upstream still routes to the same
   `X-Upstream-Addr`. Helpers in `citest/harness/router_sticky.go` and
   `Stack.StopService`. Included in `make citest-stack`.

   **S3 implementation:** `citest/s3_params_longpoll_test.go` boots the S1 stack (devshardd
   long-polling mock-dapi via `NODE_MANAGER_ADDR`), blocks on `GetRuntimeConfig` at the current
   `params_block_height`, posts `POST /testenv/params` on mock-dapi (proxied to mock-chain admin),
   asserts the long-poll returns higher height plus updated `max_nonce` / timeouts, then
   verifies caught-up clients get immediate `unchanged` and stale-height clients receive the
   patched snapshot — the same `GetRuntimeConfig` lane-C feed devshardd consumes via
   `NODE_MANAGER_ADDR` while the S1 stack is up. Included in `make citest-stack`.

   **S2 implementation:** `citest/s2_router_stickiness_test.go` hits
   `/<version>/sessions/<id>/healthz` through versiond-router eight times and asserts
   identical `X-Upstream-Addr` (nginx `$upstream_addr`); probes additional session ids
   until a second upstream is observed. Router image exposes the header via
   `versiond-router/nginx.conf.template`. Included in `make citest-stack`.

   **S1 implementation:** `citest/s1_stack_smoke_test.go` (`//go:build testenvci`) uses
   `harness.WriteS1Config` (2× versiond, `mode: multi`, Postgres), `docker compose up --wait`,
   then polls mock-chain RPC `/health`, mock-dapi `/healthz` + `/v1/epochs/latest`,
   versiond-router `/healthz`, gateway `/v1/status`, and `docker compose ps` for
   `versiond-0` + `versiond-1`. Run: `make citest-stack` or
   `TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestS1_StackSmoke`.

3. **Drop:** `*HeightSync*`, cheat-anchor, force-anchor, height-sync audit scenarios.

**Scripts:** `testenv/scripts/run-stack-citest.sh` (S1); `scripts/gen-integration-testenv-config.sh` (Phase 11).

**Exit:** `make citest-stack` green locally with Docker (S1–S6 ✅).

---

### Phase 9 — Adversarial scenarios (Go-first)

Port the **structure** from `PROTOCOL_TESTING_PROPOSAL.md` only where it applies without
height-sync anchors (refusal timeout, malicious verifier, stale escrow, bad warm-key).
Fault hooks live on `mock-dapi` / `mock-chain` (`/testenv/*`, `docker compose pause`),
never on a Host seam.

**Delivered:**

| ID | Scenario | Asserts | Status |
|----|----------|---------|--------|
| A1 | Lost first SSE chunk | `mock-openai` `drop_first_chunk` → gateway stream still completes; assembled text missing first rune | ✅ **DONE** |
| A2 | ML upstream 5xx | `mock-openai` `http_status=503` → gateway chat HTTP ≥400 | ✅ **DONE** |
| A3 | Stale escrow | `POST /testenv/escrow` settle → mock-chain gRPC reports `settled=true` | ✅ **DONE** |
| A4 | Bad warm-key | `POST /testenv/grantees` revoke → warm grantee absent from `GranteesByMessageType` | ✅ **DONE** |

Helpers: `citest/harness/adversarial.go`. Fault APIs: `POST /testenv/escrow`, `/testenv/grantees`
on mock-chain (proxied by mock-dapi); `POST /testenv/fault` on mock-openai.

Run: `make citest-adversarial` or
`TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestA1_|TestA2_|TestA3_|TestA4_'`.

**Exit:** ≥1 multi-host adversarial scenario in Go citest ✅ (A1–A4 on 2× versiond stack).

---

### Phase 10 — Observability overlay (optional)

Port `testenv/observability/` compose fragment (Jaeger/Prometheus/Loki/Grafana — same
family as `deploy/join/docker-compose.observability.yml`). Lower priority than S1–S6.

**Plan (backends, profiles, Alloy/Tempo roadmap):** [`observability-plan.md`](./observability-plan.md)

| Deliverable | Status |
|-------------|--------|
| `docker-compose.observability.yml` + `observability/*` configs | ✅ |
| OTEL env on versiond (`TESTENV_OTEL_*` via gencompose) | ✅ |
| `devshardd` calls `observability.Init` + exposes `/metrics` | ✅ |
| `make obs-up` / `make citest-observability` (O1 smoke) | ✅ |
| Selectable backends: Promtail vs Alloy, Tempo vs Jaeger | 📋 [observability-plan.md](./observability-plan.md) |

**Exit:** optional overlay; O1 citest asserts Jaeger spans (`devshardd.request`, `devshardd.inference`),
Loki structured logs (`devshard request terminal`), and Prometheus metrics
(`devshardd_request_*` via versiond, gateway `/metrics`). ✅

**Follow-up:** `TESTENV_OBS_PROFILE` compose fragments + Alloy port from `devshard-testenv` branch;
OTLP via **Alloy** (`http://alloy:4317`) when `*-alloy` profile — see [observability-plan.md](./observability-plan.md).

---

### Phase 11 — CI + Makefile + runbook ✅ **DONE**

1. `devshard/testenv/Makefile` — ✅ Phase 4 landed `gen-compose`, `dev-*` / `hot-up` (see
   `DEVELOPMENT-MODE.md`); Phase 11 adds `citest-stack`, CI wiring, top-level targets.
2. `devshard/Makefile` — ✅ `ci-testenv-unit`, `ci-testenv-integration` (dispatch; Docker).
3. `.github/workflows/devshard-testenv.yml` — ✅ unit on PRs touching testenv-related paths;
   integration on `workflow_dispatch`.
4. ✅ `devshard/docs/testenv-v2.md` operator runbook.
5. ✅ `testenv/scripts/gen-integration-testenv-config.sh` — isolated citest workspace generator.

**Exit:** ✅ CI green on unit + smoke; integration job documented.

---

### Phase 12 — Production follow-up ✅ **client done**; remainder tracked

| Item | Status |
|------|--------|
| `devshard/runtimeconfig` client → `common/runtimeconfig/client` | ✅ **DONE** — adaptive gRPC + chain fallback in common; `devshard/runtimeconfig` re-exports |
| edge-api hosts chainoracle | 📋 tracked — [`phase12-followup.md`](./phase12-followup.md) |
| dapi becomes chainoracle client-only | 📋 tracked |
| dapi `cosmosclient`/publisher → `common/chain` | 📋 tracked — `merge-plan.md` |
| gateway gRPC-only transport (queries + tx) | 📋 **active** — [`chain-transport-consolidation.md`](./chain-transport-consolidation.md); citest G1–G4 |

**Exit (client):** `go test ./common/runtimeconfig/client/...` + `go test ./devshard/runtimeparams/...` green; no testenv changes.

**Exit (gRPC transport):** G1–G3 citest green; no `RESTBridge` / `RESTChainTxClient` in gateway.

See [`phase12-followup.md`](./phase12-followup.md).

---

### Phase 13 — Rolling update citest ([`rolling-update.md`](./rolling-update.md))

**Goal:** exercise versiond binary swap (§1.1) and versiond-router host evacuation (§1.8)
in the testenv stack — after the rolling-update implementation lands in `versioned/` and
devshardd exposes `/ready` + `/drain/status`.

**Depends on:** Phase 6 compose (N versiond + router + optional `devshard-postgres`), Phase 8
citest harness, and rolling-update **Track A** (blue/green sha swap) at minimum.

**Prerequisite (production code, not testenv-only):**

| Track | Source | Required before Phase 13 |
|-------|--------|--------------------------|
| **A — devshardd binary swap** | `rolling-update.md` §1.1–§1.6 | `/ready`, `/drain/status`, `DEVSHARD_SHUTDOWN_GRACE`; versiond blue/green + drain |
| **B — versiond host evacuation** | `rolling-update.md` §1.8 | versiond-router upstream `down` + reload; drain poll loop |

**Testenv additions:**

1. **Compose:** `devshard-postgres` **required** for Track A (SQLite cannot run old+new
   devshardd concurrently — §1.2).
2. **mock-dapi:** extend `/versions` + `/testenv/*` to bump **sha256 for same version name**
   (simulates governance publishing a new binary).
3. **Binaries:** mount two devshardd builds (or tagged overrides) for swap scenarios.
4. **New citest scenarios** (after S1–S6):

   | ID | Scenario | Asserts |
   |----|----------|---------|
   | **S7** | Same-name sha swap | Long request on old child completes; concurrent new request hits new child; no 404 during swap |
   | **S8** | Router host drain | Escrow pinned to `versiond-N`; mark upstream down; in-flight completes; no new traffic to N |

**Tests:** align with `rolling-update.md` §1.9 (versiond unit/e2e + devshardd readiness);
Phase 13 adds **stack-level** Go citest on top.

**Exit:** `make citest-stack` includes S7 (+ S8 when Track B lands); rolling-update semantics
validated before deploy/join.

---

## 6. Validation matrix

Local validation commands for each layer. ✅ = implemented and passing locally; **CI unit**
wired in Phase 11 (`.github/workflows/devshard-testenv.yml`); integration on dispatch.

| Layer | Command |
|-------|---------|
| common runtimeconfig server ✅ | `go test ./common/runtimeconfig/...` |
| chain transport consolidation (2b) ✅ | `go test ./devshard/runtimeparams/... ./common/chain/...`; `go build ./...` for devshardd + devshardctl |
| dapi delegation unchanged ✅ | `go test ./decentralized-api/nodemanager/...` |
| chainoracle ✅ | `go test ./devshard/chainoracle/...` |
| testenv unit ✅ | `cd devshard && go test ./testenv/...` |
| dev overlay + gencompose (Phase 4) ✅ | `go test ./testenv/...`; `make gen-compose && make dev-build && make dev-up` |
| gencompose Phase 6 compose ✅ | `go test ./testenv/cmd/gencompose/... ./testenv/citest/... ./testenv/keyring/... -count=1` |
| mock-dapi (Phase 5) ✅ | `go test ./testenv/mockdapi/... -count=1` |
| mock-openai (Phase 5b) ✅ | `go test ./testenv/mockopenai/... -count=1` |
| mock-chain gRPC face (3a) ✅ | `go test ./devshard/testenv/mockchain/... -count=1` |
| mock-chain CometBFT RPC / devshardd events (3b) ✅ | `go test ./devshard/testenv/mockchain/rpcface/... -count=1`; `make citest-stack` S1 |
| mock-chain LCD REST / gateway escrow tx (3c) ✅ | `go test ./devshard/testenv/mockchain/restface/... -count=1`; `make citest-stack` S5 |
| stack citest (Phase 8) ✅ | `make citest-stack` (S1–S6) |
| adversarial citest (Phase 9) ✅ | `make citest-adversarial` (A1–A4) |
| observability smoke (Phase 10) ✅ | `make citest-observability` (O1) |
| common runtimeconfig client (Phase 12) ✅ | `go test ./common/runtimeconfig/client/... -count=1` |
| CI unit (Phase 11) ✅ | `make -C devshard ci-testenv-unit` |
| CI integration (Phase 11) | `workflow_dispatch` → `make -C devshard ci-testenv-integration` |
| no height-sync regression ✅ | `go test ./devshard/chainoracle/... -run TestNoForbiddenImports` |

---

## 7. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| **mock-chain fidelity (3 faces)** | D1 = staged Option A; implement used endpoints only; 3a→3b→3c |
| Long-poll extract breaks dapi | Ports + adapter, keep all backcompat tests green |
| devshardd keyring/identity wiring | ✅ Phase 6b `testenv/keyring` materializes file keyrings in `gencompose` |
| Gateway needs LCD REST + tx decode | Implement tx decode for the 2 escrow msg types only |
| versiond+devshardd slower than the old embedded host | Accept; fidelity over speed; CI uses 2 versiond |
| Router stickiness flakiness | Deterministic escrow IDs; retry with same session header |
| Client long-poll consolidation risk | Deferred to Phase 12; testenv only needs the server |
| Gateway query→gRPC migration (2b) regresses prod | Ship 2b as its own PR; keep `RESTChainFetcher` behind a flag for one release; gateway tx stays REST |
| `air` rebuild races dlv attach | `kill_delay=1s`, IDE auto-reconnect; documented in `DEVELOPMENT-MODE.md` (Phase 4 ✅) |
| Two `Snapshot` types drift (devshard vs common) | One aliases/converts to the other (Phase 2b step 4) |

---

## 8. Execution order + milestones

```text
Phase 0   Docs + decisions (D1–D3)                    ✅ done
Phase 1   common/runtimeconfig SERVER extract        ✅ DONE          ──┐ parallel
Phase 2   devshard/chainoracle port (blocks + params)  ✅ DONE (2b deferred) ──┤
Phase 2b  consolidate chain query transport→common    ✅ DONE          ──┤
Phase 3a  mock-chain gRPC face                      ✅ DONE (D1 Option A) ──┘
Phase 3b  mock-chain CometBFT RPC face              ✅ DONE
Phase 3c  mock-chain LCD REST face                  ✅ DONE
Phase 4   gencompose stub + dev overlay (keys, air, dlv)  ✅ DONE
Phase 5   mock-dapi + mock-openai (mounts chainoracle)  ✅ DONE
Phase 6   gencompose versiond×N + router + devshardctl + keyrings  ✅ DONE
Phase 7   devshardctl gateway in compose             ✅ DONE
Phase 8   Go citest harness + S1–S6 (S1–S4 ✅ stack smoke + stickiness + params + epoch switch)
Phase 9   adversarial scenarios (A1–A4 ✅)
Phase 10  observability overlay (optional)              ✅ DONE
Phase 11  CI / Makefile / runbook                       ✅ DONE
Phase 12  client long-poll → common (remainder tracked) ✅ client DONE
Phase 13  rolling-update citest (S7/S8)           needs P6,P8 + rolling-update.md Track A/B
```

- **MVP (devshardd-only):** Phases 1–2 + **3a+3b** + 5 + 6 + S1–S4 — stack up, long-poll
  works, router routes, devshardd sees escrows/events. No gateway tx yet.
- **MVP+ (full chat):** add **3c** + Phase 7 + S5 — gateway creates escrow over REST.
- **Dev loop (early):** ✅ **available now** — Phase **3 + 4** done: `make gen-compose && make dev-up`;
  mock-chain hot reload with pinned keys while building Phase 5+.
- **M2:** S2, S4–S6 + CI.
- **M3 (rolling update):** Phase 13 + S7 (+ S8 when router drain lands) — after
  `rolling-update.md` Track A/B in production code.

---

## 9. Relation to `local-test-net` and `deploy/join`

| Environment | Role |
|-------------|------|
| `devshard/testenv` | Devshard-only fast lab; **mock** chain; no `inferenced`, no dapi |
| `local-test-net` | Full node + dapi + edge-api + optional versiond overlays |
| `deploy/join` | Production compose; testenv validates the versiond-router overlay behavior |

Testenv v2 mirrors versiond-router wiring from
`deploy/join/docker-compose.versiond.yml` so sticky-routing bugs surface before deploy.

---

## 10. Glossary

- **mock-chain** — fake chain serving real Cosmos gRPC + cmtservice + minimal CometBFT
  RPC (+ LCD REST for the gateway).
- **mock-dapi** — testenv container that mounts **`devshard/chainoracle`**: params gRPC
  (`GetRuntimeConfig` long-poll) + blocks HTTP/SSE + `/versions` + `/testenv` fault API.
  Keeps the `mock-dapi` service name for `NODE_MANAGER_ADDR` wire compatibility.
- **chainoracle** — unified chain-facing oracle module (`devshard/chainoracle`):
  - **blocks** — authenticated block headers (HTTP+SSE); ported from old `blockoracle`;
  - **params** — epoch + governance params via `GetRuntimeConfig` long-poll, backed by
    `common/runtimeconfig/server` + `common/chain` fetcher.
- **mock-openai** — OpenAI-compatible ML stub (`/v1/chat/completions`, +SSE) that
  `AcquireMLNode` points devshardd at (D2); has latency/fault knobs.
- **versiond-router** — nginx consistent hash on escrow/session id.
- **gencompose** — ✅ `testenv/cmd/gencompose` (Phase 4): reads `config.yaml`, fills missing keys,
  syncs mock-chain seed fields, writes back config, renders `docker-compose.yml`.
  Phase 4 stub emits mock-chain only; Phase 6 extends the same template.
- **Dev mode** — ✅ overlay of `docker-compose.dev.yml` (Phase 4) that replaces prod Go service images
  with `Dockerfile.dev` (`air` + `dlv`), bind-mounts the repo at `/workspace`, and exposes
  dlv remote-debug ports for a selected subset of services. ✅ Phase 4 (mock-chain) +
  `DEVELOPMENT-MODE.md`; grows in Phases 5–6.

---

## 11. Decisions

Status: **D1 decided** (Option A — faithful fake, staged 3a→3b→3c), **D2 decided**
(mock OpenAI), **D3 decided + implemented** (Phase 1 done).

**D1 — mock-chain strategy. ✅ DECIDED: Option A (faithful fake, staged).**

Production `devshardd` needs Cosmos gRPC + CometBFT RPC; the gateway needs Cosmos LCD REST
(§0.1, §0.2). **Rejected:** Option B (real `inferenced` node), Option C (hybrid).

**Chosen approach:** one Go **`mock-chain`** process, shared in-memory store, three protocol
faces implemented incrementally:

1. **3a — gRPC `:9090`** first — `Params`, `EpochInfo`, bridge queries, `GetLatestBlock`.
   Serves all readers via `common/chain` (devshardd, chainoracle, later edge-api subset).
2. **3b — CometBFT RPC `:26657`** second — websocket subscribe for escrow + `NewBlock`.
3. **3c — LCD REST `:1317`** third — gateway create/settle tx only.

Phase 2b shrinks the gRPC face: one `inference.Query` implementation on mock-chain backs
devshardd bridge, params fetch, and chainoracle — not three independent query stacks.

See **Phase 3** for endpoint tables, module layout, and per-stage exit criteria.

*(Rejected: Option B — real `inferenced` node; Option C — hybrid.)*

**D2 — ML boundary. ✅ DECIDED: option (a) — run a mock OpenAI service.**
Production `devshardd` does real HTTP inference: its engine calls `AcquireMLNode` (gRPC to
`NODE_MANAGER_ADDR`), gets back an `Endpoint`, then POSTs `endpoint + "/v1/chat/completions"`
(`inference/engine.go:51`; validation does the same at `inference/validator.go:110`). So
the testenv needs an **OpenAI-compatible** HTTP service, and `mock-dapi.AcquireMLNode`
returns its URL. See `mock-openai` everywhere in this doc (Phase 5/6, §2, §4).

Implementation choice (sub-decision, not blocking):
- **Reuse `inference-mock-server`** (the wiremock image built from `testermint/Dockerfile`
  that `local-test-net` already runs as the ML mock, serving `/v1/chat/completions` from
  recorded `node_payload_mock_server_*.json`). Pro: parity with local-test-net, zero new
  code. Con: wiremock is awkward for streaming/latency/fault injection.
- **Tiny Go `cmd/mockopenai`** (recommended for testenv): deterministic completions,
  SSE streaming, and knobs for latency / chunk-drop / 5xx — needed by the adversarial
  scenarios (Phase 9) and the "lost first response" style cases.

Default: build the small Go `mock-openai`; keep the wiremock image as a drop-in fallback.

**D3 — Phase 1 scope: extract the long-poll *server* now, defer the *client*. ✅ DONE**
Implemented in Phase 1 (see §5 Phase 1). Detail below.

The `GetRuntimeConfig` long-poll has **two independent halves**:

| Half | What it does | Where it lives today | Who needs it in testenv |
|------|--------------|----------------------|-------------------------|
| **Server** | Holds the request open until params/epoch change; replies full config or `unchanged`; enforces `max_wait` | `decentralized-api/nodemanager/server.go` (coupled to dapi `apiconfig.ConfigManager` + `chainphase`) | **mock-dapi** must *serve* it → needs the server |
| **Client** | Repeatedly calls `GetRuntimeConfig`, applies snapshots, backoff, and (in devshardd) **adaptive chain fallback** | `devshard/runtimeconfig/{grpc_runner,adaptive_provider,chain_runner,…}.go` | devshardd already *consumes* it — **unchanged**; testenv needs **no new client** |

- **"Extracting only the server now"** = Phase 1 lifts the *server* loop into
  `common/runtimeconfig/server` behind ports (`SnapshotSource`/`Notifier`/`EpochSource`),
  so both **dapi** (delegates) and **mock-dapi** (testenv) share one implementation. This
  is the piece the testenv actually requires, because mock-dapi is a NodeManager *server*.

- **"Deferring the `devshard/runtimeconfig` client consolidation to Phase 12"** = we do
  **not** move devshardd's *client* long-poll loop into `common` right now. devshardd
  keeps using its existing `devshard/runtimeconfig` client as-is (talking to
  `NODE_MANAGER_ADDR=mock-dapi:9400` in the testenv). Nothing in the testenv depends on
  the client living in `common`.

Why defer the client (the risk being avoided): the devshardd client is not a plain loop —
it's an **adaptive provider** (`adaptive_provider.go`) that prefers the gRPC long-poll but
**falls back to direct chain polling** (`chain_runner.go`), with probing, stale-detection,
failback counting, and event emission (`runner_events.go`). Hoisting that into `common`
touches devshardd's live runtime-params path and risks regressions, for **no testenv
benefit**. So it's tracked as a Phase 12 follow-up (alongside the dapi/edge-api reuse).

Interaction with Phase 2b: Phase 2b already moves the **chain *fetcher*** (the
`Params`/`EpochInfo` → `Snapshot` mapping) and the `Snapshot` type into `common`. When
Phase 12 eventually consolidates the client, it will reuse those Phase 2b pieces — so the
remaining Phase 12 work is just the long-poll *loop + adaptive supervisor*, not the
snapshot/transport plumbing.
