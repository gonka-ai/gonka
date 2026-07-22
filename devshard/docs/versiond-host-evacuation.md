# versiond host evacuation

Status: implemented design and operator contract for rolling-update Track B.

This document defines how an operator removes, replaces, or upgrades a whole
`versiond` host without terminating requests that are already running. It is
the implementation contract for `rolling-update.md` section 1.8. Child binary
rolling updates remain a separate operation managed inside one live `versiond`.

## Safety invariants

1. The router marks an upstream down and successfully reloads before any signal
   is sent to `versiond` on that host.
2. A draining host cannot accept new proxy work or start, swap, or restart a
   child process.
3. A proxy admission lease covers the complete response, including the full
   lifetime of an SSE stream.
4. Child lifecycle HTTP calls and polling never run while the process manager
   mutex is held.
5. `SIGKILL` is only an external backstop after a configured timeout. A second
   `SIGINT` is the explicit interactive force command; repeated `SIGTERM` is
   idempotent. Every child process is reaped before `versiond` exits.
6. Only one host is evacuated at a time, and the last active HA upstream cannot
   be drained by the normal command.
7. The external kill grace must exceed versiond's single absolute shutdown
   budget, so `SIGKILL` remains a backstop rather than a competing deadline.
8. An established request stays on its original nginx worker and versiond
   generation. A later request for the same HA escrow may recover on another
   host from shared Postgres; legacy SQLite escrows never enter the HA pool.

## State machines

The `versiond` process owns this host lifecycle:

```text
starting -> serving -> draining -> stopping -> stopped
     |          |          |           |
     +----------+----------+-----------+-> forcing -> stopped
```

- `starting`: health is available, proxy admission is closed.
- `serving`: proxy admission is open and reconcile may change children.
- `draining`: admission and desired-state changes are closed; accepted requests
  and child lifecycle work are allowed to finish.
- `stopping`: children have received `SIGTERM` and are in their process grace.
- `forcing`: an internal deadline or explicit interrupt escalated remaining
  children.
- `stopped`: all children have been reaped and the HTTP server has stopped.

This state machine is intentionally separate from each child's process FSM
(`running -> terminating -> killing -> exited`). The host FSM decides when a
child should stop; the process FSM owns signal delivery, escalation, and reap.

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

The router persists this upstream lifecycle:

```text
active -> draining -> offline -> joining -> active
   ^          |
   +----------+  (cancel before versiond receives SIGTERM)
```

Only `active` hosts are rendered as live nginx upstreams. Other states are
rendered with the nginx `down` parameter so a reload stops new assignments while
old worker processes finish established connections.

## Context ownership

`versiond` uses separate cancellation domains:

- poll context: oracle fetches, downloads, readiness waits, and reconcile;
- child lifetime context: owned by `process.Manager`, never by a poll tick;
- host shutdown context: one absolute deadline shared by poll unwind, admission
  drain, graceful child stop, and HTTP shutdown.

Cancelling the poll context must not signal a running child. This separation is
required because host evacuation stops reconciliation before it drains work.
The host supervisor listens for force signals while canceled reconcile work is
unwinding. Child process grace may impose a shorter phase-local limit, but no
phase receives a fresh deadline that can extend the host shutdown budget. On
expiry, versiond forces remaining children and HTTP connections, then confirms
child reap during the external runtime reserve.

## Health and readiness contracts

`GET /healthz` keeps the exact legacy JSON array for existing clients. Query
parameters do not select a second control-plane schema. In particular, hostctl
does not use health responses to infer whether evacuation is complete.

The authoritative proxy lease and child lifecycle counters remain inside
versiond. Its host FSM consumes them after `SIGTERM`, waits for accepted work,
and escalates when the single shutdown budget expires. Keeping that decision in
the process that owns the counters avoids stale observations and avoids turning
a compatibility health endpoint into an orchestration protocol.

`GET /ready` is a separate, status-only replacement gate:

- `200` when the host is serving and accepting, at least one child is
  available, reconciliation has converged, and the manager is not progressing
  or degraded;
- `503` for every other state.

Host availability and router admission remain separate contracts. A
replacement stays `joining`/down in nginx until `/ready` returns `200`. The
endpoint contains no control command and exports no drain counters.

## Control plane and trust boundary

There is no network admin API in this track. Router mutation is performed by a
local `gonka-routerctl` command. Remote operation uses the deployment's existing
SSH access to invoke local commands on the router and versiond hosts. Therefore
this change adds no listener, credential format, mTLS PKI, or token lifecycle.

The router command uses a file lock, validates state transitions, writes a
recovery journal, tests the candidate nginx configuration, atomically publishes
it, reloads nginx, and persists an audit record. Repeating an already completed
operation is idempotent. Router state, journal, and audit data live on the
persistent `/var/lib/gonka/versiond-router` volume.

`gonka-routerctl status` is read-only and reports a `pending_operation` when the
journal is present. `gonka-routerctl recover` is the explicit mutating recovery
command. A transaction interrupted before confirmed reload rolls back. A
transaction whose journal reached `reloaded` verifies `new_config_sha256`, runs
`nginx -t`, reapplies the graceful reload, and commits the new state. Recovery
stops on a config SHA mismatch instead of silently choosing one side.

