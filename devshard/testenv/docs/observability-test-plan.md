# Observability test plan — disposition scenarios with mock ML nodes

**Companion to:** [observability-trace-correlation-plan.md](./observability-trace-correlation-plan.md)
(the *implementation* plan) and [gateway-tracing.md](../docs/gateway-tracing.md).

**Purpose.** [PR #1547](https://github.com/gonka-ai/gonka/pull/1547) proved that every consumed nonce
lands in exactly one accounting disposition. It asserted *counters*. This plan re-runs the same
scenario matrix and asserts *telemetry*: that for each disposition a developer can reach the failing
request from Grafana and reconstruct its full path.

**Key addition over #1547:** #1547 cannot distinguish "the devshardd host failed" from "the ML node
failed", because its harness has no ML node — the host runs a stub inference engine. This plan uses
**testenv with mock ML nodes**, where gateway, devshardd hosts and the ML node are separate
processes, so the two failure origins are genuinely distinguishable and the *shape of the trace*
becomes the assertion.

---

## 1. Why testenv and not `devshard/e2e`

| | `devshard/e2e` (testcontainers) | `devshard/testenv/citest` (compose) |
|---|---|---|
| Gateway | real `devshardctl` | real `devshardctl` |
| Host | `devshard-host` with **stub inference engine** | real `devshardd` under `versiond` |
| ML node | **none — the stub *is* the ML node** | **`mock-openai`**, a separate service |
| dapi | none | `mock-dapi` (node selection) |
| Chain | `mock-chain` | `mock-chain` |
| Observability stack | **none** | Jaeger/Loki/Prometheus/Grafana today; Tempo+Alloy after T2 |
| Host-vs-MLNode failure origin | **indistinguishable** | distinguishable |

`devshard/e2e/accounting_test.go` stays as the authority on counter correctness. This plan lives in
`devshard/testenv/citest` because that is where the collectors run and where an ML node exists as a
separate failure domain.

**Topology under test:**

```
client → devshardctl ──┬─→ versiond-0 → devshardd ──┐
                       └─→ versiond-1 → devshardd ──┼─→ mock-openai   (the "ML node")
                                           │        │
                                           └─ mock-dapi (AcquireMLNode → endpoint)
                                     mock-chain (params, epochs, escrow)
```

---

## 2. Fault taxonomy — where you inject decides what you expect

The whole point of the plan. Each injection point produces a different **failure origin**, a
different **disposition**, and — most usefully — a different **missing span**.

| # | Fault | Injected via | Expected `failure_origin` | Expected disposition | Trace signature |
|---|-------|--------------|---------------------------|----------------------|-----------------|
| F1 | ML node returns 5xx | `POST /testenv/fault {http_status:503}` | `host_response` | `ghost` (after quarantine) | host span present, `devshardd.mlnode.chat.completions` **errors** |
| F2 | ML node slow to first token | `{latency_ms: 3000}` | `host_response` | `in_flight`, then escalation | `attempt.prefill` long; a **second** `devshard.gateway.attempt` sibling appears |
| F3 | ML node stalls mid-stream | `{stream_chunk_delay_ms: 45000}` | `host_response` | `unfinished_execution` | `stream.stall.detected` event on `attempt.stream` |
| F4 | ML node truncates the stream | `{partial_stream: true}` | `host_response` | `finished_usage_unknown` | `attempt.stream` ends without usage attrs |
| F5 | ML node drops the first chunk | `{drop_first_chunk: true}` | `host_response` | empty-stream quarantine | `first_token` log absent, `attempt.prefill` never closes normally |
| F6 | ML node returns empty content | `{empty_content: true}` **(gap G2)** | `host_response` | `finished_usage_unknown` | usage attrs zero |
| F7 | ML node rejects tool calls | `{sse_error_message: …}` (mock-openai T4a) | `host_response` | `ghost` / `participant_capability_no_send` | SSE error event on the host span |
| F8 | **devshardd host stopped** | `stack.StopService("versiond-1")` | `transport_unknown` | `ghost` / `poc_unavailable_host` | **no host span at all** — gateway spans only; `attempt.dispatch` errors |
| F9 | **devshardd host restarts mid-stream** | `RestartService` during a stream | `transport_unknown` | `unfinished_refused` | `attempt.stream` truncated, no `race_completed` |
| F10 | Host slow to acknowledge receipt | `DEVSHARD_E2E_RECEIPT_DELAY_MS` **(gap G3)** | `gateway_policy` | `unfinished_refused` | `attempt.dispatch` long, **`attempt.prefill` never starts** |
| F11 | dapi has no node to give | mock-dapi with zero nodes | — | attempt fails pre-ML | `devshardd.mlnode.acquire` = `ResourceExhausted`, **no mlnode span** |
| F12 | Chain query faulted | `SetEscrowQueryFault` | `gateway_policy` | phase-gated ghost | gateway span errors before any dispatch |
| F13 | Short protocol timeouts | `PatchAdversarialFastTimeouts` (refusal 5 s, execution 8 s) | — | enabler for F3/F9/F10 | shrinks the disposition lag |

**The discriminator is span absence.** F1 and F8 both end in `ghost`, but F1 has a host span with an
errored ML call and F8 has no host span at all. Asserting *which span is missing* is what makes the
telemetry actually diagnostic, so every scenario below carries a negative assertion as well as a
positive one.

---

## 3. Harness gaps to close first

These are prerequisites; without them the matrix in §4 cannot be expressed.

| ID | Gap | Detail |
|----|-----|--------|
| **G1** | **Per-node ML fault targeting** | ✅ **Closed by T7** — `ml_nodes: N` emits `mock-openai-{i}`; mock-dapi `MOCK_ML_NODES` round-robins with real `NodeId` / exclusions / lock tracking; harness `PatchMockOpenAIFaultForNode` + `StopMLNode`; citest `TestMLNodePool_PerNodeFault` (`make citest-ml-nodes`). Unlocks S3/S16 winner–loser asymmetry. |
| **G2** | **Fault vocabulary** | Partial: T4a added `sse_error_message` + nested OpenAI/vLLM HTTP error bodies. Still missing `empty_content` / `response_body` for full host-stub parity (`DEVSHARD_STUB_INFERENCE_RESPONSE_BODY`). |
| **G3** | **Host-side e2e knobs in testenv** | `DEVSHARD_E2E_RECEIPT_DELAY_MS`, `DEVSHARD_E2E_REFUSAL_TIMEOUT_SECONDS`, `DEVSHARD_E2E_EXECUTION_TIMEOUT_SECONDS` are read by `devshard-host` but not plumbed through `versiond` → `devshardd` in the generated compose. Needed for F10 and to shorten F3/F9. |
| **G4** | **Observability overlay on the accounting stack** | `BootObservabilityStack` exists, but the adversarial/accounting stacks boot without it. Add an `Observability: true` option so any scenario can assert telemetry. |
| **G5** | **Trace/log assertion helpers** | Per §9 of the implementation plan: `WaitTraceSpan`, `WaitTraceByAttr`, `RequireLogsForTrace`, `RequireSpanAttrs`, plus `RequireNoSpan` for the negative assertions this plan depends on. |
| **G6** | **No mid-test pause** | Only stop/start/restart exist (`stack.go:220-244`, `restart.go:92-108`) — no `docker pause`/SIGSTOP. F9 therefore models a *restart*, not a freeze. Adding pause would let us test a host that is hung rather than gone; optional. |

---

## 4. Scenario matrix — #1547 dispositions, re-expressed with mock ML nodes

Each row keeps the original test as the counter authority and adds the telemetry assertions.

| ID | Disposition | #1547 test (counters) | testenv fault | Telemetry assertions |
|----|-------------|----------------------|---------------|----------------------|
| **S1** | `finished_used` | `TestE2E_AccountingHappyPath`, `…NoResidualAfterFinishedTraffic` | none | one trace, gateway + host + mlnode spans; `attempt.dispatch`/`prefill`/`stream` all present and ordered; `devshard.disposition=finished_used` |
| **S2** | `in_flight` | `TestE2E_AccountingLiveInFlightIsCountedOnce` | F2 (`latency_ms:3000`) | attempt span open at assert time; **no** disposition span; heartbeat log lines `streaming_inflight` share the trace id |
| **S3** | `finished_unused` | `TestE2E_AccountingOverscheduledLoserFinishes` | F2 on one node only (**G1**) + `speed_policy=hybrid` | two sibling attempt spans in one trace, distinct nonces; one `finished_used`, one `finished_unused`; loser has `stream_suppressed` log |
| **S4** | `finished_usage_unknown` | `TestE2E_AccountingFinishedUsageUnknown` | F6 (**G2**) or F4 | attempt completes, usage attrs absent/zero; disposition span attribute matches the counter |
| **S5** | `unfinished_refused` | `TestE2E_AccountingNoReceiptTimeoutBecomesUnfinishedRefused` | F10 (**G3**) + F8 mid-flight | `attempt.dispatch` long; **`RequireNoSpan("attempt.prefill")`**; disposition arrives as a *linked* trace (`devshard.origin_trace_id` matches) |
| **S6** | `unfinished_execution` | `TestE2E_AccountingLiveSendTimeout` | F13 + F3 | stall events present; `timeout_kind=execution`; linked disposition trace |
| **S7** | `ghost` / throttled | `TestE2E_AccountingFocusedGhostRequestNotSent` | F1 (`http_status:503`) | host span **present** with errored ML call; later ghost attempt has `no_send_reason=participant_throttled_no_send` and `quarantine_mode != none` |
| **S8** | `ghost` / capability | `TestE2E_AccountingGhostCapabilityNoSendReason` | F7 (**G2**), tool request | `no_send_reason=participant_capability_no_send`; SSE error visible on the host span |
| **S9** | `ghost` / host unavailable | `TestE2E_AccountingOneHostUnavailable` | F8 (`StopService`) | **`RequireNoSpan(service="devshardd")`** for that attempt — the discriminator against S7 |
| **S10** | timeout cross-check | `TestE2E_AccountingAppliedTimeoutCrossCheck` | F10 + F8 | `timeout_outcome=applied` on the disposition span equals the `devshard_accounting_timeout_outcome` counter |
| **S11** | `protocol_only` | unit only in #1547 | chain-driven nonce with no dispatch | **root** disposition trace with no parent — the only scenario where an orphan trace is correct |

---

## 5. Failure-origin scenarios (the new coverage)

These have no #1547 counterpart. They exist to prove the trace tells you *which layer* broke.

| ID | Story | Setup | Must assert |
|----|-------|-------|-------------|
| **S12** | ML node dies, host healthy | F1 on all nodes, hosts up | Host spans present for every attempt; every ML span errored; `failure_origin=host_response`; gateway logs and host logs share one trace id |
| **S13** | Host dies, ML node healthy | F8, no ML fault | No host span; gateway `attempt.dispatch` carries the transport error; `failure_origin=transport_unknown`; **no** ML span either, proving the request never reached the node |
| **S14** | Host dies mid-stream | F9 during an active stream | `attempt.stream` present but truncated; no `race_completed` log; the partial stream is still queryable by trace id |
| **S15** | Node selection fails | F11 (mock-dapi with no nodes) | `devshardd.mlnode.acquire` span with `ResourceExhausted`; **no** `devshardd.mlnode.chat.completions`; the failure is attributed to dapi, not the ML node |
| **S16** | Slow node, fast node | F2 on one node (**G1**) | Two attempt subtrees with clearly different `attempt.prefill` durations; the winner is the fast one; `devshard_host_first_token_seconds` and the span durations agree |
| **S17** | Chain unavailable | F12 | Gateway span errors before dispatch; no host span, no ML span; `failure_origin=gateway_policy` |

---

## 6. Invariants asserted in *every* scenario

Run these as a shared helper rather than repeating them per test.

| ID | Invariant |
|----|-----------|
| **I1** | Exactly one `trace_id` covers the request across ≥2 `compose_service` values. |
| **I2** | Every Loki line produced by the request carries that `trace_id` — no orphan lines from `devshardctl`, `versiond-*`. |
| **I3** | The trace is reachable from the `request_id` returned to the client. |
| **I4** | Nonce ↔ span: for each nonce in the accounting API response there is a span with `devshard.nonce` equal to it, or a documented reason it is absent (`protocol_only`). |
| **I5** | **Label parity (C4 from the impl plan):** for every label value present on `devshard_accounting_disposition`, a span attribute with the identical string exists. This is the contract that makes Grafana data links work. |
| **I6** | No high-cardinality identifier appears as a Prometheus label or a Loki **stream label** — lint `promtail-config.yaml` / `config.alloy` label stages. |
| **I7** | On an ML-node-failure scenario, a payload line exists carrying `devshard.prompt.sha256`, linked to the trace by `trace_id` (T4a). Validation-failure capture joins by `inference_id` + hash instead (T4b). |
| **I8** | `RequireNonceAccountingBalanced` still passes — telemetry must not change accounting behaviour. |

---

## 7. Assertion helpers to add

Extend `devshard/testenv/citest/harness/`:

```go
// tracing.go
func WaitTraceSpan(t, obs, service, operation string, timeout time.Duration) string   // → trace id
func WaitTraceByAttr(t, obs, tagQuery string, timeout time.Duration) []string
func RequireSpanAttrs(t, obs, traceID string, want map[string]string)
func RequireNoSpan(t, obs, traceID, service, operation string)                        // negative
func SpanTree(t, obs, traceID string) TraceTree                                       // shape assertions
func RequireSpanOrder(t, tree TraceTree, names ...string)

// logs.go
func RequireLogsForTrace(t, obs, traceID string, services []string)
func LogLinesForTrace(t, obs, traceID string) []LogLine
func RequireLogStage(t, lines []LogLine, stage string)

// faults.go (extends adversarial.go)
func PatchMockOpenAIFaultForNode(t, stack, nodeID string, fault Fault)   // G1
```

`WaitTraceSpan` replaces the Jaeger-specific `WaitJaegerSpan`
(`citest/harness/observability.go:150-162`) with a profile-aware version, per T2.5.

---

## 8. Timing — do not fight the deadlines

Deadline-based dispositions are slow by construction (§5.2 of the implementation plan:
`RefusalTimeout` 60 s, `ExecutionTimeout` 32 min, plus the classification sweep). For S5, S6 and S10:

1. `PatchAdversarialFastTimeouts` (refusal 5 s, execution 8 s) via mock-chain params.
2. Once **T3.0** lands, the sweep runs on its own short ticker, so no snapshot forcing is needed.
   Until then, shrink `DEVSHARD_STATS_SNAPSHOT_SECONDS` or the linked disposition trace will not
   exist when the assertion runs.
3. Poll for the *linked* trace with `WaitTraceByAttr` rather than a fixed sleep — the lag is
   inherently variable.

---

## 9. Suggested CI shape

| Job | Profile | Scenarios | Cadence |
|-----|---------|-----------|---------|
| `citest-observability` | `tempo-alloy` | I1–I3, S1, S7, S9 | every PR |
| `citest-observability-dispositions` | `tempo-alloy` | S2–S11 | nightly |
| `citest-observability-failure-origin` | `tempo-alloy` | S12–S17 | nightly |
| `citest-observability-jaeger` | `jaeger-promtail` | I1–I3, S1 | weekly, regression guard |

Keep the existing `devshard/e2e` accounting suite unchanged and green — it is the counter authority
and it runs faster than any of the above.

---

## 10. Delivery order

1. **G4 + G5** — observability overlay on the accounting stack, plus assertion helpers. Land S1 and
   the I1–I3 invariants against the *current* Jaeger stack; this validates T1 end to end.
2. **G3** — host knobs through versiond. Unlocks S5, S6, S10.
3. **G2** — fault vocabulary parity. Unlocks S4, S8.
4. **G1** ✅ — per-node fault targeting (**T7** landed). Unlocks S3, S16; `mlnode.node.id` is a real dimension.
5. **S12–S17** — failure-origin suite, once T5a gives us the `mlnode.acquire` span that S15 asserts.
