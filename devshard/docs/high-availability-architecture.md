# Architecture: edge-api, versiond, devshardd, decentralized-api

Current runtime architecture of the Gonka node stack after the
`pixelplex-refactoring` → `r2` merge. This document describes **what exists
today**. For the planned high-availability evolution see
[proposals/high-availability.md](./proposals/high-availability.md); for binary
rollout mechanics see [rolling-update.md](./rolling-update.md).

Related: [merge-plan.md](./merge-plan.md) (runtime topology),
[pixelplex-changes.md](./pixelplex-changes.md) (edge-api extraction),
[storage-design.md](./storage-design.md) (storage-mode selection).

---

## 1. Top-level topology

The public listener is `proxy-router/`, a host-local HAProxy. It distributes
TCP connections across private nginx policy workers, which retain TLS, HTTP/2,
CORS, rate limits, rewrites, and the existing on-chain route policy. The policy
workers send only the two horizontally scaled API paths back to private
HAProxy frontends:

```text
 clients
    |
    v
 proxy-router (public HAProxy, :80/:443)
    |
    | TCP + PROXY v2, active checks
    v
 proxy-policy x N (private nginx)
    |
    +-- ordinary /v1, chain, dashboard --> existing services
    |
    +-- Tier A /v1 queries --> proxy-router :18082
    |                           |
    |                           +--> ready edge-api replicas
    |
    +-- /devshard/* ------> proxy-router :18081
                                |
                                +--> ready versiond-router slot 0
                                +--> ready versiond-router slot 1
                                +--> ready versiond-router slot 2
                                         |
                                         | identical consistent hash
                                         v
                                  versiond hosts --> devshardd children
                                         |
                                  shared PostgreSQL for HA versions
```

`proxy-router` selects a ready router replica with `leastconn`. Every
`versiond-router` independently computes the same consistent-hash placement
from the escrow ID and the same DNS-discovered `versiond` pool. Router replicas
hold no shared routing state and do not need Redis, leader election, or
replica-to-replica communication. Both router tiers independently project
protocol names from dapi's existing governance `/versions` feed into local
pre-rendered backend slots.

The public `proxy-router` process is still a **single host-level failure
domain**. This deployment protects against failure or replacement of an inner
router or policy worker, not against loss of the host, Docker daemon, public
listener, or its network. A future multi-host ingress (provider LB, VIP, or
Kubernetes Service) belongs above this layer and is outside this change.

| Path (public) | Backend | Purpose |
|---------------|---------|---------|
| 22 Tier A `/v1/*` query routes | `proxy-router :18082` → ready `edge-api` | Read-only chain queries |
| Other `/v1/*`, `/api/v1/*` | `dapi` (`api:9000`) | Chat/inference, PoC, payloads, bridge, identity |
| `/devshard/<version>/sessions/...` (protocol) | `proxy-router :18081` → `versiond-router` fleet → `versiond` → `devshardd` | Chat, gossip, payloads — version binds on owner chat |
| `/devshard/sessions/...`, `/devshard/stats/...`, `/devshard/metrics` | `versiond` → bound/`primary` child | Versionless public observability (no bind) |
| `/devshard/<version>/sessions/.../diffs\|mempool\|signatures` (legacy) | join proxy **internal rewrite** → versionless | Backward-compat for scrapers |
| `/v1/devshard/*` (legacy) | rewritten → `/devshard/v1/*` → versiond | Backward-compat |
| `/chain-rpc`, `/chain-api`, `/chain-grpc` | `chain-node` | Direct chain access |

HTTP policy is rendered by `proxy/entrypoint.sh` into
`proxy/nginx.unified.conf.template`. Tier A locations are emitted before the
generic `/v1/ → dapi` location. `proxy-router/entrypoint.sh` separately renders
the public and private HAProxy pools. The two processes have different owners:
nginx decides *which service* a path belongs to; HAProxy decides *which healthy
replica* of that service receives it.

### HAProxy service pools

Membership and eligibility are derived from runtime state:

- `server-template` follows the `proxy-policy`, `versiond-router-fleet`,
  `versiond-pool`, and `edge-api-pool` DNS aliases;
- a TCP listener check gates private nginx policy workers, while active
  `/readyz` checks gate application and router pools; `init-state fully-down`
  keeps every new slot out until its first successful check;
