# Observability

End-to-end view of node telemetry: what is collected, where it goes, how to read it, and how to extend it.

## Components

| Component | Where | Purpose |
|-----------|-------|---------|
| Cosmos SDK app telemetry | `:1317/metrics?format=prometheus` | App-level counters, histograms, custom `gonka_query_*` series |
| CometBFT instrumentation | `:26660/metrics` | Consensus, mempool, P2P, block_processing_time |
| cAdvisor | `:8080/metrics` | Per-container CPU, memory, network, fs |
| Prometheus | `:9099` (host) / `:9090` (container) | Scrape + storage, 30d retention |
| Grafana | `:3000` | Dashboards, auto-provisioned |

All three scraped backends run on the same docker network as the node container (`join_default`).

## Stack files

Located in `deploy/join/` of the gonka deploy repo. All managed via `docker-compose.monitoring.yml`.

```
deploy/join/
  docker-compose.monitoring.yml   # prometheus + grafana + cadvisor
  prometheus.yml                  # scrape config
  grafana/
    entrypoint.sh                 # imports dashboards via Grafana API on first boot
    provisioning/datasources/datasource.yml
    dashboards/                   # JSON dashboards
      gonka-chain.json
      gonka-node-health.json
      gonka-storage.json
```

Bring the stack up:
```
docker compose -f docker-compose.monitoring.yml up -d
```

## Node config required

Two flags in the node's config files enable the data sources Prometheus scrapes.

`.inference/config/app.toml` (Cosmos SDK telemetry):
```toml
[telemetry]
enabled = true
prometheus-retention-time = 60
enable-hostname-label = true
enable-service-label = true

[grpc]
enable = true
address = "0.0.0.0:9090"

[api]
enable = true
address = "tcp://0.0.0.0:1317"
```

`.inference/config/config.toml` (CometBFT instrumentation):
```toml
[instrumentation]
prometheus = true
prometheus_listen_addr = ":26660"
```

After editing, restart the node container.

## Prometheus scrape config

`deploy/join/prometheus.yml`:
```yaml
scrape_configs:
  - job_name: gonka-node              # CometBFT instrumentation
    static_configs:
      - targets: ["node:26660"]

  - job_name: gonka-node-sdk          # Cosmos SDK telemetry + gonka_query_*
    metrics_path: /metrics
    params:
      format: ["prometheus"]
    static_configs:
      - targets: ["node:1317"]

  - job_name: cadvisor
    static_configs:
      - targets: ["cadvisor:8080"]
```

Reload with `curl -X POST http://127.0.0.1:9099/-/reload` after edits. Note: when the prometheus.yml inode is replaced (most editors do this), the bind mount can hold a stale view; if reload doesn't pick up the change, `docker restart prometheus`.

## gonka_query_* metrics

Comprehensive query telemetry implemented as a vendored cosmos-sdk fork patch. Sliceable by method, status, transport, and (where applicable) peer subnet.

| Metric | Type | Labels | Source |
|--------|------|--------|--------|
| `gonka_query_total` | counter | method, status, transport | gRPC interceptor + ABCI Query |
| `gonka_query_duration_seconds` | histogram | method, status, transport | end-to-end (setup + handler) |
| `gonka_query_setup_duration_seconds` | histogram | method, transport | time spent in CreateQueryContext (commit-phase blocking shows up here) |
| `gonka_query_handler_duration_seconds` | histogram | method, status, transport | time spent inside the handler (state work) |
| `gonka_query_response_bytes` | histogram | method, status, transport | gRPC: `proto.Size()`; ABCI: `len(resp.Value)` |
| `gonka_query_in_flight` | gauge | method, transport | both paths |
| `gonka_query_gas_used` | histogram | method, status, transport | sdk.Context GasMeter; gRPC + ABCI gRPC-routed |
| `gonka_query_height_lag_blocks` | histogram | method | `app.LastBlockHeight() - requested_height`; 0 = latest |
| `gonka_query_by_peer_total` | counter | peer, transport | bucketed peer subnet (IPv4 /24, IPv6 /48); cardinality cap 256 |
| `gonka_query_slow_total` | counter | method, transport | counts queries that exceeded the slow-query threshold and triggered a structured log line |

### Transport label values

- `grpc`: direct gRPC call to the standalone server on `:9090`.
- `rest`: HTTP request to the REST API server on `:1317`. Detected at the gRPC interceptor by the presence of `grpcgateway-user-agent` in incoming metadata.
- `abci`: ABCI Query (CometBFT RPC `/abci_query` and `inferenced` CLI without `--grpc-addr`).

### Status label values

