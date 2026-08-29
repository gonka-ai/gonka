# v5 deploy and test plan (delta from v4)

v4 is **already released**. Do not edit
[v4-deploy-test-plan.md](./v4-deploy-test-plan.md) — it is the operator
walkthrough that shipped with `devshard-0.2.14-v4`. Storage, HA identity,
validation leases, versionless obs, rolling update, and the sqlite →
`Devshard-Ha` 503 → postgres migrate story in v4 **§1.7 / §2–§7** still apply.

This note is what **changed for the 0.2.15 / v5 line** on the **HA join
overlay** (`docker-compose.versiond.yml`): router health, how
`VERSIOND_NON_HA_VERSIONS` must be set, catalog admission, and the citest that
proves it.

Related: [versiond-router/README.md](../../versiond-router/README.md),
[release-0.2.14-v4.md](./release-0.2.14-v4.md),
[rolling-update.md](./rolling-update.md),
[testenv/docs/scenarios.md](../testenv/docs/scenarios.md).

---

## Scope: HA overlay vs single-instance v5

The current **versiond-router and versiond** changes in this note
(HAProxy L7 `/readyz` + `/<v>/healthz`, DNS pool, catalog poll,
`VERSIOND_ROUTING_ACTIVATION_MIN_READY`, `VERSIOND_NON_HA_VERSIONS` pin
rules, dual `versiond` + shared Postgres) are **high-availability
deployment**. They are **not required** to run protocol v5 on a
single-instance join.

| Deployment | Compose | Router / catalog | What v5 still needs |
| --- | --- | --- | --- |
| **Single-instance** | `deploy/join/docker-compose.yml` (one `versiond`) | No `versiond-router`. Do **not** set `VERSIOND_ROUTING_CATALOG_URL`. | Stamped `DEVSHARD_VERSION` / protocol **v5** binaries. versiond already polls governance at `VERSIOND_ORACLE_URL` (below). |
| **HA** | base + `docker-compose.versiond.yml` (`versiond`, `versiond2`, `versiond-router`) | This whole note. Catalog URL only if the router image is catalog-capable. | Same protocol stamp, plus Postgres HA storage and the router env below. |

Single-instance `VERSIOND_DRAIN_ANNOUNCE` stays `0s` (nothing to announce to).
`/readyz` on v5 `versiond` is unused unless a router is performing L7 checks.

---

## What is changing in HA deployment

The public edge is unchanged: join proxy still sends `/devshard/` to
`versiond-router` when the HA overlay is enabled. What changed is **how
the router decides a version is routable**, and therefore how you pin
pre-HA names.

### 1. Per-version L7 health (the pin rule)

v4 nginx could list a version in `VERSIOND_NON_HA_VERSIONS` even when no child
served it. A probe might 404; nginx still emitted `X-Versiond-Backend` /
`X-Upstream-Addr` (`always`).

v5 HAProxy **actively checks** each pinned or catalogued version:

1. `GET /readyz?version=<v>` → `200` (or `404` from a **pre-v5** `versiond` that
   has no `/readyz`).
2. `GET /<v>/healthz` → **`200`**. This one is not optional.

If either check fails, that version’s backend is **NOSRV**. Clients see HTTP
503 with **no** routing headers. A fictional pin (for example `v1` on a host
that only runs `v5`) takes the legacy backend down; it does not “still prove
legacy routing.”

**Operator rule:** `VERSIOND_NON_HA_VERSIONS` may contain only version names
that **actually have a running child on `VERSIOND_LEGACY_HOST`**. Join’s default
`v1 v2 v3` is correct only while those children still exist on the legacy
volume. Remove a name when you retire it. Do not add placeholders.

HA-capable versions (`v4`, `v5`, later) stay **off** that list, same as v4.

### 2. Router backends and catalog

| v4 (nginx, released) | v5 (HAProxy) |
| --- | --- |
| One `versiond_legacy` pool for every NON_HA name | One `versiond_legacy_<v>` backend per pin; response header is still `X-Versiond-Backend: versiond_legacy` |
| Undeclared version could still hit `versiond_ha_pool` | Version absent from bootstrap **and** the accepted governance catalog → **503** (not forwarded to a host that may not run it) |
| Static `VERSIOND_HOSTS` list | Pool membership from DNS (`VERSIOND_POOL_HOST=versiond-pool`) plus active checks |
| — | `VERSIOND_VERSIONS` is a **bootstrap floor**. Join default is `v4 v5`. Further names come from `VERSIOND_ROUTING_CATALOG_URL` |
| — | HA overlay sets `VERSIOND_ROUTING_ACTIVATION_MIN_READY=2`: a newly approved name is not published until **both** replicas pass the per-version checks |

