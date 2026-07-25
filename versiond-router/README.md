# versiond-router

Nginx sticky reverse-proxy in front of N `versiond` instances.

## Routing model

| Backend | Upstream | When |
| --- | --- | --- |
| `versiond_ha_pool` | All `VERSIOND_HOSTS`, `hash $sticky_key consistent` | Version **not** listed in `VERSIOND_NON_HA_VERSIONS` (default) |
| `versiond_legacy` | Only `VERSIOND_LEGACY_HOST` | Version listed in `VERSIOND_NON_HA_VERSIONS` |

Future versions are HA by default. Only pin known pre-HA (SQLite) path segments
in `VERSIOND_NON_HA_VERSIONS`.

When `VERSIOND_HOSTS` has **more than one** host and the request uses
`versiond_ha_pool`, nginx sets request header **`Devshard-Ha: true`**.
`devshardd` rejects that header unless `DEVSHARD_STORAGE_MODE=postgres` and
`PGHOST` are set (`common/storage/mode.RequireConfiguredForHA`).

The header reflects the configured HA topology, not the number of hosts that
are currently `active`. Draining one host must not switch requests on the
survivor to a non-HA storage mode while both hosts still share PostgreSQL.

## Environment

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `VERSIOND_HOSTS` | first boot | - | Space-separated HA pool hostnames, used to bootstrap persistent state |
| `VERSIOND_PORT` | no | `8080` | Upstream listen port |
| `VERSIOND_LEGACY_HOST` | no | first of `VERSIOND_HOSTS` | Host that owns SQLite data dirs for non-HA versions |
| `VERSIOND_NON_HA_VERSIONS` | no | empty | Whitespace and/or comma-separated version path segments pinned to legacy. Empty = all versions use the HA pool |
| `VERSIOND_ROUTER_STATE` | no | `/var/lib/gonka/versiond-router/state.json` | Persistent router FSM state |
| `VERSIOND_ROUTER_AUDIT` | no | `/var/lib/gonka/versiond-router/audit.jsonl` | Rotatable transition audit log |
| `VERSIOND_ROUTER_JOURNAL` | no | `<state>.operation.json` | Committed desired-state intent awaiting reconciliation |
| `VERSIOND_ROUTER_LOCK` | no | `/run/gonka/versiond-router.lock` | Local mutation lock |
| `VERSIOND_ROUTER_TEMPLATE` | no | `/etc/nginx/template/nginx.conf.template` | Nginx configuration template rendered from router state |
| `VERSIOND_ROUTER_OUT` | no | `/etc/nginx/conf.d/default.conf` | Published nginx configuration |
| `VERSIOND_ROUTER_NGINX_BIN` | no | `nginx` | Nginx executable used for validation and reload |
| `VERSIOND_ROUTER_MAX_BODY_BYTES` | no | `10485760` | Maximum request body; keep aligned with the outer API proxy |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT` | no | `2s` | Upstream connection deadline before HA failover |
| `VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT` | no | `20m` | Idle deadline in either direction for long HTTP/SSE requests |
| `VERSIOND_ROUTER_UPSTREAM_KEEPALIVE` | no | `64` | Idle upstream connections retained per nginx worker |

Configured numeric and duration values are validated before any command runs.
Invalid values fail fast and name the offending variable; they never silently
fall back to defaults.

Debug response headers: `X-Upstream-Addr`, `X-Versiond-Backend`.

### Failover (HA pool)

On the first upstream connect error, timeout, or **502**, nginx retries another
peer in `versiond_ha_pool` (`proxy_next_upstream`; `max_fails=1`). Sticky hash
is unchanged while peers are healthy. **503** is not retried because drain and
HA-guard responses must stay sticky. A mid-stream SSE response is not replayed;
the client must reconnect with a new request.

Proxy stream timeouts are inactivity deadlines, not limits on total inference
duration. Streaming responses may run longer while data continues to flow. If
the outer Gonka API proxy is customized, keep its body limit and transfer idle
timeout at least as large as the versiond-router policy.

After the first bootstrap, the persistent state is authoritative. Changing
`VERSIOND_HOSTS` alone does not overwrite runtime host states. Use
`gonka-routerctl` for every pool mutation. The router state directory must be
stored on a persistent volume. On later starts, bootstrap recovers any pending
transaction and loads that state before reading bootstrap environment variables.
Missing or malformed bootstrap variables therefore do not block a restart when
valid persistent state exists.

The controller stores completion receipts in `<state>.receipts.json`, the last
nginx-applied generation in `<state>.applied.json`, and undelivered audit events
in `<state>.audit-outbox.json`. These files and a pending operation journal must
stay on the same persistent volume as `state.json`. The audit log may be
rotated, truncated, or archived; it is not consulted during normal replay.
When the receipt index is first created, valid terminal records are imported
from an existing audit as one transaction. A malformed or oversized audit
record fails the import without persisting a partial index. The only exception
is an unterminated final JSONL record, which can only be a torn append and is
discarded as an uncommitted tail when the receipt index is created; malformed
terminated or internal records still fail closed. Repair or restore those
records before retrying. Back up state, receipts, applied metadata, audit
outbox, and the pending journal together. Do not rotate or prune the receipt
index: its entries are the durable idempotency records for completed operation
IDs. Exact replay protection therefore grows linearly with the number of unique
completed operation IDs.

The WAL is one replace-in-place snapshot, not an append-only history. Its size
tracks the desired state, the current operation receipt, and rendered config;
it does not copy the complete receipt index. The controller lock permits only
one pending snapshot.

Router state schema 1 is migrated under the controller lock to schema 2. The
migration preserves existing membership IDs from intermediate builds, assigns
deterministic IDs to older host records, reconstructs an in-progress
`active_transfer`, and atomically persists the upgraded state. Pending schema-1,
schema-2, schema-3, and schema-4 transaction journals are upgraded before
recovery. A pre-reload legacy transaction keeps its old rollback semantics. A
legacy transaction that already reloaded nginx is converted into a forward-only
desired-state intent. Schema 3 fixed the rendered config at commit; schema 4
added a revision and render-source fingerprint; schema 5 stores only the
current completion receipt in the WAL.

## Host lifecycle

The local controller persists this state machine:

```text
add -> joining -> active -> draining -> stopping -> offline
                 ^          |
                 +-- cancel-+

