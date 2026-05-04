# Enabling observability on an existing node

Step-by-step setup for operators upgrading a running join node to the
observability build. Full reference is in `docs/observability.md`.

## 1. Switch to the observability image

`docker-compose.yml` on this branch already points the node service to
`ghcr.io/product-science/inferenced:0.2.12-observability`. Pull and recreate:

```
cd deploy/join
docker compose pull node
docker compose up -d node
```

The new binary carries the `gonka_query_*` Prometheus metrics, the setup
vs handler duration split, peer attribution, and the slow-query log.
Wire is ready; it just needs telemetry turned on in the node's TOML
files (next steps).

## 2. Edit `.inference/config/app.toml`

Telemetry is off by default in `app.toml`. Open the file and apply these
exact changes:

```toml
[telemetry]
enabled = true                       # was false
prometheus-retention-time = 60       # was 0
enable-hostname-label = true         # was false (lets metrics carry the host label)
enable-service-label = true          # was false (lets metrics carry the service label)

[api]
enable = true                        # likely already true via app_overrides.toml
address = "tcp://0.0.0.0:1317"

[grpc]
enable = true                        # default is true; verify
address = "0.0.0.0:9090"
```

Notes:
- `prometheus-retention-time` must be a positive integer (seconds). With it
  at 0 the SDK does not register a Prometheus sink and `/metrics` stays empty.
- `[api] enable=true` is what exposes `/metrics?format=prometheus` on `:1317`.
- The `enable-hostname-label` and `enable-service-label` flags are optional
  but recommended; they let Grafana group across nodes if you scale out.

## 3. Edit `.inference/config/config.toml`

CometBFT instrumentation is off by default. Find the `[instrumentation]`
section and flip:

```toml
[instrumentation]
prometheus = true                    # was false
prometheus_listen_addr = ":26660"    # default; keep
namespace = "cometbft"               # default; keep
```

This enables the existing `cometbft_*` metrics on `:26660` (block
processing time, mempool size, P2P stats). The `gonka-chain`,
`gonka-node-health`, and `gonka-storage` dashboards consume this scrape.

## 4. Optional: tune the slow-query threshold

The slow-query log fires when a query's end-to-end duration exceeds
`GONKA_SLOW_QUERY_THRESHOLD_MS` (default 500ms). To change it:

```bash
# in .env / config.env or shell before bringing the stack up
export GONKA_SLOW_QUERY_THRESHOLD_MS=250
```

Or add a literal value in `docker-compose.yml`. Set to 0 or negative to
disable slow-query logging entirely.

## 5. Restart the node

```
docker compose restart node
```

The init script preserves the `[telemetry]` and `[instrumentation]` blocks
across restarts. `app_overrides.toml` only patches `[api]` and does not
touch the rest.

## 6. Bring up the monitoring stack

```
docker compose -f docker-compose.monitoring.yml up -d
```

This starts Prometheus, Grafana, and cAdvisor on the same `join_default`
network as the node.

- Grafana: `http://<host>:3000` (admin / admin1)
- Prometheus: `http://<host>:9099` (loopback by default)

Four dashboards are auto-provisioned on first boot:
- `Gonka Chain` -- consensus, blocks, mempool
- `Gonka Node Health` -- process / container resources
- `Gonka Storage & Disk I/O` -- IAVL growth
- `Gonka Queries` -- query rate, latency, setup-vs-handler split, slow
  query rate, top peers, top offenders by total response time and call
  count, latency heatmap, error breakdown

## 7. Verify

```
# CometBFT instrumentation reachable
curl -s http://127.0.0.1:26660/metrics | head -3

# SDK telemetry reachable
curl -s 'http://127.0.0.1:1317/metrics?format=prometheus' | head -3

# Prometheus targets all up
curl -s http://127.0.0.1:9099/api/v1/targets?state=active \
  | python3 -c 'import json,sys; [print(t["labels"]["job"], t["health"]) for t in json.load(sys.stdin)["data"]["activeTargets"]]'
```

Expected: four jobs `gonka-node`, `gonka-node-sdk`, `cadvisor`,
`prometheus`, all `up`.

Then send a query and confirm `gonka_query_total` increments:

```
curl -s -o /dev/null http://127.0.0.1:1317/cosmos/staking/v1beta1/params
curl -s 'http://127.0.0.1:1317/metrics?format=prometheus' | grep '^gonka_query_total'
```

## Troubleshooting

- `/metrics` is empty: `prometheus-retention-time` is still 0, or
  `[telemetry] enabled` is false. Re-edit `app.toml`, then restart.
- Prometheus target `gonka-node` shows `down` with `connection refused`:
  CometBFT `[instrumentation] prometheus` is still false in `config.toml`.
- Prometheus target `gonka-node-sdk` shows `down`: the SDK API server
  isn't listening. Check `[api] enable` in `app.toml` and that port 1317
  is exposed inside the `join_default` network.
- New `gonka_query_*` series are missing but `cosmos_*` series are present:
  the running binary is not the observability tag. Confirm:
  ```
  docker inspect node --format '{{.Config.Image}}'
  ```
  must be `ghcr.io/product-science/inferenced:0.2.12-observability`.
- Slow-query log not appearing: set `GONKA_SLOW_QUERY_THRESHOLD_MS` to a
  small value (e.g. `1`) and resend a query; ensure the env var is
  visible inside the container (`docker exec node printenv | grep GONKA`).