### `VERSIOND_ROUTING_CATALOG_URL` (HA catalog-capable router only)

Set it to dapi’s read-only governance snapshot — the **same URL versiond
already polls** as `VERSIOND_ORACLE_URL`:

```bash
# join Compose network (service name `api`, dapi HTTP :9100):
export VERSIOND_ROUTING_CATALOG_URL=http://api:9100/versions
```

That is `GET /versions` on dapi, not the public proxy and not versiond.
Release automation is expected to set this **together with** a
catalog-capable `VERSIOND_ROUTER_IMAGE`. Until then the published nginx
`versiond-router:0.2.15` must keep the URL **empty**.

| Router image | `VERSIOND_ROUTING_CATALOG_URL` |
| --- | --- |
| Single-instance (no router) | unset / unused |
| Published **nginx** `versiond-router:0.2.15` | **empty** |
| Catalog-capable **HAProxy** | **`http://api:9100/versions`** on the join network |

Do not mix: nginx + catalog URL, or HAProxy + empty catalog URL, fails the
compose healthcheck.

`VERSIOND_LEGACY_HOST` is **required** whenever the NON_HA list is non-empty.
The router will not start without it.

`GONKA_HA` and `Devshard-Ha` are unchanged in intent: HA-pool traffic gets the
header; sqlite children still 503. Legacy backends still strip the header.

### 3. Join image cutover (nginx → catalog HAProxy)

`deploy/join/docker-compose.versiond.yml` still supports both images.
Healthcheck: nginx `GET /healthz` on `:8080`; catalog HAProxy admin
`/livez`. Until the catalog URL is set, only `VERSIOND_VERSIONS` (join
default `v4 v5`) plus NON_HA pins are routable. A governance name that
is not in that floor stays 503.

### 4. versiond `/readyz`

v5 `versiond` answers `GET /readyz?version=<v>` with 200 only while that child
is ready, 503 while starting or draining. A host can be in `v4`’s pool and out
of `v5`’s at the same time.

Pre-v5 children: `/readyz` may 404; the router still requires `/<v>/healthz`
200. That 404 fallback is the v4→v5 cutover, not a way to pin missing versions.

### 5. Protocol name

Unstamped local builds default to protocol **`v5`**
(`types.DevshardStateRootAndProtocolVersion`). Release binaries still stamp
`approved_versions.name` via `DEVSHARD_VERSION`. Citest testenv continues to
stamp **`v2`** unless you change the fixture.

### 6. Gateway vs router recreate (operational)

Height-sync and heartbeats go through the router. Force-recreating
`versiond-router` while `devshardctl` is live can 503 those calls. The gateway
**persists** a participant quarantine (~30 minutes) in
`/var/lib/devshardctl`; a process restart reloads it (`participant_limit_loaded_from_db`)
and chat becomes `429` / limiter `(0/0)`.

For planned router replacements: stop or drain the gateway first, wait until
`/{version}/healthz` is the **routed** 200 (or the intended `Devshard-Ha` 503),
then start the gateway. Citest does this in
`TestSQLiteToPostgresHAMigration` phases 2–4.

---

## What is not changing

- Same-key HA replicas, shared Postgres, `DEVSHARD_STORAGE_MODE=postgres` for
  HA versions.
- Boot migrate of **this version’s** SQLite dir only — not pre-v4 binaries
  (v4 constraint still holds; v5 does not add a versiond-managed migrate tool).
- Sticky hash on escrow/session id; first-502 failover.
- Validation leases, versionless obs, same-name SHA rolling update (v4 §2, §4, §7).

---

## Operator checklist (v5 HA overlay)

Skip this list for single-instance v5 (base `docker-compose.yml` only).

- [ ] `VERSIOND_NON_HA_VERSIONS` equals the pre-HA names that still have a
      child on `VERSIOND_LEGACY_HOST`. No fictional names.
- [ ] `VERSIOND_LEGACY_HOST` set if that list is non-empty.
- [ ] HA names (`v4`, `v5`, …) are in `VERSIOND_VERSIONS` and/or admitted by
      the catalog — **not** in the NON_HA list.