offline -> joining -> active     replace
offline -> removed               terminal decommission
```

The FSM is table-driven. Every command states the expected `from` state, the
immediate `to` state, and the final transfer target. The `(from, to)` pair must
have a registered handler, and the persisted host state must equal `from`.
Handler failure leaves both state and rendered config unchanged.

Each entry has an immutable, generated `membership_id`. A durable
`active_transfer` binds all intermediate steps to one operation ID, membership
ID, and host. It also blocks a second host transition. Reaching the final target
or canceling the drain releases that lease. This is operation ownership and
recovery metadata, not a second state machine.

Only `active` memberships receive new assignments. Nginx renders every other
stored state with the `down` parameter. `nginx -s reload` starts workers with
the new pool; old workers keep established HTTP and SSE connections until those
connections finish. `removed` is terminal for one membership and is not stored:
the host disappears from state and nginx. A later `add` with the same host name
creates a new membership ID and has no transition from the removed membership.
The receipt index makes retries of a completed operation ID idempotent without
keeping a routing tombstone.

`gonka-routerctl` runs inside the router container and exposes no network
listener:

```bash
OPERATION_ID="maintenance-$(date +%s%N)-versiond2"

docker exec versiond-router gonka-routerctl status

docker exec versiond-router gonka-routerctl recover

docker exec versiond-router gonka-routerctl operation status \
  --operation-id "$OPERATION_ID"

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id "$OPERATION_ID" \
  --from active --to draining --target offline versiond2

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id "$OPERATION_ID" \
  --from draining --to stopping --target offline versiond2

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id "$OPERATION_ID" \
  --from stopping --to offline --target offline versiond2

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id replacement-1784800000000000001-versiond2 \
  --from offline --to joining --target active \
  --address replacement-versiond2 versiond2

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id replacement-1784800000000000001-versiond2 \
  --from joining --to active --target active \
  --address replacement-versiond2 versiond2

docker exec versiond-router gonka-routerctl host transfer \
  --operation-id decommission-1784800000000000002-versiond2 \
  --from offline --to removed --target removed versiond2

docker exec versiond-router gonka-routerctl host add \
  --operation-id add-1784800000000000003-versiond3 \
  --address versiond3.internal versiond3