- the top HAProxy asks each inner router `GET /readyz?version=<v>` on its private
  admin port, and each inner router asks every `versiond` the same route-specific
  question;
- `VERSIOND_VERSIONS` is only a bootstrap floor. New approved names are added to
  both tiers through local Unix Runtime API sockets without a container reload;
  a map entry is published only after its backend health checks are enabled;
- each tier atomically persists its last fully projected governance snapshot;
  replacement processes validate and pre-render a fresh snapshot before
  listening, while stale or corrupt cache data falls back to the bootstrap floor;
  cached additions continue to consume their bounded dynamic slots after a
  restart, and a capacity reduction below that fresh state fails startup;
- consistent hashing with `hash-key addr` keeps escrow placement stable across
  DNS answer order and inner-router restarts, while top-level router and
  `edge-api` selection use `leastconn` for long requests;

HAProxy and the catalog reconciler inside one router container intentionally run
as the same unprivileged Unix user. They therefore form one container-level trust
domain: mode `0600` on the Runtime API socket protects it from outside the
container, not from sibling processes inside it. The shipped Compose services
drop Linux capabilities and enable `no-new-privileges`. Strong process-to-process
isolation would require a separate sidecar or a narrow privileged broker and is
not part of this deployment model.
- marking a backend unready affects new selections but does not move or close
  an established stream.

Both HAProxy layers retry connection failures, empty responses, and `502` only.
They disable L7 replay for non-idempotent methods: once a POST may have reached
an application, infrastructure cannot safely guess whether retrying it would
execute the operation twice. End-to-end exactly-once semantics therefore still
require an application idempotency key. Established SSE streams remain on the
connections that accepted them and are not moved during drain.

---

## 2. edge-api — stateless read-only chain query API

`edge-api/` is a small standalone service extracted from dapi (see
[pixelplex-changes.md](./pixelplex-changes.md)). It owns the **22 Tier A
`/v1/` query routes** (status, models, pricing, participants, epochs,
poc-batches, restrictions, BLS, bridge addresses, verify-proof/block, debug
helpers, versions).

- **Transport:** chain gRPC via `common/chain.Client` (`CHAIN_GRPC_URL`, e.g.
  `node:9090`, required at startup); a few routes use CometBFT gRPC
  (`cmtservice`) and ABCI store queries. When gRPC is unreachable, queries fall
  back to CometBFT RPC (`CHAIN_RPC_URL`, default `http://<gRPC host>:26657`) and
  probe gRPC again every 30 minutes. Which transport is live shows up on the
  `chain.query.transport.active` gauge and the `chain.transport` span attribute.
  RPC-mode queries go over ABCI, so the CometBFT service routes need the node to
  have `grpc.enable` or `api.enable` set — true by default, but see the
  limitation in the v4 release notes.
- **Stateless:** no DB, no keyring, no ML nodes, no broker. Each request is
  served directly from the chain. Dependencies are `common/chain`,
  `common/logging`, `common/utils`, `edge-api/observability`.
- **Entry / wiring:** `edge-api/cmd/edge-api/main.go`,
  `edge-api/internal/server/server.go`, handlers under `edge-api/queryapi/`.
- **Port:** `EDGE_API_PORT` (default `18080`).

### Multi-instance today

Because edge-api holds no state, it scales horizontally already:

- the private `proxy-router :18082` frontend balances the `edge-api-pool` DNS
  alias directly; the former dedicated `edge-api-router` hop is not part of the
  deployed steady-state topology;
- active `/readyz` checks remove an instance that cannot reach the chain and
  admit it again after recovery;
- A stopping instance reports unready for `EDGE_API_DRAIN_ANNOUNCE` while it
  keeps serving, then finishes accepted queries within
  `EDGE_API_SHUTDOWN_BUDGET`, so replacing an instance does not cut queries.
  The announce value is `0` only without a balancer; HA deployments require at
  least `5s` so the router can finish its health-check failure window.
- `deploy/join/docker-compose.edge-api-multi.yml` adds `edge-api2` and
  `edge-api3`, and points private policy workers at `proxy-router :18082`.

> edge-api is the natural foundation for the future HA "chain access layer" — see
> the [HA proposal](./proposals/high-availability.md).

---

## 3. versiond + devshardd — versioned devshard hosts

### versiond (`versioned/`, binary `versiond`)

