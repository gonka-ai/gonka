# PR #1366 — Deploy plan and test plan

Companion to [pr-1366-description-addendum.md](./pr-1366-description-addendum.md)
(branch `ak/pixelplex-refactoring-into-r2`).

This note covers **how to roll out** multi-instance HA + Postgres storage, and
**what to verify**, including a routing constraint that the boot-migrate path
cannot paper over for already-deployed versions.

Related: [storage-design.md](./storage-design.md),
[high-availability-architecture.md](./high-availability-architecture.md),
[release-0.2.13-v2-r2.md](./release-0.2.13-v2-r2.md).

---

## 1. Constraint: boot migrate cannot upgrade live pre-v4 sessions

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

## 2. Deploy plan

### 2.1 Target topology (after this PR)

```text
clients / gateway
       │
       ▼
 versiond-router  (nginx consistent-hash on session id)
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
| `VERSIOND_NON_HA_VERSIONS` | Pre-HA version path segments pinned to legacy (whitespace and/or comma; see §2.2) |

See addendum operator checklist and `deploy/join/docker-compose.versiond.yml`.

### 2.2 Routing rules (required — implemented in versiond-router)

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

### 2.3 Rollout phases

#### Phase A — Single-instance baseline (safe for all versions)

1. Deploy this PR binaries (`versiond`, `devshardd`, gateway, edge-api,
   **versiond-router** with legacy/HA split).
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
2. Keep pre-HA names in `VERSIOND_NON_HA_VERSIONS`; all other versions
   automatically use `versiond_ha_pool` + `Devshard-Ha`.
3. Confirm non-HA paths still show `X-Versiond-Backend: versiond_legacy` and
   `X-Upstream-Addr` always the legacy host.
4. Exercise stickiness + stop-one-host behaviour **on HA versions only**
   (testenv S2 / S6 / S7 patterns).

**Exit criteria:** HA sessions survive sticky routing and single-host loss as
designed; non-HA traffic never lands on a host without its SQLite data.

#### Phase C — Retire pre-v4 (optional later)

1. Drain / stop creating escrows on old version paths.
2. Remove old version prefixes from the single-host pool once idle.
3. Optionally add a **versiond-managed migrate tool** if any long-lived pre-v4
   sessions must move to Postgres before retirement (out of scope for this PR’s
   in-binary boot migrate).

### 2.4 Explicit non-goals for this deploy

- Do **not** assume flipping `DEVSHARD_STORAGE_MODE=postgres` migrates sessions
  for already-deployed `< v4` binaries.
- Do **not** put `< v4` in the multi-instance upstream set.
- Do **not** rely on v4 boot migrate to “heal” a mixed historical estate — with
  no local sessions under the v4 data dir, migrate is a no-op by design.

---

## 3. Test plan

### 3.1 Automated (already in PR)

```bash
make -C devshard ci-testenv-unit
make -C devshard ci-testenv-integration
make -C versiond-router test-render   # config render only (no live nginx)
```

| Focus | Scenarios | Covers legacy pin (`v < v4` → one host)? |
| --- | --- | --- |
| HA stickiness | **S2** (`TestS2_RouterStickiness`) | **No** — probes a version **outside** `VERSIOND_NON_HA_VERSIONS` (testenv: `v2`) and asserts **distinct** upstreams |
| Legacy pin | **S7** (`TestS7_LegacyVersionPinnedToSingleHost`) | **Yes** — `v1` (in non-HA list) → `versiond_legacy` / `versiond-0` only; other versions still multi-upstream |
| SQLite → HA-fail → migrate → HA | **S8** (`TestS8_SqliteHaFailMigrate`) | **Yes** — full §3.3 Phases 0–4 |
| One HA upstream down | **S6** | **No** — same HA pool / sticky-hash behaviour |
| Gateway chat / gRPC | S5, G1–G4 | No |
| Params / epoch | S3, S4 | No |
| Faults | A1–A4 | No |
| Router template render | `versiond-router` `test-render` | **Partial** — asserts map text for mixed / all-legacy / `*`; does **not** hit a running nginx or check `X-Upstream-Addr` |

### 3.2 Required: proxy routes `v < v4` to a single instance

**Goal:** with ≥2 versiond hosts in `VERSIOND_HOSTS`, traffic for a non-HA version path never leaves `VERSIOND_LEGACY_HOST`.

#### Autotest: **S7** (`TestS7_LegacyVersionPinnedToSingleHost`) — implemented

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestS7_LegacyVersionPinnedToSingleHost -count=1 -v -timeout 45m
```

