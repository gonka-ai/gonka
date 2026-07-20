# versiond high availability — Test plans

This note covers **how to roll out** multi-instance HA + Postgres storage and
**three operator test plans**, including a routing constraint that the boot-migrate
path cannot paper over for already-deployed versions.

| Plan | Section | What it proves |
| --- | --- | --- |
| **Test deployment plan** | §1 | Rollout phases, NON_HA pin, sqlite→migrate→HA, mixed binding |
| **Validation race plan** | §2 | Same-key HA + Postgres: one validation lease per inference |
| **High availability plan** | §3 | Kill versiond → survivors serve; restart → rejoins (check logs) |

Related: [storage-design.md](./storage-design.md),
[high-availability-architecture.md](./high-availability-architecture.md),
[release-0.2.14-v4.md](./release-0.2.14-v4.md).

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
| **Testenv multi** (`gencompose`) | Every versiond replica uses `KEY_NAME=hosts[0]` (usually `versiond-0`); other host keys stay in the shared keyring + participants but are **not** HA replica identities; escrow slots all belong to the HA participant | Same as join for the HA participant; S7/S8 exercise that topology |

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
   (testenv S2 / S6 / S7 patterns). With same-key replicas, optionally confirm
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
| HA stickiness | **S2** (`TestS2_RouterStickiness`) | **No** — probes a version **outside** `VERSIOND_NON_HA_VERSIONS` (testenv: `v2`) and asserts **distinct** upstreams |
| Validation lease exclusivity | **S9** (`TestS9_ValidationLeaseRace*`) | **No** — same-key HA + Postgres lease PASS/FAIL; see **§2 Validation race plan** (and citest S9) |
| Legacy pin | **S7** (`TestS7_LegacyVersionPinnedToSingleHost`) | **Yes** — `v1` (in non-HA list) → `versiond_legacy` / `versiond-0` only; other versions still multi-upstream |
| SQLite → HA-fail → migrate → HA | **S8** (`TestS8_SqliteHaFailMigrate`) | **Yes** — full §1.7 Phases 0–4 |
| One HA upstream down | **S6** | **No** — first-502 failover to survivor (HA pool) |
| Gateway chat / gRPC | S5, G1–G4 | No |
| Params / epoch | S3, S4 | No |
| Faults | A1–A4 | No |
| Router template render | `versiond-router` `test-render` | **Partial** — asserts map text for mixed / all-legacy / `*`; does **not** hit a running nginx or check `X-Upstream-Addr` |

### 1.6 Required: proxy routes `v < v4` to a single instance

**Goal:** with ≥2 versiond hosts in `VERSIOND_HOSTS`, traffic for a non-HA version path never leaves `VERSIOND_LEGACY_HOST`.

#### Autotest: **S7** (`TestS7_LegacyVersionPinnedToSingleHost`) — implemented

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestS7_LegacyVersionPinnedToSingleHost -count=1 -v -timeout 45m
```

#### Autotest: **S8** (`TestS8_SqliteHaFailMigrate`) — §1.7 Phases 0–4

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestS8_SqliteHaFailMigrate -count=1 -v -timeout 45m
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

Also covered by **S7** once multi-host is enabled (Phase 2).

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
| Stickiness | Same session id → same upstream across retries; distinct sessions can hit both hosts (S2-style) |
| Migrated escrows | Readable/servable from either HA host (shared Postgres), not only the original SQLite volume |
| NON_HA paths | Unchanged — still legacy host / no `Devshard-Ha` |

#### Phase checklist

Covered by **S8** (`TestS8_SqliteHaFailMigrate`) in testenv (citest version name is
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
| T5 | Stop one HA host while **v4** sticky sessions exist | First-502 failover to survivor (S6); mid-stream SSE not spliced |
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
- [ ] **NON_HA path:** covered by **S7** (`X-Versiond-Backend: versiond_legacy`)
- [ ] HA path: `versiond_ha_pool` with ≥2 distinct upstreams (S2 / S7)
- [ ] §1.7 walkthrough (sqlite → Devshard-Ha 503 → postgres migrate → HA OK)
- [ ] `DEVSHARD_STORAGE_MODE=postgres` on HA children; join fails closed without
      Postgres password / `PGHOST` as documented
- [ ] Per-versiond SQLite volumes remain distinct; Postgres is shared
- [ ] (Same-key HA) `devshard_validation_leases`: one owner row per inference;
      loser does not double-submit — full walkthrough: **§2 Validation race plan**
- [ ] Kill / restart HA versiond; survivors serve and restarted host rejoins —
      **§3 High availability plan** (verify in logs)

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

This is a **manual** walkthrough (also covered by citest **S9**). Primary
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

**Host count is a minimum, not a ceiling.** Default / citest S9 uses **3** versionds
(HA pair + one solo). For a manual stack you can add more hosts in
`config/config.yaml` (`versiond-3`, …); gencompose keeps only the first two in
`VERSIOND_HOSTS` and treats every `hosts[i≥2]` as an extra solo participant.

**Out of scope:** stickiness alone (S2), legacy pin (S7), sqlite→migrate (S8),
kill/restart routing (§3). Those do not assert lease exclusivity.

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

# Session routes have no /healthz — /mempool lazy-loads the Host (same as S6).
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
testenv scripts (same checks as citest **S9**).

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

#### Automated citest (S9)

```bash
cd devshard/testenv
make build-devshardd citest-images
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run 'TestS9_' -count=1 -v -timeout 45m
```

| Test | Covers |
| --- | --- |
| `TestS9_ValidationLeaseRaceCore` | §2.4 load + monitor PASS/FAIL |
| `TestS9_ValidationLeaseRacePendingStretch` | §2.6a slow ML → pending |
| `TestS9_ValidationLeaseRaceStaleReclaim` | §2.6b short TTL + pause ML + stop replica |

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

### 2.6. Stronger race (also in S9)

#### 2.6a. Stretch the pending window (S9 `…PendingStretch`)

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

#### 2.6b. Stale reclaim (S9 `…StaleReclaim`)

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
- [ ] `./scripts/lease-race-run.sh` (or S9) → monitor **PASS** (§2.4–§2.5)
- [ ] One lease row per inference; `pending` → `submitted`/`skipped` (§2.5)
- [ ] (Optional / S9) pending stretch + stale reclaim with ML pause (§2.6)

---

### Related

- Lease implementation: `devshard/storage/leases.go` (`INSERT … ON CONFLICT DO NOTHING`)
- Wrapper: `devshard/cmd/devshardd/inference/validator.go` (`LeaseValidator`)
- Stale reclaim: `devshard/cmd/devshardd/session/retry.go` (`AcquireOneStale`)
- HA identity + routing: this document (§1)
- Testenv scripts: `devshard/testenv/scripts/lease-race-*.sh`
- Citest S9: `devshard/testenv/citest/s9_validation_lease_race_test.go`
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
semantics also exists as citest **S6** (`TestS6_VersiondStop`,
`TestS6_VersiondRestartPersistence`); this section is the operator walkthrough.

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

### 3.5 Cleanup

```bash
cd devshard/testenv
make down
```

---

## 4. Summary for operators

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

7. **Three test plans in this doc:**
   - **§1 Test deployment plan** — rollout, routing, migrate, mixed binding.
   - **§2 Validation race plan** — lease exclusivity under same-key HA.
   - **§3 High availability plan** — kill / restart versiond; verify via logs.
