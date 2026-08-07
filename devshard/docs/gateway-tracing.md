# Gateway tracing — how request forensics works

One-page explanation of the target design for gateway request forensics via
OpenTelemetry traces + correlated logs.

**Deeper docs (testenv / implementation):**

| Doc | Role |
|-----|------|
| [observability-trace-correlation-plan.md](../testenv/docs/observability-trace-correlation-plan.md) | Design: correlation contract, span shape, late dispositions |
| [observability-t3-implementation-plan.md](../testenv/docs/observability-t3-implementation-plan.md) | Step-by-step T3 build plan (attempt spans, classification, sweep) |
| [observability-test-plan.md](../testenv/docs/observability-test-plan.md) | E2E / citest scenarios |
| [observability-plan.md](../testenv/docs/observability-plan.md) | Stack / profile selection (Tempo, Alloy, Jaeger) |

**Related proposals:**

- [proposals/gateway-observability](../../proposals/gateway-observability/observability.md) — metrics-first Grafana dashboard
- [proposals/gateway-dashboard](../../proposals/gateway-dashboard/README.md) — durable epoch accounting dashboard

---

## The goal

Given a `request_id`, an `inferenceId` (== nonce), or an accounting bucket such as `ghost` or
`unfinished_refused`, one lookup returns the whole request path — every span across gateway →
devshardd → dapi → mlnode, **and** every log line those components wrote, including the prompt that
caused the failure.

## The one idea

**`trace_id` is the join key.** Everything else follows:

- one trace id per request, minted at the gateway, propagated by `traceparent`;
- every log line carries that `trace_id`;
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

Attempts are staggered escalations to different hosts (not an up-front parallel fan-out). One user
request can grow sibling `gateway.attempt` spans over time; each carries `role` and `start_reason`
so the trace view shows why the request was split.

```
trace_id = 9f3c…
gateway.request                              [0 → 32m]
├── gateway.attempt  nonce=4711              [0 → 32m]   winner
│   ├── attempt.dispatch                     [0 → 1.2s]
│   ├── attempt.prefill                      [1.2s → 3.4s]
│   └── attempt.stream                       [3.4s → 32m]
│       ├── event stream.stall.detected      @ 11m
│       └── event stream.stall.recovered     @ 11m40s
├── gateway.attempt  nonce=4712              [0.8s → 4s]  loser / ghost
└── devshardd.inference (host, via traceparent)
    └── devshardd.mlnode.chat.completions
```

One trace id for everything above — plain parent–child nesting, no span links inside the request.
Overscheduling shows as sibling attempt subtrees, which is what makes winner/loser/ghost legible.

**Why the phases matter.** For a token stream, span *duration* is a length signal, not latency.
`attempt.prefill` is the span whose duration is meaningful (time to first token); `attempt.stream`
must be excluded from latency SLOs. Splitting also means the trace reaches Tempo within seconds
instead of only when the stream ends, because spans export on `End()`.

Every boundary above is an existing log point (`send_completed`, `receipt_received`, `first_token`,
the inter-chunk stall logs, `race_completed`), so this is "open a span where we already log".

## Streaming: what is live vs. what is stored

| Window | What you can see |
|--------|------------------|
| During the stream | Only logs. The 60 s heartbeat (`waiting_for_receipt` → `waiting_for_first_token` → `streaming_inflight`) and the 30 s inter-chunk stall lines carry `trace_id`. |
| ~8 s in | `attempt.dispatch` + `attempt.prefill` land in Tempo; the trace id is now searchable. |
| At stream end | The rest of the tree lands and the trace is complete on trace-ID lookup. |

Because those two waves can be far apart, they land in different Tempo blocks. Trace-ID lookup
merges blocks and is unaffected; TraceQL *search* sees one block at a time. Practical rule: **put
every attribute you filter on directly on the span that represents that thing**, and never rely on
a cross-span `{ A } && { B }` join.

## The late part: accounting dispositions

A nonce's disposition is decided long after the response — bounded by `RefusalTimeout` (60 s) or
`ExecutionTimeout` (32 min), plus the classification sweep (below). The request span is closed by
then, and holding it open is not an option (it would wreck latency data and stay unexported for the
duration).