```

Use a new operation ID for every lifecycle transaction. Unix epoch nanoseconds
plus the host name provide a practical operator-generated ID; never reuse an ID
for a new membership.

The controller holds a file lock and validates the transition before making a
durable decision. It then writes a WAL record containing the complete desired
state, the current operation's completion receipt, and a revisioned nginx
config projection. The
projection records its config SHA and a render-source SHA derived from the
template, normalized proxy policy, and renderer schema. That WAL write is the
commit point. A reconciler persists the desired generation, validates and
atomically publishes the journaled projection, gracefully reloads nginx, and
writes `applied.json`. Recovery always repeats these idempotent steps forward;
it never guesses whether a partially applied committed operation should be
undone.

A handler or render failure before the WAL commit leaves state and nginx
unchanged. A validation or reload failure after the commit leaves a visible
pending operation: the desired generation is retained and `recover` retries
application. Before the projection reaches `applied`, changing any render source
creates the next projection revision in the same WAL. This lets an operator fix
a bad template and rerun `recover` without deleting committed control-plane
data. Once a projection is applied, later template changes run as a separate
config-only reconciliation. Repeating an applied edge or a completed operation
is a no-op. Use the same operation ID and parameters for every edge from the
first transition to its final target. A different operation cannot take over
`active_transfer`; `--force` bypasses topology and legacy-data guards, not
operation ownership.

`status` is read-only. When a journal is present, its operation ID, phase,
membership ID, `from -> to` edge, final target, candidate generation, and config
SHA are returned as `pending_operation`, together with the render revision and
render-source SHA; the command never rewrites config or reloads nginx. The
`application` object reports desired/applied generations and config SHAs plus a
`converged` flag. If a crash happens immediately after WAL commit, `status`
reports the journaled state as desired even before `state.json` is rewritten.
`recover` resolves the journal under the controller lock. It republishes the
current committed projection, so an altered or partially restored output file
cannot deadlock recovery.

`renderSchemaVersion` is part of the source fingerprint. Increment it whenever
the Go renderer changes output independently of the template or proxy policy.
Recovery rejects changed output with an unchanged fingerprint instead of
silently applying a non-reproducible projection.

Successful routing commits enqueue audit records before removing their journal.
Audit delivery is at least once: an unavailable audit file leaves events in the
durable outbox but does not roll back or block the applied routing generation.
The next mutating or recovery command retries delivery. A crash after appending
an event but before removing it from the outbox may produce a duplicate with
the same `event_id`. If both durable outbox persistence and direct audit append
fail, routing remains committed and that audit event may be lost. This is an
explicit availability-over-observability tradeoff and is reported as a warning.

Normal commands reject concurrent host operations, a mismatched membership ID,
draining the last active HA host, draining the legacy host while it owns non-HA
versions, skipping an FSM edge, and removing the last configured host. A
decommission of the legacy host requires an active replacement via
`--legacy-host`; when non-HA versions exist, migrate their local data first and
acknowledge that operation with `--force`. These terminal guards run before the
host starts draining and again before removal. `--force` does not permit two
hosts to transition at once or an empty router pool. Forcing the last active
drain creates an all-down upstream and nginx returns `502` until a host is
activated.

## Host evacuation

Build the operator tools on a trusted administration machine:

```bash
make build-tools
```

`gonka-hostctl` connects through SSH in batch mode with bounded connect and
keepalive settings. Every local or SSH command also has a configurable deadline.
It has no admin HTTP API and adds no credentials of its own. The SSH account
needs narrowly scoped rights to run `gonka-routerctl` and to manage the selected
versiond service.

Docker example:

```bash
.bin/gonka-hostctl evacuate \
  --operation-id maintenance-1784800000000000000-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --versiond-ssh worker-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2
