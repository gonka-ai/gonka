# Streaming HA citest scenarios

End-to-end Docker Compose coverage for **always-stream upstream** and
**same-nonce reconnect**. Design overview:
[`../../docs/gateway-streaming-ha-overview.md`](../../docs/gateway-streaming-ha-overview.md).
Implementation plans:
[`../../docs/gateway-always-stream-upstream-plan.md`](../../docs/gateway-always-stream-upstream-plan.md),
[`../../docs/gateway-attempt-reconnect-plan.md`](../../docs/gateway-attempt-reconnect-plan.md).

Stack layout, ports, and general how-to-run notes live in
[`scenarios.md`](./scenarios.md). This file is only the streaming / reconnect
suites.

## How to run

```bash
cd devshard/testenv
make build-devshardd          # linux binary for versiond children
make citest-force-upstream-streaming   # always-stream + aggregate spill (this file)
make citest-attempt-reconnect          # same-nonce reconnect — see attempt-reconnect-scenarios.md
```

| Variable / tag | Purpose |
|----------------|---------|
| `TESTENV_CITEST=1` | Opt-in gate (`harness.SkipUnlessEnv`) |
| `-tags=testenvci` | Full-stack citest build tag; also enables host fault injectors used by reconnect |

Both targets appear in `make list-citest-targets` and therefore in the CI matrix.
Reconnect scenario listing + multi-step run instructions:
[`attempt-reconnect-scenarios.md`](./attempt-reconnect-scenarios.md).

---

## Force-upstream streaming (`make citest-force-upstream-streaming`)

**Status:** Landed. Gates Step 14 default-on in the always-stream plan.
**Sources:** `citest/gateway_forced_upstream_stream_test.go`,
`citest/gateway_aggregate_spill_test.go`, helpers in `citest/harness/force_upstream.go`.
**Flag:** `POST /v1/admin/settings` → `{"redundancy":{"force_upstream_streaming":…}}`
(`harness.PatchGatewayForceUpstreamStreaming`).

These tests drive the **real** gateway → proxy handoff (`handlePooledChat` →
runtime mux). Unit tests that call `normalizeChatRequest` / the proxy handler
directly cannot catch double-normalization intent bugs.

| Test | What we validate |
|------|------------------|
| `TestGatewayForcedStreamClientShape` | With flag on **and** off: `stream:false` → `application/json` `chat.completion`; `stream:true` → `text/event-stream` ending in `data: [DONE]` |
| `TestGatewayForcedStreamUsageSuppression` | Flag on: streaming client without `include_usage` never sees top-level `usage`; with it, one final usage chunk; non-stream aggregate always has `usage` |
| `TestGatewayForcedStreamLogprobStrip` | Flag on and off: client that omitted logprobs never sees `logprobs` / `top_logprobs` / `token_ids` / `prompt_logprobs`. Client that asked for logprobs without `top_logprobs` gets emptied tops; with any `top_logprobs > 0` keeps the forced upstream width (5), not a truncated client count |
| `TestGatewayForcedStreamAggregateMatchesUnforced` | Same prompt, flag on then off, `stream:false`: identical content / `finish_reason` / `model` / `object` and matching `completion_tokens` |
| `TestGatewayForcedStreamCacheIsolation` | Flag on: identical body for stream vs non-stream clients → each shape; second hit of each keeps the right `Content-Type` |
| `TestGatewayForcedStreamFlagFlip` | Admin flip mid-flight and between requests: each request keeps the client shape it started with (per-request ForceUpstreamStreaming snapshot) |
| `TestGatewayAggregateSpillRoundTrip` | Tiny `GATEWAY_AGGREGATE_MAX_MEMORY_BYTES`, flag on, `stream:false`: complete JSON fold, logs `aggregate_spilled=true`, spool dir empty afterward |
| `TestGatewayAggregateOversizeAborts` | Tiny `GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES`: typed ≥400 error, never `200` with a truncated body |

Unit-only companions (not citest): `force_upstream_escalation_test.go` (first-token
escalation under force-upstream), SSE event size cap tests in `transport/`.

---

## Same-nonce reconnect (`make citest-attempt-reconnect`)

Full scenario ↔ plan mapping, deferred rows, and step-by-step run instructions:
**[`attempt-reconnect-scenarios.md`](./attempt-reconnect-scenarios.md)**.

Landed tests (summary): `TestAttemptReconnect_AdminEnables`,
`TestAttemptReconnect_V2ProtocolSkipsSameNonce`,
`TestAttemptReconnect_V5MidStreamDetachResumesSameNonce`.

---

## Acceptance matrix (design overview mapping)

Rows from the streaming HA overview that are already covered by the suites above
(or by unit tests). Remaining rows stay soak / deferred.

| Theme | Overview intent | Coverage |
|-------|-----------------|----------|
| Client JSON vs SSE under force-upstream | Shape + usage + logprobs | `citest-force-upstream-streaming` |
| Differential aggregate | Content match flag on/off | `TestGatewayForcedStreamAggregateMatchesUnforced` |
| Cache isolation by stream intent | Separate keys / shapes | `TestGatewayForcedStreamCacheIsolation` |
| Escalation under always-stream | First-token, not reduced-`max_tokens` | Unit: `force_upstream_escalation_test.go` |
| Aggregate spill / oversize (F4) | Spill round-trip + typed abort | `TestGatewayAggregateSpill*` / `Oversize*` |
| Mid-stream same-nonce resume (v5) | One completion, one finish | `citest-attempt-reconnect` |
| Protocol gate (≤v4) | No same-nonce resume | `TestAttemptReconnect_V2ProtocolSkipsSameNonce` |
| SSE event size cap | `ErrSSEEventTooLarge` | Unit: `transport/` |
| Cross-instance ML reattach | HA reboot without aborting ML | Deferred |

---

## Related

| Doc / suite | Role |
|-------------|------|
| [`scenarios.md`](./scenarios.md) | Core stack citest index |
| [`attempt-reconnect-scenarios.md`](./attempt-reconnect-scenarios.md) | Same-nonce reconnect citest + how to run |
| [`../../docs/gateway-streaming-ha-overview.md`](../../docs/gateway-streaming-ha-overview.md) | End-to-end design |
| [`../../docs/spool-shared-library.md`](../../docs/spool-shared-library.md) | Shared aggregate / LiveStream scratch spool |
| `make citest-adversarial` | Phase-9 fault injection (orthogonal) |
| `make citest-observability` | O1 Jaeger / Loki / Prometheus smoke |
