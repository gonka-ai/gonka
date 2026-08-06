# versiond high availability — Test plans

This note covers **how to roll out** multi-instance HA + Postgres storage and
**seven operator test plans**, including a routing constraint that the boot-migrate
path cannot paper over for already-deployed versions.

| Plan | Section | What it proves |
| --- | --- | --- |
| **Test deployment plan** | §1 | Rollout phases, NON_HA pin, sqlite→migrate→HA, mixed binding |
| **Validation race plan** | §2 | Same-key HA + Postgres: one validation lease per inference |
| **High availability plan** | §3 | Kill versiond → survivors serve; restart → rejoins (check logs) |
| **Versionless observability plan** | §4 | Obs never binds version; rewrite + PG route; rate limit; health vs metrics |
| **Edge-api / deprecated dapi plan** | §5 | New proxy → edge-api Tier A; old proxy → deprecated dapi dual-serve |
| **Escrow long-poll warm plan** | §6 | Host-events warm → DB cache; inference with inference-node down (v4 × dapi **0.2.14** and **devshard-0.2.14-v4**) |
| **Rolling update plan** | §7 | Same-name SHA swap: in-flight SSE survives; Postgres blue/green overlap; hybrid stop/start |

Related: [storage-design.md](./storage-design.md),
[high-availability-architecture.md](./high-availability-architecture.md),
[release-0.2.14-v4.md](./release-0.2.14-v4.md),
[rolling-update.md](./rolling-update.md),
[pr-versionless-observability.md](./pr-versionless-observability.md),
[escrow-longpoll-plan.md](./escrow-longpoll-plan.md) / [ml-node-capacity-fallback-plan.md](./ml-node-capacity-fallback-plan.md)
([PR #1443](https://github.com/gonka-ai/gonka/pull/1443)).

---

## Constraint: boot migrate cannot upgrade live pre-v4 sessions

### What the PR implements

With `DEVSHARD_STORAGE_MODE=postgres`, a **new** `devshardd` binary (this PR /
v4-line) at boot:

1. Connects to shared Postgres (fail-closed).
2. If local SQLite session artifacts exist under **that process's data dir**,
   runs `MigrateSQLiteSessions` (+ file payload migrate), then quarantines
   SQLite files.
3. Serves Postgres-only for the rest of the process lifetime.

That path is correct **only for data owned by a binary that already contains
the migrate code**.

### Logic error if we assume “all versions migrate”

Sessions are bound to a **version path** (`/devshard/<version>/…`) and served
by the **binary versiond downloaded for that name**. Pre-v4 approved versions
already ship binaries that:

- do not implement fail-closed Postgres + boot migrate, and
- keep sessions under their own per-version data dir.

So we **cannot** migrate existing sessions for those versions by flipping
`DEVSHARD_STORAGE_MODE=postgres` on a multi-versiond host: the old child still
runs the old binary, and the new v4 child never sees those SQLite files.

| Escrow / session | Served by | Storage reality |
| --- | --- | --- |
| Bound to `< v4` (already deployed) | Old binary | Local SQLite (or whatever that binary supports); **no in-binary migrate to shared PG** |
| Bound to `v4` (this PR) | New binary | Postgres-only when mode=`postgres`; boot migrate only if **this** data dir already has SQLite artifacts |

### Consequence for HA

Shared Postgres + sticky multi-upstream routing is safe **only** for versions
whose binary understands shared Postgres (leases, fail-closed mode, schema).

Pre-v4 must remain **single-instance affinity** at the proxy: one versiond host
owns all traffic for that version prefix. Multi-instance routing for `< v4`
risks split-brain SQLite / missing leases.

### What would still be needed for true migrate of old sessions

A **versiond-managed migration tool** (outside the old child binary) that can
copy existing per-version SQLite sessions into shared Postgres. Even with that
tool, pre-v4 binaries may still need a **SQLite fallback / drain** story until
those versions are retired — old code paths are not rewritten by the tool.

Until that tool exists, treat pre-v4 as **single-host + local storage**, and
treat v4 as the first version that may join the multi-instance pool.

### Why v4 boot migrate is usually a no-op

Greenfield v4 (or a new version name with an empty data dir) has **no prior
local sessions**. Boot migrate runs, finds nothing, and starts Postgres-only.
Migration is only meaningful if an operator previously ran that same version
name on SQLite under that data dir and then switched to `postgres` mode.

---

---

## 1. Test deployment plan

Rollout topology, routing rules, and verification for multi-instance HA + Postgres.

### 1.1 Target topology (after this PR)

```text
clients / gateway
       │
       ▼
 public edge proxy  (`proxy` in deploy/join)
       │
       ├── /v1/ Tier A ──► edge-api  (optional edge-api-router)
       └── /devshard/ ──► versiond-router  (nginx consistent-hash on session id)
                                 │
                                 ├── versiond-0 ──► devshardd children (per approved version)
                                 ├── versiond-1 ──► …
                                 └── …
                                        │
                                        └── shared Postgres  (required when N>1 for eligible versions)
```

Env (join / compose overlays already seed this for multi-versiond):

| Variable | Role |
| --- | --- |
| `DEVSHARD_STORAGE_MODE=postgres` | Fail-closed shared storage for HA-capable versions |
| `DEVSHARD_POSTGRES_PASSWORD` / `PG*` | Shared DB connectivity |
| `DEVSHARD_CHAIN_GRPC` | Gateway chain I/O (no LCD REST) |
| `VERSIOND_LEGACY_HOST` | versiond host that owns pre-HA SQLite data dirs (default: first of `VERSIOND_HOSTS`) |
| `VERSIOND_NON_HA_VERSIONS` | Pre-HA version path segments pinned to legacy (whitespace and/or comma; see §1.2) |
| `VERSIOND_SERVICE_NAME` | Edge proxy → versiond upstream (`versiond-router` when HA overlay applied) |
| `EDGE_API_SERVICE_NAME` | Edge proxy → edge-api upstream (default `edge-api`) |
| `KEY_NAME` (+ shared keyring) | **Same on every HA versiond replica** of one participant (join); see §1.1.1 |

#### Join compose files (source of truth)

Update **`deploy/join/`** from the release branch — not only binaries. Canonical
paths:

| File | What to update |
| --- | --- |
| [`deploy/join/docker-compose.yml`](../../deploy/join/docker-compose.yml) | Image tags; **edge-api** service; **proxy** (`EDGE_API_SERVICE_NAME`, depends_on edge-api) |
| [`deploy/join/docker-compose.versiond.yml`](../../deploy/join/docker-compose.versiond.yml) | Postgres + versiond2 + versiond-router; `proxy.VERSIOND_SERVICE_NAME` |
| [`deploy/join/docker-compose.edge-api-multi.yml`](../../deploy/join/docker-compose.edge-api-multi.yml) | Optional multi edge-api + router |
| [`deploy/join/docker-compose.devshard-gateway.yml`](../../deploy/join/docker-compose.devshard-gateway.yml) | Gateway (`DEVSHARD_CHAIN_GRPC`) |

```bash
cd deploy/join
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d
# optional:
# docker compose -f docker-compose.yml -f docker-compose.edge-api-multi.yml up -d
```

Relevant excerpts (tags shown are current compose pins — bump for v4):

```yaml
# --- deploy/join/docker-compose.yml (edge-api + edge proxy) ---
  edge-api:
    container_name: edge-api
    image: ghcr.io/product-science/edge-api:0.2.13
    environment:
      - EDGE_API_PORT=18080
      - CHAIN_GRPC_URL=node:9090
      - CHAIN_RPC_URL=http://node:26657
    depends_on:
      - node
    restart: always

  proxy:
    container_name: proxy
    image: ghcr.io/product-science/proxy:0.2.13
    environment:
      - EDGE_API_SERVICE_NAME=${EDGE_API_SERVICE_NAME:-edge-api}
      - EDGE_API_PORT=${EDGE_API_PORT:-18080}
    depends_on:
      edge-api:
        condition: service_started
      versiond:
        condition: service_started

# --- deploy/join/docker-compose.versiond.yml ---
  versiond-router:
    image: ghcr.io/product-science/versiond-router:0.2.13
    environment:
      - VERSIOND_HOSTS=${VERSIOND_HOSTS:-versiond versiond2}
      - VERSIOND_LEGACY_HOST=${VERSIOND_LEGACY_HOST:-versiond}
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS:-v1 v2 v3}

  proxy:
    environment:
      - VERSIOND_SERVICE_NAME=versiond-router
      - VERSIOND_PORT=8080

# --- deploy/join/docker-compose.edge-api-multi.yml ---
  edge-api-router:
    image: ghcr.io/product-science/edge-api-router:0.2.13
    environment:
      - EDGE_API_HOSTS=${EDGE_API_HOSTS:-edge-api edge-api2 edge-api3}

  proxy:
    environment:
      - EDGE_API_SERVICE_NAME=edge-api-router
```

See also [release-0.2.14-v4.md](./release-0.2.14-v4.md) (images / CI note for
`edge-api`).

### 1.1.1 Same participant key on HA replicas (required)

HA means **one escrow participant** is served by **N versiond/devshardd processes**, not N different participants.

| Topology | Identity wiring | What it proves |
| --- | --- | --- |
| **Join / real HA** (`deploy/join/docker-compose.versiond.yml`) | Every HA versiond uses the **same** `KEY_NAME` and mounts the **same** keyring | Sticky routing, shared Postgres sessions, validation-lease dedup for that participant |
| **Testenv multi** (`gencompose`) | Every versiond replica uses `KEY_NAME=hosts[0]` (usually `versiond-0`); other host keys stay in the shared keyring + participants but are **not** HA replica identities; escrow slots all belong to the HA participant | Same as join for the HA participant; legacy routing and storage migration tests exercise that topology |

For **manual** HA checks in this document (Phase B, §1.7 Phase 4, §1.8 T2/T5, §1.9, and §3), keep join/testenv multi as above: **identical `KEY_NAME` / key material on all hosts in `versiond_ha_pool`**. Validation leases (`devshard_validation_leases`) only dedupe work when those processes share one signer address (`instance_address`).

### 1.2 Routing rules (required — implemented in versiond-router)

The public genesis **proxy** still forwards all `/devshard/` traffic to a single
upstream (`VERSIOND_SERVICE_NAME=versiond-router`). **versiond-router** is what
must not sticky-hash pre-HA versions across hosts.

**Implemented behaviour** (`versiond-router/entrypoint.sh` +
`nginx.conf.template`):

| Pool | Upstream | Used when |
| --- | --- | --- |
| `versiond_ha_pool` | All `VERSIOND_HOSTS` (consistent-hash on session id) | Version **not** in `VERSIOND_NON_HA_VERSIONS` (default for all future versions) |
| `versiond_legacy` | **Only** `VERSIOND_LEGACY_HOST` | Version listed in `VERSIOND_NON_HA_VERSIONS` |

| `VERSIOND_NON_HA_VERSIONS` | Effect |
| --- | --- |
| `v1 v2 v3` (join default) | Those paths pin to legacy; every other version (incl. v4+) is HA |
| empty | Every version uses the HA pool |
| `v1,v2,v3` | Same as whitespace (comma-separated also accepted) |

When `len(VERSIOND_HOSTS) > 1` and the request uses `versiond_ha_pool`, nginx
sets request header **`Devshard-Ha: true`**. `devshardd` middleware calls
`common/storage/mode.RequireConfiguredForHA()` and returns **503** unless
`DEVSHARD_STORAGE_MODE` is the literal **`postgres`** (not `auto` / `sqlite` /
`hybrid`) **and** `PGHOST` is set.

| Devshard version | Upstream set | Storage |
| --- | --- | --- |
| **Listed in `VERSIOND_NON_HA_VERSIONS`** (e.g. v1–v3) | **`VERSIOND_LEGACY_HOST` only** | Local SQLite (single-instance); no `Devshard-Ha` |
| **Any other version** (v4+, future) | Sticky hash across **all** healthy `VERSIOND_HOSTS` | Shared Postgres (`DEVSHARD_STORAGE_MODE=postgres`); `Devshard-Ha: true` |

Response headers for debugging: `X-Upstream-Addr`, `X-Versiond-Backend`
(`versiond_legacy` vs `versiond_ha_pool`).

**Deploy condition (join):** `VERSIOND_LEGACY_HOST=versiond`,
`VERSIOND_NON_HA_VERSIONS=v1 v2 v3` until those paths are retired. Do **not**
add HA-capable versions to the non-HA list.

### 1.3 Rollout phases

#### Phase A — Single-instance baseline (safe for all versions)

1. Refresh **`deploy/join/`** compose and deploy this PR images (`proxy` edge,
   `edge-api`, `versiond`, `devshardd`, gateway, **versiond-router** with
   legacy/HA split).
2. Multi-versiond overlay with `VERSIOND_NON_HA_VERSIONS=v1 v2 v3` pins those
   paths to `VERSIOND_LEGACY_HOST` (SQLite-safe).
3. Bring up shared Postgres; set `DEVSHARD_STORAGE_MODE=postgres` on versiond /
   children that run the **new** binary (required once HA routing + `Devshard-Ha`
   is active for that version).
4. Approve / force **v4** alongside existing versions; confirm v4 children boot
   clean (empty migrate → Postgres-only).
5. Leave pre-v4 children on the legacy host’s data dirs (SQLite).

**Exit criteria:** stack healthy; chat/settle on both a pre-v4 path and v4 path
through the same public entry; Postgres holds only v4 (and any intentionally
migrated) sessions; `X-Versiond-Backend: versiond_legacy` for non-HA paths.

#### Phase B — Expand HA for v4+ (default)

1. Add versiond-1…N with the same Postgres credentials and
   `DEVSHARD_STORAGE_MODE=postgres`.
2. Give every HA replica the **same** `KEY_NAME` + keyring entry as the
   participant under test (§1.1.1). Do **not** invent a new key per host for
   join-style HA.
3. Keep pre-HA names in `VERSIOND_NON_HA_VERSIONS`; all other versions
   automatically use `versiond_ha_pool` + `Devshard-Ha`.
4. Confirm non-HA paths still show `X-Versiond-Backend: versiond_legacy` and
   `X-Upstream-Addr` always the legacy host.
5. Exercise stickiness + stop-one-host behaviour **on HA versions only**
   (router stickiness, failover, and legacy routing tests). With same-key replicas, optionally confirm
   validation leases: one row per `(epoch_id, escrow_id, inference_id)` in
   `devshard_validation_leases`.

**Exit criteria:** HA sessions survive sticky routing and single-host loss as
designed; non-HA traffic never lands on a host without its SQLite data; all HA
replicas of the participant share one signer identity.

#### Phase C — Retire pre-v4 (optional later)

1. Drain / stop creating escrows on old version paths.
2. Remove old version prefixes from the single-host pool once idle.
3. Optionally add a **versiond-managed migrate tool** if any long-lived pre-v4
   sessions must move to Postgres before retirement (out of scope for this PR’s
   in-binary boot migrate).

### 1.4 Explicit non-goals for this deploy

- Do **not** assume flipping `DEVSHARD_STORAGE_MODE=postgres` migrates sessions
  for already-deployed `< v4` binaries.
- Do **not** put `< v4` in the multi-instance upstream set.
- Do **not** rely on v4 boot migrate to “heal” a mixed historical estate — with
  no local sessions under the v4 data dir, migrate is a no-op by design.

---


### 1.5 Automated (already in PR)

```bash
make -C devshard ci-testenv-unit
make -C devshard ci-testenv-integration
make -C versiond-router test-render   # config render only (no live nginx)
```

| Focus | Scenarios | Covers legacy pin (`v < v4` → one host)? |
| --- | --- | --- |
| HA stickiness | `TestRouterStickiness` | **No** — probes a version **outside** `VERSIOND_NON_HA_VERSIONS` (testenv: `v2`) and asserts **distinct** upstreams |
| Validation lease exclusivity | `TestValidationLeaseRace*` | **No** — same-key HA + Postgres lease PASS/FAIL; see **§2 Validation race plan** |
| Legacy pin | `TestLegacyVersionPinnedToSingleHost` | **Yes** — `v1` (in non-HA list) → `versiond_legacy` / `versiond-0` only; other versions still multi-upstream |
| SQLite → HA-fail → migrate → HA | `TestSQLiteToPostgresHAMigration` | **Yes** — full §1.7 Phases 0–4 |
| One HA upstream down | `TestVersiondStickySessionFailover` | **No** — first-502 failover to survivor (HA pool) |
| Stale standby catch-up | `TestHAStaleStandbyCatchupIdempotent` | **No** — primary advances PG, stop it; standby catch-up without `23505` |
| Gateway chat / gRPC | `TestGatewayChat`, G1–G4 | No |
| Params / epoch | `TestParamsLongPoll`, `TestEpochSwitch` | No |
| Faults | A1–A4 | No |
| Router template render | `versiond-router` `test-render` | **Partial** — asserts map text for mixed / all-legacy / `*`; does **not** hit a running nginx or check `X-Upstream-Addr` |

### 1.6 Required: proxy routes `v < v4` to a single instance

**Goal:** with ≥2 versiond hosts in `VERSIOND_HOSTS`, traffic for a non-HA version path never leaves `VERSIOND_LEGACY_HOST`.

#### Autotest: `TestLegacyVersionPinnedToSingleHost` — implemented

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestLegacyVersionPinnedToSingleHost -count=1 -v -timeout 45m
```

#### Autotest: `TestSQLiteToPostgresHAMigration` — §1.7 Phases 0–4

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestSQLiteToPostgresHAMigration -count=1 -v -timeout 45m
```

| Step | Action | Expect |
| --- | --- | --- |
| 1 | Compose: `VERSIOND_HOSTS` has ≥2 hosts; `VERSIOND_LEGACY_HOST=versiond-0`; `VERSIOND_NON_HA_VERSIONS=v1` | Stack up |
| 2 | Probe path `v1` (in non-HA list); versiond may 404 the unknown version — nginx still sets routing headers (`always`) | — |
| 3 | ≥16 GETs to `/v1/sessions/<varying-ids>/healthz` | Every response: `X-Versiond-Backend: versiond_legacy` and the same `X-Upstream-Addr` mapped to `versiond-0` |
| 4 | Probe HA `VersionName` with varying session ids | ≥2 distinct `X-Upstream-Addr`; `X-Versiond-Backend: versiond_ha_pool` |
| 5 | Stop non-legacy versiond; repeat legacy probes | Still pinned to the same legacy upstream |

#### Manual / join checklist (same assertions)

```bash
# After router is up with VERSIOND_NON_HA_VERSIONS=v1 (testenv) or v1 v2 v3 (join):
for i in $(seq 1 16); do
  curl -sI "http://127.0.0.1:18080/v1/sessions/citest-legacy-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done
# Expect: always versiond_legacy + one upstream address

for i in $(seq 1 32); do
  curl -sI "http://127.0.0.1:18080/v2/sessions/citest-ha-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done
# Expect: versiond_ha_pool + ≥2 distinct upstream addresses
```

### 1.7 Must-check: NON_HA pin → SQLite v4 → Devshard-Ha fail → Postgres migrate

End-to-end operator / integration walkthrough. Proves (1) NON_HA routing,
(2) `Devshard-Ha` rejects multi-host traffic under SQLite, (3) boot migrate
copies every SQLite session into shared Postgres, (4) HA serving then works.

**Volume topology (required):**

| Store | Topology |
| --- | --- |
| SQLite / local data dir | **Per versiond instance** — separate volume (e.g. join `./devshards/data` vs `./devshards2/data`, testenv `./data/versiond-0` vs `./data/versiond-1`). Never share a SQLite dir across hosts. |
| Postgres | **One shared** `devshard-postgres` (same `PGHOST` / DB / credentials on every versiond) |

#### Phase 0 — Router + NON_HA pin (single `VERSIOND_HOSTS`)

Start with **one** upstream only so SQLite can be written safely:

```bash
VERSIOND_HOSTS=versiond                 # or versiond-0
VERSIOND_LEGACY_HOST=versiond
VERSIOND_NON_HA_VERSIONS=v1 v2 v3       # join-style; whitespace or commas
```

| Check | Expect |
| --- | --- |
| Paths in `VERSIOND_NON_HA_VERSIONS` | `X-Versiond-Backend: versiond_legacy`, always the legacy host |
| Path **not** in the list (e.g. `v4`) with a **single** host | `versiond_ha_pool` but **no** `Devshard-Ha` header (header only when `len(VERSIOND_HOSTS) > 1`) |

Also covered by `TestLegacyVersionPinnedToSingleHost` once multi-host is enabled
(Phase 2).

#### Phase 1 — Create v4 sessions on SQLite (still single host)

Force SQLite on every versiond child (keep this through Phase 2):

```bash
# Preferred: explicit mode. PGHOST is ignored in sqlite mode (may stay set for later).
DEVSHARD_STORAGE_MODE=sqlite

# Equivalent auto path (only if you also clear Postgres connection):
# unset DEVSHARD_STORAGE_MODE   # or =auto
# unset PGHOST                  # auto → sqlite when PGHOST empty
```

Do **not** use `hybrid` here — hybrid still prefers Postgres when `PGHOST` is set.

On the single host, approve/force **v4** (this PR binary), then create several
escrows / bind sessions / write diffs under `/devshard/v4/…`.

**Record a pre-migrate inventory** from that host’s data dir (example paths):

```text
{data}/v4/_meta.db
{data}/v4/epoch_*.db
# plus any sealed / obs / payload trees under the version store
```

Capture at least:

- escrow ids + epoch ids from `devshard_session_index` / session meta
- per-escrow diff count, signature count, latest nonce, status
- payload / sealed / validation-obs row counts if present

#### Phase 2 — Expand to multiple `VERSIOND_HOSTS` while still on SQLite

Bring up a second versiond with its **own** empty SQLite volume (shared
Postgres env may already be present but mode stays `sqlite`):

```bash
VERSIOND_HOSTS="versiond versiond2"     # or versiond-0 versiond-1
VERSIOND_LEGACY_HOST=versiond
VERSIOND_NON_HA_VERSIONS=v1 v2 v3
# still on each versiond child:
DEVSHARD_STORAGE_MODE=sqlite
```

Recreate/reload **versiond-router**.

| Check | Expect |
| --- | --- |
| NON_HA paths (`v1`/`v2`/`v3`) | Still `versiond_legacy` → legacy host only |
| HA path `v4` | `X-Versiond-Backend: versiond_ha_pool`; nginx injects **`Devshard-Ha: true`** |
| Chat / session / health under `/devshard/v4/…` | **Fail** with **503** from `devshardd` (`RequireConfiguredForHA`: needs `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST`) — SQLite (or auto/hybrid) must not serve multi-host HA |

This is the critical negative proof: multi-host routing without fail-closed
Postgres is rejected by the header guard.

#### Phase 3 — Switch to Postgres and migrate SQLite → PG

On **every** versiond instance (same shared DB):

```bash
DEVSHARD_STORAGE_MODE=postgres
PGHOST=devshard-postgres          # shared
PGDATABASE=…  PGUSER=…  PGPASSWORD=…
```

Restart the versiond children that own the SQLite artifacts (at least the
legacy/single host that wrote Phase 1 data; other hosts boot with empty local
dirs and attach to the same Postgres).

Boot path (`DEVSHARD_STORAGE_MODE=postgres`):

1. Connect fail-closed to Postgres.
2. `MigrateSQLiteSessions` (+ file payload migrate) copies **all** local
   escrows into Postgres.
3. Quarantine local SQLite files (`*.migrated.<ts>`).
4. Serve Postgres-only.

**Compare inventory (must match Phase 1):**

| Artifact | Assert |
| --- | --- |
| `devshard_session_index` | Same escrow_id → epoch_id set |
| `devshard_sessions` (partition rows) | Same creator, config/group JSON, balances, nonce, status |
| `devshard_diffs` | Same nonce sequence / row count (and sample payloads) |
| `devshard_signatures` | Same (escrow, nonce, slot) keys |
| snapshots / sealed / validation obs | Present in PG if they existed in SQLite |
| Local SQLite | Renamed to `*.migrated.<ts>` (not re-attached on next boot) |
| `.pg-bound` | Present on stores that hold PG sessions |

#### Phase 4 — HA serving succeeds with `Devshard-Ha`

With multi-host router + `postgres` mode still set:

| Check | Expect |
| --- | --- |
| `/devshard/v4/…` | `versiond_ha_pool`, `Devshard-Ha: true`, **2xx** (no 503 from HA guard) |
| Stickiness | Same session id → same upstream across retries; distinct sessions can hit both hosts (`TestRouterStickiness`) |
| Migrated escrows | Readable/servable from either HA host (shared Postgres), not only the original SQLite volume |
| NON_HA paths | Unchanged — still legacy host / no `Devshard-Ha` |

#### Phase checklist

Covered by `TestSQLiteToPostgresHAMigration` in testenv (citest version name is
`v2`, not `v4`):

- [x] Separate SQLite volumes per versiond; one shared Postgres
- [x] Single-host router: create HA-version sessions under `DEVSHARD_STORAGE_MODE=sqlite`
- [x] NON_HA versions always hit `VERSIOND_LEGACY_HOST`
- [x] Multi-host + sqlite: HA path fails (503) due to `Devshard-Ha`
- [x] Flip to `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST`: boot migrate
- [x] `devshard_session_index` matches pre-migrate SQLite `escrow_epoch` inventory
- [x] Multi-host + postgres: HA serves correctly with `Devshard-Ha`

### 1.8 Must-check: mixed versions × mixed storage binding

Goal: prove NON_HA SQLite and HA Postgres can coexist (not only the migrate path
in §1.7).

#### Setup

1. Topology with **≥2** versiond hosts, **per-host SQLite volumes**, **shared**
   Postgres, versiond-router, gateway.
2. At least two approved version names:
   - **Old** (in `VERSIOND_NON_HA_VERSIONS`) — served only on legacy host / SQLite.
   - **v4+** (not in NON_HA list) — `DEVSHARD_STORAGE_MODE=postgres` + shared PG.
3. Router: `VERSIOND_NON_HA_VERSIONS=v1 v2 v3` (or testenv `v1`).

#### Cases

| # | Case | Expect |
| --- | --- | --- |
| T1 | Create escrow / bind session on **NON_HA** version; write diffs; restart legacy host | Session survives on **that host’s SQLite**; other HA hosts unused |
| T2 | Create escrow / bind session on **v4** (postgres mode); write diffs | Session lands in **shared Postgres**; sticky hash may pin either HA host |
| T3 | Mix: several NON_HA + several v4 escrows active concurrently | NON_HA SQLite-bound on legacy volume; v4 Postgres-bound; no cross-host SQLite bleed |
| T4 | Stop a **non-legacy** HA host while NON_HA traffic runs | NON_HA unaffected (never routed there) |
| T5 | Stop one HA host while **v4** sticky sessions exist | First-502 failover to survivor; mid-stream SSE not spliced |
| T6 | Boot v4 child with **empty** data dir + `postgres` mode | Boot migrate finds nothing; Postgres-only |
| T7 | (Negative) §1.7 Phase 2 — multi-host + sqlite for v4 | 503 from `Devshard-Ha` / `RequireConfiguredForHA` |
| T8 | §1.7 Phases 3–4 — sqlite estate then `postgres` mode on that data dir | Full migrate + HA serve; row inventory matches |

#### Assertions to record

- Backend ownership: per-host SQLite files vs shared `devshard_sessions` /
  session index in Postgres (and `.pg-bound` only where Postgres holds sessions).
- Router: `X-Versiond-Backend: versiond_legacy` for NON_HA;
  `versiond_ha_pool` + `Devshard-Ha` for HA versions when multi-host.
- Gateway `/status` (or equivalent) `session_version` matches the bound path
  version for each escrow.

### 1.9 Manual smoke (join / testenv)

**Identity:** every host in `versiond_ha_pool` must use the **same** `KEY_NAME` /
keyring key (§1.1.1). Join and testenv **multi** gencompose both do this
(`KEY_NAME=hosts[0]` on every replica).

```bash
cd devshard/testenv
make build-devshardd gen-compose up
# gateway :18081, router :18080
```

Checklist:

- [ ] **Same `KEY_NAME` on all HA versiond replicas** of the participant under test
      (join + testenv multi default)
- [ ] v4 chat stream + non-stream through router → Postgres-backed session
- [ ] **NON_HA path:** covered by `TestLegacyVersionPinnedToSingleHost`
      (`X-Versiond-Backend: versiond_legacy`)
- [ ] HA path: `versiond_ha_pool` with ≥2 distinct upstreams
      (`TestRouterStickiness` / `TestLegacyVersionPinnedToSingleHost`)
- [ ] §1.7 walkthrough (sqlite → Devshard-Ha 503 → postgres migrate → HA OK)
- [ ] `DEVSHARD_STORAGE_MODE=postgres` on HA children; join fails closed without
      Postgres password / `PGHOST` as documented
- [ ] Per-versiond SQLite volumes remain distinct; Postgres is shared
- [ ] (Same-key HA) `devshard_validation_leases`: one owner row per inference;
      loser does not double-submit — full walkthrough: **§2 Validation race plan**
- [ ] Kill / restart HA versiond; survivors serve and restarted host rejoins —
      **§3 High availability plan** (verify in logs)
- [ ] Versionless obs: unbound 404, owner chat binds, legacy rewrite —
      **§4 Versionless observability plan**

### 1.10 Follow-up test (out of scope until tool exists)

When a **versiond-managed migration tool** lands:

- Migrate a live SQLite estate for a pinned NON_HA version into shared Postgres
  without requiring that old binary to contain migrate code.
- Re-test removing that version from `VERSIOND_NON_HA_VERSIONS` only after the
  **serving** binary for that version also understands Postgres HA (or after
  the version is retired).

---

## 2. Validation race plan

Companion to §1.1.1 (same participant key) and §1.6 / §1.8 (HA routing).

**Goal:** prove that with multi-instance HA + shared Postgres, only one
`devshardd` process validates each `(escrow_id, inference_id)`. The winner
acquires a row in `devshard_validation_leases`; the loser cannot insert a
second lease and must not submit a second `MsgValidation`.

This is a **manual** walkthrough (also covered by
`TestValidationLeaseRace*`). Primary
evidence is the Postgres lease table; logs are corroboration.

### 2.0. Preconditions

| Requirement | Why |
| --- | --- |
| ≥2 HA hosts in `VERSIOND_HOSTS` (leading pair: `hosts[0]` + `hosts[1]`) | Real race needs two processes with the same key |
| ≥1 **solo** versiond (`hosts[2+]`, own `KEY_NAME`) | Executor must be a *different* participant — hosts never validate their own executions, so all-slots-on-HA produces **zero** leases |
| Solo on **sqlite**; HA pair on **shared Postgres** | Avoids multi-writer conflicts on `devshard_diffs`; leases live only on the HA PG |
| Escrow slots include HA + solo addresses | gencompose: round-robin across HA identity + every solo host |
| `DEVSHARD_STORAGE_MODE=postgres` + shared `PGHOST` | SQLite leases are no-ops |
| **Same `KEY_NAME` on HA replicas only** (`hosts[0]`) | Join + testenv; solo keeps its own key; see §1.1.1 |
| HA version **not** in `VERSIOND_NON_HA_VERSIONS` | Must use `versiond_ha_pool` + `Devshard-Ha` |
| Chat path healthy (gateway → router / solo → mock-openai) | Generates finished inferences for the HA participant to validate |
| Chain `validation_rate` = **10000** (100%) before escrow create | Every finished inference is a validation candidate → denser lease races (§2.1a) |

**Host count is a minimum, not a ceiling.** The validation lease race citest uses **3** versionds
(HA pair + one solo). For a manual stack you can add more hosts in
`config/config.yaml` (`versiond-3`, …); gencompose keeps only the first two in
`VERSIOND_HOSTS` and treats every `hosts[i≥2]` as an extra solo participant.

**Out of scope:** router stickiness, legacy pin, SQLite migration, and
kill/restart routing (§3). Those tests do not assert lease exclusivity.

---

### 2.1. Bring up the stack

#### Testenv (recommended)

```bash
cd devshard/testenv
make build-devshardd gen-compose up
# gateway :8081 (or configured), router :8080
docker compose ps
```

Confirm HA pair shares a key; every solo keeps its own (example with the
default 3-host skeleton — repeat for `versiond-3`… if you added more):

```bash
docker compose exec versiond-0 printenv KEY_NAME
docker compose exec versiond-1 printenv KEY_NAME
# Expect: identical (e.g. versiond-0) — HA replicas
docker compose exec versiond-2 printenv KEY_NAME
# Expect: versiond-2 (solo; not the HA key)
# Optional extras: versiond-3 → KEY_NAME=versiond-3, etc.
docker compose exec versiond-router printenv VERSIOND_HOSTS
# Expect: versiond-0 versiond-1  (only the HA pool; solos use direct inference_url)
```

#### Join

Use `docker-compose.yml` + `docker-compose.versiond.yml` with shared
`KEY_NAME` / keyring (already the join model). Same steps below; adjust
service names (`versiond` / `versiond2`) and ports.

---

### 2.1a. Set chain `validation_rate` to 100%

`validation_rate` is **basis points** (`10000` = 100%). It is snapshotted onto
the escrow at **create** time, so set it **before** the gateway opens the
session used for this test. Default testenv seed is often `6000` (60%), which
thins out lease attempts.

#### Option A — patch live mock-chain (testenv)

mock-dapi proxies admin faults to mock-chain (`:9100`):

```bash
curl -sS -X POST http://127.0.0.1:9100/testenv/params \
  -H 'Content-Type: application/json' \
  -d '{"validation_rate": 10000}'
# Expect: {"status":"ok"}
```

#### Option B — seed before `gen-compose`

In `config/config.yaml` (or citest skeleton):

```yaml
params:
  validation_rate: 10000
escrows:
  - id: 1
    validation_rate: 10000   # optional; gencompose copies from params if unset
```

Then `make gen-compose` and recreate the stack so seed escrows / template
params carry 100%.

| Check | Pass |
| --- | --- |
| Params patch | HTTP 200 / `status: ok` (Option A) |
| New escrow | Created **after** the patch (or from 100% seed) so its snapshot is `10000` |

Do **not** rely on patching after the test escrow already exists — that escrow
keeps its create-time rate.

---

### 2.2. Confirm HA routing (before load)

Pick the HA version name (testenv: `v2`; join v4-line: e.g. `v4`).

```bash
# Multi-upstream fan-out (sticky hash)
for i in $(seq 1 32); do
  curl -sI "http://127.0.0.1:8080/v2/sessions/lease-race-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr|Devshard-Ha'
done
```

| Check | Pass |
| --- | --- |
| `X-Versiond-Backend` | `versiond_ha_pool` |
| `X-Upstream-Addr` | ≥2 distinct addresses across the 32 probes |
| `Devshard-Ha` | Present when `len(VERSIOND_HOSTS) > 1` (may show on request path into child; router injects it for HA pool) |

Record the HA version string and at least two upstream addrs for later log
correlation.

---

### 2.3. Warm the session on **both** replicas

Sticky routing alone often keeps one replica cold. Both processes must load the
escrow Host so both can `Offer` → `LeaseValidator.Acquire`.

1. Through the **gateway**, create/bind an escrow (**after** §2.1a) and run
   **one** chat so a session exists (note `escrow_id` from `/v1/status` or
   debug state).
2. Force the same session onto each replica (bypass sticky affinity), e.g.:

```bash
# From a container on the compose network, or with published ports if available.
# Replace ESCROW and VERSION.
ESCROW=<escrow_id>
VER=v2

# Session routes have no /healthz; /mempool lazy-loads the Host.
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://versiond-0:8080/${VER}/sessions/${ESCROW}/mempool"
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://versiond-1:8080/${VER}/sessions/${ESCROW}/mempool"
```

| Check | Pass |
| --- | --- |
| Both mempool | 2xx (session warm / resolvable on each replica) |
| Optional | Both versiond logs show host/session activity for that escrow |

If you cannot hit versiond directly, generate enough distinct sticky session
probes then re-bind chat to the escrow under test and rely on OfferRescan
(~15s) after both replicas have seen the escrow via shared Postgres — less
reliable than direct warm.

---

### 2.4. Load + Postgres monitor (automated)

Drive chat load and analyze `devshard_validation_leases` in parallel. Prefer the
testenv scripts (the same checks as `TestValidationLeaseRace*`).

#### Scripts (from `devshard/testenv/`)

| Script | Role |
| --- | --- |
| [`scripts/lease-race-load.sh`](../testenv/scripts/lease-race-load.sh) | Non-stream + stream gateway chats |
| [`scripts/lease-race-monitor.sh`](../testenv/scripts/lease-race-monitor.sh) | Poll / one-shot Postgres analysis → **PASS/FAIL** (exit 0/1) |
| [`scripts/lease-race-run.sh`](../testenv/scripts/lease-race-run.sh) | Monitor in background + load + final verdict |

#### Recommended: one command

After §§2.0–2.3 (stack up, `validation_rate=10000`, escrow warm on both replicas):

```bash
cd devshard/testenv

# Combined: parallel monitor + load + PASS/FAIL (requires ≥5 lease rows)
MIN_LEASES=5 NON_STREAM=40 STREAM=20 ./scripts/lease-race-run.sh
```

#### Or run pieces separately

```bash
cd devshard/testenv

# Terminal A — watch leases (Ctrl-C then prints final PASS/FAIL)
./scripts/lease-race-monitor.sh --watch 1 --min-leases 0

# Terminal B — drive load
GATEWAY=http://127.0.0.1:8081 MODEL=test-model \
  NON_STREAM=40 STREAM=20 ./scripts/lease-race-load.sh

# Terminal A or C — final automated verdict
./scripts/lease-race-monitor.sh --min-leases 5
# PASS → exit 0 ; FAIL (duplicates or too few rows) → exit 1
```

With the session warm on both replicas, both schedulers can
`Offer` → `LeaseValidator.Acquire` for the same `(escrow_id, inference_id)`.
With §2.1a at 100%, nearly every finished inference should produce a lease
attempt.

#### Optional: live SQL / logs

Postgres is not published on the host in default testenv:

```bash
docker compose exec -it devshard-postgres \
  psql -U devshardd -d devshardd
```

```sql
SELECT epoch_id, escrow_id, inference_id, instance_address, status, claimed_at
FROM devshard_validation_leases
ORDER BY claimed_at DESC LIMIT 50;

SELECT epoch_id, escrow_id, inference_id, COUNT(*)
FROM devshard_validation_leases
GROUP BY 1, 2, 3
HAVING COUNT(*) > 1;   -- must be empty

SELECT status, COUNT(*) FROM devshard_validation_leases GROUP BY status;
```

```bash
docker compose logs -f versiond-0 versiond-1 2>&1 | \
  grep -E 'validation lease|AlreadyLeased|leased by another|mark validation submitted|submit abandoned'
```

#### Automated validation lease race citest

```bash
cd devshard/testenv
make build-devshardd citest-images
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run '^TestValidationLeaseRace' -count=1 -v -timeout 45m
```

| Test | Covers |
| --- | --- |
| `TestValidationLeaseRaceCore` | §2.4 load + monitor PASS/FAIL |
| `TestValidationLeaseRacePendingStretch` | §2.6a slow ML → pending |
| `TestValidationLeaseRaceStaleReclaim` | §2.6b short TTL + pause ML + stop replica |

---

### 2.5. Pass / fail criteria (core race)

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Monitor / uniqueness | `lease-race-monitor.sh` prints **PASS**; zero duplicate groups | **FAIL** or any duplicate `(epoch_id, escrow_id, inference_id)` |
| 2 | One row per inference | Exactly one lease row per validated inference | Two inserts |
| 3 | `instance_address` | Signer of the HA participant (same key → same bech32 on both replicas; still **one** row) | Two rows for one inference |
| 4 | Lifecycle | `pending` → `submitted` or `skipped` | Stuck without reason / double terminal status |
| 5 | Loser | No second insert; no second successful submit | Loser also submits |
| 6 | Logs (optional) | At most one successful submit path per inference | Both replicas claim submit for same id |

Record: escrow id, monitor output, status histogram.

---

### 2.6. Stronger race

#### 2.6a. Stretch the pending window (`TestValidationLeaseRacePendingStretch`)

1. Warm escrow on both replicas (§2.3).
2. **Slow ML** (testenv: `POST mock-openai /testenv/fault` with high
   `latency_ms`) so Acquire wins but Validate holds the lease in `pending`.
3. Drive concurrent chats; confirm pending with the monitor / SQL:

```sql
SELECT inference_id, instance_address, status
FROM devshard_validation_leases
WHERE status = 'pending'
ORDER BY claimed_at DESC;
```

| Check | Pass |
| --- | --- |
| Pending rows | At most **one** row per inference while both try |
| After ML recovers | Row → `submitted`/`skipped`; uniqueness still PASS |

#### 2.6b. Stale reclaim (`TestValidationLeaseRaceStaleReclaim`)

Default `DEVSHARD_VALIDATION_LEASE_TTL` is **30m** — only with a shortened TTL.

1. Set e.g. `DEVSHARD_VALIDATION_LEASE_TTL=15s` and
   `DEVSHARD_VALIDATION_RETRY_INTERVAL=5s` on **all** HA versiond children;
   recreate them (compose already exposes these env keys).
2. Warm escrow; start load under **slow ML**, then **pause ML**
   (`http_status: 503` on mock-openai) so Validate cannot finish and leases
   stay `pending`. (Pausing ML is required — otherwise the holder completes
   before you can kill the replica.)
3. `docker compose stop` one HA replica (e.g. `versiond-1`).
4. Re-warm the survivor’s escrow Host from shared Postgres (direct
   `/mempool` on the live replica) so RetryLoop can rebuild
   `ValidateRequest` from Finished inferences.
5. Wait > TTL.
6. Restore ML; survivor `RetryLoop` / `AcquireOneStale` should leave
   `pending` and move the row to `submitted` (or `skipped` if state
   catch-up is incomplete).

```sql
SELECT inference_id, instance_address, status, claimed_at
FROM devshard_validation_leases
ORDER BY claimed_at DESC LIMIT 20;
```

| Check | Pass |
| --- | --- |
| After TTL + ML restore | Still one row per inference; `pending` decreases; `submitted`/`skipped` grows |
| Uniqueness | Monitor still **PASS** |

Restore default TTL / clear mock-openai faults after the exercise.

---

### 2.7. Cleanup

```bash
cd devshard/testenv
make down
```

Restore any fault / TTL env overrides before other scenarios.

---

### 2.8. Checklist summary

- [ ] Same `KEY_NAME` on all HA replicas (§2.0 / §1.1.1)
- [ ] Chain `validation_rate` = **10000** before escrow create (§2.1a)
- [ ] HA routing: `versiond_ha_pool`, multi upstream (§2.2)
- [ ] Escrow warm on **both** replicas (§2.3)
- [ ] `./scripts/lease-race-run.sh` (or `TestValidationLeaseRaceCore`) → monitor **PASS** (§2.4–§2.5)
- [ ] One lease row per inference; `pending` → `submitted`/`skipped` (§2.5)
- [ ] Optional pending stretch + stale reclaim with ML pause (§2.6)

---

### Related

- Lease implementation: `devshard/storage/leases.go` (`INSERT … ON CONFLICT DO NOTHING`)
- Wrapper: `devshard/cmd/devshardd/inference/validator.go` (`LeaseValidator`)
- Stale reclaim: `devshard/cmd/devshardd/session/retry.go` (`AcquireOneStale`)
- HA identity + routing: this document (§1)
- Testenv scripts: `devshard/testenv/scripts/lease-race-*.sh`
- Citest: `devshard/testenv/citest/validation_lease_race_test.go`
- Unit coverage: `devshard/storage/leases_test.go`


## 3. High availability plan (manual)

**Goal:** prove that with ≥2 HA `versiond` replicas behind `versiond-router`, killing
one (or more) replicas does **not** take the HA pool offline — remaining instances
keep serving traffic (including sticky sessions that preferred the dead peer, via
**first-502 reroute**) — and that a restarted replica rejoins and serves again.
Primary evidence is **versiond / router logs** (and optional `X-Upstream-Addr`).

Router behaviour (`versiond-router`): sticky hash while healthy; on first upstream
**502** / connect error / timeout, `proxy_next_upstream` retries another HA peer
(`max_fails=1`). **503** is not retried (drain / HA guard). Mid-stream SSE after
StartConfirm / receipt is **not** spliced — that stream is lost; the client must
open a **new** request (which then lands on a survivor).

**Out of scope for this plan:** validation-lease exclusivity (§2), sqlite→migrate
(§1.7), legacy pin (§1.6). Those are separate. Automated coverage for stop/restart
semantics also exists as `TestVersiondStickySessionFailover`,
`TestVersiondRestartSessionPersistence`, and
`TestHAStaleStandbyCatchupIdempotent` (stale-standby catch-up); this section is
the operator walkthrough.

### 3.0 Preconditions

| Requirement | Why |
| --- | --- |
| ≥2 hosts in `VERSIOND_HOSTS` / `versiond_ha_pool` | Need a survivor after kill |
| HA version **not** in `VERSIOND_NON_HA_VERSIONS` | Must use sticky multi-upstream pool |
| `DEVSHARD_STORAGE_MODE=postgres` + shared `PGHOST` | Fail-closed HA serving |
| Same `KEY_NAME` on HA replicas (§1.1.1) | One participant identity |
| Chat / health path healthy through gateway → router | Something to observe in logs |

### 3.1 Bring up and baseline (all replicas serving)

```bash
cd devshard/testenv
make build-devshardd gen-compose up
# router :8080 (or :18080 in citest ports), gateway :8081 / :18081
docker compose ps
```

Confirm every HA replica is up, then drive traffic so **each** replica logs activity:

```bash
# Fan-out probes (varying session ids → distinct sticky upstreams)
for i in $(seq 1 32); do
  curl -sI "http://127.0.0.1:8080/v2/sessions/ha-alive-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done

# Optional: gateway chat load so application logs are richer than healthz
GATEWAY=http://127.0.0.1:8081 MODEL=test-model \
  NON_STREAM=20 STREAM=10 ./scripts/lease-race-load.sh
```

In parallel, watch logs on all HA hosts:

```bash
docker compose logs -f versiond-0 versiond-1 2>&1 | \
  grep -E 'GET |POST |session|chat|/v2/'
```

| Check | Pass |
| --- | --- |
| `X-Versiond-Backend` | `versiond_ha_pool` |
| `X-Upstream-Addr` | ≥2 distinct addresses across probes |
| Logs | **Both** (all) HA versiond replicas show request activity |

Record which upstream addrs map to which compose services before the kill.

### 3.2 Kill one versiond — survivors serve (first-502 reroute)

Stop one HA replica (repeat with a second kill if you have ≥3 HA hosts). Before
the kill, pick a session id that sticky-hashes to that host (record
`X-Upstream-Addr`).

```bash
# Example: take versiond-1 out of the pool
docker compose stop versiond-1

# Confirm it is down
docker compose ps versiond-1
```

Drive traffic again — **same** sticky session id that preferred the killed host,
plus new session ids:

```bash
# Same session that was pinned to the stopped host — must failover (not sticky 502)
curl -sI "http://127.0.0.1:8080/v2/sessions/<pinned-session>/healthz" \
  | grep -E 'HTTP|X-Versiond-Backend|X-Upstream-Addr'

for i in $(seq 1 32); do
  curl -sI "http://127.0.0.1:8080/v2/sessions/ha-after-kill-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr|HTTP'
done

# Gateway chats should still succeed via survivors
GATEWAY=http://127.0.0.1:8081 MODEL=test-model \
  NON_STREAM=20 STREAM=10 ./scripts/lease-race-load.sh
```

Watch logs — only living replicas should handle work:

```bash
docker compose logs -f --since=1m versiond-0 versiond-1 2>&1 | \
  grep -E 'GET |POST |session|chat|/v2/'
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Sticky failover | Pinned session gets **non-502/503** with survivor in `X-Upstream-Addr` | Sticky 502 forever / only dead peer |
| 2 | Pool still usable | New session ids get responses from live upstreams | All HA traffic fails while a survivor is up |
| 3 | Dead host idle | Stopped replica’s logs show **no new** successful handling after stop | Stopped container still appears as successful sole upstream |
| 4 | Survivors busy | Live replica logs show continued request / chat activity | Only errors; no survivor traffic |

**Stream caveat:** if an inference SSE already sent StartConfirm / receipt and the
host is killed mid-stream, nginx does **not** replay that request. The client sees
a truncated stream and must issue a **new** request; that new request fails over.

### 3.3 Restart the killed instance — it serves again

```bash
docker compose start versiond-1
# Wait until healthy / children up (logs show listen / version ready)
docker compose logs -f --since=0 versiond-1
```

Drive traffic again and confirm the restarted host is back in the pool:

```bash
for i in $(seq 1 48); do
  curl -sI "http://127.0.0.1:8080/v2/sessions/ha-after-restart-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Restarted host live | Container up; versiond/devshardd ready in logs | Restart loops / never ready |
| 2 | Receives traffic | **Restarted** replica’s logs show new request activity | Only other replicas log; restarted host stays silent under load |
| 3 | Multi-upstream again | `X-Upstream-Addr` shows ≥2 distinct live addresses (including restarted host) | Pool stuck on one host forever |
| 4 | App path | Gateway chat still succeeds after restart | Chat broken after rejoining |

Optional stronger check: stop **each** HA replica in turn (never all at once),
repeat §3.2–§3.3, and confirm the remaining set always serves while at least one
is up.

### 3.4 Checklist summary

- [ ] Baseline: ≥2 HA versionds both logging request activity (§3.1)
- [ ] Kill one versiond; **pinned** sticky session fails over (first 502 → survivor) (§3.2)
- [ ] Survivors keep serving; dead host shows no new successful handling (§3.2)
- [ ] Restart killed versiond; logs show it serving again (§3.3)
- [ ] Multi-upstream fan-out restored after restart (§3.3)
- [ ] Stale-standby catch-up / persist-hole checks (§3.6)

### 3.5 Cleanup

```bash
cd devshard/testenv
make down
```

### 3.6 Diff / persist consistency (failover catch-up + persist hole)

**Goal:** after sticky primary advances shared Postgres, failover onto a lagging
replica must succeed without SQLSTATE `23505`. Persist blips must not leave a
permanent memory-ahead gap (persist-first + retry).

**Automated:** `TestHAStaleStandbyCatchupIdempotent` (preferred for catch-up):

```bash
cd devshard/testenv
make build-devshardd citest-images
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run '^TestHAStaleStandbyCatchupIdempotent$' -count=1 -v -timeout 45m
```

#### Manual — join / shared Postgres

```bash
cd deploy/join
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d
docker compose ps
# Confirm both HA versiond + versiond-router + devshard-postgres healthy
```

1. Drive gateway chat for one escrow until `LatestNonce` advances (repeat).
2. Verify durable rows:

```bash
docker compose exec -it devshard-postgres psql -U devshardd -d devshardd
```

```sql
SELECT epoch_id, escrow_id, MAX(nonce) FROM devshard_diffs
GROUP BY 1,2 ORDER BY 3 DESC LIMIT 10;
SELECT nonce FROM devshard_diffs WHERE escrow_id = '<ESC>' ORDER BY nonce;
-- expect contiguous nonces; no duplicate PK possible
```

3. **Failover catch-up:** identify the sticky primary for that escrow
   (`X-Upstream-Addr` on `/devshard/<version>/sessions/<escrow>/mempool`), stop
   that `versiond` service, then send another chat for the same escrow.

| Expect (v4) | Fail |
| --- | --- |
| Chat succeeds; gateway `LatestNonce` advances | HTTP 500 |
| No `23505` / `duplicate key value violates unique constraint` in survivor logs | Unique violation on already-durable nonce |
| Optional: `reconcile_fast_forward` in survivor logs | Silent durable gap / stuck nonce |

4. **Persist hole:** briefly pause Postgres mid-chat, then unpause:

```bash
docker compose pause devshard-postgres
# attempt chat (expect failure / retry pressure)
docker compose unpause devshard-postgres
# retry chat
```

| Expect (v4) | Fail |
| --- | --- |
| Persist retried (`diff_persist_retry`); on hard fail in-memory nonce **unchanged** | Memory advanced while PG row missing |
| After unpause, retry chat persists and advances `LatestNonce` | Same nonce silently swallowed; durable gap remains |

5. Watch signals:

```bash
docker compose logs -f versiond versiond2 2>&1 | \
  grep -E 'reconcile_fast_forward|diff_persist_retry|diff_fork_detected'
```

| Metric / log | Healthy HA |
| --- | --- |
| `reconcile_fast_forward` | May appear on failover to a lagging replica |
| `diff_persist_retry` | May appear under induced PG blips |
| `diff_fork_detected` | **Must stay 0** (non-zero = real divergence) |

```bash
docker compose -f docker-compose.yml -f docker-compose.versiond.yml down
```

---

## 4. Versionless observability plan (manual)

Companion to [pr-versionless-observability.md](./pr-versionless-observability.md).
Unit coverage already exists (`versioned/internal/proxy`, `SessionServerExisting`,
owner-chat bind). This section is the **operator walkthrough**.

**Goal:** public observability never binds protocol version; only the escrow
owner binds via signed chat; versionless + legacy rewrite work; Postgres lookup
routes session obs to the bound child; obs GETs are rate-limited separately from
chat.

```text
Dashboard  GET /devshard/v2/sessions/E/diffs
        → join proxy rewrite (no Location) → /devshard/sessions/E/diffs
        → versiond (PG lookup or fan-out) → child
        → SessionServerExisting (no CreateSession)
        → 404 if unbound

Creator   POST /devshard/v3/sessions/E/chat/completions (owner sig)
        → BindOwnerChat → CreateSession(Version=v3)
```

**Out of scope:** multi-version merge of `/stats/shards` list; migrating already
wrongly bound escrows; lease race (§2); kill/restart (§3).

### 4.0 Preconditions

| Requirement | Why |
| --- | --- |
| ≥1 approved HA version (testenv: `v2`) | Bind + obs targets |
| Shared Postgres (`PGHOST`) on versiond | Bound-version lookup for versionless session obs |
| Gateway for owner chat | Only signed chat binds |
| **Join / genesis proxy** for rewrite + rate limit | Bare versiond-router has no `devshard_obs` rewrite zone |

**Stacks:**

| Cases | Stack |
| --- | --- |
| §4.1, §4.3, §4.4, §4.5 | testenv multi + Postgres (router `:8080`, gateway `:8081`) — use `/v2/…` or `/sessions/…` on the router; versionless paths work without join rewrite |
| §4.2 rewrite, §4.6 rate limit | **Join / local-test-net with public proxy** in front of versiond (`/devshard/…`) |

Env (see also [proxy/README.md](../../proxy/README.md)):

| Variable | Where | Role |
| --- | --- | --- |
| `PGHOST` / `DATABASE_URL` | versiond | Enable session-version lookup |
| `VERSIOND_DISABLE_SESSION_LOOKUP` | versiond | Force fan-out even when PG is set |
| `DEVSHARD_OBS_RATE_LIMIT_RPS` | join proxy | Per-IP obs GET limit (default 10) |
| `DEVSHARD_OBS_BURST` | join proxy | Obs burst (default 20) |

### 4.1 Unbound obs does not bind

Pick an escrow id that has **no** session yet (new chain escrow, or a fake id
that will 404).

```bash
# testenv router (no /devshard/ prefix)
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1:8080/sessions/UNBOUND-ESCROW/diffs"
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1:8080/v2/sessions/UNBOUND-ESCROW/diffs"

# join / public proxy
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1/devshard/sessions/UNBOUND-ESCROW/diffs"
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1/devshard/v2/sessions/UNBOUND-ESCROW/diffs"
```

| Check | Pass |
| --- | --- |
| HTTP status | **404** (not 200) |
| Side effect | No new row in `devshard_sessions` / session index for that escrow; no accidental version stamp |

### 4.2 Legacy rewrite (join proxy)

Requires the **public proxy** rewrite path (not bare versiond-router).

1. Bind an escrow via owner chat on a chosen version (e.g. `v2`) — §4.3.
2. Hit the **legacy** versioned obs URL and the canonical versionless URL:

```bash
# After bind; replace ESCROW and adjust host/port for join proxy
curl -sSI "http://127.0.0.1/devshard/v2/sessions/${ESCROW}/diffs" \
  | grep -Ei 'HTTP/|^[Ll]ocation:'
curl -sS "http://127.0.0.1/devshard/v2/sessions/${ESCROW}/diffs" -o /tmp/legacy-diffs.body
curl -sS "http://127.0.0.1/devshard/sessions/${ESCROW}/diffs" -o /tmp/canon-diffs.body
cmp /tmp/legacy-diffs.body /tmp/canon-diffs.body && echo bodies_match
```

| Check | Pass | Fail |
| --- | --- | --- |
| Status | **200** (bound escrow) | 308/301 to another path, or 404 after bind |
| `Location` | **Absent** (internal `rewrite … last`, not public redirect) | Client-visible redirect |
| Body | Legacy and versionless responses match | Different payloads |

### 4.3 Owner chat binds chosen version

1. Create/bind escrow through the **gateway** (owner key).
2. Confirm obs still **404** before first chat (§4.1).
3. Owner chat on the intended version path (gateway OpenAI path, or signed
   `POST /…/v{ver}/sessions/{id}/chat/completions`).
4. Re-check obs:

```bash
# testenv router examples after gateway chat bound escrow ESCROW to v2
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1:8080/sessions/${ESCROW}/diffs"
curl -sS -o /dev/null -w "%{http_code}\n" \
  "http://127.0.0.1:8080/stats/shards/${ESCROW}"
```

| Check | Pass |
| --- | --- |
| Before chat | Obs **404** |
| After owner chat | Diffs + stats detail **200** |
| Bind source | Only owner chat created the session (not a prior obs GET) |

### 4.4 PG routing (HA + lookup)

With `PGHOST` set and session bound (Postgres holds `sessions.version`):

1. Drive versionless `GET …/sessions/${ESCROW}/diffs` (via router or proxy).
2. Confirm traffic hits the **bound** version’s child (versiond logs / child
   access logs; with multi-versiond sticky router, correlate escrow → upstream).
3. Optional negative: set `VERSIOND_DISABLE_SESSION_LOOKUP=true`, recreate
   versiond — versionless obs still succeeds via **fan-out** (may be slower;
   lookup-error / fan-out warn may appear when PG errors).

| Check | Pass |
| --- | --- |
| Default PG lookup | Bound child serves diffs; unbound still 404 |
| Lookup disabled | Fan-out still finds bound session when a child has it |

### 4.5 Health vs metrics

```bash
# Join / public proxy shapes (adjust host). testenv router: omit /devshard/
curl -sS -o /dev/null -w "%{http_code}\n" "http://127.0.0.1/devshard/healthz"
curl -sS -o /dev/null -w "%{http_code}\n" "http://127.0.0.1/devshard/v2/healthz"
curl -sS -o /dev/null -w "%{http_code}\n" "http://127.0.0.1/devshard/metrics"
```

| Path | Expect |
| --- | --- |
| `/devshard/healthz` (or router `/healthz`) | **versiond supervisor** status — not a child process metrics scrape |
| `/devshard/{version}/healthz` | **That child** health (not rewritten to versionless) |
| `/devshard/metrics` | Pins **newest** child by numeric/dotted version sort (`v10` > `v2`) |

### 4.6 Obs rate limit (join proxy)

Requires public proxy `devshard_obs` zone. Temporarily set e.g.
`DEVSHARD_OBS_RATE_LIMIT_RPS=2`, `DEVSHARD_OBS_BURST=2`, reload proxy.

```bash
# Burst obs GETs — expect some 503
for i in $(seq 1 30); do
  curl -sS -o /dev/null -w "%{http_code}\n" \
    "http://127.0.0.1/devshard/metrics"
done | sort | uniq -c

# Chat must remain exempt (gateway or signed chat POST still succeeds)
```

| Check | Pass | Fail |
| --- | --- | --- |
| Obs burst | Some responses **503** under low RPS | All 200 under deliberate flood |
| Chat | Owner chat / gateway inference still **2xx** | Chat also rate-limited by `devshard_obs` |

Restore default obs rate env after the exercise.

### 4.7 Checklist summary

- [ ] Unbound obs → **404**; no session bind (§4.1)
- [ ] Legacy `/devshard/{v}/sessions/…/diffs` → **200**, no `Location` (§4.2)
- [ ] Owner chat binds; then versionless diffs/stats **200** (§4.3)
- [ ] PG lookup routes to bound child; fan-out works if lookup disabled (§4.4)
- [ ] Supervisor `/healthz` vs `/{v}/healthz` vs `/metrics` (§4.5)
- [ ] Obs rate limit 503; chat exempt (§4.6)

### 4.8 Cleanup

```bash
cd devshard/testenv
make down
# Restore DEVSHARD_OBS_* / VERSIOND_DISABLE_SESSION_LOOKUP on join stacks
```

---

## 5. Edge-api / deprecated dapi plan (manual)

Companion to [release-0.2.14-v4.md](./release-0.2.14-v4.md) (Tier A dual-serve note).

**Goal:** prove Tier A `/v1/` reads work in both estates operators will run
during rollout:

1. **New join proxy** (v4) steers Tier A to **edge-api** (or **edge-api-router**).
2. **Previous proxy** (no edge-api upstream) still reaches the **same** handlers
   on **decentralized-api**, which dual-serves them as **deprecated**.

Handlers are shared (`common/queryapi`). Prefer edge-api for new configs; dapi
keeps old proxies working until they upgrade.

```text
New proxy (v4)
  /v1/status|models|participants|…  ──►  edge-api (:18080)
                                         (no Deprecation header)

Old proxy (pre-edge / EDGE_API unset)
  /v1/* catch-all  ──►  dapi (:9000)
                        same Tier A handlers + Deprecation: true
```

**Out of scope:** versiond HA (§3), lease race (§2), versionless obs (§4).

### 5.0 Preconditions

| Requirement | Why |
| --- | --- |
| Chain gRPC reachable (`CHAIN_GRPC_URL` / node `:9090`) | edge-api + query handlers need chain queries |
| **edge-api** up on join stack | New-proxy path |
| **dapi** (`api`) up on `:9000` | Old-proxy / direct dual-serve path |
| Sample Tier A paths known | `EDGE_API_ROUTE_PATHS_DEFAULT` in [`proxy/entrypoint.sh`](../../proxy/entrypoint.sh) |

Representative probes (adjust host/port for your stack):

| Path | Notes |
| --- | --- |
| `GET /v1/status` | Lightweight; always on Tier A |
| `GET /v1/versions` | Hits comet / node info |
| `GET /v1/models` | Chain models |
| `GET /v1/participants` | GET only on edge-api; POST registration stays on dapi |
| `GET /v1/epochs/latest` | Sidecar / epoch clients |
| `GET /v1/bridge/addresses?chain=ethereum` | Bridge compose still uses dapi URL today |

### 5.1 New proxy + edge-api (and optional edge-api-router)

Bring up join with **v4 proxy** + **edge-api** (optional multi overlay):

```bash
cd deploy/join
docker compose -f docker-compose.yml up -d
# optional multi edge-api:
# docker compose -f docker-compose.yml -f docker-compose.edge-api-multi.yml up -d
```

Confirm proxy env points at edge:

```bash
docker compose exec proxy printenv EDGE_API_SERVICE_NAME EDGE_API_PORT
# Expect: edge-api (or edge-api-router) and 18080
```

Probe Tier A **through the public proxy** (default `:8000`):

```bash
PROXY=http://127.0.0.1:8000

curl -sSI "$PROXY/v1/status" | grep -Ei 'HTTP/|Deprecation|^[Ss]erver:'
curl -sS "$PROXY/v1/status"
curl -sS -o /dev/null -w "%{http_code}\n" "$PROXY/v1/versions"
curl -sS -o /dev/null -w "%{http_code}\n" "$PROXY/v1/models"
curl -sS -o /dev/null -w "%{http_code}\n" "$PROXY/v1/participants"
curl -sS -o /dev/null -w "%{http_code}\n" "$PROXY/v1/epochs/latest"
```

Optional — confirm traffic hits edge-api (logs / direct hit):

```bash
curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18080/v1/status
# or: docker compose logs --since=1m edge-api
# with multi overlay: EDGE_API_SERVICE_NAME=edge-api-router
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Status / versions / models via proxy | **2xx** JSON | 502/404 to wrong upstream |
| 2 | `Deprecation` on proxy responses | **Absent** (or not `true`) for Tier A via new proxy | Every Tier A response marked deprecated (likely still on dapi) |
| 3 | edge-api direct | `:18080/v1/status` **200** | edge-api down / miswired gRPC |
| 4 | Multi (if overlay) | Proxy `EDGE_API_SERVICE_NAME=edge-api-router`; probes still **2xx** | Router misconfigured |

### 5.2 Previous proxy → deprecated dapi dual-serve

Simulate an **old proxy** that still forwards `/v1/*` to dapi (no edge-api
locations), while running the **new** dapi binary that dual-serves Tier A.

**Option A — hit dapi directly** (same handlers the old proxy would call):

```bash
DAPI=http://127.0.0.1:9000   # published api port; adjust if needed

curl -sSI "$DAPI/v1/status" | grep -Ei 'HTTP/|Deprecation|Link:'
curl -sS "$DAPI/v1/status"
curl -sSI "$DAPI/v1/versions" | grep -Ei 'HTTP/|Deprecation'
curl -sS -o /dev/null -w "%{http_code}\n" "$DAPI/v1/models"
curl -sS -o /dev/null -w "%{http_code}\n" "$DAPI/v1/participants"
curl -sS -o /dev/null -w "%{http_code}\n" "$DAPI/v1/epochs/latest"
curl -sSI "$DAPI/v1/bridge/block/latest?chain=ethereum" | grep -Ei 'HTTP/|Deprecation'
```

**Option B — run previous proxy image** against the same stack (compose pin
proxy to a pre-edge tag, or unset `EDGE_API_SERVICE_NAME` so Tier A is not
steered). Then repeat the probes through that proxy’s public port.

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Tier A on dapi | **2xx** for status / versions / models / participants / epochs | 404 (routes not remounted) |
| 2 | Deprecation header | `Deprecation: true` on those responses | Missing header on dual-serve path |
| 3 | Link hint | `Link` mentions edge-api / successor | Empty |
| 4 | Legacy-only | `/v1/bridge/block/latest?chain=…` **2xx** + deprecated (not on edge Tier A) | 404 |
| 5 | POST participants | Still on dapi (registration), not broken by GET dual-serve | POST 404/410 unexpectedly |

Body payloads for shared routes should match edge-api for the same chain state
(spot-check `status` + one models/participants sample).

### 5.3 Checklist summary

- [ ] New proxy: Tier A `/v1/` → edge-api (or edge-api-router); **2xx**, not deprecated (§5.1)
- [ ] edge-api healthy on `:18080` / router (§5.1)
- [ ] Old proxy or direct dapi: same Tier A paths **2xx** with `Deprecation: true` (§5.2)
- [ ] `/v1/bridge/block/latest` still works on dapi (deprecated) (§5.2)
- [ ] Spot-check response parity edge-api vs dapi for one/two routes (§5.2)

### 5.4 Cleanup

```bash
cd deploy/join
# Restore EDGE_API_SERVICE_NAME / proxy image pin if you changed them for §5.2
docker compose ps
```

---

## 6. Escrow long-poll warm plan (manual)

Verifies [PR #1443](https://github.com/gonka-ai/gonka/pull/1443) host-events
long-poll: DAPI publishes escrow-created events over NodeManager
`GetHostEvents`; **v4 `devshardd`** (via versiond) consumes them and prefetches
escrow metadata into storage (`escrow_cache` / equivalent) **without** needing
a request-time round-trip through the inference node / DAPI warm path.

**Goal:** after a new escrow is long-polled into v4, the **first** inference for
a new session can succeed with the **inference-node down**, using the DB record
prepared by the long-poll.

### Compatibility matrix (both must pass)

Run the full §6.1–§6.3 flow **twice** — once per dapi image — with the **same**
v4 `devshardd`. Both pairings must succeed.

| # | `devshardd` | `dapi` (api image) | Expect |
| --- | --- | --- | --- |
| A | **v4** (`devshard-0.2.14-v4` / this release) | **0.2.14** (upgrade / chain-aligned dapi) | Long-poll warm + cold inference-node path works |
| B | **v4** (same as A) | **`devshard-0.2.14-v4` release** dapi | Same as A |

Do not treat “works with one dapi” as sufficient; mixed-deploy hosts may run
either api image against v4 children.

```text
chain escrow create
       │
       ▼
 DAPI HostEventRing ──GetHostEvents (long-poll)──► v4 devshardd
                                                    │
                                                    ▼
                                              escrow_cache / session meta in DB
                                                    │
 inference-node DOWN ──► first chat (new session id) ──► serve from cache
                         (must NOT require inference-node / lazy DAPI warm)
```

Design notes: [escrow-longpoll-plan.md](./escrow-longpoll-plan.md),
[ml-node-capacity-fallback-plan.md](./ml-node-capacity-fallback-plan.md).

**Out of scope for this manual plan:** capacity-fallback concurrency bounds
(ListNodeCapacity / EMA load) — optional follow-up; lease race (§2); edge-api
(§5).

### 6.0 Preconditions

| Requirement | Why |
| --- | --- |
| Join (or testenv) with **v4** `devshardd` + **dapi** NodeManager (`NODE_MANAGER_ADDR`, typically `:9400`) | Long-poll consumer + producer |
| Ability to swap / pin **dapi** to **0.2.14** and to **`devshard-0.2.14-v4` release** (matrix A then B) | Both pairings must pass |
| Shared Postgres (or SQLite single-host) for the v4 child | Observable `escrow_cache` / session tables |
| Gateway / owner key able to create escrows and chat | Drive create + first inference |
| Ability to stop / network-partition the **inference-node** (or DAPI ML acquire path used for lazy warm) | Negative proof in §6.3 |
| Logs on dapi + versiond/devshardd | Confirm GetHostEvents delivery / warm |

For each matrix row: bring up the stack with that dapi pin, run §6.1–§6.3,
record pass/fail, then switch dapi and repeat (restore inference-node between
runs — §6.6).

### 6.1 Create escrow → long-poll → DB record

1. Note baseline: no row for the upcoming escrow in storage.

```bash
# Postgres (shared HA DB — table name is prefixed)
docker compose exec -it devshard-postgres \
  psql -U devshardd -d devshardd -c \
  "SELECT escrow_id, epoch_id, cached_at FROM devshard_escrow_cache ORDER BY cached_at DESC LIMIT 5;"
# SQLite (versiond meta DB): table is escrow_cache
# sqlite3 …/meta.db "SELECT escrow_id, epoch_id, cached_at FROM escrow_cache ORDER BY cached_at DESC LIMIT 5;"
```

2. Create a **new** escrow that includes this participant’s slot (gateway admin /
   chain tx / testenv escrow create — same path you use for normal chat).

3. Wait for long-poll delivery (seconds; watch logs):

```bash
docker compose logs -f --since=1m api versiond 2>&1 | \
  grep -Ei 'GetHostEvents|host.event|escrow.*(creat|warm|cache)|HostEvent'
```

4. Re-query the DB for the new `escrow_id`.

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Event consumed | dapi / devshardd logs show escrow-created (or host-events apply) for the new id | No host-events activity after create |
| 2 | DB row | `devshard_escrow_cache` (Postgres) or `escrow_cache` (SQLite) has the new `escrow_id` + epoch / payload | Empty / missing row |
| 3 | Timing | Row appears **before** any chat to that escrow | Row only after first inference (lazy path, not long-poll) |

Record: `escrow_id`, epoch, `cached_at`, log timestamps.

### 6.2 Take inference-node down

Stop or partition the **inference-node** (ML / node manager acquire path) so
request-time warm / `AcquireMLNode` through that node cannot succeed.

```bash
# Example — adjust to your compose service name
docker compose stop <inference-node-service>
# or: iptables / disconnect the node from dapi; confirm Acquire / health fails
```

| Check | Pass |
| --- | --- |
| Node unreachable | Health / acquire against that node fails; confirm in dapi or node logs |

Do **not** stop dapi itself if you still need chain event ingest for other
tests; this step isolates the inference-node (or the path chat would use to
lazy-warm via inference).

### 6.3 First inference (new session) without inference-node

Send the **first** chat / completion for this escrow (new OpenAI-style session /
request id) through the gateway → v4 `/devshard/…` path.

```bash
# Gateway OpenAI path (adjust URL / key / model)
curl -sS "$GATEWAY/v1/chat/completions" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"<model>","messages":[{"role":"user","content":"longpoll-warm-1"}]}'
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Response | Inference **succeeds** (2xx stream or JSON) | 5xx / timeout waiting on inference-node |
| 2 | No inference-node hit | Stopped node shows **no** new successful acquire / chat handling for this request | Request still routed to the down node |
| 3 | Uses long-poll prep | Logs / metrics show session resolved from escrow cache / warm metadata (not “create via DAPI lazy”) | Cold create attempted against down path |
| 4 | DB still consistent | Escrow cache / session rows intact after the request | Cache wiped or only created mid-request |

### 6.4 Checklist summary

Run once per matrix row (**A** = dapi **0.2.14**, **B** = dapi
**devshard-0.2.14-v4** release). Both columns must be checked.

| Check | A (dapi 0.2.14) | B (dapi v4 release) |
| --- | --- | --- |
| New escrow long-polled to v4; **DB record** before chat (§6.1) | [ ] | [ ] |
| Inference-node down (§6.2) | [ ] | [ ] |
| First inference (new session) **succeeds** without inference-node (§6.3) | [ ] | [ ] |
| Evidence: logs + DB timestamps recorded | [ ] | [ ] |

### 6.5 Testenv automation (escrow long-poll warm — implemented)

**Status: implemented** as `TestEscrowLongPollWarmWithoutInferenceNode` in
`devshard/testenv/citest/escrow_longpoll_warm_test.go`. Run with
`make -C devshard/testenv citest-escrow-longpoll` (or `-run TestEscrowLongPollWarm`). See
[testenv/docs/scenarios.md](../testenv/docs/scenarios.md) → **Escrow long-poll
warm** and **Chaining vs. parallelism**.

The citest encodes §6.1–§6.3: a mock-dapi `GetHostEvents` escrow-created event is
long-polled by v4 `devshardd`, which prefetches escrow metadata into
`escrow_cache`; the first inference then binds from the warm cache with the live
mock-chain `DevshardEscrow` query faulted (`POST /testenv/escrow-query-fault`).
It wires the previously-unwired consumer side: `devshardd` now starts
`devshard/hostevents.Run` with a sink that writes `escrow_cache`, and lazy bind
reads through a `CachingEscrowBridge` (chain-first, cache fallback).

Automation notes vs. the manual matrix: the citest runs against the single mock
NodeManager producer rather than the two real dapi images (matrix rows A/B); the
mock encodes the same `GetHostEvents` contract both images must honor, so the A/B
image pinning stays a manual check. Original sketch (still the shape of the
test):

| Step | Autotest sketch |
| --- | --- |
| 1 | Stack with mock-chain + mock-dapi (NodeManager `GetHostEvents`) + versiond/v4 + gateway |
| 2 | Create escrow → assert host-events delivery → assert `escrow_cache` (or storage API) row |
| 3 | Stop / fault inference-node (or mock acquire fail) |
| 4 | Gateway chat for that escrow → **2xx**; assert no acquire to the faulted node |
| 5 | Repeat / matrix for dapi **0.2.14** contract and **devshard-0.2.14-v4** dapi (both green) |
| 6 | Optional: capacity-fallback bounds once the escrow long-poll warm citest is green |

Tracked in the scenarios index
([testenv/docs/scenarios.md](../testenv/docs/scenarios.md) → **Escrow long-poll
warm**).

### 6.6 Cleanup

```bash
docker compose start <inference-node-service>   # if stopped in §6.2
# or make down for a full testenv reset
```

---

## 7. Rolling update plan (manual)

**Goal:** prove that a **same-name, new-sha256** governance (or mock-oracle)
binary update keeps already-accepted work alive while `versiond` blue/green
swaps the `devshardd` child. On Postgres, old and new generations **overlap**;
new requests land on the new SHA; the old child drains and exits. On hybrid /
SQLite, the swap must **not** overlap (exclusive stop/start).

Design reference: [rolling-update.md](./rolling-update.md) (Track A — child
swap inside one `versiond`). Release notes:
[release-0.2.14-v4.md](./release-0.2.14-v4.md).

```text
oracle / ApprovedVersions: same name, new sha256
        │
        ▼
 versiond: probe --print-storage-mode (old + new)
        │
        ├── both postgres → start NEW on new port → /ready+/healthz
        │                 → publish NEW route → drain OLD → SIGTERM
        └── else         → exclusive stop/start (no concurrent children)
```

**Out of scope for this plan:** whole-`versiond` host evacuation / router drain
(Track B — separate from a child SHA swap); validation-lease race (§2);
edge-api (§5); long-poll warm (§6).

Automated coverage already exists as
`TestVersiondRollingUpdateSameVersionSHA` /
`TestVersiondRollingUpdateHybridFallback`
(`make -C devshard/testenv citest-versiond-rolling-update`; see
[testenv/docs/scenarios.md](../testenv/docs/scenarios.md) — **Versiond rolling
update**) and `versioned/e2e` `TestSameNameNewSHA_RollingUpdateDrainsOld`. This
section is the **operator walkthrough** on join or a live-like stack; the steps
below follow the same assertions as the citest.

**Client-visible stamp:** protocol name (`v4`) alone cannot tell old vs new SHA.
Build two archives with the **same** `DEVSHARD_VERSION=v4` but different
`DEVSHARD_BINARY_VERSION` stamps, then poll `GET …/stats/shards` for
`binary_version` drift during the flip (§7.0.1, §7.3).

### 7.0 Preconditions

| Requirement | Why |
| --- | --- |
| HA (or single) `versiond` with a **v4** `devshardd` child for the version under test | Must support admin `/ready` + `--print-storage-mode` |
| **Postgres path (§7.1–§7.4):** `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` / `PG*` | Only mode that allows blue/green overlap (citest default stack) |
| Ability to publish a **new archive** under the **same** version name (new sha256) | Drive the swap — governance proposal, or mock-dapi `POST /testenv/versions` (citest path) |
| Two zip archives with different sha256 **and** preferably different binary stamps | Same protocol `v4`; stamps `0.2.14-v4` vs `0.2.14-v4-r2` make client poll readable (§7.0.1). Citest may use identical binaries + zip markers |
| Gateway chat path that can hold a **long SSE** stream | Citest: `stream=true` + mock-openai `StreamChunkDelay` (e.g. 750ms) |
| Access to each versiond container’s local `GET /healthz` | Observe `status` / `sha256` / draining (citest polls via compose exec) |
| Public (or versioned) `GET …/stats/shards` | Exposes `protocol_version` + `binary_version` for client-side drift checks |
| **Multi-HA tip:** pin router traffic to **one** versiond host while observing overlap | Citest sets `VERSIOND_HOSTS=<one host>` per subtest so sticky hashing cannot hide drain on another replica |
| Optional: compose patch `DEVSHARD_STORAGE_MODE=hybrid` (§7.5) | Citest: `TestVersiondRollingUpdateHybridFallback` |

#### 7.0.1 Build two same-protocol archives (distinct binary stamps)

Protocol / bind name stays **`v4`**; only the link-time binary id changes. From
repo root:

```bash
# Old generation (pre-flip) — protocol v4, binary 0.2.14-v4
make devshardd-build DEVSHARD_VERSION=v4 DEVSHARD_BINARY_VERSION=0.2.14-v4
cp build/devshardd /tmp/devshardd-0.2.14-v4
# Confirm stamps:
/tmp/devshardd-0.2.14-v4 --print-protocol-version   # → v4
/tmp/devshardd-0.2.14-v4 --print-binary-version     # → 0.2.14-v4

# New generation (post-flip) — same protocol, new binary id
make devshardd-build DEVSHARD_VERSION=v4 DEVSHARD_BINARY_VERSION=0.2.14-v4-r2
cp build/devshardd /tmp/devshardd-0.2.14-v4-r2
/tmp/devshardd-0.2.14-v4-r2 --print-protocol-version  # → v4
/tmp/devshardd-0.2.14-v4-r2 --print-binary-version    # → 0.2.14-v4-r2

# Zip for versiond download (entry name inside archive must be `devshardd`)
mkdir -p /tmp/pack
cp /tmp/devshardd-0.2.14-v4 /tmp/pack/devshardd
(cd /tmp/pack && zip -j /tmp/devshardd-old.zip devshardd)
cp /tmp/devshardd-0.2.14-v4-r2 /tmp/pack/devshardd
(cd /tmp/pack && zip -j /tmp/devshardd-new.zip devshardd)
shasum -a 256 /tmp/devshardd-old.zip /tmp/devshardd-new.zip
```

Publish `devshardd-old.zip` first (ApprovedVersions / mock-dapi), run baseline,
then switch the same version **name** to `devshardd-new.zip` (§7.2).

Record before the flip:

```bash
VERSION=v4   # protocol / ApprovedVersions.name under test
# Optional (matches citest host_0 / host_1 subtests): pin router to one replica
#   VERSIOND_HOSTS=versiond-0   # or the compose service id for that host
#
# Public path (through router / proxy — join often prefixes /devshard/)
curl -sS "http://127.0.0.1:8080/devshard/healthz" | jq .
# Or per versiond host (loopback inside the container) — citest uses this
docker compose exec versiond-0 wget -q -O - http://127.0.0.1:8080/healthz | jq .

# Client-facing stamp (through proxy / versioned child):
curl -sS "http://127.0.0.1:8000/devshard/${VERSION}/stats/shards" | \
  jq '{protocol_version, binary_version, current_epoch_id}'
# expect: protocol_version=v4, binary_version=0.2.14-v4
```

Note the **running** entry on `/healthz`: `name`, `sha256`, `status=running`,
`binary_version`. Confirm storage mode (manual extra; citest assumes stack mode):

```bash
docker compose exec versiond-0 \
  sh -c 'bin=$(find /opt/versiond/bin -type f -name devshardd | head -1); "$bin" --print-storage-mode'
# expect: postgres
```

Start a stats poller in a side terminal (leave it running through §7.2–§7.4).
From `devshard/testenv/` (same folder as §2 lease-race scripts):

```bash
cd devshard/testenv

./scripts/poll-binary-version.sh \
  --url "http://127.0.0.1:8000/devshard/${VERSION}/stats/shards" \
  --interval 1 \
  --expect-from 0.2.14-v4 \
  --expect-to 0.2.14-v4-r2
```

Versionless obs path works the same once a session is bound:
`/devshard/stats/shards` (see §4). On multi-host HA without a pin, samples may
interleave hosts — that is the drift you want to see.

### 7.1 Baseline — long stream accepted on the old SHA

Start a **slow / streaming** chat so the old child has non-zero lifecycle
inflight before the SHA change. Keep this request open through §7.2–§7.3
(citest: `StartGatewayChatCompletionStream`, wait until first content chunk).

```bash
# Example: stream=true with delayed chunks (testenv: POST mock-openai /testenv/fault
# StreamChunkDelay≈750, or a long max_tokens prompt on a real ML node).
curl -N -sS "$GATEWAY/v1/chat/completions" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"model":"<model>","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"rolling-update-long-stream"}]}'
```

In another terminal, confirm the stream is live and probe continuity on the
**versioned** health path (citest probes `router/{VERSION}/healthz`):

```bash
# Continuity probes must keep succeeding during the swap (no routing gap)
while true; do
  curl -sf -o /dev/null -w "%{http_code}\n" \
    "http://127.0.0.1:8080/${VERSION}/healthz" || echo FAIL
  # join proxy may need: .../devshard/${VERSION}/healthz
  sleep 0.3
done
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Stream started | First SSE chunks arrive; HTTP 200 (citest waits on first content) | Immediate error / stream ends before flip |
| 2 | Old SHA serving | Host `/healthz` shows this version `running` with the **pre-flip** sha256 | Wrong version / not running |
| 3 | Continuity probe | Periodic `/{VERSION}/healthz` stays 2xx | Gaps / non-200 during quiet baseline |

### 7.2 Publish the new same-name SHA

Update approved versions so **name stays**, **sha256** (and binary URL) change.
Citest does **not** use a gov proposal — it calls mock-dapi:

```bash
# A) Governance / params update (join / mainnet-like) — same ApprovedVersions.name,
#    new Binary + Sha256 for the artifact you built and published.
# B) testenv mock-dapi (citest path — no gov proposal):
curl -sS -X POST "$MOCK_DAPI/testenv/versions" \
  -H "Content-Type: application/json" \
  -d "{\"versions\":[{\"name\":\"${VERSION}\",\"binary\":\"http://mock-dapi:<port>/testenv/binaries/devshardd-new.zip\",\"sha256\":\"<64-hex>\"}]}"
```

Watch versiond reconcile (poll interval is often ~30s; shorten
`VERSIOND_POLL_INTERVAL` in lab stacks):

```bash
docker compose logs -f --since=1m versiond-0 2>&1 | \
  grep -Ei 'download|rolling|overlap|ready|drain|storage.mode|starting child|sha256'
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Download | Logs show download / install of the new sha under `bin/<name>/<sha>/` (manual; citest asserts outcomes via `/healthz`) | Stuck / hash mismatch abort with no retry later |
| 2 | Storage gate | Logs indicate overlap **enabled** (postgres) or exclusive fallback | Silent wrong path |
| 3 | Old still serving | Until route publish, long stream continues and old sha remains `running` | Old killed before new ready |

### 7.3 Observe blue/green overlap and route swap (Postgres)

While the long stream is still open, poll the **pinned** versiond host health
until you see **both** generations (citest:
`requireVersiondRollingOverlap` — `running` new sha **and** `draining` old sha):

```bash
watch -n1 'docker compose exec versiond-0 wget -q -O - http://127.0.0.1:8080/healthz | jq .'
```

Expected transition on Postgres (matches citest):

1. New child appears → `running` with **new** sha256.
2. Old entry moves to `status=draining` (same name, **old** sha256) while new is
   already `running` — **overlap window**.
3. New short **non-stream** chat succeeds while the old stream is still open.

```bash
# New traffic after swap — citest: PostGatewayChatCompletion (non-stream)
curl -sS "$GATEWAY/v1/chat/completions" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"<model>","max_tokens":64,"messages":[{"role":"user","content":"after-rolling-update"}]}'
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Overlap visible | Poll shows **running** new sha **and** **draining** old sha for the same name on the pinned host | Never drain / only one child ever |
| 2 | New traffic | Post-swap chat 2xx while old stream still open | 5xx / still stuck on old-only |
| 3 | Continuity | `/{VERSION}/healthz` probes stay 200 across the publish | Routing black hole |
| 4 | Long stream | Original SSE from §7.1 still open (not reset mid-body) | Connection reset when route flips |
| 5 | Binary drift | Stats poller (§7.0) shows `protocol_version=v4` throughout; `binary_version` moves from `0.2.14-v4` toward `0.2.14-v4-r2` | Always one stamp / protocol changed |

On multi-host HA without a router pin, repeat §7.1–§7.4 once per versiond host
(citest `host_0` / `host_1` subtests).

### 7.4 Drain completes — old generation exits

Let the long stream finish (`[DONE]` / clean close — citest requires
`SawDone` + mock content). Then wait until the old sha is gone from draining
on the host(s) under test (citest: `requireNoOldDraining`):

```bash
# Repeat until no draining entry for the old sha
docker compose exec versiond-0 wget -q -O - http://127.0.0.1:8080/healthz | jq .
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Stream completes | Original request ends 200 with a full body / `[DONE]` | Cut mid-stream after SIGTERM too early |
| 2 | Drain clears | Old sha leaves `draining`; only **new** sha remains `running` | Old stuck draining past `VERSIOND_DRAIN_TIMEOUT` |
| 3 | Process gone (manual) | Old child PID / port no longer listening (versiond logs shutdown / reap) | Zombie / restart loop of the drained child |

### 7.5 Negative: hybrid — no overlap

Repeat a same-name SHA flip with `DEVSHARD_STORAGE_MODE=hybrid` on the versiond
children (citest: `PatchVersiondStorageMode(..., "hybrid")`). Do **not** require
a long stream — citest only asserts health convergence.

```bash
# After publishing the new sha, poll health during the swap window
docker compose exec versiond-0 wget -q -O - http://127.0.0.1:8080/healthz | jq .
```

| # | Check | Pass | Fail |
| --- | --- | --- | --- |
| 1 | Exclusive path | Logs: rolling overlap disabled / stop-start fallback (manual) | Overlap enabled on hybrid |
| 2 | No draining old | While converging, **never** see `draining` for the **old** sha (citest asserts this) | `draining` old appears (blue/green on unsafe storage) |
| 3 | Ends on new sha | All hosts show only the new sha `running` | Stuck on old / version missing |

### 7.6 Checklist summary

| Check | Pass |
| --- | --- |
| Pre-flip: old sha `running` on host(s); postgres mode for §7.1–§7.4 (§7.0) | [ ] |
| Two archives: `DEVSHARD_VERSION=v4` + stamps `0.2.14-v4` / `0.2.14-v4-r2` (§7.0.1) | [ ] |
| Long SSE accepted (first chunk) before SHA publish (§7.1) | [ ] |
| Same-name new sha via mock-dapi `/testenv/versions` or gov (§7.2) | [ ] |
| Postgres: overlap `running(new)` + `draining(old)` on pinned host (§7.3) | [ ] |
| New chat succeeds while old stream open; continuity probes OK (§7.3) | [ ] |
| Stats poll: `binary_version` drifts `0.2.14-v4` → `0.2.14-v4-r2` (§7.3) | [ ] |
| Long stream `[DONE]`; old draining clears (§7.4) | [ ] |
| Hybrid: converge to new sha **without** old `draining` (§7.5) | [ ] |
| Evidence: `/healthz` + stats poll histogram (+ optional versiond logs) saved | [ ] |

### 7.7 Cleanup

```bash
# Stop continuity probe loops; leave ApprovedVersions on the new sha (or
# restore the previous artifact if this was a lab-only flip).
# Full reset:
#   cd deploy/join && docker compose down
#   # or: make -C devshard/testenv down
# Automated replay:
#   make -C devshard/testenv citest-versiond-rolling-update
```

---

## 8. Summary for operators

1. **HA multi-instance + shared Postgres applies to versions outside
   `VERSIOND_NON_HA_VERSIONS`**, not to already-deployed pre-HA binaries.
2. **versiond-router pins `VERSIOND_NON_HA_VERSIONS` to `VERSIOND_LEGACY_HOST`**;
   every other version is HA by default and gets `Devshard-Ha: true` when
   multi-host (join default `v1 v2 v3`). `devshardd` requires
   `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` for that header.
3. **HA replicas of one participant share one `KEY_NAME` / keyring** (§1.1.1).
   Join and testenv multi both wire every HA versiond to `hosts[0]`.
4. **Each versiond has its own SQLite volume; Postgres is shared.**
5. **Boot migrate** copies local SQLite → Postgres when a host flips to
   `postgres` mode; validate row inventory (§1.7). Greenfield empty data dirs
   make migrate a no-op.
6. **Test both bindings**: SQLite-bound NON_HA versions and Postgres-bound HA
   versions, plus the sqlite→HA-fail→migrate sequence — not “all migrate to
   Postgres” blindly.
7. **Tier A `/v1` reads:** new proxy → **edge-api**; old proxy → **deprecated
   dapi** dual-serve (`common/queryapi`) — **§5**.
8. **Escrow long-poll warm:** create → DB cache → inference with inference-node
   down — **§6** ([PR #1443](https://github.com/gonka-ai/gonka/pull/1443));
   prove **v4 × dapi 0.2.14** and **v4 × dapi `devshard-0.2.14-v4`** (both);
   citest `citest-escrow-longpoll` covers the automated path.
9. **Rolling updates (same name, new sha):** Postgres blue/green + drain for
   in-flight SSE; hybrid/SQLite exclusive stop/start — **§7**
   ([rolling-update.md](./rolling-update.md)); stamp two builds
   (`0.2.14-v4` / `0.2.14-v4-r2`) and poll `stats/shards` `binary_version`;
   citest `citest-versiond-rolling-update` already covers the automated path.

10. **Seven test plans in this doc:**
   - **§1 Test deployment plan** — rollout, routing, migrate, mixed binding.
   - **§2 Validation race plan** — lease exclusivity under same-key HA.
   - **§3 High availability plan** — kill / restart versiond; verify via logs;
     **§3.6** stale-standby catch-up + persist-hole checks.
   - **§4 Versionless observability plan** — obs never binds; rewrite; PG route;
     rate limit; health vs metrics.
   - **§5 Edge-api / deprecated dapi plan** — new proxy → edge-api; old proxy →
     dapi dual-serve.
   - **§6 Escrow long-poll warm plan** — host-events cache; inference without
     inference-node; both dapi pins; testenv `citest-escrow-longpoll` implemented.
   - **§7 Rolling update plan** — same-name SHA swap; SSE continuity; Postgres
     overlap vs hybrid stop/start; client `binary_version` drift via stats.
