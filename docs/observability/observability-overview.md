# Observability Overview

This document describes the observability changes introduced by commit `f4bc3fd7efa719803890f94c21ad4beb1033af9d`. It covers the local `deploy/join` observability stack, OpenTelemetry tracing, Prometheus metrics, Loki logs, Grafana dashboards, devshard-specific instrumentation, and troubleshooting.

## Goals

The observability layer is designed to answer four operational questions:

1. Is the node healthy and reachable?
2. Are inference requests, devshard requests, validation jobs, chain queries, and transaction broadcasts succeeding?
3. Where is latency or failure happening across API, devshardd, ML node calls, and chain interaction?
4. Can an operator move from a dashboard panel to logs and traces for the same request or inference?

The implementation keeps consensus-critical logic separate from observability concerns. Inference-chain state transitions are not changed by tracing or metric collection.

## Stack Components

The join deployment now has an optional observability overlay in `deploy/join/docker-compose.observability.yml`.

| Component | Purpose | Default internal endpoint | External/local binding |
| --- | --- | --- | --- |
| Jaeger | OTLP trace receiver and trace UI | `jaeger:4317`, `jaeger:16686/jaeger` | proxied at `/jaeger/` when enabled (nginx basic auth) |
| Prometheus | Metrics scraping and storage | `prometheus:9090` | `127.0.0.1:9099` |
| Loki | Log storage | `loki:3100` | `127.0.0.1:3101` |
| Promtail | Docker log discovery and shipping to Loki | n/a | n/a |
| Grafana | Dashboards and data exploration | `grafana:3000` | `127.0.0.1:3000`, proxied at `/grafana/` when enabled (Grafana login) |
| cAdvisor | Container CPU, memory, network, and filesystem metrics | `cadvisor:8080` | `127.0.0.1:8088` |

Grafana datasources are provisioned automatically:

- `Prometheus` with UID `prometheus`
- `Loki` with UID `loki`
- `Jaeger` with UID `jaeger`

Loki has a derived field that extracts `trace_id` from JSON/logfmt log bodies and links directly to Jaeger.

## Enabling The Stack

The template `deploy/join/config.env.template` ships with **Jaeger and Grafana UIs disabled by default** (`JAEGER_ENABLED=false`, `GRAFANA_ENABLED=false`). Metrics, logs, and trace export to Jaeger OTLP still work when the observability overlay is running; only the **public proxy UI routes** stay off until you opt in.

### Security prerequisites (set before enabling UIs)

Jaeger has **no built-in login**. When you expose `/jaeger/` through the nginx proxy, protect it with **nginx HTTP basic auth**:

| Variable | Required when | Purpose |
| --- | --- | --- |
| `JAEGER_BASIC_AUTH_USER` | `JAEGER_ENABLED=true` | Basic auth username for `/jaeger/` |
| `JAEGER_BASIC_AUTH_PASSWORD` | `JAEGER_ENABLED=true` | Basic auth password for `/jaeger/` |

Grafana has its own admin login. Set a **strong admin password before** enabling public UI proxying:

| Variable | Required when | Purpose |
| --- | --- | --- |
| `GRAFANA_ADMIN_USER` | recommended | Grafana admin username (default `admin`) |
| `GRAFANA_ADMIN_PASSWORD` | `GRAFANA_ENABLED=true` | Grafana admin password; must not be left at defaults |

The proxy **refuses to start** if `JAEGER_ENABLED=true` without Jaeger basic auth credentials, or if `GRAFANA_ENABLED=true` with a missing/placeholder password (`admin1`, `<FILLIN>`, etc.).

### Example configuration

Copy `config.env.template` to `config.env`, set secrets, then enable UIs:

```bash
# 1. Set credentials first
export JAEGER_BASIC_AUTH_USER=jaeger
export JAEGER_BASIC_AUTH_PASSWORD='your-jaeger-basic-auth-secret'
export GRAFANA_ADMIN_USER=admin
export GRAFANA_ADMIN_PASSWORD='your-grafana-admin-secret'

# 2. Enable trace/log export (can stay on even when UIs are disabled)
export DAPI_OTEL_ENABLED=true
export DEVSHARD_OTEL_ENABLED=true
export OTEL_ENDPOINT=http://jaeger:4317

# 3. Enable public UI routes only when credentials above are set
export JAEGER_ENABLED=true
export GRAFANA_ENABLED=true
```

Use the overlay when starting the join stack:

```bash
cd deploy/join
source ./config.env
docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d
```

The proxy exposes UI paths only when the matching flags are enabled:

- `http://<host>:<API_PORT>/grafana/` — Grafana login (`GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD`)
- `http://<host>:<API_PORT>/jaeger/` — nginx basic auth (`JAEGER_BASIC_AUTH_USER` / `JAEGER_BASIC_AUTH_PASSWORD`), then Jaeger UI

OTLP trace ingest (`jaeger:4317`) and Loki log push (`loki:3100`) are **not** proxied on the public API port; they remain on the Docker internal network.

For local-only access, Grafana is also bound on `127.0.0.1:3000`, Prometheus on `127.0.0.1:9099`, Loki on `127.0.0.1:3101`, and cAdvisor on `127.0.0.1:8088`.

## Tracing

### Export Path

Both `decentralized-api` and `devshardd` export traces through OTLP/gRPC to `OTEL_ENDPOINT`. In the join stack this points directly to Jaeger v2:

```text
decentralized-api / devshardd -> OTLP gRPC -> jaeger:4317 -> Jaeger storage/UI
```

Jaeger is configured for traces only. Metrics are intentionally exposed through Prometheus scrape endpoints, not through OTLP metrics export.

### Environment Variables

`decentralized-api` reads:

| Variable | Meaning |
| --- | --- |
| `DAPI_OTEL_ENABLED` | Enables trace export for the API process. Accepts Go boolean values such as `true`/`false`. |
| `OTEL_ENDPOINT` | OTLP endpoint URL, for example `http://jaeger:4317`. |
| `OTEL_HEADERS` | Optional comma-separated OTLP headers, for example `key=value,tenant=prod`. |

`devshardd` reads:

| Variable | Meaning |
| --- | --- |
| `DEVSHARD_OTEL_ENABLED` | Enables trace export for standalone devshardd instances. |
| `OTEL_ENDPOINT` | Same endpoint as above. |
| `OTEL_HEADERS` | Same header format as above. |

When tracing is disabled or the endpoint is empty, both processes keep running normally and install W3C TraceContext propagation so existing incoming trace IDs can still be forwarded.

### Resource Identity

API spans use `service.name=decentralized-api` and include the local participant address when available.

devshardd spans use `service.name=devshardd` and `service.version` from the runtime version managed by versiond.

### Propagation

The system uses W3C TraceContext (`traceparent`) for distributed traces.

Trace context is extracted from inbound HTTP requests and injected into outbound calls, including:

- API transfer-agent calls to executor API endpoints
- API calls to ML nodes
- API validation payload retrieval calls
- devshard API and devshardd HTTP calls
- Cosmos gRPC query calls through metadata injection

`X-Request-Id` is separate from tracing. It is retained for log correlation and request identity, but it does not create parent/child span relationships. For connected Jaeger traces, verify that `traceparent` is present on outbound requests.

### API Spans

`decentralized-api/observability` exposes two tracers through the process-wide facade:

- `observability.Inference`
- `observability.Chain`

Important inference span names:

| Span | Meaning |
| --- | --- |
| `inference.request` | Root public API request span for `/v1/chat/completions`. |
| `inference.transfer` | Transfer-agent path work. |
| `inference.transfer.forward_executor` | Client-side HTTP call from transfer agent to executor. |
| `inference.executor.execute` | Executor-side processing. |
| `mlnode.chat.completions` | ML node call during normal execution. |
| `inference.finish.submit` | Publishing finish/result transaction. |
| `inference.validation.event` | Handling inference-finished events from the chain. |
| `inference.status_update.event` | Handling inference status update events. |
| `inference.validation.sample` | Sampling inferences for validation. |
| `inference.validation.execute` | Re-executing one inference for validation. |
| `inference.payload.retrieve` | Umbrella span for retrieving executor payloads. |
| `inference.payload.retrieve.attempt` | One retry attempt inside payload retrieval. |
| `inference.payload.fetch` | Client-side HTTP fetch of payloads from executor. |
| `mlnode.chat.completions.validation` | ML node call during validation. |
| `inference.validation.compare_logits` | Logit comparison for validation. |

Important chain span names:

| Span | Meaning |
| --- | --- |
| `chain.tx.broadcast` | Cosmos transaction broadcast. |
| `chain.store.query` | ABCI store query. |
| `chain.grpc.query` | Cosmos gRPC query. |

`Operation.Finish(err)` records span status and duration/error metrics. Use `defer op.FinishErr(&err)` with named-return errors so the final error value is captured correctly.

### Devshard Spans

`devshard/observability` exposes `observability.Request` with server, host, ML node, validation, and generic handler spans.

Important devshard span names:

| Span | Meaning |
| --- | --- |
| `devshardd.request` | Server-side inbound HTTP span. |
| `devshardd.inference` | `HandleInference` processing. |
| `devshardd.mlnode.chat.completions` | ML node call from devshard execution or validation path. |
| `devshardd.validation` | Validation re-execution. |
| `devshardd.handler.<name>` | Generic transport handler span, for example diffs, mempool, signatures, gossip. |

`EchoMiddleware` extracts W3C trace context and creates server spans. It intentionally skips `/metrics` and `/healthz`.

Standalone `devshardd` does not install `EchoMiddleware` at the root router. Lazy session routes install it through `RegisterLazySessionRoutes`; root `/healthz` and `/metrics` stay untraced.

## Metrics

Metrics are exposed through Prometheus scrape endpoints.

### Scrape Targets

`deploy/join/observability/prometheus.yml` defines these jobs:

| Job | Target | Purpose |
| --- | --- | --- |
| `prometheus` | `prometheus:9090` | Prometheus self metrics. |
| `decentralized-api` | `api:9100/metrics` | API metrics and in-process devshard metrics. |
| `devshardd` | HTTP SD from `api:9100/sd/devshardd` | Standalone devshardd versions behind versiond. |
| `gonka-node` | `node:26660` | CometBFT metrics. |
| `gonka-node-sdk` | `node:1317/metrics?format=prometheus` | Cosmos SDK metrics when exposed by the node. |
| `cadvisor` | `cadvisor:8080` | Container metrics. |

The API's ML server exposes:

- `/metrics` for Prometheus
- `/sd/devshardd` for Prometheus HTTP service discovery of approved devshard versions

`/sd/devshardd` returns targets pointing at `versiond:8080` with a per-version `__metrics_path__` such as `/v1/metrics`.

### API Metrics

API metrics have the `decentralized_api_` Prometheus prefix and the OTel metric namespace `decentralized_api.*`.

Prometheus metric families:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `decentralized_api_inference_active_operations` | gauge | `operation`, `model` | Number of active instrumented operations. |
| `decentralized_api_inference_operation_duration_seconds` | histogram | `operation`, `model` | Operation duration. |
| `decentralized_api_inference_operation_errors_total` | counter | `operation`, `model` | Operations finished with an error. |
| `decentralized_api_inference_prompt_tokens` | histogram | `operation`, `model` | Prompt token counts. |
| `decentralized_api_inference_completion_tokens` | histogram | `operation`, `model` | Completion token counts. |
| `decentralized_api_inference_total_tokens` | histogram | `operation`, `model` | Prompt + completion token counts. |

Only `operation` and `model` are exposed as Prometheus labels to keep series cardinality bounded. High-cardinality values such as inference ID, request URL, executor address, validator address, and trace ID are span attributes or log fields instead.

### Devshard Metrics

Devshard metrics are kept in a private registry and exposed in two places:

