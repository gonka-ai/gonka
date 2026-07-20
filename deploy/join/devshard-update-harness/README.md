# Devshard gateway update harness

Minimal production-shaped stack for testing a blue/green devshard gateway
update on a host that does **not** run the full join node stack.

## What this provides

```text
clients / load test
   |
   v
proxy                         container name: proxy
   |                           config: /etc/nginx/nginx.conf
   |                           public path: /devshard-gateway/v1/...
   v
devshard-gateway              from ../docker-compose.devshard-gateway.yml
   |
   +-- temp gateway later      alias: devshard-gateway-temp (created by update flow)
```

This harness matches the container names and nginx switch points expected by
the production update workflow (`devshard-gateway` <-> `devshard-gateway-temp`
inside `proxy`).

It does **not** start node, api, versiond, explorer, miners, or any other join
services. Point the gateway at remote chain/API endpoints in your local
`config.devshard.env`.

## Files

| File | Role |
| --- | --- |
| `docker-compose.yml` | Umbrella: gateway + proxy |
| `docker-compose.proxy.yml` | Lightweight `proxy` only |
| `nginx/nginx.conf` | Switchable upstream to `devshard-gateway:8080` |

Gateway service definition is reused from
`../docker-compose.devshard-gateway.yml`.

Gateway env is **not** committed here. Use the existing join template:

- `../config.devshard.env.template` -> copy to `../config.devshard.env`
- fill in chain/API URLs, keys, model, and `DEVSHARD_ROUTE_PREFIX` locally

The automated update script and its JSON config live under
`devshard/docs/devshard-update/` (merge that docs/PR branch separately).

## Prerequisites

From `deploy/join`:

1. Create the shared Docker network (once per host):

```bash
docker network create join_default
```

2. Prepare gateway env (local only, not committed):

```bash
cp config.devshard.env.template config.devshard.env
chmod 600 config.devshard.env
# edit: DEVSHARD_CHAIN_REST, DEVSHARD_PUBLIC_API, DEVSHARD_PRIVATE_KEY, etc.
```

3. Set the gateway image tag in `docker-compose.devshard-gateway.yml` to the
   image you built or pulled on that host.

4. Export compose-level vars if needed:

```bash
export DEVSHARD_ENV_FILE=./config.devshard.env
export DEVSHARD_INSTANCE_NAME=devshard-gateway
export DEVSHARD_STORAGE_HOST_DIR=.devshardctl
```

## Start

```bash
cd deploy/join
docker compose -f devshard-update-harness/docker-compose.yml up -d
```

Or start pieces separately:

```bash
docker compose -f docker-compose.devshard-gateway.yml up -d
docker compose -f devshard-update-harness/docker-compose.proxy.yml up -d
```

Default localhost bindings (override in compose if your host needs different ports):

- gateway admin/direct: `127.0.0.1:18080`
- proxy public path: `127.0.0.1:18090`

## Smoke checks

Direct gateway:

```bash
curl -fsS http://127.0.0.1:18080/v1/status
```

Through proxy (production-shaped path):

```bash
curl -fsS http://127.0.0.1:18090/devshard-gateway/v1/status
```

Confirm nginx upstream:

```bash
docker exec proxy sh -lc "grep -nE 'devshard-gateway|devshard-gateway-temp' /etc/nginx/nginx.conf"
```

## Blue/green update test

After merging the update docs/script branch:

1. Run the update workflow from `deploy/join` using
   `devshard/docs/devshard-update/scripts/update.sh` and a local
   `update.config.json` (not committed).
2. Point continuous inference at the proxy URL, not the gateway port directly.
3. When changing protocol version (e.g. v2 -> v3), update both:
   - image build arg `DEVSHARD_PROTOCOL_VERSION`
   - runtime `DEVSHARD_ROUTE_PREFIX` in your local `config.devshard.env`
   - escrow `protocol_version` in your local update config

## Stop

```bash
cd deploy/join
docker compose -f devshard-update-harness/docker-compose.yml down
docker rm -f devshard-gateway-temp 2>/dev/null || true
```