```

The command performs these ordered steps:

1. Verify that the configured runtime exists, then start a transfer to
   `offline`: move `active -> draining`, validate the nginx config, and reload.
2. Capture the Docker restart policy, set `restart=no`, and persist
   `term_requested`.
3. Move `draining -> stopping`; that edge verifies the durable transfer owner
   and traffic barrier. Reassert `restart=no` and start a managed stop with
   `SIGTERM`.
4. Let versiond close its own admission and drain accepted requests, children,
   and HTTP within `VERSIOND_HOST_SHUTDOWN_BUDGET`; on expiry it forces
   remaining work and confirms child reap.
5. Wait for versiond to exit; send `SIGKILL` only if the outer kill grace
   expires.
6. Confirm `stopping -> offline` and complete the transfer.

If a new `evacuate` operation finds the membership already in the stable
`offline` state, it converges from that observed state instead of replaying
`active -> draining`. Hostctl first rejects any transfer owned by another
operation and confirms that the configured runtime is stopped or absent. A
present Docker container is pinned to `restart=no`; an absent container or
systemd unit is accepted with a warning. The operation then checkpoints
`router_offline` and completes without signaling a process.

The same observed-state rule applies if the service disappears after an
operation starts. Each stop-side runtime action first classifies the service as
`running`, `stopped`, or `absent`. Only a running service requires the external
stop contract and a signal; stopped or absent means the runtime side of the
stop is already complete, so hostctl continues the durable router workflow.
Before the first `active -> draining` edge, however, an absent runtime is
rejected so a typo in `--versiond-service` cannot report a false success. If
the runtime was intentionally removed before evacuation, rerun the same
operation with `--allow-absent-runtime`. This explicit recovery override is
valid only for `evacuate` and `decommission`. Docker absence must name the
configured service in an explicit `no such object/container` response, and
systemd absence requires `LoadState=not-found`; all ambiguous runtime errors
remain fail-closed.

Hostctl does not poll versiond in-flight counters. Those counters are internal
to versiond and are consumed by its shutdown state machine. This avoids making
the compatibility-oriented `/healthz` response part of the control-plane
protocol.

For systemd, set both runtime flags to `systemd`. The stop step uses
`systemctl stop --no-block`, so a unit with `Restart=` cannot resurrect during
evacuation. Before router drain of a running unit, hostctl requires
`TimeoutStopSec` to cover the configured kill grace, `KillMode=mixed`, and
`SendSIGKILL=yes`.
`KillMode=mixed` sends the initial `SIGTERM` only to versiond so it can drain
its children, while systemd retains the whole control group for final timeout
enforcement. Docker hostctl uses explicit `TERM`/`KILL` signals, so its
kill grace is owned by hostctl. The separate preflight for an explicit
`StopTimeout` protects external `docker stop`, Compose teardown, daemon
shutdown, and redeploy from using Docker's short default. The supplied compose
files set `stop_grace_period: 30m`; custom deployments must provide the same
runtime contract.

For example, the systemd unit should include:

```ini
[Service]
TimeoutStopSec=30min
KillMode=mixed
SendSIGKILL=yes
```

The operation journal defaults to
`$XDG_STATE_HOME/gonka/hostctl/<operation-id>.json`, or
`~/.local/state/gonka/hostctl/<operation-id>.json` when `XDG_STATE_HOME` is
unset. If SSH or the operator process is interrupted, rerun the same command
with the same operation ID and journal. It resumes after the last durable
phase. The journal records the router, upstream, SSH destination, runtime, and
service scope; a retry with different targets is rejected.
Hostctl journals use schema 3. Validated schema-1 and schema-2 journals are
atomically migrated on first resume; a legacy terminal cancellation without
explicit cancellation checkpoints is normalized to
`cancellation_phase=complete`.

Hostctl orchestration is a table-driven durable workflow. Each persisted phase
selects exactly one outgoing edge and named handler. The handler must succeed
before hostctl atomically checkpoints the target phase, so a retry resumes at
the failed edge without repeating earlier lifecycle work. Evacuation,
decommission, add, replace, and cancellation have separate transition tables.
Runtime validation and restart-policy capture have their own checkpoints;
mutable safety settings are still revalidated or reasserted immediately before
starting or signaling versiond. The legacy `host_idle` phase remains a
resume-only alias for schema-1 and schema-2 journals.

Completed journals are retained intentionally and are never deleted
automatically. They remain the local replay record, and a completed evacuation
journal can also supply the original Docker restart policy to `replace`. Keep
them through the dependent replacement and the operational replay window. They
may then be moved to an archive according to the operator's retention policy,
but an archived operation ID must never be reused. A new maintenance action
must always receive a new operation ID.
One router-scoped local file lock prevents concurrent hostctl operations from
mutating the same router, even when they use different operation IDs or custom
journal paths.
Commands do not queue behind that lock. A concurrent command exits immediately
with the owner operation ID, action, upstream, PID, start time, and current
journal phase. The router's durable `active_transfer` remains the authoritative
cross-machine guard when operators invoke hostctl from different workstations.
To abandon an active pre-signal evacuation, interrupt its owning process and
then run `cancel`. At or after `term_requested`, resume `evacuate` instead.

If an operation cannot continue before `SIGTERM` was sent, it may be abandoned
without leaving the cluster blocked:

```bash
.bin/gonka-hostctl cancel \
  --operation-id maintenance-1784800000000000000-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --versiond-ssh worker-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2