- Merged into API `/metrics` for in-process devshard code paths.
- Exposed by standalone `devshardd` `/metrics` for versiond-managed child processes.

Runtime Go/process collectors are registered only by standalone devshardd. This avoids duplicate `go_*` and `process_*` metric families when the devshard registry is merged into API's default registry.

Important devshard metric families:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `devshard_inflight` | gauge | `stage` | In-flight devshard operations by lifecycle stage. |
| `devshard_request_terminal_total` | counter | `terminal`, `reason` | Terminal outcome for inference requests. |
| `devshard_interruption_total` | counter | `class`, `reason` | Operator-actionable interruptions. |
| `devshard_session_resolution_total` | counter | `route`, `status`, `reason` | Lazy session resolution results. |
| `devshard_receipt_orphan_total` | counter | `reason` | Requests that produced a receipt but did not publish finish. |
| `devshard_validation_total` | counter | `stage`, `status` | Validation lifecycle events. |
| `devshard_validation_orphan_total` | counter | `reason` | Validation jobs that did not publish expected validation txs. |
| `devshard_validation_queue_drops_total` | counter | none | Validation jobs dropped because the queue was full. |
| `devshard_payload_request_total` | counter | `status`, `reason` | Executor payload-serving request outcomes. |
| `devshard_mlnode_attempts_total` | counter | `path`, `outcome`, `node_id` | ML node attempt outcomes. |
| `devshard_mlnode_call_seconds` | histogram | `path`, `node_id`, `phase` | ML node latency. |
| `devshard_mlnode_tokens` | histogram | `path`, `node_id`, `kind` | Prompt/completion token distribution. |
| `devshard_http_connections` | gauge | `server`, `state` | Current HTTP connection states. |
| `devshard_http_connections_total` | counter | `server`, `state` | HTTP connection state transitions. |
| `devshard_validation_queue_depth` | gauge | `escrow_id` | Validation queue depth per session. |
| `devshard_mempool_size` | gauge | `escrow_id` | Mempool size per session. |
| `devshard_build_info` | gauge | `binary`, `version`, `commit` | Build/runtime identity. |

Terminal values include:

- `finish_published`
- `no_receipt_expected`
- `no_receipt_interrupted`
- `receipt_no_execution_expected`
- `receipt_no_execution_interrupted`
- `execution_no_finish`
- `client_cancelled_after_receipt`

Common reason values include `ok`, `timeout`, `transport_err`, `http_4xx`, `http_5xx`, `not_executor`, `queue_full`, `payload_not_found`, `validation_parse_err`, and other typed constants in `devshard/observability/ctx.go`.

### Node And Container Metrics

The chain node has CometBFT Prometheus instrumentation enabled through:

```text
CONFIG_instrumentation__prometheus=true
```

This exposes CometBFT metrics on `node:26660` for the `gonka-node` job.

The `gonka-node-sdk` job scrapes `node:1317/metrics?format=prometheus`. If this target is down or empty, verify that the node build and runtime configuration expose Cosmos SDK telemetry at the REST API endpoint.

cAdvisor provides container-level CPU, memory, filesystem, and network metrics used by node health dashboards.

## Logs

Promtail discovers Docker containers through the Docker socket and ships logs to Loki.

Promtail labels each stream with:

| Label | Source |
| --- | --- |
| `container` | Docker container name |
| `compose_service` | Docker Compose service name |
| `compose_project` | Docker Compose project name |
| `stream` | stdout/stderr |
| `environment` | constant `join` |
| `service` | parsed from JSON/logfmt log body when present |
| `level` | parsed from JSON/logfmt log body when present |

High-cardinality values such as `trace_id`, `inference_id`, `model`, and `escrow_id` remain in the log body. Use LogQL parsing stages in Grafana panels or Explore, for example:

```logql
{compose_project="join", compose_service="api"} |~ "subsystem=devshardd" | logfmt | inference_id=~"$inference_id"
```

For API logs:

```logql
{compose_project="join", compose_service="api"} | json
```