#### Autotest: **S8** (`TestS8_SqliteHaFailMigrate`) — §3.3 Phases 0–4

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

### 3.3 Must-check: NON_HA pin → SQLite v4 → Devshard-Ha fail → Postgres migrate

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

### 3.4 Must-check: mixed versions × mixed storage binding

Goal: prove NON_HA SQLite and HA Postgres can coexist (not only the migrate path
in §3.3).

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
| T5 | Stop one HA host while **v4** sticky sessions exist | Behaviour matches S6 for HA versions |
| T6 | Boot v4 child with **empty** data dir + `postgres` mode | Boot migrate finds nothing; Postgres-only |
| T7 | (Negative) §3.3 Phase 2 — multi-host + sqlite for v4 | 503 from `Devshard-Ha` / `RequireConfiguredForHA` |
| T8 | §3.3 Phases 3–4 — sqlite estate then `postgres` mode on that data dir | Full migrate + HA serve; row inventory matches |

#### Assertions to record

- Backend ownership: per-host SQLite files vs shared `devshard_sessions` /
  session index in Postgres (and `.pg-bound` only where Postgres holds sessions).
- Router: `X-Versiond-Backend: versiond_legacy` for NON_HA;
  `versiond_ha_pool` + `Devshard-Ha` for HA versions when multi-host.
- Gateway `/status` (or equivalent) `session_version` matches the bound path
  version for each escrow.

### 3.5 Manual smoke (join / testenv)

```bash
cd devshard/testenv
make build-devshardd gen-compose up
# gateway :18081, router :18080
```

Checklist:

- [ ] v4 chat stream + non-stream through router → Postgres-backed session
- [ ] **NON_HA path:** covered by **S7** (`X-Versiond-Backend: versiond_legacy`)
- [ ] HA path: `versiond_ha_pool` with ≥2 distinct upstreams (S2 / S7)
- [ ] §3.3 walkthrough (sqlite → Devshard-Ha 503 → postgres migrate → HA OK)
- [ ] `DEVSHARD_STORAGE_MODE=postgres` on HA children; join fails closed without
      Postgres password / `PGHOST` as documented
- [ ] Per-versiond SQLite volumes remain distinct; Postgres is shared

### 3.6 Follow-up test (out of scope until tool exists)

When a **versiond-managed migration tool** lands:

- Migrate a live SQLite estate for a pinned NON_HA version into shared Postgres
  without requiring that old binary to contain migrate code.
- Re-test removing that version from `VERSIOND_NON_HA_VERSIONS` only after the
  **serving** binary for that version also understands Postgres HA (or after
  the version is retired).

---

## 4. Summary for operators

1. **HA multi-instance + shared Postgres applies to versions outside
   `VERSIOND_NON_HA_VERSIONS`**, not to already-deployed pre-HA binaries.
2. **versiond-router pins `VERSIOND_NON_HA_VERSIONS` to `VERSIOND_LEGACY_HOST`**;
   every other version is HA by default and gets `Devshard-Ha: true` when
   multi-host (join default `v1 v2 v3`). `devshardd` requires
   `DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` for that header.
3. **Each versiond has its own SQLite volume; Postgres is shared.**
4. **Boot migrate** copies local SQLite → Postgres when a host flips to
   `postgres` mode; validate row inventory (§3.3). Greenfield empty data dirs
   make migrate a no-op.
5. **Test both bindings**: SQLite-bound NON_HA versions and Postgres-bound HA
   versions, plus the sqlite→HA-fail→migrate sequence — not “all migrate to
   Postgres” blindly.