So the late verdict is emitted as its **own short root trace**, `devshard.nonce.disposition`,
carrying the full `CounterKey` attribute set, with a `trace.Link` back to the attempt span plus a
plain `devshard.origin_trace_id` attribute. Links are used **only** here — for the unbounded gap —
never inside a request. A structured classification log line with the same `trace_id` is the
backbone of the reverse lookup (disposition → request) even when Tempo stitching disappoints.

Where each disposition can be observed:

| Trigger | Disposition | Known |
|---------|-------------|-------|
| request not sent | `ghost` | at dispatch — attribute on the live attempt span |
| protocol finish + winner / non-winner / unknown usage | `finished_used` / `finished_unused` / `finished_usage_unknown` | at race outcome — live attempt span |
| real send, deadline not reached | *(in flight)* | absence of a disposition |
| deadline + no receipt | `unfinished_refused` | ≈ 60 s later — linked trace |
| deadline + receipt applied | `unfinished_execution` | up to ≈ 32 min later — linked trace |
| nonce consumed with no dispatch | `protocol_only` | root trace, no parent by definition |

### The disposition queue

Both the log line and the late span come off one bounded channel inside the accounting tracker
(`dispositionQueueSize = 1024`, `accounting/disposition.go`).

**Who fills it.** `finalizeNonce` — the single choke point every nonce passes through on its way out
of the live set — plus the `protocol_only` path. Its hottest caller is the per-diff observer, which
runs inside the sequencer's `Session.mu` critical section, so the enqueue is a **non-blocking send**:
if the queue is full the event is dropped and `devshard_accounting_disposition_drops` counts it. The
sequencer is never made to wait on telemetry.

**What it carries.** Immutable `DispositionEvent` values — the finished `CounterKey`, the nonce's
`TraceRef`, and the send/observe timestamps. It is an output tap, not a work queue: every state
mutation has already been committed under `Tracker.mu` before the event is enqueued, so dropping one
loses observability, never accounting truth. That is why a bounded queue with a drop counter is the
right trade rather than backpressure or unbounded growth.

**Who drains it.** One goroutine per tracker, calling the registered sink. Sink work — building a
log line, starting a span, exporting it — therefore never runs on a lock-holding writer's goroutine.
1024 is sized to absorb a settlement burst while still surfacing a wedged sink through the drop
counter within seconds instead of quietly eating memory.

**When it fires for a long stream.** Not until the stream ends. A nonce becomes terminal only once
the protocol `FinishInference` diff and the gateway's usage fact have both landed, and a
past-deadline nonce with neither is held back by `persistable`, so it stays visible as
`devshard_accounting_in_flight` / `timeout_pending` rather than emitting a premature verdict.

### Why the accounting sweep runs every 5 seconds

Deadline-based dispositions (`unfinished_refused`, `unfinished_execution`) are time-dependent: a
nonce that goes silent generates **no further accounting event** after the last gateway fact
(`TimeoutResult`). Classification only advances when something calls `refreshDerived`.

Historically that caller was only the 5-minute snapshot/`Flush` path. That is the wrong cadence for
telemetry:

1. **The deadline transition is eventless by construction.** `TimeoutBuffer` is added *on top of*
   `RefusalTimeout` / `ExecutionTimeout`, so the last gateway fact is guaranteed to arrive *before*
   the accounting deadline. Crossing the deadline therefore produces no record event for a
   disposition log line or late span to hang off — unless a short sweep re-evaluates live nonces.
2. **Restart loses unpromoted classifications.** Live nonce state is not persisted. A past-deadline
   nonce that has not yet been folded into counters disappears on restart and resurfaces as
   `Unclassified`. A 5 s sweep shrinks that loss window from up to one snapshot interval to one
   sweep interval.
3. **Terminal live entries are only reaped by reclassification.** Without a sweep, deadline-terminal
   nonces linger in the in-memory `Live` map until the next snapshot.