For devshardd logs running under versiond:

```logql
{compose_project="join", compose_service="versiond"} |~ "service=devshardd" | logfmt
```

`devshard/logging.NewSlogAdapter` lets embedders route devshard package logs into the host process' default `slog` handler with fixed fields such as `subsystem=devshardd`.

## Dashboards

Grafana dashboards are provisioned from `deploy/join/observability/grafana/dashboards`.

| Dashboard | Purpose |
| --- | --- |
| `gonka-devshard-observability.json` | Fleet-level devshard lifecycle, interruptions, terminals, ML node latency, validation, payload, and connection health. |
| `devshard-details.json` | Focused devshard details, including terminal outcomes, execution latency, tokens, logs, build info, and related API operations. |
| `gonka-chain.json` | Chain-level metrics and chain service health. |
| `gonka-node-health.json` | Node and container health. |
| `gonka-queries.json` | Query-related API/chain visibility. |
| `gonka-storage.json` | Storage and container filesystem visibility. |

 Setup report data remains available through the admin API endpoint, but it is not exported as a Prometheus collector for Grafana.

## Operational Checks

### Verify Containers

```bash
cd deploy/join
docker compose -f docker-compose.yml -f docker-compose.observability.yml ps
```

Expected observability services:

- `jaeger`
- `prometheus`
- `loki`
- `promtail`
- `cadvisor`
- `grafana`

### Verify API Metrics

From the host:

```bash
curl -s http://127.0.0.1:9100/metrics | grep -E 'decentralized_api_|devshard_'
```

From inside the compose network:

```bash
docker compose exec prometheus wget -qO- http://api:9100/metrics
```

### Verify devshardd Service Discovery

```bash
curl -s http://127.0.0.1:9100/sd/devshardd
```

Expected response shape:

```json
[
  {
    "targets": ["versiond:8080"],
    "labels": {
      "__metrics_path__": "/v1/metrics",
      "version": "v1",
      "service": "devshardd"
    }
  }
]
```

### Verify Prometheus Targets

Open Prometheus and check Status -> Targets:

```text
http://127.0.0.1:9099/targets
```

Or through the proxy if exposed by your deployment.

### Verify Traces

Generate an inference request or wait for validation activity, then open:

```text
http://<host>:<API_PORT>/jaeger/
```

Search by service:

- `decentralized-api`
- `devshardd`

Useful span attributes to search/filter by:

- `inference.id`
- `model`
- `requester.address`
- `executor.address`
- `mlnode.node.id`
- `tx.hash`
- `rpc.service`
- `rpc.method`

### Verify Logs

Open Grafana Explore with Loki and run:

```logql
{compose_project="join"}
```

Then narrow by service:

```logql
{compose_project="join", compose_service="api"} | json
{compose_project="join", compose_service="versiond"} | logfmt
```

If a log line has `trace_id`, Grafana should show a `View trace` derived field link to Jaeger.

## Troubleshooting

### Grafana Is Not Available At `/grafana/`

Check:

1. `GRAFANA_ADMIN_PASSWORD` is set to a strong, non-placeholder value in `deploy/join/config.env`.
2. `GRAFANA_ENABLED=true` is present in `deploy/join/config.env`.
3. The stack was started with both compose files.
4. `grafana` is healthy: `docker compose ps grafana`.
5. `proxy` was recreated after changing the environment (proxy validates the password at startup).

Common fix:

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d --force-recreate proxy grafana
```

### Jaeger Is Not Available At `/jaeger/`

Check:

1. `JAEGER_BASIC_AUTH_USER` and `JAEGER_BASIC_AUTH_PASSWORD` are set in `deploy/join/config.env`.
2. `JAEGER_ENABLED=true` is present.
3. `jaeger` is running and listening on `16686` internally.
4. Proxy logs include the Jaeger location block and "basic auth enabled" during startup.
5. Your browser prompt uses the Jaeger basic auth credentials (Jaeger itself has no login screen).

Common fix:

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml logs proxy jaeger --tail=100
```

