# Gateway tracing — how request forensics works

How a gateway request is followed across the gateway, `devshardd`, dapi and the ML node through OpenTelemetry traces and correlated logs.

**Deeper docs (testenv):**

| Doc | Role |
|-----|------|
| [observability-plan.md](../testenv/docs/observability-plan.md) | Stack / profile selection (Tempo, Alloy, Jaeger) |
| [scenarios.md](../testenv/docs/scenarios.md) | Citest rows for the observability and ML-node suites |

---

## The one idea

**`trace_id` is the join key.** Everything else follows:

- one trace id per request, minted at the gateway, propagated by `traceparent`;
- every log line carries that `trace_id`;
- Tempo indexes span attributes, Loki indexes the log line body, so high-cardinality identifiers (`nonce`, `escrow_id`, `request_id`) go there — **never** into Prometheus or Loki stream labels.

| Tier | Carries | Cardinality |
|------|---------|-------------|
| Prometheus labels | bounded enums: `disposition`, `model`, `participant`, timeout and failure reasons | bounded only |
| Span attributes (Tempo) | the hosts' span attributes: escrow, nonce, request and slot ids | unbounded, fine |
| Log line fields (Loki) | `trace_id`, `span_id`, `request_id`, `nonce` | unbounded, inside the line |

## Span tree

Every chat request opens one `gateway.request` root span (`observability.StartGatewayRequest`), and the race runs under it. The transport stamps `traceparent` and `X-Request-Id` on every host call made under that span, so each host's spans join the request's trace:

```
trace_id = 9f3c…
gateway.request
└── devshardd.request (host, via traceparent)
    └── devshardd.inference
        ├── devshardd.mlnode.acquire
        ├── devshardd.mlnode.chat.completions
        └── devshardd.mlnode.release
```

The gateway opens no span per attempt: the attempts of one race show as sibling host subtrees under the same request span, and the gateway's log lines (`send_completed`, `receipt_received`, `first_token`, the inter-chunk stall lines, `race_completed`) say which attempt won. Timeout votes and settlement run after the request on their own context and start no trace; their log lines carry the escrow and nonce, which is how a late verdict is joined back to its request.

## Streaming: what is live vs. what is stored

Spans export on `End()`, so a streamed request reaches Tempo only when its stream ends. During the stream only the logs show it: the 60 s heartbeat (`waiting_for_receipt` → `waiting_for_first_token` → `streaming_inflight`) and the 30 s inter-chunk stall lines carry `trace_id`. For a token stream, span duration is a length signal, not latency.

## Queries

```traceql
{ resource.service.name = "gateway" && name = "gateway.request" }
```

```logql
{compose_service=~"gateway|versiond.*"} | json | trace_id = "9f3c…"
{compose_service="gateway"} | json | nonce = "4711"
```

Grafana wiring: Loki derived field `trace_id` → Tempo; Tempo `tracesToLogsV2` → Loki.

## Stack

`tempo-alloy` is the primary e2e profile. `jaeger-promtail` stays supported as the legacy fallback.

**Why Alloy, not Promtail.** Tempo and Jaeger are interchangeable *storage/query* backends for traces. The choice that matters for the agent is Alloy vs Promtail:

| Profile shape | App OTLP target | Logs | Traces |
|---------------|-----------------|------|--------|
| `*-alloy` | **Alloy** `:4317` | Alloy → Loki | Alloy → Tempo *or* Jaeger |
| `*-promtail` | **Tempo or Jaeger** directly | Promtail → Loki | app → Tempo/Jaeger (no agent) |

Promtail only ships logs. Under a Promtail profile, spans go straight from the process to Jaeger or Tempo; if that backend is down or the link flaps, those spans are dropped at the client — Promtail cannot buffer them.

Alloy is the single telemetry agent: metrics scrape, Docker logs → Loki, **and** OTLP trace ingest. Apps never talk to Tempo/Jaeger when an `*-alloy` profile is active. Alloy can queue and retry both logs and traces across backend blips, then deliver them when the backend recovers. That is why Alloy is the better default, and why `tempo-alloy` (apps → Alloy → Tempo + Loki) is the primary stack. Buffering is best-effort (queue/WAL sized by config), not infinite durable storage if Alloy itself dies with a full disk.

## What is built

1. Context-aware logging with `trace_id`/`span_id`; OTel init in the gateway; `traceparent` + `X-Request-Id` on the gateway → host hop.
2. Tempo + Alloy profiles (`tempo-alloy` e2e default; Jaeger/Promtail still green).
3. The dapi node-selection hop and the ML node: `devshardd.mlnode.acquire`/`.release` client spans, shared gRPC interceptors carrying `traceparent` + `x-request-id`, and shared `stage=mlnode_acquire`/`mlnode_release` logs (`common/observability.Stage` + `common/nodemanager` stage names) on both mock-dapi and production `decentralized-api` (Init + `UnaryServerTraceInterceptor`). C8/C9 green via `make citest-observability` (or the focused `citest-dapi-correlation`).
