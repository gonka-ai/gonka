# versiond host evacuation

Status: implemented design and operator contract for rolling-update Track B.

This document defines how an operator removes, replaces, or upgrades a whole
`versiond` host without terminating requests that are already running. It is the
implementation contract for `rolling-update.md` section 1.8. Child binary rolling
updates remain a separate operation managed inside one live `versiond`
([rolling-update.md](./rolling-update.md), Track A).

## The operator contract

There are no evacuation commands. The lifecycle of a host is the lifecycle of
its container:

| Intent | Command | What makes it safe |
| --- | --- | --- |
| Evacuate / stop | `docker compose stop versiond2` | versiond fails `/readyz` first, then stops accepting; the router removes it before it stops taking work |
| Replace / restart | `docker compose up -d versiond2` | it rejoins the pool only once `/readyz` returns 200 |
| Add a host | start another container on the pool alias | DNS gains an A record; the router finds it within seconds |
| Decommission | stop it and do not start it again | DNS loses the record; the router forgets it |
| Quiesce without stopping | `docker compose exec versiond-router gonka-drain out versiond2` | keeps serving accepted work, receives nothing new |

This works because the router derives everything it needs by observation:
membership from DNS, health from active `/readyz` checks. Nothing has to be told
about the change, so nothing can be told about it incorrectly.

## Safety invariants

1. A host stops receiving new work **before** it stops accepting it. versiond
   reports unready for `VERSIOND_DRAIN_ANNOUNCE` (default 5s) while still
   serving, and the router's 1s health check removes it inside that window.
2. A draining host cannot accept new proxy work or start, swap, or restart a
   child process.
3. A proxy admission lease covers the complete response, including the full
   lifetime of an SSE stream.
4. Child lifecycle HTTP calls and polling never run while the process manager
   mutex is held.
5. `SIGKILL` is only an external backstop after a configured timeout. A second
   `SIGINT` is the explicit interactive force command; repeated `SIGTERM` is
   idempotent. Every child process is reaped before `versiond` exits.
6. The external kill grace must exceed versiond's single absolute shutdown
   budget, so `SIGKILL` remains a backstop rather than a competing deadline.
7. An established request stays on its original router connection and versiond
   generation. A later request for the same HA escrow may recover on another host
   from shared Postgres; legacy SQLite escrows never enter the HA pool.
8. A host that is running but not yet converged is not routed to: readiness is a
   statement about capacity to serve, not about having finished booting.

