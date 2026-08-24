# Observability test plan — trace, log and disposition scenarios

**Companion to:** [observability-trace-correlation-plan.md](./observability-trace-correlation-plan.md)
(the *implementation* plan) and [gateway-tracing.md](../../docs/gateway-tracing.md).

**Purpose.** [PR #1547](https://github.com/gonka-ai/gonka/pull/1547) proved that every consumed nonce
lands in exactly one accounting disposition. It asserted *counters*. This plan re-runs the same
scenario matrix and asserts *telemetry*: that for each disposition a developer can reach the failing
request from Grafana and reconstruct its full path.

**Key addition over #1547:** #1547 cannot distinguish "the devshardd host failed" from "the ML node
failed", because its harness has no ML node — the host runs a stub inference engine. This plan uses
**testenv with mock ML nodes**, where gateway, devshardd hosts and the ML node are separate
processes, so the two failure origins are genuinely distinguishable and the *shape of the trace*
becomes the assertion.

This file is the complete scenario catalogue: every scenario below carries its own boot, setup,
drive and assertion steps, so a test can be written from the section alone.
[`scenarios.md`](./scenarios.md) links here instead of duplicating them.

---

## 1. Topology and why testenv

| | `devshard/e2e` (testcontainers) | `devshard/testenv/citest` (compose) |
|---|---|---|
| Gateway | real `devshardctl` | real `devshardctl` |
| Host | `devshard-host` with **stub inference engine** | real `devshardd` under `versiond` |
| ML node | **none — the stub *is* the ML node** | **`mock-openai`**, a separate service |
| dapi | none | `mock-dapi` (node selection over the real `NodeManager` gRPC API) |
| Chain | `mock-chain` | `mock-chain` |
| Observability stack | **none** | Tempo + Alloy (default), Jaeger + Promtail (legacy), Loki, Prometheus, Grafana |
| Host-vs-MLNode failure origin | **indistinguishable** | distinguishable |

`devshard/e2e/accounting_test.go` stays the authority on counter correctness. These scenarios live in
`devshard/testenv/citest` because that is where the collectors run and where an ML node exists as a
separate failure domain.

```
client → devshardctl ──┬─→ versiond-0 → devshardd ──┐
                       └─→ versiond-1 → devshardd ──┼─→ mock-openai   (the "ML node")
                                           │        │
                                           └─ mock-dapi (AcquireMLNode → endpoint)
                                     mock-chain (params, epochs, escrow)
```

**Host identity matters when picking a stack.** `BootObservabilityStack` gives two versiond hosts
that are **one** on-chain participant behind the router (an HA pair), so it cannot produce
host-diversity or host-loss effects: stopping one host removes no participant, and the picker
reports "tried every host in escrow" after a single attempt. Scenarios that need two *participants*
boot `BootObservabilityStackHASolo` — HA pair (`versiond-0`/`versiond-1`) **plus** a solo executor
(`versiond-2`) whose `InferenceURL` is direct.

---

## 2. Fault injection reference

Each injection point produces a different **failure origin**, a different **disposition**, and — most
usefully — a different **missing span**.

| # | Fault | Injected via | Expected `failure_origin` | Expected disposition | Trace signature |
|---|-------|--------------|---------------------------|----------------------|-----------------|
| F1 | ML node returns 5xx | `POST /testenv/fault {http_status:503}` | `host_response` | `ghost` (after quarantine) | host span present, `devshardd.mlnode.chat.completions` **errors** |
| F2 | ML node slow to first token | `{latency_ms: 3000}` | `host_response` | `in_flight`, then escalation | `attempt.prefill` long; a **second** `devshard.gateway.attempt` sibling appears |
| F3 | ML node stalls mid-stream | `{stream_chunk_delay_ms: 45000}` | `host_response` | `unfinished_execution` | `stream.stall.detected` event on `attempt.stream` |
| F4 | ML node truncates the stream | `{partial_stream: true}` | `host_response` | `finished_usage_unknown` | `attempt.stream` ends without usage attrs |
| F5 | ML node drops the first chunk | `{drop_first_chunk: true}` | `host_response` | empty-stream quarantine | `first_token` log absent, `attempt.prefill` never closes normally |
| F6 | ML node returns empty content | `{empty_content: true}` | `host_response` | `finished_usage_unknown` | usage attrs zero |
| F7 | ML node returns an SSE error | `{sse_error_message: …}` (vLLM-shaped nested body) | `host_response` | `ghost` / `participant_capability_no_send` | SSE error event on the host span |
| F8 | **devshardd host stopped** | `stack.StopService("versiond-2")` | `transport_unknown` | `ghost` / `poc_unavailable_host` | **no host span at all** — gateway spans only; `attempt.dispatch` errors |
| F9 | **devshardd host restarts mid-stream** | `RestartService` during a stream | `transport_unknown` | `unfinished_refused` | `attempt.stream` truncated, no `race_completed` |
| F10 | Host slow to acknowledge receipt | `DEVSHARD_E2E_RECEIPT_DELAY_MS` on devshardd | `gateway_policy` | `unfinished_refused` | `attempt.dispatch` long, **`attempt.prefill` never starts** |
| F11 | dapi has no node to give | mock-dapi with zero nodes | — | attempt fails pre-ML | `devshardd.mlnode.acquire` = `ResourceExhausted`, **no mlnode span** |
| F12 | Chain query faulted | `SetEscrowQueryFault` | `gateway_policy` | phase-gated ghost | gateway span errors before any dispatch |
| F13 | Short protocol timeouts | `PatchAdversarialFastTimeouts` (refusal 5 s, execution 8 s) | — | enabler for F3/F9/F10 | shrinks the disposition lag |
| F14 | Per-node fault in a pool | `PatchMockOpenAIFaultForNode` / `StopMLNode` on `mock-openai-{i}` | `host_response` on one node only | scenario-dependent | one attempt subtree degraded, its sibling healthy |

**The discriminator is span absence.** F1 and F8 both end in `ghost`, but F1 has a host span with an
errored ML call and F8 has no host span at all. Asserting *which span is missing* is what makes the
telemetry diagnostic, so scenarios carry a negative assertion as well as a positive one.

---

## 3. Invariants asserted in every scenario

Run these as a shared helper rather than repeating them per test.

| ID | Invariant |
|----|-----------|
| **I1** | Exactly one `trace_id` covers the request across ≥2 `compose_service` values. |
| **I2** | Every Loki line produced by the request carries that `trace_id` — no orphan lines from `devshardctl`, `versiond-*`, `mock-dapi`. |
| **I3** | The trace is reachable from the `request_id` returned to the client (`X-Request-Id`). |
| **I4** | Nonce ↔ span: for each nonce in the accounting API response there is a span with `devshard.nonce` equal to it, or a documented reason it is absent (`protocol_only`, see S11). |
| **I5** | **Label parity:** for every label value present on `devshard_accounting_disposition`, a span attribute with the identical string exists. This is the contract that makes Grafana data links work (asserted directly by C4). |
| **I6** | No high-cardinality identifier appears as a Prometheus label or a Loki **stream label** — lint `promtail-config.yaml` / `config.alloy` label stages. |
| **I7** | On an ML-node-failure scenario, a payload line exists carrying `devshard.prompt.sha256`, linked to the trace by `trace_id` (C5a). Validation-failure capture joins by `inference_id` + hash instead (C5b). |
| **I8** | `RequireNonceAccountingBalanced` still passes — telemetry must not change accounting behaviour. |

---

## 4. Correlation scenarios

### C1 / C2 — one chat, one trace across gateway and host

**Test:** `TestTraceLogCorrelation` — `citest/trace_log_correlation_test.go`

**Intent.** A single chat must be reconstructable end to end from one `trace_id`: spans from both
`devshardctl` and `devshardd`, and Loki lines from both `compose_service` families carrying that
same id. This is the base correlation contract every other scenario builds on — if it breaks, no
other trace assertion means anything.

**Boot:** `BootObservabilityStack` (2 hosts, `tempo-alloy` by default). `WaitObservabilityReady`,
`WaitStackHealthy`, `WaitGatewayChatReady` before driving traffic, so a cold collector cannot be
mistaken for a missing span.

**Setup:** none — happy path.

**Drive:** one non-streaming chat via `PostGatewayChatCompletionEx`; capture `X-Request-Id`.

**Assert:**

1. Response content is mock-openai's (`RequireMockOpenAIContent`) — the trace must belong to a
   genuinely served request, not a rejected one.
2. `WaitTraceCoveringServices(t, obs, []string{"devshardctl", "devshardd"}, …)` returns a trace id.
3. `RequireLogsForTrace(t, obs, traceID, []string{"devshardctl", "versiond.*"}, …)` — at least one
   Loki line per service family carries that `trace_id`.
4. `WaitTraceSpan(devshardctl, "gateway.request")` and `WaitTraceSpan(devshardd, "devshardd.request")`.

**Dump on fail:** `devshardctl`, `versiond-0`, `versiond-1`, and the profile's collectors.

---

### C3 — ghost disposition reachable by TraceQL

**Test:** `TestDispositionTraceGhost` — `citest/disposition_trace_test.go`

**Intent.** A burned (ghost) nonce must be findable from Grafana by disposition alone:
`{ span.devshard.disposition = "ghost" }` returns the trace, and that trace's Loki lines are
reachable from it. Counters say "a ghost happened"; this says "here is the request it happened to".

**Boot:** `BootObservabilityStackHASolo`. The solo executor is what makes ghosts possible: its
`InferenceURL` is direct, so transport failures quarantine that participant and later slots burn
`ghostThrottled`.

**Setup:**

1. Drive one warm chat first, so escrow slots are live and both participants are in the picker.
2. `stack.StopService(t, "versiond-2")` (F8).

**Drive:** `len(cfg.Hosts)*4` chats through `PostGatewayChatSoft` — failures are expected and must
not fail the test. That many rounds gives the dead participant time to take transport quarantine and
then burn ghost slots.

**Assert:**

1. `WaitLokiLogQL` finds the gateway's disposition line:
   `{compose_service="devshardctl"} | json | stage="nonce_disposition" | disposition="ghost"`.
2. `WaitTraceByAttr` on `{ span.devshard.disposition = "ghost" }` returns ≥1 trace id.
3. `RequireSpanAttrs(ids[0], {"devshard.disposition": "ghost"})`.
4. `RequireLogsForTrace(ids[0], []string{"devshardctl"})` — the trace found by attribute is joinable
   back to logs.

**Note:** ghost dispositions come from the late accounting sweep, not the request path. Poll with
`WaitTraceByAttr` / `WaitLokiLogQL`; a fixed sleep will be either flaky or slow.

---

### C4 — disposition label parity between Prometheus and spans

**Test:** `TestDispositionLabelValuesMatchSpanAttrs` — `citest/disposition_trace_test.go`

**Intent.** Grafana data links jump from a Prometheus series to a trace by matching a label value to
a span attribute *string*. That only works if every positive label value exists verbatim as a span
attribute. This is invariant I5 asserted directly, over the whole live label set rather than a
hard-coded list.

**Boot:** `BootObservabilityStackHASolo`.

**Setup:** produce at least two different dispositions in one run, so parity is checked on more than
the happy path.

**Drive:**

1. One happy chat → `finished_used`.
2. `stack.StopService(t, "versiond-2")`, then `len(cfg.Hosts)*4` soft chats → `ghost`.

**Assert:**

1. Both dispositions have landed: `WaitTraceByAttr` for `finished_used`, the `nonce_disposition`
   LogQL line for `ghost`, then `WaitTraceByAttr` for `ghost`.
2. Poll `TryQueryPrometheusInstant` on `devshard_accounting_disposition > 0` until it returns series;
   on timeout dump the gateway `/metrics` body for the metric before failing.
3. For every series and each label → attribute pair — `disposition`, `no_send_reason`,
   `quarantine_mode`, `failure_origin`, `dispatch_phase` mapping to `devshard.*` — run
   `WaitTraceByAttr({ span.<attr> = "<value>" })` and `RequireSpanAttrs` on the returned trace.
4. The observed set must include both `devshard.disposition=finished_used` and
   `…=ghost`, so a run that produced only one disposition fails instead of passing vacuously.

**Note:** only positive series are checked — a zero-valued counter has no request behind it and
therefore no span to match.

---

### C5a — ML-node failure payload joins the trace

**Tests:** `TestPayloadCaptureHTTP503`, `TestPayloadCapturePartialStream`,
`TestPayloadCaptureSSEError` — `citest/payload_capture_test.go`

**Intent.** When the ML node breaks a request, the operator needs the actual bytes, not just a
disposition. Capture must emit a `payload_captured` line carrying a prompt fingerprint and a
response sample on the request's trace — and the size-only `payload_quarantine` line must leak
neither prompts nor bodies.

**Boot:** `BootPayloadCaptureStack(prefix, "full")` — the observability stack with
`DEVSHARD_LOG_PAYLOADS*` enabled. Level `full` is testenv-only and is what puts body samples on the
line; production defaults to hashes and sizes.

**Setup**, per variant:

- **HTTP 503:** `PatchMockOpenAIFault{HTTPStatus: 503}` (F1), `PatchAdversarialFastTimeouts`,
  `PatchAdversarialFastRedundancy`, and `PatchGatewayAdminSettings` with
  `participant_throttle.empty_stream_threshold: 1` so a single strike causes a quarantine
  transition, and with it the `payload_quarantine` line the negative assertion needs.
- **Partial stream:** `PatchMockOpenAIFault{PartialStream: true}` (F4), fast timeouts and
  redundancy; the prompt is padded so the truncated body is unambiguously non-empty.
- **SSE error:** `PatchMockOpenAIFault{SSEErrorMessage: "TimeoutError"}` (F7) — a vLLM-shaped nested
  error body — plus fast timeouts.

**Drive:** one chat whose prompt carries a unique needle (`fmt.Sprintf("citest-payload-capture-…-%d",
time.Now().UnixNano())`), which is how the assertion finds its own request among concurrent traffic.
Use `PostGatewayChatExpectFailure` for the 503 variant and `PostGatewayChatStreamResult` for the two
streaming variants.

**Assert:**

1. `WaitPayloadCapturedLog(needle)` returns a line with `devshard.prompt.sha256`, `failed_at` and
   `response_ms`.
2. **503:** the body may already be gone after host receipt, so when `response_bytes == 0` the line
   must still carry `http_status` or `response_headers` as the fallback that identifies the upstream
   failure.
3. **Partial stream:** `response_bytes > 0` **and** a non-empty `response` field — at `full` level a
   truncated stream must leave its body sample.
4. **SSE error:** `response_bytes > 0` **or** `error_type` / `error_message` set.
5. **Negative (503 variant):** the `payload_quarantine` line has `request_bytes` (sizes are the
   point) but no `devshard.prompt.sha256`, no `response` body, and its `request` field is the
   request id rather than a JSON body — `logging.Stage` always sets `request=<request_id>`, which is
   not a payload.

---

### C5b — validation-failure payload capture

**Intent.** When a validator returns `Valid: false` — including the hash-mismatch path — a payload
line must exist for the invalidated `inference_id` carrying both prompt and response, so the
disagreement between executor and validator can be reconstructed from the two bodies.

**Boot:** observability stack with payload capture enabled and validation forced on (validation rate
100, as `WriteValidationLeaseRaceConfig` already does).

**Setup:** make one inference fail validation — either an executor/validator mismatch, or a forced
`Valid: false` from the validating host.

**Drive:** one chat with a unique needle; record the `inference_id` from the accounting API.

**Assert:**

1. A payload line exists for that `inference_id` carrying prompt and response (or their hashes plus
   samples, at the configured level).
2. Executor-side and validator-side lines are joinable to each other by `inference_id` + payload
   hash.
3. The gateway's invalidation accounting for that nonce is reachable from the same `inference_id`.

**Note:** validation runs in a different process on a different trace, so this scenario joins on
`inference_id` + payload hash, **not** on `trace_id` — the one correlation scenario where I1 does not
apply. The natural capture point is `cmd/devshardd/inference/validator.go`, which holds both payloads
at the decision point, before the hash-mismatch check discards them.

---

### C6 — the default profile serves traces, logs and metrics end to end

**Test:** `TestObservabilitySmoke` — `citest/observability_smoke_test.go`

**Intent.** The canary that separates "the feature is broken" from "the collector is broken": OTLP
export reaches Tempo, container logs reach Loki, and both gateway and host Prometheus endpoints
serve their metric families.

**Boot:** `BootObservabilityStack` (`tempo-alloy`).

**Setup:** none.

**Drive:** one happy chat.

**Assert:**

1. `WaitTraceSpan(devshardctl, "gateway.request")`, `WaitTraceSpan(devshardd, "devshardd.request")`,
   `WaitTraceSpan(devshardd, "devshardd.inference")` — gateway, host request and host inference legs
   all export.
2. `WaitLokiSubstring("devshard request terminal")` — logs reach Loki independently of traces.
3. `RequireMetricsBody` finds `devshardd_request_duration_seconds` on the router's
   `/{version}/metrics` and `devshard_http_requests_total` on the gateway's `/metrics`.

**Note:** the Alloy-UI receiver→exporter inspection stays a manual check; the automated leg proves
the data arrives in Tempo and Loki, which is what the scenarios depend on.

---

### C7 — the legacy profile has not regressed

**Test:** `TestJaegerPromtailRegression` — `citest/jaeger_promtail_regression_test.go`

**Intent.** Profile-parity guard. Everything else runs on `tempo-alloy`; `jaeger-promtail` must still
produce the same correlation shape so a rollback remains possible.

**Boot:** `t.Setenv("TESTENV_OBS_PROFILE", "jaeger-promtail")` then `BootObservabilityStack`. Fail
immediately if the resolved profile is not `jaeger-promtail` — otherwise a mis-wired override
silently re-runs C1/C2 on the default profile and the guard proves nothing.

**Drive:** one happy chat.

**Assert:** the C1/C2 set, through the Jaeger query API and Promtail-fed Loki —
`WaitTraceCoveringServices([devshardctl, devshardd])`, `RequireLogsForTrace([devshardctl,
versiond.*])`, and both request spans.

**Note:** the profile is a per-test environment variable, so this runs inside the default suite; run
the whole suite on the legacy profile with `OBS_PROFILE=jaeger-promtail make citest-observability`.

---

### C8 — three-service correlation: gateway, host and node manager

**Test:** `TestTraceLogCorrelationGatewayHostDapi` — `citest/dapi_trace_correlation_test.go`

**Intent.** One user chat must produce **one** `trace_id` and **one** `request_id` across all three
hops:

| Hop | Compose service | Role |
|-----|-----------------|------|
| Gateway | `devshardctl` | accepts chat, races hosts |
| Host | `versiond-*` / `devshardd` | Acquire → ML HTTP → Release |
| Node manager | `mock-dapi` | `AcquireMLNode` / `ReleaseMLNode` (the same gRPC surface as real dapi) |

C1/C2 cover gateway + host. The node-selection hop is the one that used to drop the caller's context
on the floor (`AcquireMLNode` took `_ context.Context`), which is exactly how an orphan root trace
appears in Tempo.

**Boot:** `BootObservabilityStack` (2 hosts, `tempo-alloy`), payload capture off.

**Setup:** none — happy path.

**Drive:** one chat via `PostGatewayChatCompletionEx`, unique needle in the prompt; capture
`X-Request-Id` and require it non-empty.

**Assert:**

1. **Start from the client's request id, not from "the most recent trace".**
   `RequireSingleTraceForRequest(obs, "devshardctl", requestID)` — a warm-up chat must not be able to
   satisfy the rest, and "exactly one" is simultaneously the negative case: an orphan Acquire that
   started its own root trace shows up here as a second id.
2. `RequireLogsForTrace(traceID, ["devshardctl", "versiond.*", "mock-dapi"])` — every hop wrote at
   least one line on that trace.
3. `RequireRequestIDOnTrace(traceID, requestID, [same three families])` — those lines describe the
   same *user request*, not merely the same trace.
4. `RequireStagesForTrace(traceID, "mock-dapi", [StageMLNodeAcquire])`: the acquire line exists,
   names the node it handed back (`node_id`), and carries the client's `request_id`.
5. Spans: `WaitTraceServices(traceID, ["devshardctl", "devshardd", "mock-dapi"])`,
   `WaitTraceSpanNames(traceID, ["gateway.request", "devshardd.request"])`, and
   `WaitTraceSpanNameAny(traceID, ["devshardd.mlnode.acquire",
   "nodemanager.NodeManager/AcquireMLNode"])` — the acquire hop is visible either as the host client
   span or the mock-dapi server span, and either is sufficient.

**Note:** logs are the hard requirement for `mock-dapi`; spans are the readable form of the same
fact. Prefer asserting `mlnode.node.id` on the span when the attribute is available.

**Dump on fail:** `devshardctl`, `versiond-0`, `mock-dapi`, `alloy`, `loki`, `tempo`.

---

### C9 — shadow host → multi-host attempts, one client identity

**Test:** `TestShadowHostMultiAttemptSameTrace` — `citest/shadow_multi_host_trace_test.go`

**Intent.** Shadow quarantine still sends **real** traffic (`ParticipantRequestLimiter`: probe =
no-send; shadow = real send, no-winner). Combined with redundancy escalation, the gateway opens
attempts on **≥2 distinct hosts** for one client request. All attempts must stay under the **same**
`request_id` and parent `trace_id`, including the `mock-dapi` acquires each attempt triggers.

**Boot:** `BootObservabilityStackHASolo` — two *participants* are required, and the plain 2-host
stack is a single participant, so no fan-out is possible there. Then
`PatchAdversarialFastRedundancy` (so a secondary attempt starts without waiting out the production
receipt timeout) and `PatchAdversarialFastTimeouts`.

**Setup:**

1. Resolve the escrow id (`GetGatewayEscrowID`) and the participant keys from the admin throttle
   snapshot (`GetParticipantThrottles`).
2. Force shadow quarantine on one participant: set `empty_stream_threshold: 1` and drive an
   empty-stream failure against it (`ForceShadowQuarantine`).
3. **Leave exactly one participant quarantined.** The fault drive is stack-wide, so every
   participant that takes an attempt is struck; with all of them quarantined no host may win and the
   client request fails with `no non-empty response`. `ForceOneShadowQuarantine` clears the rest via
   `POST /v1/admin/participants/unquarantine` and returns the key that stays.
4. Confirm the limiter reports shadow mode for that key (`RequireShadowQuarantineMode`) *before*
   driving the measured chat.

**Drive:** chats with unique needles, in a bounded loop (8 rounds), capturing `X-Request-Id` each
time. One chat is not enough: the picker does not always make the shadow host primary, and the first
rounds after the fault drive can still fail while the healthy host works off its probation strikes.
Use `PostGatewayChatSoftEx` and skip a round that returns a non-200 or empty content — an unserved
round says nothing about attempt fan-out. Keep the first round that yields one trace id
(`TryTraceIDsForRequest`) and ≥2 host attempts (`TryWaitMultiHostAttempts`).

**Assert:**

1. ≥2 `gateway.attempt` spans on the chosen trace, and
2. those attempts target ≥2 **distinct** hosts (`GatewayAttemptParticipants`) — not two attempts on
   the same slot.
3. `RequireSingleTraceForRequest` again for the chosen `request_id`: the extra attempts must not have
   minted a second trace.
4. `RequireLogsForTrace(traceID, ["devshardctl", "versiond.*", "mock-dapi"])` — all three services
   are still on the one trace.
5. Every `mlnode_acquire` line on that trace carries the chosen `request_id`
   (`RequireStagesForTrace`), so a second attempt cannot smuggle in a second request identity.
6. `RequireRequestIDOnTrace` across the three families: attempts add `span_id`s, they do not mint a
   new `request_id`.

**Note:** do **not** use probe quarantine here — probe burns silent probes and hides the multi-host
real-send shape this scenario exists to prove.

---

## 5. Disposition scenarios

Each keeps its #1547 counter test as the authority on the count and adds the telemetry assertions.
All of them also assert the §3 invariants.

### S1 — `finished_used`, the full span tree

**Counter authority:** `TestE2E_AccountingHappyPath`, `…NoResidualAfterFinishedTraffic`.

**Intent.** The baseline shape: a served request produces one trace containing gateway, host and
ML-call spans in dispatch → prefill → stream order, and its disposition is `finished_used` on both
the counter and the span.

**Boot:** `BootObservabilityStack`.

**Setup:** none.

**Drive:** one non-streaming and one streaming chat, unique needles, capturing `X-Request-Id`.

**Assert:**

1. `RequireSingleTraceForRequest` → `traceID`; `WaitTraceServices(traceID, [devshardctl, devshardd,
   mock-dapi])`.
2. `attempt.dispatch`, `attempt.prefill` and `attempt.stream` all present under one attempt subtree,
   in that order (`SpanTree` + `RequireSpanOrder`).
3. `devshard.disposition=finished_used` on the disposition span, matching
   `devshard_accounting_disposition{disposition="finished_used"}`.
4. Invariants I1–I4.

**Note:** run this first after any collector or profile change — every later scenario assumes the
happy shape is correct.

---

### S2 — `in_flight` counted once while the stream is open

**Counter authority:** `TestE2E_AccountingLiveInFlightIsCountedOnce`.

**Intent.** While a request is still streaming, the nonce is `in_flight`: no terminal disposition may
exist yet, and the heartbeat logs must already be joinable to the trace so an operator can watch a
live request rather than wait for its post-mortem.

**Boot:** `BootObservabilityStack`.

**Setup:** F2/F3 — `{latency_ms: 3000, stream_chunk_delay_ms: …}` so the attempt is still open when
the assertions run. Keep the chunk delay well above the poll interval.

**Drive:** start a streaming chat and assert *while it runs*; do not wait for completion.

**Assert (stream open):**

1. The attempt span exists and has not ended; the trace carries no terminal
   `devshard.disposition` (`RequireNoSpan` on the disposition span).
2. Loki `streaming_inflight` heartbeat lines carry the same `trace_id` **and** `request_id`.
3. `devshard_accounting_disposition{disposition="in_flight"}` is 1 for that nonce and stays 1 across
   two scrapes — the "counted once" half.

**Assert (after completion):** the same nonce flips to `finished_used`, and `in_flight` does not
remain outstanding.

---

### S3 — `finished_unused`, the overscheduled loser

**Counter authority:** `TestE2E_AccountingOverscheduledLoserFinishes`.

**Intent.** Under hybrid speed policy the gateway races two hosts; the loser still finishes its
stream and must be accounted `finished_unused`. The trace must show two sibling attempt subtrees with
distinct nonces under one client request.

**Boot:** HA-solo hosts **and** an ML node pool (`ml_nodes: 2`), so each host attempt can be pointed
at a different `mock-openai` and faulted independently.

**Setup:**

1. `speed_policy=hybrid` and redundancy enabled via `PatchGatewayAdminSettings` /
   `PatchAdversarialFastRedundancy`.
2. `PatchMockOpenAIFaultForNode(node 0, {latency_ms: 3000})` (F14) — exactly one slow node.

**Drive:** one chat with a unique needle.

**Assert:**

1. Two sibling `gateway.attempt` spans on one trace with distinct `devshard.nonce`.
2. One resolves `finished_used`, the other `finished_unused`; both counters increment.
3. The loser has a `stream_suppressed` log line on the same trace.
4. `GatewayAttemptParticipants` reports two distinct hosts.

**Note:** the pool is what makes this work — a stack-wide fault slows both attempts equally and the
race disappears.

---

### S4 — `finished_usage_unknown`

**Counter authority:** `TestE2E_AccountingFinishedUsageUnknown`.

**Intent.** A stream that ends without usage data is served but unbillable. The span must say so the
same way the counter does, so an operator can tell it apart from a normal completion.

**Boot:** `BootPayloadCaptureStack` (so I7's payload line is available too).

**Setup:** F4 `{partial_stream: true}`, or F6 `{empty_content: true}` for the zero-usage variant.

**Drive:** one streaming chat with a unique needle.

**Assert:**

1. `attempt.stream` completes — this is *not* a stall — with usage attributes absent or zero.
2. `devshard.disposition=finished_usage_unknown` on the span and on
   `devshard_accounting_disposition`.
3. A `payload_captured` line for the needle exists on the same trace (I7).

---

### S5 — `unfinished_refused` when the host never acknowledges receipt

**Counter authority:** `TestE2E_AccountingNoReceiptTimeoutBecomesUnfinishedRefused`.
**Test:** `TestDispositionTraceUnfinishedRefused` — `citest/disposition_trace_test.go` (currently skipped).

**Intent.** When the host never acknowledges receipt, the gateway gives up and records
`unfinished_refused`. The trace must show the dispatch hanging with prefill never starting: the
**missing `attempt.prefill`** is the discriminator against a host that accepted the work and then
failed.

**Boot:** observability stack with the host protocol timeouts actually shortened on devshardd —
`DEVSHARD_E2E_RECEIPT_DELAY_MS`, `DEVSHARD_E2E_REFUSAL_TIMEOUT_SECONDS` and
`DEVSHARD_E2E_EXECUTION_TIMEOUT_SECONDS` must reach `devshardd` through `versiond` in the generated
compose. `PatchAdversarialFastTimeouts` alone patches mock-dapi params and leaves the host's
production `ExecutionTimeout` (~32 min) in place, which is longer than any citest budget.

**Setup:** F10 (receipt delay) with F8 mid-flight for the transport half.

**Drive:** one chat, expecting failure.

**Assert:**

1. `attempt.dispatch` present and long.
2. `RequireNoSpan("attempt.prefill")` — the negative assertion that defines this scenario.
3. `devshard.disposition=unfinished_refused` on span and counter.
4. The disposition arrives as a **linked** trace: `devshard.origin_trace_id` equals the request's
   trace id. The sweep runs on its own trace, so poll with `WaitTraceByAttr` on that attribute rather
   than expecting the disposition span nested inside the request trace.

---

### S6 — `unfinished_execution` from a mid-stream stall

**Counter authority:** `TestE2E_AccountingLiveSendTimeout`.

**Intent.** A stream that goes silent mid-flight must be detected, terminated and accounted
`unfinished_execution`, with the stall visible as an event on the stream span.

**Boot:** as S5 (short host timeouts), plus F13 `PatchAdversarialFastTimeouts`.

**Setup:** F3 `{stream_chunk_delay_ms: 45000}` after first token.

**Drive:** one streaming chat; let the stall trip the execution timeout.

**Assert:**

1. `stream.stall.detected` event on `attempt.stream`.
2. `timeout_kind=execution` on the span; `devshard_accounting_timeout_outcome` agrees.
3. `devshard.disposition=unfinished_execution`, again on a linked disposition trace.

---

### S7 — `ghost` because the participant is throttled

**Counter authority:** `TestE2E_AccountingFocusedGhostRequestNotSent`.

**Intent.** A node that keeps failing gets its participant quarantined; subsequent slots burn as
ghosts *without being sent*. The distinguishing evidence is that the earlier, real attempts have a
host span with an **errored ML call** — the failure originated at the node, not in transport.

**Boot:** `BootObservabilityStackHASolo`.

**Setup:** F1 `{http_status: 503}` on the node plus a low `empty_stream_threshold`, then drive
traffic until the limiter quarantines that participant.

**Drive:** one more chat after quarantine, with a unique needle.

**Assert:**

1. For the failing attempts: host span **present**, `devshardd.mlnode.chat.completions` errored,
   `failure_origin=host_response`.
2. For the ghost: `no_send_reason=participant_throttled_no_send` and `quarantine_mode != none`.
3. The ghost is reachable by the C3 TraceQL lookup.

**Note:** compare with S9 — same `ghost` disposition, opposite span shape.

---

### S8 — `ghost` because the participant lacks the capability

**Counter authority:** `TestE2E_AccountingGhostCapabilityNoSendReason`.

**Intent.** A request the participant cannot serve (a tool call it rejects) must be recorded as a
capability no-send, not as a generic failure, and the rejection itself must be visible on the host
span rather than inferred from the absence of a response.

**Boot:** `BootPayloadCaptureStack` (the SSE error body is worth capturing).

**Setup:** F7 `{sse_error_message: …}` and a tool-calling chat request.

**Drive:** one streaming chat with a unique needle.

**Assert:**

1. `no_send_reason=participant_capability_no_send` on span and counter.
2. The SSE error is visible on the host span (event) and on the `payload_captured` line
   (`error_type` / `error_message`).

---

### S9 — `ghost` because the host is unavailable

**Counter authority:** `TestE2E_AccountingOneHostUnavailable`.

**Intent.** When the host process is gone, the request never reaches a node. The trace must make
that unambiguous by having **no host span at all** — this is the discriminator against S7.

**Boot:** `BootObservabilityStackHASolo`.

**Setup:** F8 `stack.StopService("versiond-2")`.

**Drive:** enough chats for the picker to select the dead participant.

**Assert:**

1. `RequireNoSpan(service="devshardd")` for that attempt.
2. `attempt.dispatch` carries the transport error; `failure_origin=transport_unknown`.
3. Disposition `ghost` (or `poc_unavailable_host`) on span and counter.

---

### S10 — timeout outcome cross-check

**Counter authority:** `TestE2E_AccountingAppliedTimeoutCrossCheck`.

**Intent.** The timeout the gateway *applied* must be the timeout it *reports*, in both telemetry
channels — otherwise timeout tuning is guesswork.

**Boot / Setup:** as S5 (F10 receipt delay + F8).

**Drive:** one chat that trips the timeout.

**Assert:** `timeout_outcome=applied` on the disposition span equals the
`devshard_accounting_timeout_outcome` counter label for the same nonce.

---

### S11 — `protocol_only`, the legitimate orphan trace

**Intent.** A nonce consumed by the chain protocol with no gateway dispatch must still be accounted.
Its disposition trace is legitimately a **root with no parent** — the single documented exception to
I1, and the reason I4 allows a nonce with no request span.

**Boot:** `BootObservabilityStack`.

**Setup:** drive a chain-side nonce that produces no dispatch.

**Drive:** advance the protocol (epoch / settlement) rather than sending a chat.

**Assert:**

1. A disposition span exists with `devshard.disposition=protocol_only` and no parent span.
2. No gateway attempt span and no host span exist for that nonce.
3. `RequireNonceAccountingBalanced` still holds (I8).

**Note:** this is the only scenario where an orphan trace is a pass rather than a bug; assert the
absence of a parent explicitly so a regression that re-parents it is caught.

---

## 6. Failure-origin scenarios

These have no #1547 counterpart. They exist to prove the trace tells you *which layer* broke.

### S12 — ML node dies, host healthy

**Intent.** Every attempt reaches a host, and every host reaches a node that fails. The evidence must
point at the node: host spans present, ML spans errored.

**Boot:** `BootObservabilityStack`. **Setup:** F1 on all nodes, hosts left up.

**Drive:** one chat with a unique needle.

**Assert:** host spans present for every attempt; every ML span errored;
`failure_origin=host_response`; gateway and host logs share one trace id.

---

### S13 — host dies, ML node healthy

**Intent.** The mirror image of S12, and the reason span *absence* is an assertion: with no host
span **and** no ML span, the trace proves the request never left the gateway.

**Boot:** `BootObservabilityStackHASolo`. **Setup:** F8, no ML fault.

**Drive:** enough chats to hit the dead participant.

**Assert:** `RequireNoSpan(devshardd)`; `RequireNoSpan` for the ML call as well;
`attempt.dispatch` carries the transport error; `failure_origin=transport_unknown`.

---

### S14 — host dies mid-stream

**Intent.** A stream cut in half must remain diagnosable: the partial output stays queryable by trace
id, and the absence of the completion marker is what identifies the truncation.

**Boot:** `BootObservabilityStackHASolo`. **Setup:** F9 — `RestartService` during an active stream.

**Drive:** one streaming chat, restarting the serving host after first token.

**Assert:** `attempt.stream` present but truncated; no `race_completed` log line; the partial stream
content is still reachable from the trace id; disposition `unfinished_refused`.

**Note:** only stop/start/restart exist in the harness, so this models a host that *went away*, not
one that hung. A hung-host variant needs `docker pause` support.

---

### S15 — node selection fails

**Intent.** When dapi has nothing to hand out, the failure belongs to node selection — not to the ML
node, which was never contacted. The acquire span carries the error and the ML-call span must not
exist at all.

**Boot:** observability stack with `mock-dapi` configured with zero nodes (F11).

**Drive:** one chat with a unique needle.

**Assert:**

1. `devshardd.mlnode.acquire` span present with `ResourceExhausted`.
2. `RequireNoSpan("devshardd.mlnode.chat.completions")`.
3. `mock-dapi` logged `stage=mlnode_acquire` with the failing outcome on the same trace and
   `request_id` (the C8 join, on the failure path).

---

### S16 — slow node against fast node

**Intent.** With a pool, attempt durations must be *readable*: the trace should show which node was
slow, and the histogram and the span durations must tell the same story.

**Boot:** ML node pool (`ml_nodes: 2`) with observability, HA-solo hosts.
**Setup:** F14 `PatchMockOpenAIFaultForNode(node 0, {latency_ms: 3000})`.

**Drive:** one chat with redundancy enabled.

**Assert:** two attempt subtrees with clearly different `attempt.prefill` durations; the winner is
the fast one; `devshard_host_first_token_seconds` and the span durations agree.

---

### S17 — chain unavailable

**Intent.** A gateway that cannot read the chain must fail *before* dispatch, and the trace must
attribute the failure to gateway policy rather than to a host or node that was never involved.

**Boot:** `BootObservabilityStack`. **Setup:** F12 `SetEscrowQueryFault`.

**Drive:** one chat, expecting failure.

**Assert:** the gateway span errors before any dispatch; no host span and no ML span exist;
`failure_origin=gateway_policy`; the disposition is a phase-gated ghost.

---

## 7. Harness helpers these scenarios use

In `devshard/testenv/citest/harness/`:

| Helper | Purpose |
|--------|---------|
| `BootObservabilityStack` / `BootObservabilityStackHASolo` | 2-host (one participant) and 3-host (HA pair + solo executor) stacks with the observability overlay |
| `BootPayloadCaptureStack` | Same, with `DEVSHARD_LOG_PAYLOADS*` enabled at a given level |
| `BootMLNodePoolStack` | 2-host stack with N `mock-openai` instances behind mock-dapi |
| `WaitTraceCoveringServices` / `WaitTraceServices` | Service coverage: "any recent trace" vs. pinned to one trace id |
| `WaitTraceSpan` / `WaitTraceSpanNames` / `WaitTraceSpanNameAny` | Span presence by service + operation, or by name within a trace |
| `WaitTraceByAttr` / `RequireSpanAttrs` | TraceQL attribute lookup and per-span attribute assertions |
| `TraceSpans` / `GatewayAttemptParticipants` / `TryWaitMultiHostAttempts` | Raw spans, attempt count and host diversity from `gateway.attempt` |
| `RequireLogsForTrace` / `WaitLokiLogQL` / `WaitLokiEntries` / `WaitLokiSubstring` | Loki assertions by trace, by LogQL, by substring |
| `TryTraceIDsForRequest` / `RequireSingleTraceForRequest` | `request_id` → `trace_id`, with the "exactly one trace" negative case |
| `RequireRequestIDOnTrace` / `RequireStagesForTrace` | `request_id` parity per service; `stage=mlnode_acquire` / `mlnode_release` on a trace |
| `PatchMockOpenAIFault` / `PatchMockOpenAIFaultForNode` / `StopMLNode` | Stack-wide and per-node ML faults |
| `PatchAdversarialFastTimeouts` / `PatchAdversarialFastRedundancy` / `PatchGatewayAdminSettings` | Shorten protocol timers and patch limiter settings |
| `ForceShadowQuarantine` / `ForceOneShadowQuarantine` / `ClearParticipantQuarantine` / `RequireShadowQuarantineMode` | Drive and shape participant quarantine state |
| `WaitPayloadCapturedLog` / `WaitPayloadQuarantineLog` | Payload capture lines by needle |
| `TryQueryPrometheusInstant` / `RequireMetricsBody` | Prometheus series and raw `/metrics` bodies |
| `PostGatewayChatCompletionEx` / `PostGatewayChatSoftEx` / `PostGatewayChatSoft` / `PostGatewayChatExpectFailure` / `PostGatewayChatStreamResult` | Chat drivers: with headers, non-failing, failure-expecting, streaming |

Still to add, all of them negative or shape assertions the failure-origin scenarios depend on:

| Helper | Purpose |
|--------|---------|
| `RequireNoSpan(t, obs, traceID, service, operation)` | The negative assertion behind S2, S5, S13, S15 |
| `SpanTree(t, obs, traceID)` / `RequireSpanOrder(t, tree, names…)` | Parent/child shape and ordering (S1, S3, S16) |

---

## 8. Timing

Deadline-based dispositions are slow by construction (`RefusalTimeout` 60 s, `ExecutionTimeout`
32 min, plus the classification sweep). For S5, S6 and S10:

1. Shorten the protocol timers — `PatchAdversarialFastTimeouts` for mock-dapi params, and the
   `DEVSHARD_E2E_*` knobs on devshardd itself for the host-side deadlines.
2. The sweep runs on its own short ticker; if a run predates that, shrink
   `DEVSHARD_STATS_SNAPSHOT_SECONDS` or the linked disposition trace will not exist when the
   assertion runs.
3. Poll for the *linked* trace with `WaitTraceByAttr` rather than sleeping — the lag is inherently
   variable.

---

## 9. Suites

CI discovers suites with `make -C devshard/testenv list-citest-targets` and fans them out one runner
per target, so a new `citest-*` target runs in parallel with no workflow edit.

| Target | Profile | Scenarios |
|--------|---------|-----------|
| `citest-observability` | `tempo-alloy` (default) | C1/C2, C3, C4, C5a, C6, C7, C8, C9 |
| `citest-observability` with `OBS_PROFILE=jaeger-promtail` | `jaeger-promtail` | the same set on the legacy profile |
| `citest-dapi-correlation` | `tempo-alloy` | C8 and C9 only — a subset for iterating on the node-selection hop, so CI's matrix skips it |
| `citest-ml-nodes` | none | per-node fault targeting (`TestMLNodePool_PerNodeFault`), the pool behaviour S3 and S16 build on |

As the S-series lands, split by cost rather than growing one target:

| Job | Profile | Scenarios | Cadence |
|-----|---------|-----------|---------|
| `citest-observability` | `tempo-alloy` | C1–C9, I1–I3, S1, S7, S9 | every PR |
| `citest-observability-dispositions` | `tempo-alloy` | S2–S11 | nightly |
| `citest-observability-failure-origin` | `tempo-alloy` | S12–S17 | nightly |
| `citest-observability-jaeger` | `jaeger-promtail` | I1–I3, S1 | weekly, regression guard |

Keep the existing `devshard/e2e` accounting suite unchanged and green — it is the counter authority
and it runs faster than any of the above.