- [ ] If the router image is catalog-capable:
      `VERSIOND_ROUTING_CATALOG_URL=http://api:9100/versions` (same as
      `VERSIOND_ORACLE_URL`). If the image is nginx 0.2.15: leave it empty.
- [ ] `GET /<each-pinned-version>/healthz` is 200 through the router before
      you call the pin “live.”
- [ ] `X-Versiond-Backend: versiond_legacy` + one `X-Upstream-Addr` (legacy
      host) for pinned names; pool/catalog backend + `Devshard-Ha` for HA names
      when `GONKA_HA` / two hosts are up.
- [ ] Do not recreate `versiond-router` under a live gateway unless you accept
      a persisted participant quarantine.

---

## Automated tests (what moved vs v4 §1.6)

v4 §1.6 still describes the **released** nginx-era dummy `v1` probe. That
procedure is wrong on v5 HAProxy. Use these instead.

### `TestLegacyVersionPinnedToSingleHost`

Proves catalog admission, then pins the **running** `VersionName` (a real
child), not a fictional `v1`.

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestLegacyVersionPinnedToSingleHost -count=1 -v -timeout 45m
```

| Step | Action | Expect |
| --- | --- | --- |
| 1 | Stack up; `VERSIOND_NON_HA_VERSIONS` empty; catalog admits `VersionName` | `/{version}/healthz` 200; ≥2 distinct upstreams; `versiond_dynamic_*` |
| 2 | Recreate router with `VERSIOND_NON_HA_VERSIONS=<VersionName>` | `/{version}/healthz` 200 |
| 3 | Session probes | Every response: `X-Versiond-Backend: versiond_legacy`, upstream = `versiond-0` |
| 4 | Stop non-legacy versiond; repeat probes | Still pinned to the same legacy upstream |

### `TestSQLiteToPostgresHAMigration` — v4 §1.7 on a real child

Same migrate story as v4 §1.7. The pin is the running `VersionName` at **boot**
(so the router is not recreated while the gateway seeds). Unpin happens in the
same recreate as `GONKA_HA=true`. Gateway is stopped across phases 2–3.

```bash
make -C devshard/testenv build-devshardd citest-images
cd devshard/testenv && TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestSQLiteToPostgresHAMigration -count=1 -v -timeout 45m
```

| Step | Action | Expect |
| --- | --- | --- |
| 0 | sqlite, `GONKA_HA` empty, pin `VersionName`, stop `versiond-1` | `/{version}/healthz` 200; `versiond_legacy` / `versiond-0` |
| 1 | Gateway chat ×3; inventory SQLite | Sessions in `{data}/versiond-0/<version>/_meta.db` |
| 2 | Stop gateway; unpin; `GONKA_HA=true`; start `versiond-1`; recreate router | Routed `/{version}/healthz` **503** (`Devshard-Ha`), with routing headers (not catalog NOSRV) |
| 3 | `DEVSHARD_STORAGE_MODE=postgres`; recreate both versiond | `*.migrated.*`, `.pg-bound`, Postgres index matches inventory |
| 4 | Refresh router DNS; start gateway; chat; sticky fan-out | Pool/catalog backend; ≥2 distinct upstreams |

testenv gencompose default `VERSIOND_NON_HA_VERSIONS` is **empty**. Suites that
need a SQLite pin set it to `VersionName` themselves.

### Manual join check (only if that child exists)

Do **not** curl `/v1/...` unless `v1` is actually running on the legacy host.

```bash
# VERSION is a name in VERSIOND_NON_HA_VERSIONS that has a child:
for i in $(seq 1 16); do
  curl -sI "http://127.0.0.1:18080/${VERSION}/sessions/citest-legacy-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done
# Expect: always versiond_legacy + one upstream (VERSIOND_LEGACY_HOST)

# HA_VERSION is not in the NON_HA list and is catalogued or in VERSIOND_VERSIONS:
for i in $(seq 1 32); do
  curl -sI "http://127.0.0.1:18080/${HA_VERSION}/sessions/citest-ha-$i/healthz" \
    | grep -E 'X-Versiond-Backend|X-Upstream-Addr'
done
# Expect: versiond_ha_pool / versiond_pool_* / versiond_dynamic_* and ≥2 upstreams
```

If `/<VERSION>/healthz` is 503 with no `X-Versiond-Backend`, the pin has no
child (or catalog min-ready is not met). Fix the list or wait for admission;
do not treat that as a successful legacy pin.
