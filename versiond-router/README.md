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
| `VERSIOND_HOSTS` | yes | - | Space-separated HA pool hostnames, used to bootstrap persistent state |
| `VERSIOND_PORT` | no | `8080` | Upstream listen port |
| `VERSIOND_LEGACY_HOST` | no | first of `VERSIOND_HOSTS` | Host that owns SQLite data dirs for non-HA versions |
| `VERSIOND_NON_HA_VERSIONS` | no | empty | Whitespace and/or comma-separated version path segments pinned to legacy. Empty = all versions use the HA pool |
| `VERSIOND_ROUTER_STATE` | no | `/var/lib/gonka/versiond-router/state.json` | Persistent router FSM state |
| `VERSIOND_ROUTER_AUDIT` | no | `/var/lib/gonka/versiond-router/audit.jsonl` | Append-only transition audit log |
| `VERSIOND_ROUTER_JOURNAL` | no | `<state>.operation.json` | Write-ahead transaction journal |
| `VERSIOND_ROUTER_LOCK` | no | `/run/gonka/versiond-router.lock` | Local mutation lock |
| `VERSIOND_ROUTER_MAX_BODY_BYTES` | no | `10485760` | Maximum request body; keep aligned with the outer API proxy |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT` | no | `75s` | Upstream connection deadline |
| `VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT` | no | `20m` | Idle deadline in either direction for long HTTP/SSE requests |
| `VERSIOND_ROUTER_UPSTREAM_KEEPALIVE` | no | `64` | Idle upstream connections retained per nginx worker |

Debug response headers: `X-Upstream-Addr`, `X-Versiond-Backend`.

Proxy stream timeouts are inactivity deadlines, not limits on total inference
duration. Streaming responses may run longer while data continues to flow. If
the outer Gonka API proxy is customized, keep its body limit and transfer idle
timeout at least as large as the versiond-router policy.

After the first bootstrap, the persistent state is authoritative. Changing
`VERSIOND_HOSTS` alone does not overwrite runtime host states. Use
`gonka-routerctl` for every pool mutation. The router state directory must be
stored on a persistent volume.

## Host lifecycle

The local controller persists this state machine:

```text
active -> draining -> offline -> joining -> active
   ^          |
   +----------+  cancel a drain before versiond receives SIGTERM
```

Only `active` hosts receive new assignments. Nginx renders every other state
with the `down` parameter. `nginx -s reload` starts workers with the new pool;
old workers keep established HTTP and SSE connections until those connections
finish.

`gonka-routerctl` runs inside the router container and exposes no network
listener:

```bash
docker exec versiond-router gonka-routerctl status

docker exec versiond-router gonka-routerctl recover

docker exec versiond-router gonka-routerctl host drain \
  --operation-id maintenance-20260718-versiond2 versiond2

docker exec versiond-router gonka-routerctl host offline \
  --operation-id maintenance-20260718-versiond2 versiond2

docker exec versiond-router gonka-routerctl host join \
  --operation-id replacement-20260718-versiond2 \
  --address replacement-versiond2 versiond2

docker exec versiond-router gonka-routerctl host activate \
  --operation-id replacement-20260718-versiond2 versiond2
```

The controller holds a file lock, validates the transition, writes a recovery
journal, renders a candidate config, runs `nginx -t`, publishes atomically,
reloads nginx, and then commits state and audit data. A failed validation or
reload restores the previous config. Repeating a completed transition is a
no-op. The operation ID owns the transitional state: use the same ID for
`drain -> offline` and a new, stable ID for `join -> active`. A different
operation cannot take over a transitional host unless the operator explicitly
uses `--force`.

`status` is read-only. When a journal is present, its operation ID, phase,
candidate generation, and config SHA are returned as `pending_operation`; the
command never rewrites config or reloads nginx. `recover` resolves that journal
under the controller lock. A transaction interrupted before confirmed reload
rolls back. A transaction in `reloaded` verifies the published config SHA,
validates it, reloads idempotently, and commits the new state. A SHA mismatch is
left for explicit operator investigation rather than guessing which file is
authoritative.

Normal commands reject concurrent host operations, draining the last active HA
host, and draining the legacy host while it owns non-HA versions. `--force`
overrides the capacity and legacy guards for an explicitly approved outage; it
does not permit two hosts to transition at once. Forcing the last active host
creates an all-down upstream and nginx returns `502` until a host is activated.

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
  --operation-id maintenance-20260718-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --versiond-ssh worker-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2
```

The command performs these ordered steps:

1. Move the router host to `draining`, validate the nginx config, and reload.
2. Poll `versiond:8080/healthz?summary=1` until the host is known idle or the
   drain timeout expires.
