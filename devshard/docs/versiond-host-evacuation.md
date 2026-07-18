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
7. The external kill grace must exceed versiond's host-drain and child-stop
   budget, so `SIGKILL` remains a backstop rather than a competing deadline.

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
- host drain context: admission and child-idle deadline;
- child stop context: post-`SIGTERM` process grace and forced escalation.

Cancelling the poll context must not signal a running child. This separation is
required because host evacuation stops reconciliation before it drains work.

## Health contract

`GET /healthz` keeps the legacy JSON array for existing clients. Optional fields
add per-generation proxy counters without changing the response shape.

`GET /healthz?summary=1` is the operator contract. It returns a versioned object:

```json
{
  "schema_version": 1,
  "state": "draining",
  "ready": false,
  "accepting": false,
  "proxy_inflight": 0,
  "lifecycle_inflight": 0,
  "inflight": 0,
  "inflight_known": true,
  "idle": true,
  "children": []
}
```

`proxy_inflight` is the host admission lease count. `lifecycle_inflight` is the
sum reported by child admin endpoints. These counters observe overlapping work,
so `inflight` is their maximum, not their sum. `idle` is true only when the host
proxy count is zero and every child counter is known and zero. A legacy child
without `/drain/status` keeps `inflight_known=false`; the operator must use the
timeout path and report that reduced certainty.

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

Each transitional host is owned by a stable operation ID. The same ID advances
`draining -> offline`; a separate replacement ID advances
`joining -> active`. This prevents two SSH orchestrators from controlling the
same host concurrently. A forced takeover is an explicit recovery action.

## Evacuation transaction

```text
routerctl host drain HOST
  -> persist intent
  -> render HOST as down
  -> nginx -t
  -> atomic config replace
  -> nginx reload
  -> verify and persist generation

poll HOST:8080/healthz?summary=1 until idle or ROUTER_DRAIN_TIMEOUT
disable the versiond service's automatic restart policy
send SIGTERM to versiond
wait ROUTER_DRAIN_KILL_GRACE
send SIGKILL only if the process still exists
routerctl host offline HOST
```

On replacement, start the new host, move it to `joining`, wait for a serving and
ready health summary, and only then move it to `active`. A replacement may keep
the logical host name while changing its upstream address. SSH disconnects and
operator retries resume from the persisted router state and the local hostctl
checkpoint rather than infer success from an in-memory command step.

`gonka-hostctl` defaults to `15m` for the external idle wait, `2s` for polling,
and `30m` after `SIGTERM` before the kill backstop. The latter covers versiond's
default `15m` host drain, `10m` child stop grace, and an escalation cushion.
Exact Docker and systemd commands are documented in
`versiond-router/README.md`.

## Failure policy

- nginx validation failure: keep the old config and state; do not signal host.
- nginx reload failure: restore the old config and reload it; record failure.
- host drain timeout with work: warn with the last health snapshot, then follow
  the configured stop policy.
- unknown child inflight: never report idle; use timeout and legacy grace.
- repeated `SIGTERM`: keep draining; this makes SSH retries safe.
- second `SIGINT`: transition to `forcing`, close HTTP, force children, and wait
  for process reap.
- replacement readiness failure: keep the upstream in `joining`/down.