```

The command checks that versiond is still running and first persists a
cancellation intent. It then checkpoints Docker restart-policy restoration and
the router transition to `active` as separate compensating phases. If either
step fails while versiond remains running, rerun `cancel` with the same
operation ID. If versiond has already stopped before router reactivation,
rollback is no longer possible: rerun the original `evacuate` or `decommission`
command. It reasserts `restart=no`, atomically abandons the unfinished
compensation, and resumes the forward workflow to `offline` or `removed`.
Cancellation is rejected at and after the durable `term_requested` phase, which
is written before the remote signal command. At that point always rerun the
original stop command; do not reactivate the host even if SSH lost the command
result.

## Permanent decommission

Use `decommission` for scale-down or permanent host removal. It executes the
same drain and stop transaction as `evacuate`, moves the stopped host to
`offline`, and then commits the terminal `offline -> removed` edge:

```bash
.bin/gonka-hostctl decommission \
  --operation-id decommission-1784800000000000000-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --versiond-ssh worker-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2
```

The same command also accepts a host left `offline` by an earlier completed
evacuation, including one whose container or systemd unit has already been
removed. It rejects a transfer still owned by another operation before any
runtime mutation, verifies that a present runtime is stopped, reasserts Docker
`restart=no`, adopts `router_offline` as the first durable checkpoint of the new
operation, and removes the membership without repeating drain or shutdown.

The final edge deletes the membership from persistent router state and nginx.
It cannot transition again. A retry is answered from the completed-operation
receipt index, not by a `removed -> removed` FSM edge. If the target is
`VERSIOND_LEGACY_HOST`, pass an active survivor with `--legacy-host`. With
non-HA versions, migrate their local data before retrying with
`--force-router-guard`.

After a successful decommission, also remove the host from the deployment's
bootstrap `VERSIOND_HOSTS` value. Persistent router state remains authoritative
at runtime, but the bootstrap manifest is the disaster-recovery source if that
volume is ever lost. Reducing a two-host pool to one also removes the
`Devshard-Ha` request guard; it does not change the remaining process's
`DEVSHARD_STORAGE_MODE`. Keep that process in `postgres` mode while it uses the
shared database.

## Host addition

Use `add` for a new logical host. The service or container must already be
provisioned but stopped. The router first records it as `joining`/down, hostctl
starts it, waits for the loopback-only `GET /ready`, and only then activates it:

```bash
.bin/gonka-hostctl add \
  --operation-id scale-up-1784800000000000000-versiond3 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond3 \
  --upstream-address versiond3.internal \
  --versiond-ssh worker-3.example.net \
  --versiond-runtime docker \
  --versiond-service versiond3 \
  --docker-restart-policy unless-stopped
```

A new Docker service requires an explicit restart policy; hostctl does not
guess one. `add` creates a fresh membership ID even when the same host name was
previously decommissioned. Add the host to the deployment's bootstrap
`VERSIOND_HOSTS` value after the runtime transaction succeeds.

## Host replacement

Prepare the replacement container or systemd unit while its router state is
still `offline`, then run:

```bash
.bin/gonka-hostctl replace \
  --operation-id replacement-1784800000000000000-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --upstream-address replacement-versiond2 \
  --versiond-ssh replacement-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2 \
  --evacuation-journal \
    ~/.local/state/gonka/hostctl/maintenance-1784800000000000000-versiond2.json
