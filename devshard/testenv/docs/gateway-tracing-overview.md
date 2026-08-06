# Gateway observability with tracing — short version

One-page summary of the target design. Full reasoning, audit and phasing:
[observability-trace-correlation-plan.md](./observability-trace-correlation-plan.md).
Test scenarios: [observability-test-plan.md](./observability-test-plan.md).
Stack/profile selection: [observability-plan.md](./observability-plan.md).

---

## The goal

Given a `request_id`, an `inferenceId` (== nonce), or an accounting bucket such as `ghost` or
`unfinished_refused`, one lookup returns the whole request path — every span across gateway →
devshardd → dapi → mlnode, **and** every log line those components wrote, including the prompt that
caused the failure.

## The one idea

**`trace_id` is the join key.** Everything else follows:

- one trace id per request, minted at the gateway, propagated by `traceparent`;
- every log line carries that `trace_id` (this is the piece that does not exist today);
- Tempo indexes span attributes, Loki indexes the log line body, so high-cardinality identifiers
  (`nonce`, `escrow_id`, `request_id`) go there — **never** into Prometheus or Loki stream labels.

| Tier | Carries | Cardinality |
|------|---------|-------------|
| Prometheus labels | bounded enums: `disposition`, `quarantine_mode`, `no_send_reason`, `failure_origin`, `timeout_*`, `model`, `participant` | bounded only |
| Span attributes (Tempo) | the same enums **plus** `escrow.id`, `devshard.nonce`, `request.id`, `slot.id`, prompt hash | unbounded, fine |
| Log line fields (Loki) | `trace_id`, `span_id`, `request_id`, `nonce`, prompt text | unbounded, inside the line |

Enum values must be byte-identical between Prometheus labels and span attributes, so a Grafana panel
can link into Tempo by template substitution.

## Span tree

```
trace_id = 9f3c…
gateway.request                              [0 → 32m]
├── devshard.gateway.attempt  nonce=4711     [0 → 32m]   winner
│   ├── attempt.dispatch                     [0 → 1.2s]
│   ├── attempt.prefill                      [1.2s → 3.4s]
│   └── attempt.stream                       [3.4s → 32m]
│       ├── event stream.stall.detected      @ 11m
│       └── event stream.stall.recovered     @ 11m40s
├── devshard.gateway.attempt  nonce=4712     [0.8s → 4s]  loser / ghost
└── devshardd.inference (host, via traceparent)
    └── devshardd.mlnode.chat.completions
```

One trace id for everything above — plain parent–child nesting, no span links. Overscheduling shows
as sibling `devshard.gateway.attempt` subtrees, which is what makes winner/loser/ghost legible.

**Why the phases matter.** For a token stream, span *duration* is a length signal, not latency.
`attempt.prefill` is the span whose duration is meaningful (time to first token); `attempt.stream`
must be excluded from latency SLOs. Splitting also means the trace reaches Tempo within seconds
instead of only when the stream ends, because spans export on `End()`.

Every boundary above is an existing log point (`send_completed`, `receipt_received`, `first_token`,
the inter-chunk stall logs, `race_completed`), so this is "open a span where we already log".

## Streaming: what is live vs. what is stored

| Window | What you can see |
|--------|------------------|
| During the stream | Only logs. The 60 s heartbeat (`waiting_for_receipt` → `waiting_for_first_token` → `streaming_inflight`) and the 30 s inter-chunk stall lines already exist — they just need `trace_id` stamped on them. |
| ~8 s in | `attempt.dispatch` + `attempt.prefill` land in Tempo; the trace id is now searchable. |
| At stream end | The rest of the tree lands and the trace is complete on trace-ID lookup. |

Because those two waves can be far apart, they land in different Tempo blocks. Trace-ID lookup
merges blocks and is unaffected; TraceQL *search* sees one block at a time. Practical rule: **put
every attribute you filter on directly on the span that represents that thing**, and never rely on
a cross-span `{ A } && { B }` join.

## The late part: accounting dispositions

A nonce's disposition is decided long after the response — bounded by `RefusalTimeout` (60 s) or
`ExecutionTimeout` (32 min), plus the classification sweep. The request span is closed by then, and
holding it open is not an option (it would wreck latency data and stay unexported for the duration).

So the late verdict is emitted as its **own short root trace**, `devshard.nonce.disposition`,
carrying the full `CounterKey` attribute set, with a `trace.Link` back to the attempt span plus a
plain `devshard.origin_trace_id` attribute. Links are used **only** here — for the unbounded gap —
never inside a request.

Where each disposition can be observed:

| Trigger | Disposition | Known |
|---------|-------------|-------|
| request not sent | `ghost` | at dispatch — attribute on the live attempt span |
| protocol finish + winner / non-winner / unknown usage | `finished_used` / `finished_unused` / `finished_usage_unknown` | at race outcome — live attempt span |
| real send, deadline not reached | *(in flight)* | absence of a disposition |
| deadline + no receipt | `unfinished_refused` | ≈ 60 s later — linked trace |
| deadline + receipt applied | `unfinished_execution` | up to ≈ 32 min later — linked trace |
| nonce consumed with no dispatch | `protocol_only` | root trace, no parent by definition |

## Prompts

`DEVSHARD_LOG_PROMPTS` = `off` (production) | `hash` | `redacted` | `full` (testenv). Hash, size and
token counts always go on the span; the prompt text goes to a Loki line on failure only, inheriting
`trace_id` so the trace view links straight to it.

## Queries

```traceql
{ span.devshard.disposition = "unfinished_refused" && span.model = "…" }
{ span.devshard.nonce = 4711 }
```

```logql
{compose_service=~"devshardctl|versiond.*"} | json | trace_id = "9f3c…"
{compose_service="devshardctl"} | json | disposition = "ghost"
```

Grafana wiring: Loki derived field `trace_id` → Tempo; Tempo `tracesToLogsV2` → Loki; Prometheus
exemplars carrying `trace_id` on the accounting counters, so a spike in
`devshard_accounting_disposition` links straight to a real request.

## Stack

`tempo-alloy` is the primary e2e profile (apps → Alloy `:4317` → Tempo; Alloy also ships Docker logs
to Loki). `jaeger-promtail` stays supported.

## What has to be built first

Nothing above works until logs carry a trace id. In order:

1. **T1** — in **`common/observability`** (shared by devshardd, devshardctl, dapi, edge-api): a
   ctx-aware slog handler stamping `trace_id`/`span_id`, the request-id context helpers, and the
   OTel `Init` that is currently copy-pasted three times. Then OTel init in `devshardctl` (it has
   none today) and `traceparent` + `X-Request-Id` forwarding on the gateway → host hop.
2. **T2** — Tempo + Alloy profiles.
3. **T3.0** — run the accounting classification sweep on its own short ticker instead of only inside
   the 5-minute snapshot write (also fixes stale metrics and stale accounting API).
4. **T3** — attempt spans, then the classification log line, then the linked disposition trace.
5. **T4** — prompt capture.
6. **T5.0** — dapi cleanup: delete the dead inference surface (`InferenceValidator`,
   `DoWithLockedNodeHTTPRetry`, `InferenceTracer` and friends — all zero call sites), add `*Ctx`
   variants to `common/logging`, and thread `ctx` through broker/nodemanager so dapi logs can carry
   `trace_id` at all.
7. **T5** — the dapi hop. dapi is only a **node broker** on this path (its `/v1/chat/completions` is
   410 Gone), so this is just spans around the `AcquireMLNode`/`ReleaseMLNode` gRPC calls —
   startable entirely from the devshardd side, with shared gRPC interceptors in `common/` after.
