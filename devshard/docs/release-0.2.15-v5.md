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
  (Track B) — see [versiond-host-evacuation.md](./versiond-host-evacuation.md)
  and [rolling-update.md §1.8](./rolling-update.md#18-versiond-router-draining-versiond-hosts-ha).
- **Graceful `versiond` shutdown budgets** for both the single-instance join
  stack and the HA overlay (below).

## Breaking / operator-facing changes

### Graceful versiond shutdown (single-instance and HA)

`versiond` now owns one graceful shutdown budget across proxy admission,
accepted requests (including complete SSE streams), child drain, child stop,
and HTTP shutdown. This applies to the base single-instance join stack as well
as the HA overlay. HA evacuation first removes the host from router admission;
single-instance `docker compose down`, `stop`, or `restart` has no alternate
host, but still lets work accepted before `SIGTERM` finish.

The join Compose defaults are:

| Setting | Default | Role |
| --- | --- | --- |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | `25m` | Internal absolute deadline; expiry forces remaining work and reaps children |
| `VERSIOND_STOP_GRACE_PERIOD` | `30m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

These are maximum waits, not fixed delays: an idle versiond exits immediately.
A busy or stuck node may now make a routine Compose stop wait longer than the
old Docker default of roughly 10 seconds. Keep
`VERSIOND_STOP_GRACE_PERIOD > VERSIOND_HOST_SHUTDOWN_BUDGET` so versiond can
finish its own escalation and child reap. Operators may override both values
for a deliberately shorter maintenance window, but doing so can terminate
accepted inference streams; do not shorten only the outer Docker grace.

## High-availability deployment

_TBD. Track B operator commands are documented in
[versiond-host-evacuation.md](./versiond-host-evacuation.md) and
[versiond-router/README.md](../../versiond-router/README.md)._

## Upgrade / rollout checklist

- [ ] Confirm `VERSIOND_HOST_SHUTDOWN_BUDGET` and the larger
      `VERSIOND_STOP_GRACE_PERIOD` match the maximum acceptable maintenance
      wait; short values can terminate accepted inference streams

## Known follow-ups

_TBD._

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-host-evacuation.md](./versiond-host-evacuation.md) | Whole-host evacuation / replacement design and operator contract (Track B) |
| [rolling-update.md](./rolling-update.md) | Same-name SHA blue/green + drain (Track A) and §1.8 host draining |
| [versiond-router/README.md](../../versiond-router/README.md) | Router state, `gonka-routerctl` / `gonka-hostctl` reference |
| [release-0.2.14-v4.md](./release-0.2.14-v4.md) | Previous release line |
