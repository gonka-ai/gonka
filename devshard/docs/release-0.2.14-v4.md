# Release guide: `devshard-0.2.14-v4`

Operator-facing notes for the v4 line: multi-instance HA, Postgres storage,
gRPC-only gateway chain transport, versionless observability, **same-name
binary rolling updates** (blue/green + drain), and rollout constraints for
mixed pre-v4 / v4 estates.

Detailed deploy verification: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).
Observability: [pr-versionless-observability.md](./pr-versionless-observability.md).
Rolling updates: [rolling-update.md](./rolling-update.md).

---

## Overview

v4 is the first version intended to run **N `versiond` / `devshardd` replicas**
behind **versiond-router** on **shared Postgres**, with sticky session routing
and validation-lease exclusivity. Pre-v4 approved versions stay **single-host**
(SQLite) until retired.

The gateway (`devshardctl`) talks to the chain over **gRPC only** — LCD REST
create/settle paths are gone.

Public observability is **versionless**: dashboards scrape
`/devshard/sessions|stats|metrics` without binding protocol version. Only the
escrow owner binds via signed chat. Legacy `/devshard/{version}/…` obs URLs keep
working via join-proxy internal rewrite.

When governance publishes a **new binary under the same version name** (name
unchanged, only `sha256` changes), `versiond` performs a **blue/green child
swap with drain** instead of stop-then-start: the new `devshardd` must pass
admin `/ready` and public `/healthz` before receiving traffic; already-accepted
requests (including SSE) finish on the old generation. Overlap requires both
binaries to report `postgres` via `--print-storage-mode`; SQLite / hybrid /
unknown modes fall back to exclusive stop/start.

---

## What's in this release

| Area | Change |
| --- | --- |
| **HA topology** | Multi-versiond + `versiond-router` (consistent-hash on session/escrow id); optional legacy pin for pre-HA versions |
| **Storage** | `DEVSHARD_STORAGE_MODE=postgres` fail-closed; boot migrate local SQLite → PG for **this** version’s data dir |
| **Rolling updates** | Same-name SHA swap: blue/green overlap + drain when both children report `postgres`; else stop/start. Private admin `/ready`, `/drain`, `/drain/status` |
| **Validation** | Shared Postgres leases (`devshard_validation_leases`); SQLite leases are no-ops |
| **Gateway transport** | Chain queries + escrow tx via gRPC; `--chain-rest` / `DEVSHARD_CHAIN_REST` removed |
| **Query fallback** | edge-api / devshardd / gateway chain queries fall back to CometBFT RPC when gRPC is down, and probe back every 30 min; txs stay gRPC-only |
| **Settle confirm** | Settle waits for DeliverTx (`GetTx`) after SYNC CheckTx — same pattern as create |
| **Failover** | Router retries another HA peer on first upstream 502 / connect failure |
| **HA diff/persist** | Idempotent identical `AppendDiff` (fork on conflict); lazy RAM reconcile from PG on nonce gap; persist-first so a failed write cannot leave memory ahead of Postgres |
| **Versionless obs** | Obs GETs never bind; owner chat binds; join rewrite + PG session lookup; `devshard_obs` rate limit |
| **Status field** | Gateway `protocol_version` → `session_version` (bind / settlement tag) |
| **Tier A `/v1` reads** | Served by **edge-api** on new proxies; same handlers still dual-served on **dapi** as deprecated |
| **testenv** | Named stack behavior tests plus G1–G4, A1–A4, and versiond rolling-update citest (see [testenv/docs/scenarios.md](../testenv/docs/scenarios.md)) |

---

## Breaking / operator-facing changes

### Gateway CLI: `--chain-rest` → `--chain-grpc`

`devshardctl` no longer accepts LCD REST for chain I/O.