A supervisor + version-prefix reverse proxy:

- **Version discovery (oracle):** polls `VERSIOND_ORACLE_URL`
  (`VERSIOND_POLL_INTERVAL`, default 30s) for
  `{ versions: [{ name, binary, sha256 }] }`. The source of truth is chain
  governance (`approved_versions`) surfaced by dapi at `:9100/versions`.
  Files: `versioned/internal/oracle/client.go`, `cmd/versiond/main.go`.
- **Child processes:** spawns one `devshardd` per approved version, each on a
  stable local port from `BasePort=5000` (`internal/process/manager.go`,
  `assignPort`). Binaries are downloaded + sha256-verified before launch.
- **Routing:** in-process reverse proxy keyed by the first path segment
  (`/<version>/...`), backed by an `atomic.Value` route table of
  `version → localhost:port` for **running** children only
  (`internal/proxy/proxy.go`, `rebuildRoutes`).
- **Versionless observability:** also serves `/sessions/…/diffs|mempool|signatures`,
  `/stats/…`, `/metrics` without a version prefix. With shared Postgres
  (`PGHOST` / `DATABASE_URL`), session-scoped routes look up
  `sessions.version` and forward to that child; unbound → 404. Without PG
  (SQLite-only), fan-out across children. See
  [versionless-observability-plan.md](./versionless-observability-plan.md).
- **HTTP:** `:8080`, `GET /healthz` (per-child status) + version-prefix /
  versionless obs proxy.
- **Overrides:** `VERSIOND_OVERRIDE_<name>` (local binary), `VERSIOND_FORCE`
  (force-run a version).

### devshardd (`devshard/cmd/devshardd/`)

The standalone devshard **host** process (a versiond child, never a direct
compose service). It runs the per-escrow session protocol:

- **Routes:** `GET /healthz`, `GET /metrics`, and session routes
  `POST /sessions/:id/chat/completions` (**owner bind**), `verify-timeout`,
  `challenge-receipt`, `gossip/*`,
  `GET /sessions/:id/{diffs,mempool,signatures}` (observability — never binds),
  `GET /sessions/:id/payloads` (validator protocol)
  (`devshard/cmd/devshardd/server.go`, `devshard/server/routes.go`).
  Public observability is also reachable versionless via the join proxy /
  versiond (see [versionless-observability-plan.md](./versionless-observability-plan.md)).
- **Chain:** gRPC client (`common/chain`) + CometBFT WebSocket for
  `NewBlock`, `devshard_escrow_created`, `devshard_escrow_settled`; tracks a
  `chain.Phase` (epoch/height). Bridge queries + dispute submission via
  `cmd/devshardd/bridge/chain.go` and `cmd/devshardd/tx/manager.go`.
- **ML nodes:** acquires a locked node through dapi's NodeManager gRPC
  (`common/nodemanager`, `NODE_MANAGER_ADDR` default `:9400`) and forwards
  inference.
- **Process contract:** `--port <N> --data-dir <path>`; the rest via env
  inherited from the versiond container.

### versiond-router (`versiond-router/`)

HAProxy with **consistent hashing on escrow/session ID**, so all requests for one
escrow stick to the same versiond host. Pool membership comes from the
`versiond-pool` DNS alias and health from active `/readyz` checks, so hosts can
be added, drained or removed with no router config change and no reload.
Protocol names come from the same governance `/versions` endpoint used by
`versiond`; approving a new name needs no host-side environment edit or router
rollout. `versiond-router-fleet.sh wait-version <v>` is the machine-readable
post-approval gate for per-host end-to-end capacity.
Streaming responses are not buffered. SSE inactivity is bounded by
`VERSIOND_ROUTER_STREAM_IDLE_SECONDS`; the separate tunnel timeout applies only
after an HTTP Upgrade or CONNECT. Request path:

```text
client
  → public proxy-router
  → nginx policy worker
  → proxy-router :18081
  → one ready versiond-router replica
  → versiond-N:8080
  → devshardd :500x
```

The fleet defaults to three fixed slots and requires two ready peers before one
slot may stop. Each slot is a separate Compose project built from the same
manifest. The main node Compose project therefore cannot recreate every router
with one `up -d`. Fleet inventory is scoped by an immutable `fleet_id`; duplicate
slot ownership and unknown slots fail closed.

