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

Debug response headers: `X-Upstream-Addr`, `X-Versiond-Backend`.

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

Normal commands reject concurrent host operations, draining the last active HA
host, and draining the legacy host while it owns non-HA versions. `--force`
overrides the capacity and legacy guards for an explicitly approved outage; it
does not permit two hosts to transition at once.

## Host evacuation

Build the operator tools on a trusted administration machine:

```bash
make build-tools
```

`gonka-hostctl` connects through SSH in batch mode. It has no admin HTTP API and
adds no credentials of its own. The SSH account needs narrowly scoped rights to
run `gonka-routerctl` and to manage the selected versiond service.

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
3. Disable the Docker restart policy, send `SIGTERM`, and wait for versiond to
   drain and reap its children.
4. Send `SIGKILL` only if the kill grace expires.
5. Move the router host to `offline`.

For systemd, set both runtime flags to `systemd`. The stop step uses
`systemctl stop --no-block`, so a unit with `Restart=` cannot resurrect during
evacuation.

The operation journal defaults to
`~/.config/gonka/hostctl/<operation-id>.json`. If SSH or the operator process is
interrupted, rerun the same command with the same operation ID and journal. It
resumes after the last durable phase. The journal records the router, upstream,
SSH destination, runtime, and service scope; a retry with different targets is
rejected.
An operation-wide local file lock prevents two processes from replaying the
same checkpoint and duplicating lifecycle steps.

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
  --versiond-service versiond2
```

The replacement remains `joining` and therefore down in nginx while it starts.
It becomes `active` only after the versioned health summary reports
`state=serving`, `ready=true`, and `accepting=true`.

The orchestration flags default from these environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ROUTER_DRAIN_TIMEOUT` | `15m` | Maximum wait for known host idle or replacement readiness |
| `ROUTER_DRAIN_POLL_INTERVAL` | `2s` | Health and process polling interval |
| `ROUTER_DRAIN_KILL_GRACE` | `30m` | Maximum wait after `SIGTERM` before the kill backstop |

Keep `ROUTER_DRAIN_KILL_GRACE` greater than the versiond shutdown budget. With
the defaults it covers `VERSIOND_HOST_DRAIN_TIMEOUT` (`15m`) plus the child
shutdown grace (`10m`) and an escalation cushion.

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