Invariant 6 in earlier revisions ("only one host is evacuated at a time, and the
last active upstream cannot be drained") is now split: `gonka-drain` still
refuses to drain the last host taking traffic, but nothing prevents an operator
from stopping two containers at once. Stopping is a Docker operation and cannot
be intercepted, so the guarantee has to come from procedure, not from software
pretending to own the host.

## State machines

The `versiond` process owns this host lifecycle:

```text
starting -> serving -> announcing -> draining -> stopping -> stopped
     |          |           |            |           |
     +----------+-----------+------------+-----------+-> forcing -> stopped
```

- `starting`: health is available, proxy admission is closed.
- `serving`: proxy admission is open and reconcile may change children.
- `announcing`: **still accepting**, but already reporting unready. This is the
  window in which the load balancer notices and stops routing here.
- `draining`: admission and desired-state changes are closed; accepted requests
  and child lifecycle work are allowed to finish.
- `stopping`: children have received `SIGTERM` and are in their process grace.
- `forcing`: an internal deadline or explicit interrupt escalated remaining
  children.
- `stopped`: all children have been reaped and the HTTP server has stopped.

The host FSM is table-driven. Each state specification owns its allowed targets,
its admission policy, and whether it advertises readiness; `serving` is the only
state that both accepts new proxy leases and advertises readiness, and
`announcing` is the only state that accepts without advertising. Unknown states
and transitions are rejected without changing lifecycle or admission state.

This state machine is intentionally separate from each child's process FSM
(`running -> terminating -> killing -> exited`). The host FSM decides when a
child should stop; the process FSM owns signal delivery, escalation, and reap.
The process FSM is event-driven and table-defined: each state/event pair chooses
the next state and `SIGTERM`, `SIGKILL`, or finish action. Finish is reachable
only through the process-exited event produced by `cmd.Wait`.

Each child generation has a lifecycle above the OS process FSM:

```text
preparing -> starting -> running -> retiring -> draining -> stopping -> stopped
                 |          |
                 +-> failed <-+
                       |
                       +-> starting
```

`retiring` removes the generation from route admission, `draining` waits for
proxy and child lifecycle counters, and `stopping` owns post-`SIGTERM` grace.
Every reconcile is also registered as a cancellable control operation. Entering
host `draining` cancels these operations before waiting for the poll worker, so
downloads, preflight probes, readiness, and non-overlap stop/start waits cannot
hide the force path.

**The router has no state machine.** Each server slot is either resolved and
passing checks (taking traffic), resolved and failing checks (not taking
traffic), administratively drained, or unresolved. All four are derived, none
are stored, and a router restart rebuilds the full picture within a couple of
seconds.

## Context ownership

`versiond` uses separate cancellation domains:

- poll context: oracle fetches, downloads, readiness waits, and reconcile;
- child lifetime context: owned by `process.Manager`, never by a poll tick;
- host shutdown context: one absolute deadline shared by the announce window,
  poll unwind, admission drain, graceful child stop, and HTTP shutdown.

Cancelling the poll context must not signal a running child. This separation is
required because host evacuation stops reconciliation before it drains work. The
host supervisor listens for force signals while canceled reconcile work is
unwinding, including during the announce window — an operator who wants to skip
the wait can interrupt again. Poll unwind receives at most ten percent of the
host budget, capped at five seconds. If a worker ignores cancellation, versiond
logs the failure and continues teardown; the manager drain barrier already
rejects new reconcile operations and disables child restart. Child process grace
may impose a shorter phase-local limit, but no phase receives a fresh deadline
that can extend the host shutdown budget. On expiry, versiond forces remaining
children and HTTP connections, then confirms child reap during the external
runtime reserve.

## Health and readiness contracts

Endpoints, all on the traffic listener (`:8080`):

| Endpoint | Answers | Used by |
| --- | --- | --- |
| `GET /healthz` | the legacy JSON array of per-version child state | operators, dashboards, existing clients |
| `GET /readyz?version=<v>` | `200` when this host has a running child serving `<v>` | the router's per-version health check |
| `GET /readyz` | `200` when this host should receive new work at all | the router's fallback check for undeclared versions |

`/readyz` is on the public listener on purpose. It is not a private admin
control: it is the contract the load balancer reads, and there is nothing in it a
caller could abuse — it exposes strictly less than `/healthz` already does.

`/readyz` returns 200 when **all** of:

- the host FSM advertises readiness (`serving`), and is accepting;
- at least one child is available to serve traffic;
- the manager has run every desired version at least once (`Converged`).

`Converged` latches. Once a versiond has run its full desired set, a later
download or child restart does not retract it. Without the latch, a routine
same-name SHA bump would briefly un-converge every host at once and evict the
entire pool — the failure mode readiness exists to prevent.

`/readyz?version=<v>` is the question the balancer actually has, and it needs no
convergence latch and no view of the desired set: either a running child serves
that version here or it does not. It reads the same route table the proxy uses,
so the answer cannot disagree with what a request would get. Draining still takes
every version out at once, or the announce window would not empty the host.

**A failed reconcile is not a readiness failure.** Every versiond reads the same
oracle, so anything gated on the outcome of that read fails everywhere at once:
one unreachable oracle, or one bad archive, would empty the pool while every
child is still running and able to serve. The failure is kept in versiond's
internal `Degraded` condition and logged; it is a deployment problem,
not a routing one.

`/healthz` is deliberately unchanged and does not carry it — that array is a
contract existing clients parse. Reconcile failures currently have no
machine-readable exposure; a metric is where that belongs.

A host that fails to install one version is handled by the per-version check
above rather than by the host-level one: it drops out of that version's pool and
keeps serving the rest. Gating the *host* on it instead would be the correlated
failure again — the same archive fails on every host, so the whole pool would
leave at once over a version most traffic does not even use.

If a condition is ever found under which accepting traffic is genuinely unsafe,
it belongs in its own typed condition rather than in the generic reconcile error.

`GET /healthz` keeps the exact legacy JSON array for existing clients. Query
parameters do not select a second schema.

## Storage safety in an HA deployment

When the deployment declares itself HA (`GONKA_HA`), two independent guards
apply:

1. **At startup**: `devshardd` refuses to boot if its storage is not fail-closed
   Postgres. A child that could fork session state never starts.
2. **At request time**: the router stamps `Devshard-Ha: true` on sticky-pool
   traffic, and `devshardd` answers `503` if it is serving that request from
   storage a sibling cannot see.

The second guard exists because the first can be bypassed by a partial rollout:
a host that was configured before the deployment became HA is already running.
The `versiond_legacy` backend strips the header, because a single-server backend
has no sibling by construction.

## Failure policy

| Situation | Behaviour |
| --- | --- |
| Host stops answering `/readyz` | removed from routing after one failed check (1s) |
| Host answers `/readyz` again | restored after two passing checks |
| Host disappears from DNS | slot empties; no further traffic |
| Router restarts | rebuilds membership and health from scratch in ~2s; in-flight requests on the old process are lost |
| Every host unready | requests get `503` from the router; there is nowhere correct to send them |
| Host stops mid-stream (`docker kill`) | that stream fails; this is why `stop` has a grace period and `kill` does not |
| Shutdown budget expires | remaining children are forced and reaped, then versiond exits |

`503` is never retried onto another host: a draining host and the HA storage
guard both answer `503`, and retrying would hit the same condition next door.
Connection failures, empty responses, and upstream `502`s are retried, because
those prove the request did not run. Non-idempotent methods are never replayed.

## Verifying it

```console
$ make -C devshard/testenv citest-versiond-host-evacuation
ok  	devshard/testenv/citest	TestVersiondHostEvacuation
```

The acceptance test drives the full lifecycle against a real stack: it starts a
paused SSE stream through the host it is about to evacuate, stops that host, and
asserts that the router stops routing there while the stream is still running,
that the stream completes, that the session is recoverable on the survivor, that
a continuity probe never sees a failure during the whole evacuation, and that the
restarted host rejoins the pool only after it reports ready.

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-router/README.md](../../versiond-router/README.md) | Router routing, health, and `gonka-drain` reference |
| [rolling-update.md](./rolling-update.md) | Same-name SHA blue/green inside one versiond (Track A) |
| [high-availability-architecture.md](./high-availability-architecture.md) | Where each component sits |
| [release-0.2.15-v5.md](./release-0.2.15-v5.md) | Operator-facing release notes |