Rolling slots must retain one placement contract: pool DNS and backend-network
identity, legacy owner, legacy pins, and placement-protocol image label. A
change to that contract uses the explicit full-fleet maintenance operation,
which drains every old router
before exposing the new generation and restores exact old image+env snapshots
if the required live routes do not return.

### Multiple versiond instances (multi-host)

This is the **key capability**: versiond instances can run on **separate
IPs/machines**, each supervising its own set of devshardd children per version,
all behind the `versiond-router` fleet for sticky session affinity. The
`deploy/join/docker-compose.versiond.yml` overlay attaches the hosts to the
shared backend network; `deploy/join/versiond-router-fleet.sh` owns the router
slots on independent Compose projects.

> **Multi-instance requires a shared Postgres** — see §4.

### Diff / persist consistency under HA

Sticky routing keeps an escrow on one versiond while healthy, but failover can
land catch-up on a **stale standby**: another replica already wrote higher
nonces to shared Postgres while this process still holds an older in-memory
session. Without extra care, re-applying an already-durable nonce as a bare
`INSERT` produced SQLSTATE `23505` and HTTP 500 even though durable state was
correct.

Shipped behaviour on the host (`devshardd`) and gateway (`devshardctl`):

| Mechanism | Behaviour |
|-----------|-----------|
| **Idempotent `AppendDiff`** | Same `(epoch, escrow, nonce)` with **identical** payload → success (HA replay). Conflicting bytes → typed fork error + `devshard_diff_fork_detected_total` (never overwrite). |
| **Lazy reconcile** | Incoming `diff.Nonce > memNonce+1` → load the gap from Postgres (`GetDiffs`), apply durable rows in memory **without** re-inserting, then apply the tip. Emits `reconcile_fast_forward` log + `devshard_reconcile_fast_forward_total`. |
| **Persist-first** | Validate / preview on a clone → `AppendDiff` (with bounded retry) → commit to the live state machine. Persist failure leaves memory **unchanged** (failure direction is store-ahead or no-op, not memory-ahead). |
| **Gateway catch-up** | Still driven by per-slot `hostSyncNonce` toward group members; the gateway does **not** address individual versiond HA replicas. |

Operator signals (versiond / host logs and Prometheus):

- `reconcile_fast_forward` — expected on failover onto a lagging replica.
- `diff_persist_retry` / `devshard_diff_persist_retry_total` — transient Postgres blips.
- `diff_fork_detected` / `devshard_diff_fork_detected_total` — **must stay 0** in healthy HA; non-zero means real divergence and needs alert investigation.

Gateway catch-up and sticky failover remain independent. HAProxy redispatches
connection failures for every method and permits L7 retries only for
`GET`/`HEAD`/`OPTIONS`; host reconcile heals RAM from shared Postgres when the
request reaches another replica.

---

## 4. Storage: per-instance SQLite vs shared Postgres

devshardd selects exactly one storage backend per process at boot
(`devshard/storage/factory.go`; see [storage-design.md](./storage-design.md)):

| Condition | Backend |
|-----------|---------|
| Store dir already has SQLite sessions | **SQLite** (drain mode even if `PGHOST` set) |
| No SQLite sessions + `PGHOST` set + Postgres connects | **Postgres** (writes `.pg-bound` marker) |
| Fresh store, no `PGHOST` | **SQLite** |
| `.pg-bound` exists but `PGHOST` unset | **Boot error** (would orphan PG sessions) |

The crucial property for multi-instance:

- **SQLite is a single-writer, per-instance file.** It cannot be shared across
  processes/machines. Its validation-lease store is now a **no-op**
  (`devshard/storage/leases.go`: `SQLite.Acquire` always grants;
  `AcquireOneStale`/`SetResult` do nothing), because there is no second instance
  to coordinate with.
- **Postgres is a shared, multi-writer DB.** It provides the real
  cross-instance validation-lease table (`devshard_validation_leases`) that
  guarantees only one devshardd validates each `(escrow_id, inference_id)` pair.

Therefore:

> **Running multiple versiond/devshardd instances (HA) requires the shared
> `devshard-postgres` backend — not a DB-per-instance.** Set `PGHOST` so every
> instance selects Postgres. SQLite is for single-instance / local-dev / tests
> only. This rule is also stated in
> [release-0.2.14-v4.md](./release-0.2.14-v4.md) and
> [rolling-update.md](./rolling-update.md).