```

The replacement remains `joining` and therefore down in nginx while it starts.
It becomes `active` only after `GET /ready` returns `200` on versiond's private
listener, which defaults to `127.0.0.1:8081`. The readiness endpoint stays at
`503` until
versiond is serving, accepting traffic, has an available child, and is fully
reconciled without a progressing or degraded condition. The public `:8080`
listener returns `404` for `/ready`.
This gate is deliberately fail-closed: one approved version that cannot be
downloaded or started blocks activation even when other versions are serving.
Activating a partially reconciled host would let the router send it requests
for a version it cannot serve.
Availability deliberately requires at least one approved version and one
running child route. A fresh host with an empty desired-version set remains in
`starting` and cannot be activated. Ensure governance exposes at least one
approved version before running `add` or `replace`.

For Docker, replacement has no implicit restart-policy default. When reusing a
service, pass its completed evacuation journal as above; the exact original
policy, including an `on-failure` retry count, is restored. The journal must
match the same router upstream and logical versiond runtime/service; the SSH
destination and upstream address may change for a replacement machine. For a
new service, set the intended policy explicitly, for example
`--docker-restart-policy unless-stopped`. Policy validation happens before the
router moves the host to `joining`.

The orchestration flags default from these environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ROUTER_READY_TIMEOUT` | `15m` | Maximum wait for replacement/addition readiness |
| `ROUTER_DRAIN_POLL_INTERVAL` | `2s` | Readiness and process polling interval |
| `ROUTER_DRAIN_KILL_GRACE` | `30m` | Maximum wait after `SIGTERM` before the kill backstop |
| `ROUTER_COMMAND_TIMEOUT` | `30s` | Maximum duration of one local or SSH command |

Keep `ROUTER_DRAIN_KILL_GRACE` greater than the versiond shutdown budget. With
the defaults it covers the single `VERSIOND_HOST_SHUTDOWN_BUDGET` (`25m`) and
leaves five minutes for process reap and control-plane delays. Admission drain,
child drain, graceful child stop, and HTTP shutdown all share that one versiond
deadline; their timeouts are not added together. Forced reap uses the outer
reserve rather than receiving another application timeout.

Planned stops must use `gonka-hostctl`. A direct signal can close versiond
admission after nginx has selected that upstream, producing `503`. Nginx does
not retry an inference POST after it has been sent because replay can duplicate
work.

Use `--ready-url` when versiond does not expose readiness at
`http://127.0.0.1:8081/ready`. This URL is evaluated on the versiond host or
inside its container, not on the administration machine. Set the corresponding
versiond listener with `VERSIOND_ADMIN_LISTEN_ADDR`; versiond accepts only
loopback addresses.

## Interrupted operations

1. Run `gonka-routerctl status`. If it reports `pending_operation`, run
   `gonka-routerctl recover` locally in the router container. Continue only
   when `application.converged` is `true`.
2. Rerun the original `gonka-hostctl add`, `evacuate`, `decommission`, or
   `replace` command with its operation ID, flags, and journal path. Completed
   phases are not repeated.
3. If evacuation must be abandoned before `term_requested`, run `gonka-hostctl
   cancel` with the same operation ID and scope. If the original `evacuate`
   process still owns the operation lock, interrupt it first; `cancel` reports
   the owner instead of waiting. If cancellation itself was interrupted, rerun
   `cancel` while versiond remains running. If it has already stopped before
   router reactivation, rerun the original `evacuate` or `decommission`
   command. Hostctl reads the router's completion receipt: it either abandons
   compensation and finishes the safe forward path, or records a remotely
   committed cancellation as terminal and requires a new operation ID.
4. If `term_requested` is durable, finish `evacuate` or `decommission`. Never
   reactivate an upstream whose process may already be stopping.

A host intentionally left in `draining` or `joining` blocks another host
transition. This is the cluster's one-host-at-a-time safety guard, not a lease
that expires automatically. Recovery must finish or cancel the owning operation
before starting maintenance on another host.

`gonka-routerctl operation status --operation-id ID` reads the durable
completion receipt without changing router state. Hostctl uses this lookup when
a local checkpoint is missing but the router membership or cancellation may
already have been committed.

## Local render check

```bash
make test-render

VERSIOND_HOSTS='versiond versiond2' \
VERSIOND_LEGACY_HOST=versiond \
VERSIOND_NON_HA_VERSIONS='v1,v2 v3' \
VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
VERSIOND_ROUTER_OUT=/tmp/versiond-router.conf \
VERSIOND_ROUTER_STATE=/tmp/versiond-router-state.json \
VERSIOND_ROUTER_AUDIT=/tmp/versiond-router-audit.jsonl \
VERSIOND_ROUTER_LOCK=/tmp/versiond-router.lock \
VERSIOND_ROUTER_NGINX_BIN=true \
go run ./cmd/gonka-routerctl bootstrap
```

## Deploy notes

See `devshard/docs/v4-deploy-test-plan.md` §1.2 (routing) and §3 (HA kill / first-502).
