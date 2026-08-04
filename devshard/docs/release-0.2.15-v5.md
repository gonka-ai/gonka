# Release guide: `devshard-0.2.15-v5`

Operator-facing notes for the v5 line. This guide is being assembled as v5
features land; sections without content yet are placeholders.

Previous line: [release-0.2.14-v4.md](./release-0.2.14-v4.md).
Host evacuation: [versiond-host-evacuation.md](./versiond-host-evacuation.md).
Rolling updates: [rolling-update.md](./rolling-update.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).

---

## Overview

_TBD as v5 scope settles._

## What's in this release

- **Whole-`versiond` host evacuation, replacement, addition and decommission**
  (Track B) — Compose owns both the running containers and their persisted
  replica counts; see
  [versiond-host-evacuation.md](./versiond-host-evacuation.md) and
  [rolling-update.md §1.8](./rolling-update.md#18-versiond-router-draining-versiond-hosts-ha).
- **Both routers moved from nginx to HAProxy** with active health checks and
  DNS-based membership (below).
- **Graceful `versiond` shutdown budgets** for both the single-instance join
  stack and the HA overlay (below).
- **`edge-api` readiness and graceful shutdown** — an instance that cannot reach
  the chain leaves the round-robin rotation instead of answering with failing
  queries, and a stopping instance drains the same way versiond does (below).

## Breaking / operator-facing changes

### Routers are HAProxy, and the pool is a DNS name

`versiond-router` and `edge-api-router` are now HAProxy. Two consequences for
operators:

1. **The pool is no longer a host list.** `VERSIOND_HOSTS` and `EDGE_API_HOSTS`
   are replaced by `VERSIOND_POOL_HOST` / `EDGE_API_POOL_HOST`: one DNS name that
   resolves to every instance. In Compose that is a network alias
   (`versiond-pool`, `edge-api-pool`), which the shipped overlays set for you.
   Starting or stopping an instance changes the pool; nothing else has to.
2. **Health is measured, not declared.** Each router polls `GET /readyz` on every
   instance once a second and routes only to hosts that answer `200`. A host that
   is draining, still converging on a new binary, or cut off from the chain takes
   no traffic, and starts taking it again on its own when it recovers.

Removed with the old model:

| Removed | Replacement |
| --- | --- |
| `gonka-routerctl`, `gonka-hostctl` | `docker compose stop` / `start`; a read-only pool diagnostic ships inside the router image |
| Router state volume (`/var/lib/gonka/versiond-router`) | nothing — the router holds no durable state |
| `VERSIOND_HOSTS`, `EDGE_API_HOSTS` | `VERSIOND_POOL_HOST`, `EDGE_API_POOL_HOST` |
| `VERSIOND_ADMIN_LISTEN_ADDR` (loopback readiness listener) | `GET :8080/readyz` on the traffic listener |

Sticky routing keys its hash ring on each versiond's address, so the mapping
survives a router restart. Moving onto the new router does re-home sessions once,
because the ring is not the one nginx computed; HA sessions recover from shared
Postgres, so this costs a lookup rather than a session.

### Automatic v4 PostgreSQL migration

The v4 overlay left `devshard-postgres` on the anonymous
`/var/lib/postgresql/data` volume declared by `postgres:16-alpine`. The v5
overlay stores `PGDATA` in the stable `DEVSHARD_POSTGRES_DATA_DIR` bind
(`./devshards/postgres` by default) and migrates an existing v4 cluster
automatically.

The first v4-to-v5 cutover is a **devshard maintenance operation**, not a
rolling update. It restarts the one shared PostgreSQL instance, and the v4 nginx
router cannot use the v5 readiness protocol while the two versiond hosts are
being replaced. Schedule it outside PoC/cPoC, make sure no long inference or SSE
request is still in flight, and update multiple network nodes one at a time. This
matches the maintenance guidance in
[Network Updates](https://gonka.ai/docs/network-updates/).

Do not stop the whole node for this cutover. The commands below name every
service they may recreate and use `--no-deps`, so `node`, `api`, `tmkms`,
`bridge`, `proxy`, `explorer`, and ML containers remain running. If a separate
maintenance plan requires stopping those services too, first follow the full
[node stopping procedure](https://gonka.ai/docs/host/quickstart/#stopping-and-cleaning-up-your-node):
disable the ML nodes, wait for the next epoch, and verify reward and active-set
state before stopping the Network Node.

Choose the procedure that matches the existing deployment. Compose operations
must keep using the same complete model that created the installation; applying
the single-edge model to an `edge-api-multi` installation removes its pool
configuration.

#### Existing single-edge-api deployment

Upgrade in place from `deploy/join`:

```bash
(
set -e
source ./config.env
compose=(docker compose -f docker-compose.yml -f docker-compose.versiond.yml)

# Pull only the services changed by the devshard v5 release.
"${compose[@]}" \
  pull devshard-postgres versiond versiond2 versiond-router edge-api

# Recreate PostgreSQL first and wait for the v4 data migration and healthcheck.
"${compose[@]}" \
  up -d --no-deps --wait --wait-timeout 2100 devshard-postgres

# Replace versiond hosts one at a time. The legacy SQLite owner is last.
# --wait consumes the Compose /readyz healthcheck; a failed or slow reconcile
# stops this sequence before the other working host is touched.
"${compose[@]}" \
  up -d --no-deps --wait --wait-timeout 2100 versiond2
"${compose[@]}" \
  up -d --no-deps --wait --wait-timeout 2100 versiond

# Install the health-aware router only after both hosts provide /readyz.
"${compose[@]}" \
  up -d --no-deps versiond-router

# Update the single edge-api without touching its node/api dependencies.
"${compose[@]}" \
  up -d --no-deps --wait --wait-timeout 180 edge-api
)
```

#### Existing edge-api-multi deployment

Use this procedure when the running installation was created with
`docker-compose.edge-api-multi.yml`. Every command retains all three files.
The old edge-api router remains in service while its replicas are replaced one
at a time; the HAProxy router is installed only after all three answer
`/readyz`:

```bash
(
set -e
source ./config.env
compose=(
  docker compose
  -f docker-compose.yml
  -f docker-compose.versiond.yml
  -f docker-compose.edge-api-multi.yml
)

"${compose[@]}" pull \
  devshard-postgres versiond versiond2 versiond-router \
  edge-api edge-api2 edge-api3 edge-api-router

"${compose[@]}" up -d --no-deps --wait --wait-timeout 2100 \
  devshard-postgres

# Keep one ready versiond while replacing the other; install its router last.
"${compose[@]}" up -d --no-deps --wait --wait-timeout 2100 versiond2
"${compose[@]}" up -d --no-deps --wait --wait-timeout 2100 versiond
"${compose[@]}" up -d --no-deps versiond-router

# Preserve the old router and at least two ready Tier A replicas throughout.
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 edge-api2
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 edge-api3
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 edge-api

# Replace nginx only after every v5 replica has passed /readyz. This final wait
# also confirms that the new HAProxy has discovered a healthy pool member.
"${compose[@]}" up -d --no-deps --wait --wait-timeout 60 edge-api-router
)
```

Do not run `docker compose down` or use `up --renew-anon-volumes` before this
first v5 `up`. During an in-place recreation, Compose carries the v4 anonymous
volume into the replacement container. Before PostgreSQL starts, the shipped
entrypoint copies the stopped cluster to a staging directory, validates
`PG_VERSION`, syncs it, and atomically renames it to the new `PGDATA`. The old
volume is not modified and remains the physical rollback copy. Later starts use
the bind-mounted cluster directly, so `down` / `up` no longer risks this database
migration. It is still a full-stack maintenance operation and must follow the
official node stopping procedure above.

This is fail-closed if an operator removed the old container first. When the old
volume is no longer attached but existing versiond artifacts are present, the
entrypoint refuses to initialize an empty database. It logs a direct recovery
error instead of starting an apparently healthy node with lost HA state.
`DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` bypasses that guard only for an
intentional new HA database; it is not a recovery mechanism.

After startup, verify both the persistent mount and the database:

```bash
docker inspect devshard-postgres --format \
  '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/gonka"}}{{.Type}} {{.Source}}{{end}}{{end}}'
docker logs devshard-postgres 2>&1 | grep 'gonka-postgres-entrypoint'
source ./config.env
docker exec devshard-postgres \
  pg_isready -U "${DEVSHARD_POSTGRES_USER:-devshardd}" \
  -d "${DEVSHARD_POSTGRES_DB:-devshardd}"
docker exec devshard-postgres \
  psql -U "${DEVSHARD_POSTGRES_USER:-devshardd}" \
  -d "${DEVSHARD_POSTGRES_DB:-devshardd}" -Atc \
  "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = 'public';"
```

The first command must report `bind` and the configured host directory. An
upgraded v4 node also logs `v4 PostgreSQL migration completed`; a fresh node logs
that it is initializing persistent `PGDATA`. Keep the old anonymous volume and a
normal database backup through the rollback window. If the fail-closed guard
reports a detached v4 volume, stop the stack and reattach or copy that volume
into `DEVSHARD_POSTGRES_DATA_DIR` before retrying. Do not use the empty-init
override to silence this condition.

### Graceful versiond shutdown (single-instance and HA)

`versiond` now owns one graceful shutdown budget across proxy admission,
accepted requests (including complete SSE streams), child drain, child stop,
and HTTP shutdown. This applies to the base single-instance join stack as well
as the HA overlay. Under HA, a stopping host first spends
`VERSIOND_DRAIN_ANNOUNCE` reporting unready while still serving, which is what
lets the router remove it before it stops accepting; single-instance
`docker compose down`, `stop`, or `restart` has no alternate host, but still lets
work accepted before `SIGTERM` finish.

The join Compose defaults are:

| Setting | Default | Role |
| --- | --- | --- |
| `VERSIOND_DRAIN_ANNOUNCE` | `5s` | Keep serving after `/readyz` starts failing, so the balancer notices first. Counts against the shutdown budget; `0` = no balancer; below `5s` refuses to boot |
| `VERSIOND_HEALTH_START_PERIOD` | `30m` | Compose startup allowance for downloads and first reconcile. A successful `/readyz` check marks the host healthy immediately; ordered upgrades wait at most 35 minutes |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | `25m` | Internal absolute deadline; expiry forces remaining work and reaps children |
| `VERSIOND_STOP_GRACE_PERIOD` | `30m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

Before upgrading, audit custom versiond duration values. Duration settings now
use Go duration syntax and fail startup on malformed or non-positive values
instead of silently falling back to defaults. Use values with units such as
`15m` or `1s`; bare numbers and `VERSIOND_DRAIN_TIMEOUT=0` are invalid. Only
`VERSIOND_DRAIN_ANNOUNCE=0` is accepted, where it explicitly declares that no
balancer announcement window is needed.

These are maximum waits, not fixed delays: an idle versiond exits shortly after
the announce window. A busy or stuck node may make a routine Compose stop wait
longer than the old Docker default of roughly 10 seconds. Keep
`VERSIOND_STOP_GRACE_PERIOD > VERSIOND_HOST_SHUTDOWN_BUDGET` so versiond can
finish its own escalation and child reap. Operators may override these for a
deliberately shorter maintenance window, but doing so can terminate accepted
inference streams; do not shorten only the outer Docker grace.

### edge-api drains instead of cutting queries at 10 seconds

`edge-api` used to answer `SIGTERM` with a fixed 10-second `Shutdown`, which cut
any query still running. It now follows the same contract as versiond:

| Setting | Default | Role |
| --- | --- | --- |
| `EDGE_API_DRAIN_ANNOUNCE` | `5s` | `/readyz` answers 503 while the instance keeps serving, so the router drops it before it stops accepting. `0` declares no balancer; any other value below `5s` refuses to boot |
| `EDGE_API_HEALTH_START_PERIOD` | `2m` | Compose allowance for a replacement to reach the chain and pass `/readyz`; ordered upgrades wait at most three minutes |
| `EDGE_API_SHUTDOWN_BUDGET` | `2m` | How long accepted queries then have to finish; matches the router's default read timeout |
| `EDGE_API_STOP_GRACE_PERIOD` | `3m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

A second `SIGTERM`/`SIGINT` cuts the announce window short, and a further one
during the drain itself closes remaining connections immediately — the process
handles the signals itself, so without that an operator watching a stuck drain
would have nothing left but `SIGKILL`. If the budget expires with queries still
running, edge-api closes them and logs why, rather than being `SIGKILL`ed
mid-write.

The shipped Compose files set `stop_grace_period` on every edge-api service,
including the single-instance one: without it Docker's 10-second default would
`SIGKILL` the process during its own drain.

### Routing is per version, not per host

The router asks each host about the version it is about to route to
(`/readyz?version=<v>`), and keeps one pool per version listed in
`VERSIOND_VERSIONS`. A host that cannot run one version leaves that version's
pool and keeps serving every other — no eviction, no reload, no config change.

Once a version is declared this also makes its rollout safe: until a host has it
running it is not in that version's pool, so the new version's traffic goes only
where it can be served while everything else carries on.

**Declaring a version is a prerequisite for routing it.** While any version is
declared, a request for one that is not gets `503` from the router naming the
setting to fix, instead of being sent to a host that may not run it. Approving a
new version is therefore two-phase:

1. Add it to `VERSIOND_VERSIONS` and replace the router container from
   `deploy/join`:

   ```bash
   source ./config.env && \
   docker compose -f docker-compose.yml -f docker-compose.versiond.yml \
     up -d --force-recreate versiond-router
   ```

   A plain `restart` keeps the old environment. Recreating it adds an empty pool,
   which changes nothing for the versions already running.
2. Approve it in governance; each host joins that pool as it installs it.

Replacing the single shipped router service is a short maintenance operation:
Compose will not start the new container until the old one exits. HAProxy uses a
soft stop, but `VERSIOND_ROUTER_STOP_GRACE_PERIOD` defaults to 10 seconds so one
long SSE stream cannot leave the listener absent for minutes; a stream still open
at that bound is closed. A deployment that requires seamless router upgrades
must run redundant routers behind a stable frontend and roll them one at a time.
**Declare the versions you expect ahead of time** and governance approval needs
step 2 only: a pool for a version nobody runs yet has no healthy members and costs
nothing.

Leaving `VERSIOND_VERSIONS` empty disables the mechanism entirely and keeps the
previous host-level behaviour — and an HA deployment refuses to start that way,
because the host-level check would keep routing a version whose child went
unready wherever another version is still healthy
(`VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1` overrides, for stacks that cannot
declare versions up front). The join overlay declares `v4` through `v8`, so an
approval inside that window needs no router change; extend the list before
governance moves past it.

Coarse mode is an explicit two-part opt-in. Persist both lines in `config.env`,
then recreate `versiond-router` using the complete Compose model for the
installation:

```bash
export VERSIOND_VERSIONS=""
export VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true
```

An empty value is different from an unset value. Removing the first export
restores the join overlay's `v4 v5 v6 v7 v8` default. Likewise, after migrating
all legacy versions to Postgres, clear their pins with
`export VERSIOND_NON_HA_VERSIONS=""`; removing that export restores the
`v1 v2 v3` default.

### A failed version poll no longer takes hosts out of rotation

`/readyz` reports whether a host can serve, not whether its last reconcile
succeeded. Previously any reconcile error — an unreachable oracle, an archive
that fails to download — made the host report unready. Because every versiond
reads the same oracle, that failure arrives on all of them at once, so a
control-plane hiccup could empty the pool while every child was still running and
serving normally.

A reconcile failure is still recorded — versiond keeps it in its internal
`Degraded` condition and logs it — it is simply not a routing decision. Note
that `/healthz` is unchanged and does **not** carry it: its JSON array of
per-version child state is the same contract as before, so alerting on reconcile
failures means reading the logs today. A host that fails to install a newly
approved version leaves *that version's* pool through the per-version check
above, and keeps serving the versions it does have. Pinned pre-HA versions use
the same per-version check against their single SQLite owner, so a failed v5
install cannot hide otherwise healthy v1-v3 routes after the restart.

### HA storage is enforced at boot as well as per request

The HA overlay sets `GONKA_HA=true`. A `devshardd` child then refuses to start
unless its storage is fail-closed Postgres, instead of starting and failing every
HA-marked request. The existing per-request `Devshard-Ha` guard stays: a process
that started before the deployment became HA is still caught at request time.
The router also stamps that header when it sees more than one usable host in the
backend selected for a request, so accidentally scaling without the overlay
fails closed at request time. The explicit flag remains required because it
keeps the guard enabled through a partial pool outage.

If a versiond container exits at startup with a storage-mode error after this
upgrade, its `DEVSHARD_STORAGE_MODE`/`PGHOST` did not match the HA overlay — that
node was previously serving HA traffic with unsafe storage.

## High-availability deployment

Run these commands from `deploy/join`. Choose one complete Compose model and use
the same set of `-f` arguments for later service-targeted operations. These
full-model `up` commands are for initial deployment; use the targeted cutover
above when upgrading a running v4 node:

```bash
# HA versiond and shared PostgreSQL, with one edge-api.
source ./config.env && \
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d

# HA versiond plus the optional multi-instance edge-api pool.
source ./config.env && \
docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.edge-api-multi.yml \
  up -d
```

Day-to-day operations:

| Task | Command |
| --- | --- |
| Take `versiond2` out of service temporarily | `source ./config.env && docker compose -f docker-compose.yml -f docker-compose.versiond.yml stop versiond2` |
| Put it back / replace it | `source ./config.env && docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d --no-deps --wait --wait-timeout 2100 versiond2` |
| Decommission `versiond2` permanently | persist `VERSIOND2_REPLICAS=0` in `config.env`, then run the `stop` and `rm` commands in the [host evacuation runbook](./versiond-host-evacuation.md#permanent-membership-changes) |
| Inspect the router's live view | `source ./config.env && docker compose -f docker-compose.yml -f docker-compose.versiond.yml exec versiond-router /usr/local/lib/versiond-router/pool-status` (read-only) |

Taking a host out of rotation is stopping it — there is no router-side drain,
because HAProxy reuses server slots and a drain would be inherited by whichever
host lands in that slot next.

Do not stop or decommission `VERSIOND_LEGACY_HOST` while
`VERSIOND_NON_HA_VERSIONS` is non-empty. Those versions have SQLite state on
that host and no failover backend. A plain `stop` is also not a permanent
decommission: `restart: always` can bring the container back after a Docker
daemon restart. Persist the corresponding replica count as `0` first.

## Upgrade / rollout checklist

- [ ] Set `VERSIOND_VERSIONS` to every version this deployment serves — an
      undeclared version is refused, and the list must be updated *before*
      governance approves a new one
- [ ] Replace `VERSIOND_HOSTS` / `EDGE_API_HOSTS` overrides with
      `VERSIOND_POOL_HOST` / `EDGE_API_POOL_HOST`, or drop them and take the
      shipped defaults
- [ ] Remove `VERSIOND_ADMIN_LISTEN_ADDR` from `config.env`; readiness is now
      `:8080/readyz`
- [ ] Confirm every versiond in the HA overlay has
      `DEVSHARD_STORAGE_MODE=postgres` and a reachable `PGHOST` before enabling
      `GONKA_HA`
- [ ] Perform the first v4-to-v5 cutover with in-place `docker compose up -d`,
      not `down` or `up --renew-anon-volumes`; confirm the Postgres log reports a
      completed automatic migration and retain the old volume through rollback
- [ ] Confirm `VERSIOND_HOST_SHUTDOWN_BUDGET` and the larger
      `VERSIOND_STOP_GRACE_PERIOD` match the maximum acceptable maintenance
      wait; short values can terminate accepted inference streams
- [ ] Keep `VERSIOND_REPLICAS` and `VERSIOND2_REPLICAS` in `config.env`; use a
      persisted value of `0` for permanent decommission and never decommission
      `VERSIOND_LEGACY_HOST` while `VERSIOND_NON_HA_VERSIONS` is non-empty
- [ ] To remove all legacy pins, persist
      `VERSIOND_NON_HA_VERSIONS=""`; do not unset it, because unset restores
      the `v1 v2 v3` default
- [ ] Keep `--wait --wait-timeout 2100` on each ordered versiond replacement;
      do not start replacing the legacy owner until `versiond2` is healthy
- [ ] For an existing edge-api-multi installation, retain all three Compose
      files, replace `edge-api2`, `edge-api3`, and `edge-api` one at a time with
      `--wait`, and replace `edge-api-router` last
- [ ] Confirm `EDGE_API_STOP_GRACE_PERIOD` exceeds
      `EDGE_API_DRAIN_ANNOUNCE + EDGE_API_SHUTDOWN_BUDGET` if any of them is
      overridden
- [ ] Use the targeted `--no-deps` cutover above: migrate PostgreSQL first,
      replace `versiond2` and then the legacy owner `versiond`, and install the
      router only after both hosts expose `/readyz`; do not reconcile the whole
      base Compose model as part of this devshard-only release
- [ ] After the stack is up, check the pool diagnostic lists every versiond as
      `UP` using the read-only `pool-status` command above

## Known follow-ups

- Kubernetes: the readiness contract is on the traffic listener specifically so a
  `readinessProbe` and `preStop` can replace the router's role unchanged. Not in
  this release.
- Version names: the router now routes any name that can appear literally in a
  path segment, but one containing `/`, `?`, `#`, `%` or whitespace cannot be
  routed at all, because the request path would no longer match the name.
  Narrowing the chain's parameter validation to a routable grammar is the proper
  fix.
- Reconcile failures have no machine-readable exposure. They belong in a metric,
  not bolted onto the `/healthz` array that existing clients parse; versiond has
  no metrics endpoint of its own yet.

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-host-evacuation.md](./versiond-host-evacuation.md) | Whole-host evacuation / replacement design and operator contract (Track B) |
| [rolling-update.md](./rolling-update.md) | Same-name SHA blue/green + drain (Track A) and §1.8 host draining |
| [versiond-router/README.md](../../versiond-router/README.md) | Router routing, per-version health checks, and how to read the pool |
| [release-0.2.14-v4.md](./release-0.2.14-v4.md) | Previous release line |
