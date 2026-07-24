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
add -> joining -> active -> draining -> stopping -> offline
                 ^          |
                 +-- cancel-+

offline -> joining -> active     replacement
offline -> removed               terminal decommission
```

Only `active` hosts are rendered as live nginx upstreams. Other states are
rendered with the nginx `down` parameter so a reload stops new assignments while
old worker processes finish established connections.

The router FSM is table-driven. Every mutation supplies the expected `from`
state, the immediate `to` state, and the final transfer target. The controller
accepts it only when the `(from, to)` handler exists and the persisted
membership is currently in `from`. A rejected handler leaves state and nginx
unchanged.

Every configured host is one membership with an immutable, generated
`membership_id`. One durable `active_transfer` owns all intermediate edges for
that membership and blocks a competing host transition. It is released only at
the final target or on `draining -> active` cancellation. `removed` is terminal
for that membership and is not persisted: the host disappears from state and
nginx. Re-adding the same name creates a new membership ID. Completed operation
IDs are deduplicated from a compact persistent receipt index, so no routing
tombstone or `removed -> removed` edge is needed. The audit is observability
data and may be rotated without changing replay behavior.

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

The router command uses a file lock and validates state transitions before
commit. It then writes a WAL entry containing the complete desired state,
completion receipts, and exact rendered nginx config. A reconciler persists
that desired generation, runs `nginx -t`, atomically publishes and reloads the
config, and records the applied generation. Repeating an already completed
operation is idempotent.

Router state, applied metadata, receipt index, pending journal, audit outbox,
and audit data live on the persistent `/var/lib/gonka/versiond-router` volume.
Back up all control-plane files together. The audit may be rotated, but the
receipt index must not be rotated or pruned: it is the durable idempotency
record for completed operation IDs.

State schema 1 is migrated automatically to schema 2 under the router lock.
Existing membership IDs are preserved; older per-host operation ownership is
converted into one `active_transfer`. Pending schema-1 and schema-2 transaction
journals are migrated before recovery. Legacy pre-reload transactions retain
their rollback behavior; legacy transactions that already reloaded nginx become
forward-only desired-state intents. The receipt index is committed with desired
state. If it does not exist during an upgrade, valid terminal audit entries are
imported once; malformed audit lines are skipped and never block router
mutation.

Bootstrap settings such as `VERSIOND_HOSTS` are fallback input for creating the
first state only. On restart, journal recovery and the persisted state run first;
bootstrap settings are not parsed when authoritative state already exists.

`gonka-routerctl status` is read-only. It reports `pending_operation` plus
desired/applied generations and a convergence flag. `gonka-routerctl recover`
is the explicit mutating recovery command. Writing the current WAL is the
desired-state commit point, so recovery always converges forward: it rewrites
state and receipts, republishes the exact journaled config, validates it,
reloads nginx idempotently, and updates applied metadata. An externally changed
output config is repaired from the WAL instead of deadlocking recovery.

Applied routing does not depend on audit availability. Audit events use a
durable outbox and at-least-once delivery; a failed append leaves an event for a
later retry without rolling back nginx or desired state.

Each active transfer is owned by a stable operation ID and membership ID. The
same ID and transfer parameters advance every edge to `offline`, `removed`, or
`active`; a replacement uses a new operation ID while retaining the membership
ID. This prevents two SSH orchestrators from controlling the same host
concurrently. `--force` may acknowledge topology or legacy-data risk, but
cannot take ownership from another transfer.
The local operation lock is fail-fast rather than a queue: a second hostctl
process receives the active action, PID, start time, and journal phase. An
operator who wants to cancel a running pre-signal evacuation first interrupts
that process, then runs `cancel`; after `term_requested`, the original
evacuation or decommission operation must resume.

## Evacuation transaction

```text
routerctl host transfer --from active --to draining --target offline HOST
  -> render exact config with HOST down
  -> commit desired generation and exact config to WAL
  -> persist desired state
  -> atomically publish the WAL config
  -> nginx -t
  -> nginx reload
  -> persist applied generation
  -> enqueue audit and retire WAL

capture the original restart policy once and enforce restart=no
persist term_requested
routerctl host transfer --from draining --to stopping --target offline HOST
  -> verify the same membership and active transfer still own the traffic barrier
send SIGTERM to versiond (managed stop)
  -> close versiond admission
  -> wait for accepted proxy leases
  -> drain and gracefully stop children and HTTP
  -> enforce one VERSIOND_HOST_SHUTDOWN_BUDGET across all graceful phases
  -> force remaining work on expiry and confirm child reap
wait for process exit up to ROUTER_DRAIN_KILL_GRACE
send SIGKILL only if the process still exists
routerctl host transfer --from stopping --to offline --target offline HOST
```

Permanent scale-down uses `removed` as the final target of the same transfer:

```text
routerctl host transfer --from offline --to removed --target removed HOST
  -> recheck at least one remaining configured host
  -> atomically transfer legacy ownership when required
  -> remove HOST from state.Hosts and rendered nginx upstreams
  -> nginx -t, publish, reload, persist terminal completion receipt
```

`gonka-hostctl decommission` owns the full stop-plus-remove sequence.
`gonka-hostctl add` performs the inverse admission sequence for a provisioned,
stopped service: create a new membership in `joining`, start, wait for
`/ready`, then activate.
`evacuate` intentionally stops at `offline` so replacement remains possible.

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

- nginx validation failure: restore the previous output projection, retain the
  committed desired generation as pending, and do not signal the host.
- nginx reload failure: keep the previous live nginx generation and valid new
  output projection, retain the committed desired generation as pending, and do
  not signal the host.
- interrupted router transaction: show desired/applied divergence in read-only
  status and reconcile the exact WAL config forward after the fault is fixed.
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
- removal reload failure: restore the `offline` host and previous nginx config.
- interrupted decommission after removal: retry with the same operation ID; the
  terminal receipt makes the completed operation a no-op.
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

The one-host-at-a-time router transfer deliberately has no automatic expiry. A
stale `draining`, `stopping`, or `joining` transfer must be resumed or, before
`term_requested`, canceled with the same operation ID. This avoids another
operator interpreting a control-plane outage as permission to drain a second
host.

The full-stack `TestVersiondHostEvacuation` test holds a stream on the selected
host, drains that router upstream, and verifies that new work moves to a
survivor while the established stream completes before versiond exits. It then
replaces the host, permanently decommissions it, verifies that nginx no longer
contains its DNS name, and adds it back through the readiness gate. Checkpoint
recovery and cancellation transitions are covered by hostctl unit tests.

For Docker replacement, the restart policy has no guessed default. Reusing an
evacuated service requires its completed evacuation journal; a newly provisioned
service requires an explicit `--docker-restart-policy`. The policy is resolved
before the router enters `joining`.

Persistent router state is authoritative after bootstrap, but operators must
also update the deployment's `VERSIOND_HOSTS` source after add or decommission.
Otherwise loss of the router state volume can reconstruct stale membership.