3. Reconfirm the router `draining` state before the first irreversible action.
4. Capture the Docker restart policy once, reassert `restart=no`, send
   `SIGTERM`, and wait for versiond to drain and reap its children.
5. Send `SIGKILL` only if the kill grace expires.
6. Move the router host to `offline`.

For systemd, set both runtime flags to `systemd`. The stop step uses
`systemctl stop --no-block`, so a unit with `Restart=` cannot resurrect during
evacuation. Before router drain, hostctl requires `TimeoutStopSec` to cover the
configured kill grace, `KillMode=control-group` or `mixed`, and
`SendSIGKILL=yes`. Docker hostctl uses explicit `TERM`/`KILL` signals, so its
kill grace is owned by hostctl. The separate preflight for an explicit
`StopTimeout` protects external `docker stop`, Compose teardown, daemon
shutdown, and redeploy from using Docker's short default. The supplied compose
files set `stop_grace_period: 30m`; custom deployments must provide the same
runtime contract.

For example, the systemd unit should include:

```ini
[Service]
TimeoutStopSec=30min
KillMode=control-group
SendSIGKILL=yes
```

The operation journal defaults to
`~/.config/gonka/hostctl/<operation-id>.json`. If SSH or the operator process is
interrupted, rerun the same command with the same operation ID and journal. It
resumes after the last durable phase. The journal records the router, upstream,
SSH destination, runtime, and service scope; a retry with different targets is
rejected.
An operation-wide local file lock prevents two processes from replaying the
same checkpoint and duplicating lifecycle steps.

If an operation cannot continue before `SIGTERM` was sent, it may be abandoned
without leaving the cluster blocked:

```bash
.bin/gonka-hostctl cancel \
  --operation-id maintenance-20260718-versiond2 \
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
step fails, rerun `cancel` with the same operation ID. The forward `evacuate`
command refuses to continue through an unfinished cancellation. Cancellation
is rejected at and after the durable `term_requested` phase, which is written
before the remote signal command. At that point rerun `evacuate`; do not
reactivate the host even if SSH lost the command result.

## Host replacement

Prepare the replacement container or systemd unit while its router state is
still `offline`, then run:

```bash
.bin/gonka-hostctl replace \
  --operation-id replacement-20260718-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --upstream-address replacement-versiond2 \
  --versiond-ssh replacement-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2 \
  --evacuation-journal \
    ~/.config/gonka/hostctl/maintenance-20260718-versiond2.json
```

The replacement remains `joining` and therefore down in nginx while it starts.
It becomes `active` only after the versioned health summary reports
`state=serving`, `ready=true`, `accepting=true`, `available=true`,
`reconciled=true`, `progressing=false`, and `degraded=false`.

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
| `ROUTER_DRAIN_TIMEOUT` | `15m` | Maximum wait for known host idle or replacement readiness |
| `ROUTER_DRAIN_POLL_INTERVAL` | `2s` | Health and process polling interval |
| `ROUTER_DRAIN_KILL_GRACE` | `30m` | Maximum wait after `SIGTERM` before the kill backstop |
| `ROUTER_COMMAND_TIMEOUT` | `30s` | Maximum duration of one local or SSH command |

Keep `ROUTER_DRAIN_KILL_GRACE` greater than the versiond shutdown budget. With
the defaults it covers `VERSIOND_HOST_DRAIN_TIMEOUT` (`15m`) plus the child
shutdown grace (`10m`) and an escalation cushion.

Planned stops must use `gonka-hostctl`. A direct signal can close versiond
admission after nginx has selected that upstream, producing `503`. Nginx does
not retry an inference POST after it has been sent because replay can duplicate
work.

Use `--health-url` when versiond does not expose its summary at
`http://127.0.0.1:8080/healthz?summary=1`. This URL is evaluated on the versiond
host or inside its container, not on the administration machine.

## Interrupted operations

1. Run `gonka-routerctl status`. If it reports `pending_operation`, run
   `gonka-routerctl recover` locally in the router container.
2. Rerun `gonka-hostctl evacuate` or `replace` with the original operation ID,
   flags, and journal path. Completed phases are not repeated.
3. If evacuation must be abandoned before `term_requested`, run `gonka-hostctl
   cancel` with the same operation ID and scope. If cancellation itself was
   interrupted, rerun `cancel`, not `evacuate`.
4. If `term_requested` is durable, finish evacuation. Never reactivate an
   upstream whose process may already be stopping.

A host intentionally left in `draining` or `joining` blocks another host
transition. This is the cluster's one-host-at-a-time safety guard, not a lease
that expires automatically. Recovery must finish or cancel the owning operation
before starting maintenance on another host.

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

See `devshard/docs/pr-1366-deploy-test-plan.md` §2.2.
