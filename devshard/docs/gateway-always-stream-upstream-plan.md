# Gateway Always-Stream Upstream — implementation plan

Status: proposal / plan.
Proposal: [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md)

## Goal

The gateway always requests a **streamed** completion from devshardd (and therefore from
vLLM), regardless of what the client asked for. The client's `stream` flag becomes purely a
response-shape decision made at the gateway's client boundary:

- client `stream: true` → forward SSE chunks as today,
- client `stream: false` → aggregate the chunks into one `chat.completion` JSON.

This buys real liveness signal (first-token escalation, inter-chunk stall detection) and real
TTFT metrics for 100% of chat traffic, and lets devshardd eventually support only streaming.

## Current state (findings)

| Concern | Today |
|---|---|
| Client `stream` parsing | `request_filters_parameters.go:355-360` → `chatRequest.Stream` |
| `stream` upstream | Registered as a pass-through vLLM parameter (`request_filters_parameters.go:668`); client value reaches vLLM inside `PayloadJSON.Prompt` |
| `stream_options` | Whitelisted to `include_usage` at `RequestFilterStagePreValidation`, and **stripped entirely** when `stream` is not `true` (`paramvalidators/stream_options.go:42`) |
| Client shape branch | `proxy.go:238-242` → `handleStreaming` / `handleNonStreaming` |
| Non-stream "aggregation" | `assembleSSEChunks` (`proxy.go:579`) returns the **last** `data:` line |
| gateway↔devshardd framing | Always SSE (`transport/server.go:343-348`) |
| devshardd → ML | `stream` taken from the prompt; SSE upstream is proxied line-by-line (`inference/proxy.go:61`), JSON upstream is wrapped as one event (`inference/execute.go:106`) |
| Stored response body | JSON upstream → raw completion JSON; SSE upstream → `{"events":[…]}` (`completionapi/responseprocessor.go:78-88`) |
| `ResponseHash` | `sha256` of that stored body (`inference/execute.go:112`); only the hash goes on chain |
| Validation | Forces `stream:false` on replay and compares logits, not bytes (`common/validation/validation.go:345-347`) |
| Escalation gating | First-token escalation disabled when `!params.Stream` (`redundancy.go:2681`); non-stream gets a 140s reduced-`max_tokens` retry (`redundancy.go:2484`) and a 30-min no-content timeout (`redundancy.go:2500`) |
| Gateway cache key | `sha256(model + "\0" + normalizedBody)` (`response_cache.go:98`); `stream` lives inside the body, so shapes are isolated by accident |

Two pre-existing defects that this work would amplify from "streaming traffic only" to
"all traffic":

- **B1 — reconnect replay of a streamed inference is malformed.** `replaySSEBody`
  (`transport/server.go:429`) emits `data: <body>` for the cached body, but a streamed
  inference's cached body is the `{"events":[…]}` wrapper, not a completion object.
- **B2 — streamed usage is silently zeroed.** `StreamedCompletionResponse.GetUsage`
  (`completionapi/completionresponse.go:205-225`) falls back to
  `PromptTokens: 0, CompletionTokens: len(logprobs)` when no chunk carries `usage`, so
  `MsgFinishInference.InputTokens` becomes `0`.

## Non-goals

- No change to the transport protocol, the `devshard_receipt` / `devshard_meta` envelopes,
  or any chain message.
- No change to what devshardd hashes or stores (deferred to Step 11, explicitly optional).
- No shape-agnostic response cache; stream/non-stream stay separate cache entries.
- No change to `/v1/completions`, embeddings, or rerank paths.

## Design decisions

### D1. Force `stream` upstream in the existing filter pipeline, not ad hoc

`ChatRequestPipeline.Normalize` (`request_filters.go:94`) runs
`RequestFilterStagePreValidation` → message normalization → `ctx.DecodeRequest()` →
output-token limits → `RequestFilterStagePostLimits`. Because `DecodeRequest` populates
`chatRequest` *before* PostLimits, the pipeline already has the exact idiom we need: the
`logprobs` / `top_logprobs` / `return_token_ids` rules force values on the wire at PostLimits
while `chatRequest.Logprobs` keeps the client's ask
(`request_filters_parameters.go:578-583`, and the comment at `request_filters.go:20-29`).