- `ok`: handler returned no error and ABCI response code 0.
- `<grpc-code>`: gRPC interceptor maps handler errors via `status.FromError(err).Code().String()` (e.g. `NotFound`, `InvalidArgument`, `Unknown`, `ResourceExhausted` for OOG).
- `code_<n>`: ABCI Query path uses the SDK error code from `resp.Code` when non-zero.

### Bucket choices

- duration_seconds: `[0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]`
- setup_duration_seconds: `[0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1]` (sub-ms granularity for cheap latest queries)
- handler_duration_seconds: same as duration_seconds
- response_bytes: `64 * 4^n` for n=0..8 (64 B - 4 MB)
- gas_used: `[1k, 10k, 50k, 100k, 500k, 1M, 5M, 10M, 50M]`
- height_lag_blocks: `[0, 1, 10, 100, 1k, 10k, 100k, 1M]`

### Cardinality estimate

Roughly 90-120 distinct method strings, 3 transports, 6 typical statuses, 14 buckets.
Worst-case `gonka_query_duration_seconds` ~ 30k samples; total across all `gonka_query_*` series ~ 90-120k. Well within single-node Prometheus.

### Patch locations in vendored fork

`/home/ubuntu/workspace/cosmos-sdk/baseapp/`:

- `query_metrics.go` (new): metric vector definitions, peer sanitization, slow-query threshold, slow-query log helper, status classifiers, unified `gonkaObserve` function.
- `grpcserver.go`: extended interceptor records all metrics, splits setup vs handler, attaches peer label, emits slow-query log. Transport detected via `grpcgateway-user-agent` metadata.
- `abci.go`: `Query()` records all metrics with `transport="abci"`; `handleQueryGRPC()` populates setup/handler durations and gas onto the shared record so the top-level defer reads them.

All edits tagged `// Gonka:` to remain visible across SDK rebases.

The legacy go-metrics counters (`query_count`, `query_<method>`) and summary (`_<method>{quantile=...}`) are kept untouched for backward compatibility with existing dashboards.

## Slow-query log

Every query whose end-to-end duration exceeds the threshold emits a structured log line via the standard `app.logger` and increments `gonka_query_slow_total`. The threshold is read once at process start from environment variable `GONKA_SLOW_QUERY_THRESHOLD_MS` (default 500). Set to 0 or negative to disable.

Log fields:

```
WRN slow query
  method=/cosmos.staking.v1beta1.Query/Validators
  transport=rest|grpc|abci
  status=ok|<grpc-code>|code_<n>
  duration_ms=<total>
  setup_ms=<context creation>
  handler_ms=<handler call>
  gas_used=<sdkCtx GasMeter>
  response_bytes=<proto.Size or len(value)>
  requested_height=<from request>
  current_height=<chain head>
  peer=<sanitized subnet>
  request="<truncated proto string, max 512 bytes>"
```

Use these lines to:

- pinpoint the exact query, its arguments, and its source when investigating "this took 30s at 14:32:15"
- correlate `setup_ms` vs `handler_ms` to determine cause-vs-victim (high `setup_ms` with flat `handler_ms` indicates commit-phase blocking, not the query itself)
- attribute volume bursts to a specific subnet via the `peer` field

Aggregate slow-query lines via Loki, journald, or any log shipper. The structured key/value format works directly with `slog`-aware parsers.

## Setup vs handler duration

Total `gonka_query_duration_seconds` is the sum of two distinct phases:

- `gonka_query_setup_duration_seconds`: `CreateQueryContext`. Acquires a snapshot of the multistore and resolves the query height. This call blocks while the chain is in commit phase. A spike here without a corresponding spike in handler duration means the query was *waiting on the chain*, not doing work.
- `gonka_query_handler_duration_seconds`: the module's `Query` handler. State reads, computation, marshalling.

Cause-vs-victim test:
- High setup p99, flat handler p99 -> commit-phase contention; investigate consensus/IO, not the queries.
- Flat setup p99, high handler p99 -> the handler itself is slow; investigate the method.
- Both rising -> overload; investigate concurrency (`gonka_query_in_flight`) and chain throughput.

## Useful PromQL

Top 10 query methods by RPS:
```
topk(10, sum by (method) (rate(gonka_query_total[5m])))
```

p99 query latency by method:
```
histogram_quantile(0.99, sum by (method, le) (rate(gonka_query_duration_seconds_bucket[5m])))
```

REST vs gRPC p99 same method:
```
histogram_quantile(0.99, sum by (transport, le) (rate(gonka_query_duration_seconds_bucket{method="/inference.inference.Query/Params"}[5m])))
```

Concurrent queries by transport:
```
sum by (transport) (gonka_query_in_flight)
```

Error rate by method:
```
sum by (method) (rate(gonka_query_total{status!="ok"}[5m]))
```