| Before (pre-v4 / r1) | After (r2 / v4) |
| --- | --- |
| `--chain-rest` / `DEVSHARD_CHAIN_REST` (LCD, e.g. `:1317`) | `--chain-grpc` / `DEVSHARD_CHAIN_GRPC` (gRPC, e.g. `:9090`) |
| Optional `DEVSHARD_TX_QUERY_REST` | Unused — tx confirm uses gRPC `GetTx` |
| Admin / persisted `chain_rest` | Deprecated and **ignored**; use `chain_grpc` / env |

**Migration:**

1. Point gateway at chain gRPC: `--chain-grpc host:9090` or
   `DEVSHARD_CHAIN_GRPC=host:9090` (also accepts `NODE_GRPC_URL`).
2. Remove `--chain-rest` and `DEVSHARD_CHAIN_REST` / `DEVSHARD_TX_QUERY_REST`
   from compose, systemd, and scripts.
3. Optional TLS: `CHAIN_GRPC_TLS=true|1|yes` (default remains plaintext).
4. Override chain id when not mainnet: `DEVSHARD_CHAIN_ID` (default
   `gonka-mainnet`).

Join / testenv compose already seeds `DEVSHARD_CHAIN_GRPC` only.

### edge-api / devshardd / gateway: chain query fallback to CometBFT RPC

edge-api, devshardd and the devshardctl gateway still prefer direct chain gRPC
(`:9090`), but their chain **queries** now fall back to CometBFT RPC (`:26657`)
when gRPC is unreachable. This covers nodes with `:9090` not published, bound to
`localhost`, or firewalled, which previously made every query fail. A node with
gRPC fully disabled is covered for module queries only — see the limitation
below.

Only the gRPC endpoint is required. The RPC endpoint defaults to the same host on
port `26657`, so an existing deployment gains the fallback without new settings:

| Service | gRPC | CometBFT RPC |
| --- | --- | --- |
| edge-api | `CHAIN_GRPC_URL=node:9090` (required) | `CHAIN_RPC_URL` (default `http://<gRPC host>:26657`) |
| devshardd | `NODE_GRPC_URL` (default `<NODE_HOST>:9090`) | `NODE_RPC_URL` (default `http://<NODE_HOST>:26657`) |
| gateway | `DEVSHARD_CHAIN_GRPC` / `NODE_GRPC_URL` | `DEVSHARD_CHAIN_RPC` / `NODE_RPC_URL` (default `http://<gRPC host>:26657`) |

edge-api still **exits at startup** if `CHAIN_GRPC_URL` is missing. It used to
default that endpoint to `localhost:9090`, which turned a misconfigured stack
into a stream of connection errors at query time. A derived `CHAIN_RPC_URL` is
logged once at startup so an unexpected host is visible in the logs.

How the switch works:

1. Queries start on gRPC.
2. A transport failure (gRPC `Unavailable`, closed connection) moves queries to
   RPC and retries that same request there, so the caller sees no error.
3. Queries stay on RPC for 30 minutes. Application errors never trigger a
   switch, so a `NotFound` response does not cost a probe.
4. After 30 minutes exactly one request is routed over gRPC as a probe. If gRPC
   answers, it becomes primary again; if not, that request retries on RPC and
   another 30 minutes starts.

A probe that is canceled or times out proves nothing about gRPC, so it neither
restores gRPC nor consumes the probe window: the next request probes again.

The recovery probe means a node that enables gRPC later is picked up without
restarting edge-api or devshardd.

When a query fails on both transports the returned error names both causes, so a
fully unreachable node does not look like a plain ABCI failure.

Transactions are unaffected: devshardd's tx manager and the gateway keep using
the direct gRPC connection, and gRPC streams have no RPC equivalent so they
also stay direct. Broadcasting a transaction over the *query* connection is
rejected outright rather than served: Cosmos's `client.Context` turns a
`BroadcastTx` into a CometBFT broadcast instead of a query, and the fallback
retries a failed request on the other transport — which for a broadcast means
submitting the same signed transaction twice.

#### Seeing that a service is on the fallback

Both transports are instrumented the same way, so switching changes what the
telemetry says rather than silencing it:

| Signal | On gRPC | On CometBFT RPC |
| --- | --- | --- |
| `chain.grpc.query` spans | `chain.transport=grpc` | `chain.transport=comet-rpc` |
| `chain.query.transport.active` gauge | `grpc`=1, `comet-rpc`=0 | `grpc`=0, `comet-rpc`=1 |
| Logs | — | one warning on the switch, then one per failed 30-min probe |

The gauge is the thing to alert on: `comet-rpc`=1 held for longer than a brief
blip means the node's `:9090` has been unreachable that whole time. The
per-probe warning is the same signal in the logs, so a service that has been
degraded for a week no longer looks identical to one that never fell back. Both
report from process start, so an absent `comet-rpc` series means healthy rather
than unreported.

#### Known limitation: CometBFT service queries

RPC-mode queries run as ABCI queries against the node's gRPC query router.
Module queries (inference, BLS, restrictions) are always registered there, so
they work on any node. The CometBFT service queries (node info, blocks,
validator sets, and the ABCI store queries behind epoch proofs) are registered
only when the node has `grpc.enable` or `api.enable` set in `app.toml`, because
the SDK gates `RegisterTendermintService` on those two flags.

`grpc.enable` defaults to **true** and nothing in this repo turns it off, so
every deployment we ship resolves these queries normally — including the cases
this fallback targets, where gRPC is enabled but bound to `localhost` or not
published. A node with **both** flags disabled is the exception: module queries
would keep working while `/v1/versions`, `/v1/epochs/{epoch}/participants`,
`/v1/debug/verify/{height}`, `POST /v1/verify-block`, and devshardd's startup
chain-ID lookup returned `Unimplemented`.

Closing that gap means serving those methods from CometBFT's own `/status`,
`/block`, `/validators` and `/abci_query` endpoints, which are available
regardless of `app.toml`. That is deliberately not done here: no node we operate
is configured that way, and the change is not worth carrying untested against a
real gRPC-disabled node. `common/chain.newRPCQueryConn` documents the same
constraint, and `TestRPCQueryConn_CometServiceGoesThroughTheABCIRouter` pins the
behaviour so the limitation is visible in the test suite rather than only here.

### Gateway status: `protocol_version` → `session_version`

The gateway runtime status field **`protocol_version`** (always `"1"` from the
removed client routing enum) is replaced by **`session_version`**.

| Before | After |
|--------|-------|
| `protocol_version: "1"` | `session_version: "v2"` (example) |

**`session_version`** is the session bind tag from
`EscrowState.StateRootAndProtocolVersion` — the same value used for state-root
hashing and on-chain settlement (`state_root_and_protocol_version`). That is
what external tools should display or assert on, not the old `"1"` enum.

**Migration:** update any monitor, dashboard, or script that polled gateway
`/status` (or equivalent) for `protocol_version` to read **`session_version`**
instead.

Gateway devshard config/API: the old **`protocol_version`** request field is
**`route_prefix`** (e.g. `/devshard/v2`). The legacy HTTP mount
`/v1/devshard` is no longer supported; clients must use `/devshard/<version>/`.

### Storage backend: Postgres required for HA / multi-instance

To run **more than one** `versiond` / `devshardd` instance for the same HA
participant, you **must** use Postgres:

| Deployment | Backend |
|------------|---------|
| Single instance / local dev | SQLite **or** Postgres |
| **Multiple HA versiond replicas** | **Postgres required** (`DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` / `PG*`) |

Why: validation leases and shared session state only work on a shared store.
SQLite is single-writer; its lease store is a no-op under multi-instance.

Set `Devshard-Ha: true` (injected by the router when multi-host HA) requires
literal `DEVSHARD_STORAGE_MODE=postgres` and `PGHOST` or the child returns
**503**.

See [storage-design.md](./storage-design.md) and
[rolling-update.md](./rolling-update.md).

### Rolling updates (same name, new sha256)