### No Traces In Jaeger

Check the exporter switches:

```bash
docker compose exec api printenv | grep -E 'DAPI_OTEL_ENABLED|OTEL_ENDPOINT|OTEL_HEADERS'
docker compose exec versiond printenv | grep -E 'DEVSHARD_OTEL_ENABLED|OTEL_ENDPOINT|OTEL_HEADERS'
```

Expected:

```text
DAPI_OTEL_ENABLED=true
DEVSHARD_OTEL_ENABLED=true
OTEL_ENDPOINT=http://jaeger:4317
```

Then check process logs for initialization messages:

```bash
docker compose logs api versiond --tail=200 | grep -i opentelemetry
```

If you see endpoint errors, verify that `jaeger` is reachable from the compose network:

```bash
docker compose exec api sh -c 'nc -vz jaeger 4317'
```

### Traces Exist But Spans Are Not Connected

Disconnected traces usually mean the W3C `traceparent` header was not propagated across an HTTP or gRPC boundary.

Check likely boundaries:

- Transfer-agent to executor API call
- API/devshard calls to ML nodes
- Validation payload retrieval from executor
- devshardd session routes behind versiond
- Cosmos gRPC query metadata injection

Remember that `X-Request-Id` is not trace propagation. It is useful for logs, but Jaeger parent/child relationships require `traceparent`.

### Prometheus Target Is Down

Open:

```text
http://127.0.0.1:9099/targets
```

Then check by job:

- `decentralized-api`: verify `api:9100/metrics` is reachable.
- `devshardd`: verify `api:9100/sd/devshardd` returns targets and versiond serves `/<version>/metrics`.
- `gonka-node`: verify CometBFT instrumentation is enabled and `node:26660` is reachable.
- `gonka-node-sdk`: verify the node exposes SDK telemetry at `node:1317/metrics?format=prometheus`.
- `cadvisor`: verify Docker host mounts are available to cAdvisor.

### `/metrics` Shows Duplicate `go_*` Or `process_*` Collection Errors

This happens when a private registry containing Go/process collectors is merged with another registry that already has them.

The intended setup is:

- API `/metrics` merges API default registry with devshard's private registry.
- devshard private registry does not register Go/process collectors by default.
- Standalone devshardd explicitly calls `RegisterRuntimeCollectors()` because its `/metrics` endpoint owns its registry.

If this error appears, look for accidental calls to `RegisterRuntimeCollectors()` in code paths where the registry is later passed to `MergedMetricsHandler`.

### Devshard Runtime Logs Panel Shows No Data

devshardd runs inside the `versiond` container, not the `api` container.

Use this LogQL base query:

```logql
{compose_project="join", compose_service="api|versiond"} | logfmt
```

If filtering by inference ID:

```logql
{compose_project="join", compose_service="api|versiond"} | logfmt | inference_id=~"$inference_id"
```

Also verify that Promtail can read Docker logs:

```bash
docker compose logs promtail --tail=100
```

### Loki Has No Logs

Check:

1. `promtail` is running.
2. `promtail` has Docker socket and container log mounts.
3. `loki` is reachable from promtail.
4. The compose project label matches the query. The default environment label is `join`.

Useful commands:

```bash
docker compose logs promtail loki --tail=200
curl -s http://127.0.0.1:3101/ready
```

### Token Throughput Or Inference Rate Is Zero

For devshard panels, token and execution-rate metrics are emitted when the local process actually executes or validates ML node work.

Common causes of zero values:

1. The local host is not selected as executor/validator for the current activity.
2. There has been no recent inference traffic in the dashboard time range.
3. Prometheus is scraping the wrong target or the `devshardd` HTTP SD list is empty.
4. The running image/binary does not include the instrumentation changes.

Check:

```promql
sum(rate(devshard_mlnode_attempts_total[5m])) by (path, outcome)
sum(rate(devshard_mlnode_tokens_sum[5m])) by (path, kind)
sum(rate(decentralized_api_inference_operation_duration_seconds_count[5m])) by (operation)
```