p99 response size by method:
```
histogram_quantile(0.99, sum by (method, le) (rate(gonka_query_response_bytes_bucket[5m])))
```

Fraction of historical-height queries:
```
1 - (
  sum(rate(gonka_query_height_lag_blocks_bucket{le="0"}[5m]))
  /
  sum(rate(gonka_query_height_lag_blocks_count[5m]))
)
```

p99 gas used:
```
histogram_quantile(0.99, sum by (method, le) (rate(gonka_query_gas_used_bucket[5m])))
```

## Dashboards

Auto-provisioned on first Grafana boot from `deploy/join/grafana/dashboards/*.json`:

- `gonka-chain`: consensus, blocks, missed proposals, mempool, P2P.
- `gonka-node-health`: process and container resource usage, ABCI timing per module.
- `gonka-storage`: IAVL/database size and growth.
- `gonka-queries`: query performance (this doc's focus). 18 panels organized as:
  - Top stats: qps, p99, in-flight, slow/min, error rate, historical-height fraction.
  - Query rate: by transport, top 10 methods.
  - Latency: p99 by method (top 10).
  - Cause-vs-victim: setup vs handler p99 overlay; setup p99 vs CometBFT block_processing_time.
  - Resource per query: p99 response size and p99 gas used by method.
  - Concurrency: in-flight by transport; slow query rate by method.
  - Source: top peers by RPS; p99 height-lag by method.
  - Correlation: query p99 overlaid with mempool size.

Edits made in the Grafana UI persist to the volume but not to the JSON files; export from the UI and overwrite the JSON to commit changes.

## Deploying the patched binary

The patches live in the local cosmos-sdk fork at `/home/ubuntu/workspace/cosmos-sdk` (branch checked out at tag `v0.53.3-ps17`). The inference-chain `go.mod` replaces the upstream module with this local path. The decentralized-api `go.mod` does the same. Both Dockerfiles and Makefiles use BuildKit named contexts (`--build-context cosmos-sdk-src=../../cosmos-sdk`) to inject the source into the Docker build.

Build the upgrade artifact:
```
make -C inference-chain build-for-upgrade
```

Output: `public-html/v2/inferenced/inferenced` (musl-linked, alpine-compatible).

Install on the test node:
```
docker stop node
sudo cp public-html/v2/inferenced/inferenced \
  ~/deploy/gonka/deploy/join/.inference/cosmovisor/genesis/bin/inferenced
docker start node
```

A backup of the previous binary is kept at `inferenced.bak.pre-grpc-telemetry` in the same directory.

## Performance analysis runbook

Symptom: clients report slow queries.

1. Open the `gonka-queries` dashboard. Confirm the spike on the p99-by-method panel and identify the affected method.
2. Look at the cause-vs-victim row:
   - If setup p99 spiked while handler p99 stayed flat -> commits are blocking queries. Investigate `cometbft_state_block_processing_time` and IO. The queries are victims, not the cause.
   - If handler p99 spiked while setup p99 stayed flat -> the method itself is slow. Continue to step 3.
   - If both spiked -> overload. Check `gonka_query_in_flight` and request rate.
3. Cross-reference `gonka_query_in_flight`. Surged concurrency means queue-driven slowness. Scale read capacity or rate-limit upstream.
4. Check `gonka_query_height_lag_blocks` for the affected method. A spike of historical-height queries warms cold IAVL and slows everything. Identify the client subnet on the "Top peers by RPS" panel.
5. Cross-reference `gonka_query_response_bytes` p99. Big responses correlate with serialization cost; consider pagination.
6. Cross-reference `gonka_query_gas_used` p99. High gas with high duration means the handler is doing more state work than usual.
7. Pull the slow-query log lines for the affected method and time window:
   ```
   docker logs --since 10m node 2>&1 | grep 'slow query' | grep '/cosmos.staking.v1beta1.Query/Validators'
   ```
   Each line carries the request payload, peer subnet, gas, response size, and setup/handler split for the *exact* slow request — not just an aggregate.
8. Check cAdvisor for the node container. Disk I/O wait on the IAVL volume usually accompanies cold-state queries.

## Known limitations

- ABCI queries via CometBFT GET-style RPC (`/abci_query?height=N`) do not always parse the height correctly on the client side; lag attribution can fall back to 0 (latest). Use JSON-RPC POST or gRPC for accurate historical-query lag tracking.
- Streaming gRPC query RPCs are not instrumented. Cosmos SDK does not currently expose any, so this is theoretical.
- gas_used is unobserved for non-gRPC ABCI paths (`/store/*`, `/p2p/*`) since sdk.Context is not constructed there.