Governance still updates `DevshardEscrowParams.ApprovedVersions` the same way
(name + binary URL + sha256). What changes is **how versiond applies** a
same-name SHA change:

| Storage mode (both old + new `--print-storage-mode`) | Swap behavior |
| --- | --- |
| Exactly `postgres` | Blue/green: start new child on a new port → wait admin `/ready` + public `/healthz` → route new traffic to new generation → drain old (proxy leases then `/drain/status`) → SIGTERM |
| `sqlite`, `hybrid`, `auto`/unknown, legacy binary without the flag | Exclusive stop/start (no overlapping children) |

Operator knobs (all defaulted; see [rolling-update.md](./rolling-update.md)):

| Env | Default | Role |
| --- | --- | --- |
| `VERSIOND_READY_TIMEOUT` | `60s` | Abort swap if incoming child never becomes ready (old keeps serving) |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | Max wait for old proxy leases + lifecycle inflight |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | Legacy no-status cushion / process kill backstop |
| `DEVSHARD_SHUTDOWN_GRACE` | `10m` | `devshardd` graceful HTTP shutdown after SIGTERM |

`versiond /healthz` shows per-child `status` (`running` / `draining`), `sha256`,
and `binary_version` during a swap. Whole-`versiond` host evacuation is a
**separate** layer (router drain) — not part of a child SHA swap.

Manual walkthrough: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) **§7**.

### Versionless observability (bind safety + canonical URLs)

Observability GETs no longer call `CreateSession`. Unbound escrow obs returns
**404**; the first signed owner `POST …/chat/completions` binds the chosen
version. Prefer canonical monitor paths:

| Preferred | Legacy (still works) |
| --- | --- |
| `/devshard/sessions/{id}/diffs` | `/devshard/{version}/sessions/{id}/diffs` (join proxy rewrite, no `Location`) |
| `/devshard/stats/shards/{id}` | `/devshard/{version}/stats/shards/{id}` |
| `/devshard/metrics` | `/devshard/{version}/metrics` |

| Path | Meaning |
| --- | --- |
| `/devshard/healthz` | versiond supervisor |
| `/devshard/{version}/healthz` | that child (not rewritten) |

With Postgres, versiond routes versionless session obs via `sessions.version`
(fan-out fallback if lookup disabled / SQLite). Join proxy rate-limits obs GETs
separately from chat:

| Env | Where | Default |
| --- | --- | --- |
| `DEVSHARD_OBS_RATE_LIMIT_RPS` | join proxy | 10 |
| `DEVSHARD_OBS_BURST` | join proxy | 20 |
| `VERSIOND_DISABLE_SESSION_LOOKUP` | versiond | unset (lookup on when `PGHOST` set) |

Manual walkthrough: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §4.
Design note: [pr-versionless-observability.md](./pr-versionless-observability.md).

---

## High-availability deployment

### Target topology

```text
clients / gateway (devshardctl)
       │
       ▼
 public edge proxy  (deploy/join `proxy` — nginx)
       │
       ├── /v1/ Tier A ──► edge-api  (or edge-api-router)
       └── /devshard/ ──► versiond-router  (sticky hash; first-502 failover)
                                 │
                                 ├── versiond-0 ──► devshardd (per approved version)
                                 ├── versiond-1 ──► …
                                 └── …
                                        │
                                        └── shared Postgres
```

**Deprecated dapi dual-serve:** New join proxies steer Tier A `/v1/` reads to
**edge-api** (see `EDGE_API_ROUTE_PATHS` in `proxy/entrypoint.sh`). The same
handlers (from `common/queryapi`) remain mounted on **decentralized-api** for
operators still running **pre-v4 / old proxy** configs that forward `/v1/*` to
dapi. Those dapi responses set `Deprecation: true` (and a `Link` successor hint);
prefer edge-api. Also still on dapi (deprecated): `/v1/bridge/block/latest` and
`/v1/supply/total` (not on edge-api Tier A).

