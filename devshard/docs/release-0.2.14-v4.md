# Release guide: `devshard-0.2.14-v4`

Operator-facing notes for the v4 line: multi-instance HA, Postgres storage,
gRPC-only gateway chain transport, versionless observability, and rollout
constraints for mixed pre-v4 / v4 estates.

Detailed deploy verification: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).
Observability: [pr-versionless-observability.md](./pr-versionless-observability.md).

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

---

## What's in this release

| Area | Change |
| --- | --- |
| **HA topology** | Multi-versiond + `versiond-router` (consistent-hash on session/escrow id); optional legacy pin for pre-HA versions |
| **Storage** | `DEVSHARD_STORAGE_MODE=postgres` fail-closed; boot migrate local SQLite → PG for **this** version’s data dir |
| **Validation** | Shared Postgres leases (`devshard_validation_leases`); SQLite leases are no-ops |
| **Gateway transport** | Chain queries + escrow tx via gRPC; `--chain-rest` / `DEVSHARD_CHAIN_REST` removed |
| **Settle confirm** | Settle waits for DeliverTx (`GetTx`) after SYNC CheckTx — same pattern as create |
| **Failover** | Router retries another HA peer on first upstream 502 / connect failure |
| **Versionless obs** | Obs GETs never bind; owner chat binds; join rewrite + PG session lookup; `devshard_obs` rate limit |
| **Status field** | Gateway `protocol_version` → `session_version` (bind / settlement tag) |
| **Tier A `/v1` reads** | Served by **edge-api** on new proxies; same handlers still dual-served on **dapi** as deprecated |
| **testenv** | Docker citest S1–S9, G1–G4, A1–A4 (see [testenv/docs/scenarios.md](../testenv/docs/scenarios.md)) |

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
3. Exercise stickiness, kill-one-host failover (S6 / §3 plan), optional lease race (§2).

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
- [ ] Optional: run §2 lease race, §3 kill/restart, and §4 versionless obs from
      the deploy test plan

---

## Known follow-ups

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
| [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) | Deploy + four manual/operator test plans (§1–§4) |
| [pr-versionless-observability.md](./pr-versionless-observability.md) | Versionless obs design + unit/manual checklist |
| [high-availability-architecture.md](./high-availability-architecture.md) | Current runtime topology |
| [storage-design.md](./storage-design.md) | Storage mode selection |
| [rolling-update.md](./rolling-update.md) | Binary swap / drain |
| [testenv/docs/chain-transport-consolidation.md](../testenv/docs/chain-transport-consolidation.md) | REST → gRPC migration detail |