So add two PostLimits rules:

```go
newParameter("stream").
    withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{
        Handler: paramvalidators.ForceLiteralParameter{Value: true},
    }),
newParameter("stream_options").
    withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
        Validator: paramvalidators.StreamOptionsValidator{},
    }).
    withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{
        Handler: paramvalidators.ForceLiteralParameter{Value: map[string]any{"include_usage": true}},
    }),
```

`ForceLiteralParameter.Value` is `any` (`paramvalidators/handlers.go:141-154`), so both work.
Two mechanical notes: `stream` currently lives in the bare whitelist group
(`request_filters_parameters.go:664-669`, "register them as known so the whitelist keeps
them") and has to be promoted to its own `newParameter` entry; `stream_options` already has
one at `request_filters_parameters.go:574` and just gains a second rule, which the builder
supports (`top_p` at `657-659` chains two).
Ordering is safe: `StreamOptionsValidator` runs at PreValidation and has already rejected
malformed wrappers and dropped non-whitelisted sub-fields, so overwriting the object at
PostLimits cannot smuggle a client value through. `stream` keeps its existing PreValidation
type check (`request_filters_parameters.go:355`), so `"stream": "yes"` is still a 400.

`include_usage` is forced at the gateway rather than relying on devshardd's
`ModifyRequestBodyWithLogprobsMode` (`common/completionapi/request.go:57-71`, which adds it
when `stream` is true) so that the on-chain prompt hash reflects what actually ran and the
gateway's own `sseChunkUsageCompletionTokens` heuristic (`redundancy.go:80`) is guaranteed a
usage chunk.

### D2. Client intent travels in a `streamClientIntent`, mirroring `logprobClientIntent`

`logprobClientIntent` (`stream_rewrite.go:15-36`) already solves this exact problem for
logprobs: capture the client's ask, carry it in the request context, apply it at the client
boundary. Add the same for streaming:

```go
type streamClientIntent struct {
    wantsStream bool // original request had stream:true
    wantsUsage  bool // original request had stream_options.include_usage:true
}
```

`wantsUsage` needs a new `chatRequest` field (`StreamOptions.IncludeUsage`) decoded in
`readChatRequestFields`. It is only meaningful for a streaming client — PreValidation already
drops `stream_options` when `stream` is not `true`, so a non-streaming client can never carry
one, and the aggregated JSON always includes `usage` the way OpenAI's non-streaming responses
do.

### D3. Decouple "response shape" from "escalation policy"

`InferenceParams.Stream` (`user/session.go:230`) is read for two unrelated purposes: the
client shape branch (`proxy.go:238`) and the redundancy escalation policy
(`redundancy.go:2660`, `2681`, `2345`, `2355`, `3279`). Keep the field meaning "client asked
for SSE" and change the escalation sites to the unconditional streaming policy, because the
upstream is now always streaming. Gate that flip behind the same rollout setting as D5 so it
is revertible independently of the aggregation work.

### D4. A real aggregator, with a passthrough escape hatch

`assembleSSEChunks` is only correct because a non-streaming upstream produces exactly one
data event. Once the upstream streams, "last data line" returns the final `include_usage`
chunk (empty `choices`). The replacement must:

- group by `choices[].index` (`n > 1` is accepted — `chatRequest.N` exists),
- concatenate `delta.content`, `delta.reasoning_content`, `delta.refusal`,
- take the first non-empty `delta.role`, defaulting to `assistant`,
- merge `delta.tool_calls` by their `index`: first non-empty `id` / `type` /
  `function.name`, concatenated `function.arguments` fragments,
- concatenate per-chunk `choices[].logprobs.content[]` into one `message`-level array,
- keep the last non-null `finish_reason` and `stop_reason`,
- take `usage` from the last event that carries a non-null one,
- take `id` / `created` / `model` / `system_fingerprint` from the first event,
- emit `object: "chat.completion"` with `message` instead of `delta`.

It must also **pass through unchanged** any stream that is already a single
`chat.completion` (a `"message"`-shaped event). That covers an old host, a
`replaySSEBody` cache replay, and the `InProcessClient` synthetic stream
(`user/session.go:163-208`) — the same compatibility direction that
`rewriteStreamingPayload` covers for streaming clients (`stream_rewrite.go:80`).

Host error payloads keep flowing through untouched so `jsonErrorPayloadDetails`
(`proxy.go:569`) still recognizes them.

### D5. Rollout behind one gateway setting

Add `ForceUpstreamStreaming bool` to `RedundancySettings` (`redundancy.go:428`), persisted
and admin-settable like the other knobs (`gateway.go:2488`, `3018`). It gates D1's forcing,
D3's escalation flip, and the usage-chunk suppression in Step 8 together, so a single toggle
reverts the whole behavior change without a redeploy. The aggregator itself (Steps 3–4) ships
unconditionally, because it is strictly more correct than `assembleSSEChunks` on both inputs.

### D6. Cache-key isolation must become explicit

Forcing `stream: true` into the normalized body makes the bodies of a streaming and a
non-streaming client identical, so `chatCacheKey` (`response_cache.go:98`) would collapse
them into one entry and serve an SSE body to a JSON client. The key must take the client
intent as a separate input. `chatRequestStream(body)` (`request_capture.go:392`) has the same
problem — after forcing it always returns `true` — and its callers (cache logging at
`gateway.go:1425`, forensic capture at `request_capture.go:191`) must read intent instead.

### D7. Storage growth is accepted, not solved here

Every inference's stored payload becomes `{"events":[…]}`: one JSON-escaped SSE line per
token, each repeating `id` / `object` / `created` / `model` plus a choice wrapper, on top of
the forced `logprobs` with `top_logprobs: 5` and `return_token_ids`. Expect roughly
1.5–2× the bytes of the equivalent single JSON. This is a real cost of the change and is
measured in Step 10, with the optional Step 11 as the fix.

---

## Step-by-step implementation plan

Steps 1 and 2 are independent bug fixes worth shipping on their own. Steps 3–9 are the
change proper. Steps 10–11 are rollout and optional follow-up.

### Step 1 — Make streamed `usage` mandatory (fixes B2)

`devshard/cmd/devshardd/inference/execute.go`, `common/completionapi/completionresponse.go`.

1. In `processExecutionHTTPResponse`, after `completionResp.GetUsage()`, reject a usage
   object with `PromptTokens == 0` on a non-empty completion instead of publishing
   `MsgFinishInference` with `InputTokens: 0`.
2. Add an explicit sentinel in `completionapi` (e.g. `ErrStreamedUsageMissing`) so the
   `GetUsage` fallback at `completionresponse.go:217` becomes opt-in rather than silent;
   keep the old lenient behavior behind an explicit call used only by non-critical readers.
3. Emit a counter (`devshard_inference_missing_usage_total`) and an error log with the
   inference ID before failing, so a vLLM version that drops the usage chunk is visible
   rather than quietly under-billing.

Tests: unit test in `completionapi` for a streamed response with and without a usage chunk;
devshardd unit test that an execution whose stream carries no usage does not publish a finish
message with zero input tokens.

### Step 2 — Fix reconnect replay of streamed payloads (fixes B1)

`devshard/transport/server.go`.

1. In `replaySSEBody`, detect the `{"events":[…]}` envelope
   (`SerializedStreamedResponse`) and replay each stored event as its own `data:` line,
   followed by `[DONE]`. A body that is not the envelope keeps today's single-event
   behavior.
2. Do not re-derive or re-hash anything; this is purely a wire-format fix at replay time.

Tests: `transport/server_test.go` — reconnect to a completed streamed inference and assert
the gateway-side parse produces the same chunk sequence as the live stream; reconnect to a
completed JSON inference and assert the single-event behavior is unchanged.

### Step 3 — Add the SSE aggregator (no wiring yet)

New `devshard/cmd/devshardctl/stream_aggregate.go`.

1. `func aggregateSSEStream(raw []byte) []byte` implementing D4.
2. Keep it allocation-conscious but not clever: this runs once per non-streaming request on a
   fully buffered body, not in the streaming hot path.
3. Preserve the existing failure contract: an input with no usable data event returns the
   same `{"error":{"message":"no response data"}}` body that `assembleSSEChunks` returns, so
   the caller's error handling is unchanged.

Tests: `stream_aggregate_test.go` covering multi-chunk content, `reasoning_content`,
`refusal`, streamed `tool_calls` argument fragments across chunks, `n: 2` interleaved
indices, logprob concatenation, `finish_reason` / `stop_reason`, the trailing usage chunk,
single-`chat.completion` passthrough, `{"events":[…]}`-shaped input, host error payload
passthrough, and a truncated stream with no `[DONE]`.

### Step 4 — Wire the aggregator into the non-streaming path

`devshard/cmd/devshardctl/proxy.go`.

1. `handleNonStreaming` calls `aggregateSSEStream` instead of `assembleSSEChunks`; the
   `filterClientInternalFields` + `jsonErrorPayloadDetails` + `writeJSONPayload` sequence
   stays exactly as it is (`proxy.go:564-575`).
2. Delete `assembleSSEChunks` once no caller remains.

At this point behavior is unchanged for real traffic (the upstream is still non-streaming, so
the aggregator takes its passthrough branch), but the gateway is ready for streamed input.

Tests: existing `TestE2E_NonStreamingHappyPath`, `TestE2E_NonStreamingChatCompletionShape`,
`TestE2E_NonStreamingCacheHitKeepsJSONShape` and
`non_streaming_corner_cases_test.go` must pass with no edits — that is the signal that the
aggregator is a faithful superset.

### Step 5 — Capture client stream intent

`devshard/cmd/devshardctl/request_filters.go`, `request_filters_parameters.go`,
`stream_rewrite.go` (or a new `stream_intent.go`), `proxy.go`.

1. Add `StreamOptions struct{ IncludeUsage bool }` to `chatRequest` and decode it in
   `readChatRequestFields` next to the existing `stream` read.
2. Add `streamClientIntent`, `streamClientIntentFromRequest`, and the
   `withStreamClientIntent` / `streamClientIntentFromContext` pair, following
   `logprobClientIntent` (`stream_rewrite.go:27-36`).
3. In `handleChatCompletions`, attach it to the request context on the same line where the
   logprob intent is attached (`proxy.go:236`), and branch on the intent rather than
   `req.Stream` at `proxy.go:238`.
4. Rename the handler to `handleAggregated` (or keep the name with a comment) so the code
   stops implying that the upstream is non-streaming.

Tests: `request_filters_test.go` for the new decode; a proxy unit test that the intent
survives to the handler branch.

### Step 6 — Make cache keying and capture read intent, not the body

`devshard/cmd/devshardctl/response_cache.go`, `request_capture.go`, `gateway.go`.

1. `chatCacheKey(model string, body []byte, clientStream bool)` — mix the client shape into
   the digest.
2. Replace `chatRequestStream(body)` with the decoded intent at its call sites
   (`gateway.go:1425-1432`, `request_capture.go:191/212/237`); delete the helper.
3. `cachedChatResponse.Stream` keeps driving replay headers (`response_cache.go:181-198`)
   with no change.

Do this **before** Step 7. Landing Step 7 first would collapse the two cache entries and
serve an SSE body to a JSON client.

Tests: `TestE2E_StreamingThenNonStreamingCacheIsolation` and
`TestE2E_NonStreamingThenStreamingCacheIsolation` must still pass; add a unit test that two
requests differing only in client `stream` produce different cache keys even when their
normalized bodies are byte-identical.

### Step 7 — Force `stream: true` + `include_usage` upstream, behind the setting

`devshard/cmd/devshardctl/request_filters_parameters.go`, `redundancy.go`, `gateway.go`.

1. Add `ForceUpstreamStreaming` to `RedundancySettings` / `DefaultRedundancySettings`
   (default **false**), plumb it through `ApplyRedundancySettings` and the admin endpoint.
2. Add the two PostLimits rules from D1, active only when the setting is on. The pipeline is
   constructed per-normalize call (`defaultChatRequestPipeline()`, `request_filters.go:85`),
   so the flag can be read there rather than baked into the package-level catalog.
3. Log the effective upstream stream value in `proxy_request_started`
   (`proxy.go:233`) alongside the client value so the two are distinguishable in logs.

Now `stream: false` clients get a real streamed upstream and the Step 3 aggregator does real
work. Note that the on-chain prompt hash (`CanonicalPromptHash`, `user/session.go:815`) now
covers `"stream": true`; that is consistent for new inferences, needs no migration, and does
not affect validation, which forces `stream: false` on replay
(`common/validation/validation.go:345`).

Tests: pipeline test asserting the forced body; an integration test with the mock ML node
(`testenv/mockopenai/server.go:59` already branches on `req.Stream`) asserting a
`stream: false` client receives a correct `chat.completion` while the mock saw a streaming
request; a differential test asserting the aggregated JSON for a fixed seed matches the
mock's own non-streaming JSON field-for-field.

### Step 8 — Suppress the forced usage chunk for streaming clients

`devshard/cmd/devshardctl/stream_rewrite.go`, `proxy.go`.

A streaming client that did not send `stream_options: {include_usage: true}` must not
suddenly receive a trailing usage-only chunk, because we forced `include_usage` upstream.
This is the same class of leak that `filterClientInternalFields` already handles for forced
logprobs.

1. In the streaming write path, drop a `chat.completion.chunk` event whose `choices` is empty
   and whose only payload is `usage`, when `streamClientIntent.wantsUsage` is false.
2. Keep it in the hot-path-cheap style of `rewriteStreamingPayload` (`stream_rewrite.go:80`):
   a cheap byte check first, full parse only on a candidate event.

Tests: streaming client without `include_usage` sees no usage chunk; with `include_usage`
sees exactly one; the aggregated JSON path always carries `usage` regardless.

### Step 9 — Unify escalation policy on the streaming timeouts

`devshard/cmd/devshardctl/redundancy.go`.

When `ForceUpstreamStreaming` is on, every attempt is a streamed attempt, so:

1. `escalationForInflight` (`redundancy.go:2660`, `2681`) stops short-circuiting on
   `!params.Stream`; first-token escalation and `attempt_failed` escalation apply to all
   requests.
2. Stop arming the 140s reduced-`max_tokens` timer (`redundancy.go:2345`, `2484`) and the
   30-minute no-content timer (`redundancy.go:2355`, `2500`) — the first-token budget plus
   `InterChunkStall` now cover the same failure modes far faster. Keep
   `reducedMaxTokensParams` (`redundancy.go:2040`) in the tree for one release in case the
   flag is reverted.
3. Revisit `longNonStreamEmptyFailureExempt` (`redundancy.go:3279`): with a real stream the
   "long empty non-stream" shape it exempts should no longer occur, and keeping it would
   mask genuinely empty streams from the perf tracker.
4. While here, either wire the configured `InterChunkStallTimeout` (default 60s,
   `redundancy.go:428`) into the stall logic or delete the setting — it is currently set but
   unused, with a hardcoded 30s threshold used instead. Do it as a separate commit; it is a
   pre-existing inconsistency, not part of this change.

Tests: a redundancy unit test that a `stream: false` client request against a host that never
sends a first token escalates within the first-token budget rather than at 140s; assert no
test still depends on the reduced-`max_tokens` path when the flag is on.

### Step 10 — Enable by default, soak, dashboards

1. Flip `ForceUpstreamStreaming` default to `true`.
2. Confirm on the soak environment:
   - `devshard_gateway_participant_first_content_seconds` and
     `devshard_host_first_token_seconds` are populated for essentially all chat requests,
   - `devshard_gateway_timeout_actions_total` shows no `response_timeout_reduced_max_tokens`
     actions,
   - `devshard_inference_missing_usage_total` stays at zero,
   - p50/p95 end-to-end latency for `stream: false` clients does not regress,
   - payload-store growth rate against the D7 estimate.
3. Update the gateway dashboard so the TTFT panels are no longer implicitly
   streaming-only (`gateway_dashboard_test.go` guards the metric aliases).

### Step 11 — devshardd streaming-only (optional, after Step 10 is stable)

1. Add a devshardd counter for executions whose upstream ML response was **not**
   `text/event-stream` (`inference/execute.go:84-85`). Watch it reach zero.
2. Then normalize or reject a payload arriving with `stream != true`, keeping acceptance for
   one deprecation window so an older gateway is not broken by a host upgrade.
3. Only after that, consider deleting the JSON-upstream branches in
   `inference/execute.go` / `inference/proxy.go`.

**Separately optional, and must not be mixed with any step above:** have devshardd store and
hash an *aggregated* canonical completion rather than the `{"events":[…]}` envelope, which
would undo the D7 storage growth. This changes `ResponseHash` inputs, so it needs its own
proposal, its own rollout, and a compatibility story for
`NewCompletionResponseFromLinesFromResponsePayload` (`completionresponse.go:339`) reading
older payloads.

---

## Testing plan

| Area | Test | Where |
|---|---|---|
| Aggregation fidelity | Multi-chunk content, reasoning, refusal, tool-call fragments, `n: 2`, logprobs, finish/stop reason, usage chunk | `cmd/devshardctl/stream_aggregate_test.go` (new) |
| Aggregation compatibility | Single `chat.completion` passthrough, `{"events":[…]}` input, error payload passthrough, truncated stream | same |
| Differential | Aggregated JSON vs mock's own non-streaming JSON, same seed, field-for-field | `testenv/citest` |
| Client shape | `stream: false` client + streamed upstream → `application/json`; `stream: true` unchanged | `e2e/non_streaming_test.go`, `e2e/streaming_test.go` (existing, unedited) |
| Cache isolation | Identical normalized bodies, different client intent → different keys | `cmd/devshardctl/response_cache_test.go` |
| Usage leak | Streaming client with/without `include_usage` | `cmd/devshardctl/proxy_test.go` |
| Escalation | Non-streaming client, dead host → first-token escalation, not 140s | `cmd/devshardctl/redundancy_*_test.go` |
| Reconnect replay | Streamed inference replay parses as a normal stream | `transport/server_test.go` |
| Usage strictness | Streamed response without usage does not finish with zero input tokens | `completionapi`, devshardd inference tests |
| Corner cases | Malformed JSON, host down, post-finalize (existing) | `e2e/non_streaming_corner_cases_test.go` |

Run with the workspace Go cache prefix:

```bash
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test ./devshard/cmd/devshardctl/... ./devshard/transport/... ./common/completionapi/...
```

## Rollout & risks

| Risk | Mitigation |
|---|---|
| Aggregated JSON differs subtly from vLLM's own non-streaming output (tool calls, `stop_reason`, unusual fields) | Differential test in Step 7; single revertible flag; manual diff against a real vLLM node before Step 10 |
| Token accounting now always depends on the final usage chunk | Step 1 makes a missing usage chunk a hard failure with a counter, landed before the flip |
| Payload-store growth (D7) | Measured in Step 10; Step 11's optional aggregation is the fix |
| Larger upstream traffic from per-chunk forced logprobs / `top_logprobs: 5` | Measured in Step 10; the alternative is reducing `TopLogprobsForcedValue`, which is a validation decision, not a streaming one |
| Escalation flip makes the gateway more aggressive on previously-quiet requests | Step 9 is behind the same flag; watch `devshard_gateway_timeout_actions_total` and per-participant attempt counts during soak |
| Cache-entry collapse serving the wrong content type | Step 6 lands before Step 7; guarded by the two existing E2E isolation tests |
| Older devshardd hosts | No protocol change; the aggregator's passthrough branch handles a host that still answers with a single `chat.completion` |

## Task checklist

- [ ] Step 1 — streamed `usage` mandatory, counter + tests
- [ ] Step 2 — `replaySSEBody` unwraps `{"events":[…]}`
- [ ] Step 3 — `aggregateSSEStream` + unit tests
- [ ] Step 4 — wire into `handleNonStreaming`, drop `assembleSSEChunks`
- [ ] Step 5 — `streamClientIntent` + `chatRequest.StreamOptions`
- [ ] Step 6 — cache key and capture read intent
- [ ] Step 7 — force `stream` / `stream_options` upstream behind `ForceUpstreamStreaming`
- [ ] Step 8 — suppress forced usage chunk for streaming clients
- [ ] Step 9 — unify escalation on streaming timeouts (+ separate `InterChunkStallTimeout` cleanup)
- [ ] Step 10 — default on, soak, dashboards
- [ ] Step 11 — devshardd streaming-only deprecation (optional)

## Related

- [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md) — the proposal
- [proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md) —
  joining an in-flight stream; benefits from a single always-streaming upstream path
- [proposals/chat-cache-attribution.md](./proposals/chat-cache-attribution.md) — response
  cache attribution, touches the same cache keyed in Step 6