| Variable | Role |
| --- | --- |
| `VERSIOND_HOSTS` | HA pool hostnames |
| `VERSIOND_LEGACY_HOST` | Single host for pre-HA SQLite versions (default: first of `VERSIOND_HOSTS`) |
| `VERSIOND_NON_HA_VERSIONS` | Version path segments pinned to legacy (join default: `v1 v2 v3`) |
| `VERSIOND_SERVICE_NAME` | Edge proxy upstream for `/devshard/` (set to `versiond-router` by HA overlay) |
| `EDGE_API_SERVICE_NAME` | Edge proxy upstream for Tier A `/v1/` (default `edge-api`; multi → `edge-api-router`) |
| `DEVSHARD_STORAGE_MODE=postgres` | Fail-closed shared storage for HA-capable versions |
| `DEVSHARD_POSTGRES_PASSWORD` / `PG*` | Shared DB |
| `DEVSHARD_CHAIN_GRPC` | Gateway chain gRPC (no LCD) |
| `KEY_NAME` (+ shared keyring) | **Same** on every HA replica of one participant |

### HA failover and shared-Postgres diffs

On multi-instance HA, sticky routing plus shared Postgres means a standby can
have **stale in-memory** session state after the primary advanced durable
nonces. Catch-up after `proxy_next_upstream` failover must not fail with
Postgres unique violations (`23505`) for already-durable identical diffs.

What operators should expect on v4:

1. **Identical replay is OK** — re-persisting the same nonce/payload succeeds;
   different bytes at the same nonce fail loudly (`diff_fork_detected` metric /
   log). Alert if that counter is non-zero outside incident response.
2. **Stale standby self-heals** — when catch-up skips ahead of RAM, the host
   fast-forwards from Postgres (`reconcile_fast_forward`) then applies the tip.
3. **Persist-first** — host and gateway validate/preview, write the diff, then
   commit memory. A Postgres blip retries briefly (`diff_persist_retry`); hard
   failure leaves in-memory nonce unchanged (no permanent “RAM ahead of PG”
   hole). Retry the request after PG recovers.
4. **Gateway catch-up API unchanged** — still `hostSyncNonce`-driven toward
   group slots; no per-versiond-replica addressing from the gateway.

Automated coverage: `TestHAStaleStandbyCatchupIdempotent` in testenv citest
(see [testenv/docs/scenarios.md](../testenv/docs/scenarios.md)). Manual
walkthrough: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) **§3.6**.

#### Crash recovery vs ML execution

Persist-first and durable diff replay cover the **journal / state machine**
only. After a crash (or failover onto a cold process), recovery **replays
diffs**; it does **not** re-run ML automatically.

| Recovered protocol state | What happens on a later client request |
| --- | --- |
| Inference still `Pending` in the SM, this host is the executor, client retries with payload | Host **can execute again** (same reconnect behaviour as before persist-first). Dedup of in-flight / cached bodies is **in-process** (`executing` / `completedResponses`), not durable across restart. |
| Inference already `Finished` or timed out in later diffs | `signReceipt` will **not** start execution again. |

