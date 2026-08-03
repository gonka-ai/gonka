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
  (Track B) — `docker compose stop` / `start` is the entire operator interface;
  see [versiond-host-evacuation.md](./versiond-host-evacuation.md) and
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
| `gonka-routerctl`, `gonka-hostctl` | `docker compose stop` / `start`; `gonka-drain status` to see what the router believes |
| Router state volume (`/var/lib/gonka/versiond-router`) | nothing — the router holds no durable state |
| `VERSIOND_HOSTS`, `EDGE_API_HOSTS` | `VERSIOND_POOL_HOST`, `EDGE_API_POOL_HOST` |
| `VERSIOND_ADMIN_LISTEN_ADDR` (loopback readiness listener) | `GET :8080/readyz` on the traffic listener |

Sticky routing keys its hash ring on each versiond's address, so the mapping
survives a router restart. Moving onto the new router does re-home sessions once,
because the ring is not the one nginx computed; HA sessions recover from shared
Postgres, so this costs a lookup rather than a session.

**Upgrade order matters.** The new router expects hosts that serve `/readyz` and
a pool alias in DNS, so bring the whole stack down and up rather than swapping the
router under a running pool:

```bash
docker compose -f docker-compose.yml -f docker-compose.versiond.yml down
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d
```

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
| `VERSIOND_DRAIN_ANNOUNCE` | `5s` | Keep serving after `/readyz` starts failing, so the balancer notices first |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | `25m` | Internal absolute deadline; expiry forces remaining work and reaps children |
| `VERSIOND_STOP_GRACE_PERIOD` | `30m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

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
| `EDGE_API_DRAIN_ANNOUNCE` | `5s` | `/readyz` answers 503 while the instance keeps serving, so the router drops it before it stops accepting |
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

1. add it to `VERSIOND_VERSIONS` and replace the router container
   (`docker compose up -d --force-recreate versiond-router`; a plain `restart`
   keeps the old environment) — it gains an empty pool, which changes nothing for
   the versions already running;
2. approve it in governance; each host joins that pool as it installs it.

Replacing the router is a maintenance operation: Compose will not start the new
container until the old one is gone, and `stop_signal: SIGUSR1` — HAProxy's soft
stop — makes the old one finish the streams it is carrying first. **Declare the
versions you expect ahead of time** and a governance approval needs step 2 only:
a pool for a version nobody runs yet has no healthy members and costs nothing.

Leaving `VERSIOND_VERSIONS` empty disables the mechanism entirely and keeps the
previous host-level behaviour. The join overlay declares `v4` through `v8`, so an
approval inside that window needs no router change; extend the list before
governance moves past it.

### A failed version poll no longer takes hosts out of rotation

`/readyz` reports whether a host can serve, not whether its last reconcile
succeeded. Previously any reconcile error — an unreachable oracle, an archive
that fails to download — made the host report unready. Because every versiond
reads the same oracle, that failure arrives on all of them at once, so a
control-plane hiccup could empty the pool while every child was still running and
serving normally.

A reconcile failure is still recorded — versiond keeps it in its internal
`Degraded` condition and logs it — it is simply not a routing
decision. Note that `/healthz` is unchanged and does **not** carry it: its JSON
array of per-version child state is the same contract as before, so alerting on
reconcile failures means reading the logs today. A host that fails to install a newly approved version leaves *that version's*
pool through the per-version check above, and keeps serving the versions it does
have.

### HA storage is enforced at boot as well as per request

The HA overlay sets `GONKA_HA=true`. A `devshardd` child then refuses to start
unless its storage is fail-closed Postgres, instead of starting and failing every
HA-marked request. The existing per-request `Devshard-Ha` guard stays: a process
that started before the deployment became HA is still caught at request time.

If a versiond container exits at startup with a storage-mode error after this
upgrade, its `DEVSHARD_STORAGE_MODE`/`PGHOST` did not match the HA overlay — that
node was previously serving HA traffic with unsafe storage.

## High-availability deployment

```bash
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d
docker compose -f docker-compose.yml -f docker-compose.edge-api-multi.yml up -d
```

Day-to-day operations:

| Task | Command |
| --- | --- |
| Take a host out of service | `docker compose stop versiond2` |
| Put it back / replace it | `docker compose up -d versiond2` |
| Inspect the router's live view | `docker compose exec versiond-router gonka-drain status` (read-only) |

Taking a host out of rotation is stopping it — there is no router-side drain,
because HAProxy reuses server slots and a drain would be inherited by whichever
host lands in that slot next.

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
- [ ] Confirm `VERSIOND_HOST_SHUTDOWN_BUDGET` and the larger
      `VERSIOND_STOP_GRACE_PERIOD` match the maximum acceptable maintenance
      wait; short values can terminate accepted inference streams
- [ ] Confirm `EDGE_API_STOP_GRACE_PERIOD` exceeds
      `EDGE_API_DRAIN_ANNOUNCE + EDGE_API_SHUTDOWN_BUDGET` if any of them is
      overridden
- [ ] Bring the stack down and up as a whole for the router change, rather than
      recreating only the router
- [ ] After the stack is up, check `gonka-drain status` lists every versiond as
      `UP`

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