Each transitional host is owned by a stable operation ID. The same ID advances
`draining -> offline`; a separate replacement ID advances
`joining -> active`. This prevents two SSH orchestrators from controlling the
same host concurrently. A forced takeover is an explicit recovery action.
The local operation lock is fail-fast rather than a queue: a second hostctl
process receives the active action, PID, start time, and journal phase. An
operator who wants to cancel a running pre-signal evacuation first interrupts
that process, then runs `cancel`; after `term_requested`, evacuation must resume.

## Evacuation transaction

```text
routerctl host drain HOST
  -> persist intent
  -> render HOST as down
  -> nginx -t
  -> atomic config replace
  -> nginx reload
  -> verify and persist generation

reconfirm routerctl host drain HOST
capture the original restart policy once and enforce restart=no
send SIGTERM to versiond (managed stop)
  -> close versiond admission
  -> wait for accepted proxy leases
  -> drain and gracefully stop children and HTTP
  -> enforce one VERSIOND_HOST_SHUTDOWN_BUDGET across all graceful phases
  -> force remaining work on expiry and confirm child reap
wait for process exit up to ROUTER_DRAIN_KILL_GRACE
send SIGKILL only if the process still exists
routerctl host offline HOST
```

On replacement, start the new host, move it to `joining`, wait for `GET /ready`
to return `200`, and only then move it to `active`.
A replacement may keep the logical host name while changing its upstream
address. SSH disconnects and operator retries resume from the persisted router
state and the local hostctl checkpoint rather than infer success from an
in-memory command step.

`gonka-hostctl` defaults to `15m` for replacement readiness, `2s` for readiness
and process polling, `30s` for each local or SSH command, and `30m` after
`SIGTERM` before the kill backstop. versiond uses one internal `25m` shutdown
budget, leaving a five-minute outer reserve for forced process reap and
control-plane delays. SSH also uses bounded connect and keepalive settings.
Exact Docker and systemd commands are documented in
`versiond-router/README.md`.

Before the first router mutation, hostctl validates the service runtime.
systemd evacuation uses a managed stop job, so `TimeoutStopSec`, `KillMode`, and
`SendSIGKILL` directly govern that path. Docker evacuation uses explicit
`TERM`/`KILL` signals and its own hostctl deadline; Docker `StopTimeout` instead
guards external `docker stop`, Compose teardown, daemon shutdown, and redeploy.
It remains a fail-closed deployment requirement so those paths cannot bypass
the application drain budget. The standard compose files, including the local
test network, set `stop_grace_period: 30m` for versiond and the nginx router.

## Failure policy

- nginx validation failure: keep the old config and state; do not signal host.
- nginx reload failure: restore the old config and reload it; record failure.
- interrupted router transaction: show it in read-only status; roll back before
  confirmed reload or verify the candidate SHA and roll forward after reload.
- versiond shutdown budget expires with work: log the remaining internal proxy
  leases, force child/HTTP teardown, and continue process reap.
- remote command timeout: retain the last durable hostctl phase and require an
  idempotent retry; no SSH call can block the whole operation indefinitely.
- concurrent hostctl command: fail immediately with lock-owner and journal
  diagnostics; never wait silently behind a long evacuation.
- unknown legacy child inflight: versiond uses its conservative legacy drain
  cushion inside the same host shutdown budget.
- repeated `SIGTERM`: keep draining; this makes SSH retries safe.
- second `SIGINT`: transition to `forcing`, close HTTP, force children, and wait
  for process reap.
- replacement readiness failure: keep the upstream in `joining`/down.
- abandoned pre-signal evacuation: `gonka-hostctl cancel` first persists a
  cancellation intent, then checkpoints restart-policy restoration and router
  activation separately. If either action fails, rerun `cancel`; `evacuate`
  cannot cross the unfinished compensation transaction.
- evacuation at or after `term_requested`: cancellation is forbidden; that
  intent is durable before SSH sends `SIGTERM`, so resume to `offline` even when
  the remote command result is unknown.
- direct, uncoordinated `SIGTERM`: versiond returns `503` after admission closes;
  nginx does not replay inference POSTs because duplicate execution is less safe
  than an explicit client-visible failure.

The one-host-at-a-time router guard deliberately has no automatic expiry. A
stale `draining` or `joining` owner must be resumed or, before `term_requested`,
canceled with the same operation ID. This avoids another operator interpreting
a control-plane outage as permission to drain a second host.

The full-stack `TestVersiondHostEvacuation` test holds a stream on the selected
host, drains that router upstream, and verifies that new work moves to a
survivor while the established stream completes before versiond exits. It then
replaces the host and verifies that router activation waits for `/ready`.
Checkpoint recovery and cancellation transitions are covered by hostctl unit
tests.

For Docker replacement, the restart policy has no guessed default. Reusing an
evacuated service requires its completed evacuation journal; a newly provisioned
service requires an explicit `--docker-restart-policy`. The policy is resolved
before the router enters `joining`.