In-flight payload, live ML job attachment, and on-disk `CachedResponseBody`
across `devshardd` instances are **not** solved in this release. Work to
restore interrupted inferences (decouple ML from the user stream, same-nonce
gateway reconnect, durable executor state for HA) is tracked in
[gonka-ai/gonka#1466](https://github.com/gonka-ai/gonka/issues/1466).

### Join docker-compose (must bump for v4)

Production join stack lives under **`deploy/join/`**. Operators must pull/update
these files (or equivalent k8s manifests) when rolling v4 — image tags, the
**edge-api** service, edge **proxy** `EDGE_API_*` / `VERSIOND_SERVICE_NAME`, and
the HA overlays.

| File | Role |
| --- | --- |
| [`deploy/join/docker-compose.yml`](../../deploy/join/docker-compose.yml) | Base: node, api (dapi), **edge-api**, versiond, **proxy** (edge), explorer, … |
| [`deploy/join/docker-compose.versiond.yml`](../../deploy/join/docker-compose.versiond.yml) | HA: postgres + versiond2 + versiond-router; proxy → router |
| [`deploy/join/docker-compose.edge-api-multi.yml`](../../deploy/join/docker-compose.edge-api-multi.yml) | Optional: edge-api2/3 + edge-api-router; proxy → router |
| [`deploy/join/docker-compose.devshard-gateway.yml`](../../deploy/join/docker-compose.devshard-gateway.yml) | Optional gateway |

```bash
# Baseline join
cd deploy/join
docker compose -f docker-compose.yml up -d

# HA versiond (+ shared Postgres)
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d

# Optional multi edge-api
docker compose -f docker-compose.yml -f docker-compose.edge-api-multi.yml up -d
```

**Base `edge-api` + edge `proxy` (from `docker-compose.yml`):**

```yaml
  edge-api:
    container_name: edge-api
    image: ghcr.io/product-science/edge-api:0.2.13
    environment:
      - EDGE_API_PORT=18080
      - CHAIN_GRPC_URL=node:9090
      - CHAIN_RPC_URL=http://node:26657
      - EDGE_API_OTEL_ENABLED=${EDGE_API_OTEL_ENABLED:-false}
      - OTEL_ENDPOINT=${OTEL_ENDPOINT:-}
      - OTEL_HEADERS=${OTEL_HEADERS:-}
    depends_on:
      - node
    restart: always

  proxy:
    container_name: proxy
    image: ghcr.io/product-science/proxy:0.2.13
    ports:
      - "${API_PORT:-8000}:80"
      - "${API_SSL_PORT:-8443}:443"
    environment:
      # … other proxy env …
      - EDGE_API_SERVICE_NAME=${EDGE_API_SERVICE_NAME:-edge-api}
      - EDGE_API_PORT=${EDGE_API_PORT:-18080}
    depends_on:
      # … node, api, versiond, explorer, …
      edge-api:
        condition: service_started
```

**HA overlay (`docker-compose.versiond.yml`) — points edge proxy at versiond-router:**

```yaml
  versiond-router:
    container_name: versiond-router
    image: ghcr.io/product-science/versiond-router:0.2.13
    environment:
      - VERSIOND_HOSTS=${VERSIOND_HOSTS:-versiond versiond2}
      - VERSIOND_PORT=8080
      - VERSIOND_LEGACY_HOST=${VERSIOND_LEGACY_HOST:-versiond}
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS:-v1 v2 v3}

  proxy:
    environment:
      - VERSIOND_SERVICE_NAME=versiond-router
      - VERSIOND_PORT=8080
```

**Multi edge-api overlay (`docker-compose.edge-api-multi.yml`):**

```yaml
  edge-api-router:
    container_name: edge-api-router
    image: ghcr.io/product-science/edge-api-router:0.2.13
    environment:
      - EDGE_API_HOSTS=${EDGE_API_HOSTS:-edge-api edge-api2 edge-api3}
      - EDGE_API_PORT=18080

  proxy:
    environment:
      - EDGE_API_SERVICE_NAME=edge-api-router
```

Bump image tags (`edge-api`, `proxy`, `versiond`, `versiond-router`, …) to the
v4 release tag when publishing. Full file contents: see the paths in the table
above (do not edit only env without refreshing compose from the release branch).

### Images / release tooling note (`edge-api`)

| Artifact | Local build | In `make release` | Upgrade zip (`make build-for-upgrade`) | GH Actions publish to GHCR |
| --- | --- | --- | --- | --- |
| `ghcr.io/product-science/edge-api` | `make edge-api` / `edge-api-build-docker` | Yes (`edge-api-release` → build + `docker-push`) | Yes → `public-html/v2/edge-api/edge-api-amd64.zip` (also uploaded by `publish_upgrade_binaries.yml` on `release/v*` tags) | **No** dedicated push workflow — use local `VERSION=<tag> make edge-api-release` (same as other join images) |
| `ghcr.io/product-science/edge-api-router` | `make edge-api-router-build-docker` | **No** — still not a `release` dependency | **No** | **No** |
| `ghcr.io/product-science/proxy` (edge proxy) | `make proxy-build-docker` | Yes (`proxy-release` → buildx `--push`) | **No** | Manual via `make release` / proxy Makefile |

CI today:

- [`.github/workflows/verify.yml`](../../.github/workflows/verify.yml) — `build-and-test-edge-api` (compile + `scripts/validate-edge-api.sh`); does **not** push images.
- [`.github/workflows/test-workflow.yml`](../../.github/workflows/test-workflow.yml) — builds via `make build-docker`, saves `edge-api.tar` / `edge-api-router.tar` as **job artifacts** for Testermint (retention 1 day); not a GHCR release.
- [`.github/workflows/docker-build.yml`](../../.github/workflows/docker-build.yml) — only decentralized-api + inference-chain (different image names under `ghcr.io/<owner>/…`).
- [`.github/workflows/publish_upgrade_binaries.yml`](../../.github/workflows/publish_upgrade_binaries.yml) — on `release/v*` tag push, uploads upgrade zips (including `edge-api-*.zip`) to `product-science/race-releases`; does **not** push GHCR images.

Publish the join image with:

```bash
VERSION=<tag> make edge-api-release
# or as part of the full join release:
VERSION=<tag> make release
```

### Routing rules (critical)

| Version | Upstream | Storage |
| --- | --- | --- |
| Listed in `VERSIOND_NON_HA_VERSIONS` (e.g. v1–v3) | **`VERSIOND_LEGACY_HOST` only** | Local SQLite |
| Any other (v4+, future) | Sticky hash across **all** healthy `VERSIOND_HOSTS` | Shared Postgres; `Devshard-Ha: true` |

Pre-v4 binaries do **not** implement fail-closed Postgres + boot migrate for
already-deployed data dirs. Do **not** put `< v4` in the multi-instance pool.
Flipping `DEVSHARD_STORAGE_MODE=postgres` does **not** migrate sessions owned by
old binaries — only the v4 (or newer) child’s own data dir can boot-migrate.

Debug headers: `X-Upstream-Addr`, `X-Versiond-Backend`
(`versiond_legacy` vs `versiond_ha_pool`).

### Rollout phases (summary)

**Phase A — Single-instance baseline**

1. Refresh `deploy/join/` compose; deploy v4 images (`proxy` / edge, `edge-api`,
   `versiond`, `versiond-router`, gateway, …).
2. Pin pre-HA versions: `VERSIOND_NON_HA_VERSIONS=v1 v2 v3`.
3. Bring up shared Postgres; set `postgres` mode on HA-capable children.
4. Approve/force **v4**; leave pre-v4 on the legacy host’s SQLite volumes.

**Phase B — Expand HA for v4+**

1. Add versiond-1…N with the same Postgres credentials and **same `KEY_NAME`**.
2. Keep NON_HA pin; confirm HA paths show `versiond_ha_pool` + multi upstream.
3. Exercise stickiness, kill-one-host failover (§3), the optional lease race (§2),
   and a same-name SHA rolling update on a Postgres HA version (§7).

**Phase C — Retire pre-v4 (later)**

Drain old version paths; remove from `VERSIOND_NON_HA_VERSIONS` only when idle
(or after an out-of-band migrate tool exists).

Full checklists and negative proofs (multi-host + sqlite → 503, migrate inventory):
[v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §1.

### Operator test plans (same doc)

| Plan | Proves |
| --- | --- |
| §1 Test deployment | NON_HA pin, sqlite→HA-fail→migrate→HA, mixed binding |
| §2 Validation race | Same-key HA: one lease row per inference |
| §3 High availability | Kill versiond → survivors serve (first-502); restart rejoins |
| §4 Versionless observability | Unbound obs 404; owner chat binds; rewrite; PG route; rate limit |
| §5 Edge-api / deprecated dapi | New proxy → edge-api; old proxy → dapi dual-serve |
| §6 Escrow long-poll warm | Host-events cache; inference with inference-node down |
| §7 Rolling update | Same-name SHA: in-flight SSE survives; Postgres overlap; hybrid stop/start |

---

## Upgrade / rollout checklist

- [ ] Refresh **`deploy/join/`** compose from the release branch (base + versiond /
      edge-api overlays); bump image tags including **edge-api** and **proxy**
- [ ] Edge proxy: `EDGE_API_SERVICE_NAME` / `EDGE_API_PORT` set; HA overlay sets
      `VERSIOND_SERVICE_NAME=versiond-router`
- [ ] Publish / pull `ghcr.io/product-science/edge-api:<tag>` via
      `VERSION=<tag> make edge-api-release` (or full `make release`)
- [ ] Gateway: replace `--chain-rest` / `DEVSHARD_CHAIN_REST` with `--chain-grpc` /
      `DEVSHARD_CHAIN_GRPC`
- [ ] Shared Postgres up; `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` on HA children
- [ ] `VERSIOND_NON_HA_VERSIONS` set for every still-deployed pre-HA version
- [ ] HA replicas share one `KEY_NAME` / keyring for the participant
- [ ] Per-versiond SQLite volumes remain **distinct**; Postgres is shared
- [ ] Monitors read `session_version`, not `protocol_version`
- [ ] Prefer versionless obs URLs (`/devshard/sessions|stats|metrics`); confirm
      legacy `/devshard/{v}/…` still 200 with no public redirect
- [ ] Join proxy: set/tune `DEVSHARD_OBS_RATE_LIMIT_RPS` / `DEVSHARD_OBS_BURST` if needed
- [ ] Smoke: chat/settle on a NON_HA path and on v4 HA path; Tier A `/v1/` via edge-api
- [ ] Confirm old proxies (no `EDGE_API_SERVICE_NAME`) still reach Tier A via
      deprecated dapi dual-serve; new proxies should use edge-api
- [ ] Optional: run §2 lease race, §3 kill/restart (incl. **§3.6** stale-standby
      catch-up / persist hole), §4 versionless obs, and **§7 rolling update**
      (same-name SHA with a long stream) from the deploy test plan
- [ ] Confirm HA children report `postgres` via `devshardd --print-storage-mode`
      before expecting blue/green overlap on a governance SHA bump

---

## Known follow-ups

### Interrupted inference resume across crash / HA

Durable diffs make protocol status (`Pending` / `Finished` / timeout)
recoverable, but executor proof path after drop, reboot, or failover still
depends on process-local state. See
[gonka-ai/gonka#1466](https://github.com/gonka-ai/gonka/issues/1466) for the
intended fixes (shared accept/execute path, ML lifetime independent of the
client stream, same-nonce gateway reconnect, durable payload / receipt /
cached body for cross-instance resume).

### Escrow ID: `string` vs `uint64`

On this branch, devshard keeps **`escrowID` as `string`** in the bridge API,
session storage keys, HTTP paths, and devshard protocol messages. On-chain
`DevshardEscrow.id` remains **`uint64`**; adapters parse/format at the chain
boundary.

Unifying to **`uint64` throughout** is a protocol / wire contract change — bundle
with larger protocol bumps rather than a standalone migration.

---

## Related docs

| Doc | Use |
| --- | --- |
| [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) | Deploy + operator test plans (§1–§7; HA diff/persist in §3.6) |
| [pr-versionless-observability.md](./pr-versionless-observability.md) | Versionless obs design + unit/manual checklist |
| [high-availability-architecture.md](./high-availability-architecture.md) | Current runtime topology |
| [storage-design.md](./storage-design.md) | Storage mode selection |
| [rolling-update.md](./rolling-update.md) | Same-name SHA blue/green + drain (Track A) |
| [testenv/docs/chain-transport-consolidation.md](../testenv/docs/chain-transport-consolidation.md) | REST → gRPC migration detail |