### gRPC Query Spans Show `Unknown` Status Too Often

`chain.grpc.query` spans derive `rpc.grpc.status_code` from the original gRPC error before wrapping it for trace context. If many non-OK statuses still appear as `Unknown`, inspect the returned error type at the query boundary and confirm it is a gRPC status error.

### Devshardd `/metrics` Works But API `/metrics` Does Not Include Devshard Metrics

API `/metrics` exposes the merged default registry plus `devshardobservability.Registry()`.

Check:

```bash
curl -s http://127.0.0.1:9100/metrics | grep '^devshard_'
```

If empty:

1. Verify API was rebuilt with the observability changes.
2. Verify code still uses `observability.MergedMetricsHandler(devshardobservability.Registry())`.
3. Verify devshard metrics were initialized by a devshard code path or `devshardobservability.Init`.

### Dashboard Lint Fails

There are tests that check Grafana dashboards reference declared metric names.

Run:

```bash
cd decentralized-api
go test ./observability/ -run TestDashboardsLint

cd ../devshard
go test ./observability/ -run TestDashboardsLint
```

If a dashboard references a new metric, declare/register the metric in Go first. If a metric was removed, update or remove the dashboard panel.

## Development Guidelines

### Adding A New API Operation Span

1. Add a typed span name in `decentralized-api/observability/names.go`.
2. Add a method on `InferenceTracer` or `ChainTracer`.
3. Start the span at the call site and pass the returned context downstream.
4. Use `defer op.FinishErr(&err)` for named-return functions, or `defer op.Finish(err)` where appropriate.
5. Keep Prometheus labels low-cardinality; put IDs and URLs on spans/logs instead.

### Adding A New Devshard Lifecycle Metric

1. Declare the instrument in `devshard/observability/metrics_lifecycle.go`.
2. Register it in `initRegistry()`.
3. Add a typed helper such as `Inc...`, `Observe...`, or `Set...`.
4. Use typed constants from `ctx.go` for labels such as `Reason`, `Stage`, `Terminal`, `Path`, and `MetricStatus`.
5. Add or update dashboard lint tests if the metric appears in Grafana JSON.

### Adding Logs That Link To Traces

Prefer structured logs with `trace_id` when an active span exists. Keep high-cardinality values in the log body, not as Loki labels. Loki labels should remain limited to low-cardinality dimensions such as service, level, compose service, and environment.

### Avoiding Cardinality Problems

Do not add Prometheus labels for:

- request IDs
- trace IDs
- inference IDs
- escrow IDs unless the metric is explicitly per-session and bounded
- URLs with dynamic path/query data
- wallet addresses unless the metric is intentionally per participant and bounded

Use span attributes and log fields for these values instead.

## Quick Reference

Useful URLs:

| Tool | URL |
| --- | --- |
| Grafana through proxy | `http://<host>:<API_PORT>/grafana/` (set `GRAFANA_ADMIN_PASSWORD` before enabling) |
| Jaeger through proxy | `http://<host>:<API_PORT>/jaeger/` (set `JAEGER_BASIC_AUTH_*` before enabling) |
| Grafana local bind | `http://127.0.0.1:3000` |
| Prometheus local bind | `http://127.0.0.1:9099` |
| Prometheus targets | `http://127.0.0.1:9099/targets` |
| Loki readiness | `http://127.0.0.1:3101/ready` |
| API metrics | `http://127.0.0.1:9100/metrics` |
| devshardd service discovery | `http://127.0.0.1:9100/sd/devshardd` |

Useful commands:

```bash
cd deploy/join

# Start full stack with observability.
docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d

# See observability service logs.
docker compose -f docker-compose.yml -f docker-compose.observability.yml logs -f prometheus grafana jaeger loki promtail

# Check API metrics.
curl -s http://127.0.0.1:9100/metrics | grep -E 'decentralized_api_|devshard_'

# Check devshard service discovery.
curl -s http://127.0.0.1:9100/sd/devshardd
```