Compose: `local-test-net/docker-compose.devshard-postgres.yml`,
`deploy/join/docker-compose.versiond.yml` bring up one shared `devshard-postgres`
for all versiond children. The join overlay persists that database in the
operator-visible `DEVSHARD_POSTGRES_DATA_DIR` bind directory. On the first
in-place v5 start, its entrypoint atomically imports the anonymous Postgres
volume used by v4. If that source was detached while versiond artifacts remain,
startup fails instead of initializing an empty shared database (see
[release-0.2.15-v5.md](./release-0.2.15-v5.md)).

---

## 5. decentralized-api (dapi) — current responsibilities

dapi (`decentralized-api/`) is the largest service and today bundles many
responsibilities into one process (`decentralized-api/main.go`):

| Area | Where | Notes |
|------|-------|-------|
| **Chain event listener** | `internal/event_listener/` | CometBFT WebSocket `NewBlock` + RPC `BlockResults` per-tx events; drives the phase engine |
| **Phase engine** | `internal/event_listener/new_block_dispatcher.go` | Phase transitions → broker commands, PoC stages, validation sampling, reward recovery |
| **Inference API** | `internal/server/public/` | `/v1/chat/completions`, `/completions`, payloads, identity, participants, bridge status |
| **ML callbacks** | `internal/server/mlnode/` | PoC v2 artifact ingest, `/versions` oracle feed (:9100) |
| **Admin REST** | `internal/server/admin/` | Node CRUD, model registration, raw tx, BLS request, setup report, etc. |
| **Node manager (broker)** | `broker/` | ML node lifecycle reconciliation per epoch phase |
| **NodeManager gRPC** | `nodemanager/` | `AcquireMLNode`/`ReleaseMLNode`/`GetRuntimeConfig` (used by devshardd) |
| **PoC / cPoC** | `poc/` | Artifact store, commit worker, off-chain validation, proof client/serve |
| **Tx pipeline** | `cosmosclient/`, `cosmosclient/tx_manager/` | Sign (warm key + authz/feegrant), batch, broadcast, observe |
| **NATS** | `internal/nats/server/server.go` | **Embedded per process** JetStream for tx send/observe/batch queues |
| **BLS** | `internal/bls/` | DKG, threshold signing driven by chain events |
| **Storage** | `payloadstorage/`, `statsstorage/`, `apiconfig/` | Payloads (PG/file), stats (PG), config (SQLite KV) |

### Single-instance constraints today

- **Chain queries:** dapi still queries the inference-chain **directly** over
  gRPC (`cosmosclient/`) for params, epochs, participants, inferences, PoC
  commits, bridge addresses, etc. It does not depend on edge-api.
- **No leader election:** the event listener and phase engine have no singleton
  guard. Two dapi instances against the same keys would **duplicate chain
  transactions and ML-node commands**.
- **Embedded NATS + local keyring + local `last_processed_height`:** all
  per-process; nothing is shared across replicas.

These are the constraints the [HA proposal](./proposals/high-availability.md)
addresses by splitting dapi into independently-scalable services around shared
NATS, Redis, and Postgres, and by sourcing chain state/events from a
highly-available edge-api.

---

## 6. Service / instance summary

| Service | Stateless? | Multi-instance today | Shared state needed for HA |
|---------|-----------|----------------------|----------------------------|
| `proxy-router` | yes | one public instance; host-level SPOF accepted here | none |
| `proxy-policy` | yes | **yes**, private nginx replicas | shared TLS certificate volume |
| `edge-api` | yes | **yes**, balanced directly by `proxy-router` | none |
| `versiond-router` | yes | **yes**, independent fixed slots | none |
| `versiond` + `devshardd` | per-escrow state | **yes**, sticky hash behind router fleet | **shared Postgres** |
| `decentralized-api` | no (event loop, NATS, keyring) | **no** (single-instance) | NATS, Redis, Postgres + leader election (proposed) |

---

## 7. Where to go next

- **Binary rollout (same version, new sha; multi-instance drain):**
  [rolling-update.md](./rolling-update.md).
- **Full HA target architecture (HA edge-api event hub, dapi service split,
  signer/NATS, Redis):** [proposals/high-availability.md](./proposals/high-availability.md).