The sweep calls `refreshDerived` under the write lock and does **not** write SQLite. Persistence
stays on the 5-minute snapshot tick. Override with `DEVSHARD_STATS_SWEEP_SECONDS` (`0` disables).

**Not why the sweep exists:** keeping Prometheus or `/api/v1/epochs` fresh. `Query` already folds
live nonces through `counterKey` at read time with a fresh clock, so API and scrape views were
never lagging the deadline — only promotion into persisted counters and event-driven telemetry
were.

See **T3.0** in
[observability-t3-implementation-plan.md](../testenv/docs/observability-t3-implementation-plan.md).

## Payloads

`DEVSHARD_LOG_PAYLOADS` (`off`|`hash`|`redacted`|`full`, default `off`) plus per-trigger switches
(`_MLNODE`, `_QUARANTINE`, `_VALIDATION`) control whether failing request/response bodies are
written to Loki. Payloads go to a Loki line inheriting `trace_id` — not to span attributes. Only a
fingerprint join key on the log line and a `payload.captured` span event stay on the trace.
`full` is testenv-only.

## Queries

```traceql
{ span.devshard.disposition = "unfinished_refused" && span.model = "…" }
{ span.devshard.nonce = 4711 }
{ span.devshard.attempt.start_reason = "receipt_timeout" }
```

```logql
{compose_service=~"devshardctl|versiond.*"} | json | trace_id = "9f3c…"
{compose_service="devshardctl"} | json | disposition = "ghost"
```

Grafana wiring: Loki derived field `trace_id` → Tempo; Tempo `tracesToLogsV2` → Loki; Prometheus
panels link into Tempo via TraceQL templates on disposition labels.

## Stack

`tempo-alloy` is the primary e2e profile. `jaeger-promtail` stays supported as the legacy fallback.

**Why Alloy, not Promtail.** Tempo and Jaeger are interchangeable *storage/query* backends for
traces. The choice that matters for the agent is Alloy vs Promtail:

| Profile shape | App OTLP target | Logs | Traces |
|---------------|-----------------|------|--------|
| `*-alloy` | **Alloy** `:4317` | Alloy → Loki | Alloy → Tempo *or* Jaeger |
| `*-promtail` | **Tempo or Jaeger** directly | Promtail → Loki | app → Tempo/Jaeger (no agent) |

Promtail only ships logs. Under a Promtail profile, spans go straight from the process to Jaeger or
Tempo; if that backend is down or the link flaps, those spans are dropped at the client — Promtail
cannot buffer them.

Alloy is the single telemetry agent: metrics scrape, Docker logs → Loki, **and** OTLP trace ingest.
Apps never talk to Tempo/Jaeger when an `*-alloy` profile is active. Alloy can queue and retry both
logs and traces across backend blips, then deliver them when the backend recovers. That is why
Alloy is the better default, and why `tempo-alloy` (apps → Alloy → Tempo + Loki) is the primary
stack. Buffering is best-effort (queue/WAL sized by config), not infinite durable storage if Alloy
itself dies with a full disk.

## Build order (phases)

1. **T1** ✅ — ctx-aware logging with `trace_id`/`span_id`; OTel init in `devshardctl`; `traceparent` +
   `X-Request-Id` on the gateway → host hop.
2. **T2** ✅ — Tempo + Alloy profiles (`tempo-alloy` e2e default; Jaeger/Promtail still green).
3. **T3.0** ✅ — 5 s classification sweep (this document, above).
4. **T3** ✅ — attempt spans, classification log line, linked disposition trace
   (`TestDispositionTrace*` / C3–C4 via `make citest-observability`; unfinished late-path citest still pending G3).
5. **T4a** ✅ — ML-node failure + quarantine payload capture (`DEVSHARD_LOG_PAYLOADS*`); T4b (validation) still deferred.
6. **T5** — dapi node-selection hop + mlnode (devshardd client spans first).
   Test plan: [observability-t5-test-plan.md](../testenv/docs/observability-t5-test-plan.md) (C8 three-service
   logs; C9 shadow multi-host under one `request_id` / `trace_id`).
7. **T6** — Grafana forensics dashboards + citest C1–C7 (+ C8/C9 once T5 lands).
