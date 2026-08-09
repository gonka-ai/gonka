# Gateway Always-Stream Upstream — implementation plan

Status: proposal / plan.
Proposal (design only, includes reconnect): [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md)

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
- No change to what devshardd hashes or stores (deferred to Step 14, explicitly optional).
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
D3's escalation flip, and the usage-chunk suppression in Step 10 together, so a single toggle
reverts the whole behavior change without a redeploy. The aggregator itself (Steps 5–6) ships
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
measured in Step 13, with the optional Step 14 as the fix.

---

## Step-by-step implementation plan

Steps 1 and 2 are independent bug fixes worth shipping on their own. Steps 3 and 4 make an
interrupted stream survivable, which matters much more once every request streams. Steps 5–11 are
the change proper. Step 12 is an independent availability fix on the gateway↔host SSE parser
(unbounded line buffering). Steps 13–14 are rollout and optional follow-up. Step 15 is a later
gateway UX/efficiency follow-up: let a duplicate client request join an in-flight stream. Step 16
adds reconnect / winner-continuity OTel spans once the observability e2e branch has landed.

### Step 1 — Make streamed `usage` mandatory (fixes B2) ✅

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

### Step 2 — Fix reconnect replay of streamed payloads (fixes B1) ✅

`devshard/transport/server.go`.

1. In `replaySSEBody`, detect the `{"events":[…]}` envelope
   (`SerializedStreamedResponse`) and replay each stored event as its own `data:` line,
   followed by `[DONE]`. A body that is not the envelope keeps today's single-event
   behavior.
2. Do not re-derive or re-hash anything; this is purely a wire-format fix at replay time.

Tests: `transport/server_test.go` — reconnect to a completed streamed inference and assert
the gateway-side parse produces the same chunk sequence as the live stream; reconnect to a
completed JSON inference and assert the single-event behavior is unchanged.

### Step 3 — Drain the ML stream independently of the client connection ✅

`devshard/cmd/devshardd/inference/proxy.go`, `inference/execute.go`, `devshard/host/host.go`.

Step 2 only fixes the *format* of a replay; it does nothing unless a completed body actually
exists. Today it usually does not, because generation is bound to the gateway's HTTP request:
`HandleInference` passes the Echo request context into `RunExecution`
(`transport/server.go:274-277`, `379-381`), the host hands that same context to the ML POST
(`host/host.go:863` → `inference/engine.go:89`, `NewRequestWithContext`), and
`proxyTextStreamResponse` closes the upstream body on the first client write error
(`inference/proxy.go:86-95`). So a gateway↔host drop aborts vLLM mid-generation,
`completedResponses` (`host/host.go:903`) is never populated, and — after Step 1 — the truncated
stream carries no usage chunk, so no `MsgFinishInference` is published either. The work is paid
for in GPU time and then thrown away.

Note the asymmetry this fixes: the gateway already refuses to bind upstream reads to the end
user's connection (`proxy.go:54-57`, `383-388`) precisely so the protocol can complete. devshardd
should apply the same principle one hop down.

1. Detach execution lifetime from the request context: run the ML call under a context derived
   from the host's own lifecycle, with its own deadline, and treat client cancellation as
   "stop proxying", not "stop generating".
2. In `proxyTextStreamResponse`, on a write error mark the writer dead and stop touching it, but
   keep scanning the ML body through `[DONE]` so `ExecutorResponseProcessor` still accumulates
   every event. Do not close `resp.Body` early.
3. Let the normal tail run on the drained stream: usage validation (Step 1), `ResponseHash`,
   payload store, `MsgFinishInference`, and `completedResponses[inferenceID]` — which is exactly
   what makes a later same-nonce reconnect replayable via Step 2.
4. Bound the orphan work: an absolute drain deadline plus the existing graceful-drain state
   (`SetLifecycleInflight`) so a hung vLLM cannot pin a node indefinitely.
5. Set the `PartialResponse` / `PartialResponseReason` / `PartialResponseWhere` fields on
   `ExecuteResult` (`devshard/types.go:27-29`) when the drain ends without `[DONE]`. They exist
   and are already logged by `RunExecution` (`host/host.go:893-902`) but are never set today.
6. Metrics: a counter for "client detached, drain continued" and one for drain outcome
   (`completed` / `deadline` / `ml_error`), so the retained work is visible.

Tests (unit, landed): a write-failing `ResponseWriter` does not stop event accumulation, and
execution still yields a full body with usage; the drain deadline path terminates and is counted;
a client disconnect no longer cancels the ML request; missing `[DONE]` sets `PartialResponse*`.
Tests (e2e, `testenv/citest`): drop the gateway↔host connection mid-stream (`mockopenai`
`PartialStream` / delay faults plus a killable client), then assert the host published
`MsgFinishInference` with non-zero input tokens and that a same-nonce reconnect returns the
complete replay — covered with reconnect E2E in
[gateway-attempt-reconnect-plan.md](./gateway-attempt-reconnect-plan.md) Step 5.

### Step 4 — Reconnect the interrupted attempt before escalating to a new nonce

Separate plan: [gateway-attempt-reconnect-plan.md](./gateway-attempt-reconnect-plan.md).

With Steps 2 and 3 in place a broken attempt is *resumable*, but the gateway still throws it away:
a mid-stream failure either escalates straight to a **new nonce on a different host**
(`startAdditionalInflight`, `redundancy.go:1990`) — a completely independent generation — or, if
the attempt had already streamed content, fails the request outright with
`winner_failed_after_content` (`redundancy.go:2448`) while the secondary's tokens are suppressed
and never reach the user.

Step 4 makes that path fault-tolerant: try a **same-nonce reconnect to the same host** first,
escalate only after a bounded budget (default 1s), and keep the original attempt as the winner
while it can still be resumed — continuing from the delivered prefix rather than restarting the
text. The linked plan carries the design decisions, the protocol hazards (duplicate
`MsgConfirmStart`, double `ProcessResponse`), the settings, and its own unit/e2e test matrix.

**Protocol gate:** the reconnect code ships in the **gateway-v4** binary, but is applied only to
sessions bound to protocol **≥ `v5`**. On ≤v4 escrows (hosts that abort ML on disconnect and lack
Steps 2–3), the gateway keeps today's escalate-to-new-nonce behavior. See R0 in the linked plan.

This step is independent of the `ForceUpstreamStreaming` flag, but it becomes materially more
valuable once every request streams: with a streamed upstream, every request has a delivered
prefix worth preserving.

### Step 5 — Add the SSE aggregator (no wiring yet)

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

### Step 6 — Wire the aggregator into the non-streaming path

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

### Step 7 — Capture client stream intent

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

### Step 8 — Make cache keying and capture read intent, not the body

`devshard/cmd/devshardctl/response_cache.go`, `request_capture.go`, `gateway.go`.

1. `chatCacheKey(model string, body []byte, clientStream bool)` — mix the client shape into
   the digest.
2. Replace `chatRequestStream(body)` with the decoded intent at its call sites
   (`gateway.go:1425-1432`, `request_capture.go:191/212/237`); delete the helper.
3. `cachedChatResponse.Stream` keeps driving replay headers (`response_cache.go:181-198`)
   with no change.

Do this **before** Step 9. Landing Step 9 first would collapse the two cache entries and
serve an SSE body to a JSON client.

Tests: `TestE2E_StreamingThenNonStreamingCacheIsolation` and
`TestE2E_NonStreamingThenStreamingCacheIsolation` must still pass; add a unit test that two
requests differing only in client `stream` produce different cache keys even when their
normalized bodies are byte-identical.

### Step 9 — Force `stream: true` + `include_usage` upstream, behind the setting

`devshard/cmd/devshardctl/request_filters_parameters.go`, `redundancy.go`, `gateway.go`.

1. Add `ForceUpstreamStreaming` to `RedundancySettings` / `DefaultRedundancySettings`
   (default **false**), plumb it through `ApplyRedundancySettings` and the admin endpoint.
2. Add the two PostLimits rules from D1, active only when the setting is on. The pipeline is
   constructed per-normalize call (`defaultChatRequestPipeline()`, `request_filters.go:85`),
   so the flag can be read there rather than baked into the package-level catalog.
3. Log the effective upstream stream value in `proxy_request_started`
   (`proxy.go:233`) alongside the client value so the two are distinguishable in logs.

Now `stream: false` clients get a real streamed upstream and the Step 5 aggregator does real
work. Note that the on-chain prompt hash (`CanonicalPromptHash`, `user/session.go:815`) now
covers `"stream": true`; that is consistent for new inferences, needs no migration, and does
not affect validation, which forces `stream: false` on replay
(`common/validation/validation.go:345`).

Tests: pipeline test asserting the forced body; an integration test with the mock ML node
(`testenv/mockopenai/server.go:59` already branches on `req.Stream`) asserting a
`stream: false` client receives a correct `chat.completion` while the mock saw a streaming
request; a differential test asserting the aggregated JSON for a fixed seed matches the
mock's own non-streaming JSON field-for-field.

### Step 10 — Suppress the forced usage chunk for streaming clients

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

### Step 11 — Unify escalation policy on the streaming timeouts

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

### Step 12 — Cap gateway SSE event size (unbounded `ReadBytes` DoS)

`devshard/transport/client.go` (`HTTPClient.parseSSEResponse`).

A selected executor can return HTTP 200 + `Content-Type: text/event-stream`, begin a line with
`data: `, and then stream attacker-controlled bytes **without ever sending `\n`**, `[DONE]`, or
`devshard_receipt`. Today's parser does:

```go
br := bufio.NewReaderSize(r, 64<<10)
raw, readErr := br.ReadBytes('\n')
```

The 64 KiB reader size is only the internal buffer; `ReadBytes` grows the returned slice until
it sees the delimiter (the file comment already notes "ReadBytes is bounded only by memory",
`client.go:286-290`). The allocation can grow for up to the inference timeout (default tens of
minutes), and concurrent inferences against the same malicious participant amplify it into a
gateway OOM. PR #1240 capped the *gateway* `raceWriter` classify reassembly
(`redundancy.go:1313+`); it does **not** bound this transport-layer read, which is the root cause.

This is independent of `ForceUpstreamStreaming`, but always-stream makes every chat request hit
the SSE path between gateway and host, so the blast radius grows with Step 13's default-on.

1. Introduce an explicit max SSE event / line size (default **1 MiB**, matching the historical
   `bufio.Scanner` cap this code deliberately left and the per-attempt classify cap from
   PR #1240). Make it configurable via env / `ClientConfig` if useful, but ship a hard default —
   do not leave it unbounded behind a unset-zero.
2. Replace the unbounded `ReadBytes('\n')` with a bounded read that aborts as soon as the
   accumulated line exceeds the limit (e.g. loop on `br.ReadSlice('\n')` / incremental reads,
   or check `len(raw)` and return a typed error such as `ErrSSEEventTooLarge` without retaining
   the oversized buffer). **Do not silently truncate** — that is why Scanner was abandoned;
   truncate-and-continue would mis-parse protocol events.
3. On oversize: close/cancel the upstream body, return a clear error to `Send` so redundancy
   records an attempt failure (`eof_transport` / a dedicated reason like `sse_event_too_large`)
   and can escalate to another host. Count
   `devshard_gateway_sse_event_too_large_total{participant_key}` (or reuse
   `participant_transport_errors_total` with a distinct reason).
4. Keep legitimate large chunks working under the cap: a single chat-completion chunk with
   forced logprobs / `top_logprobs: 5` / `return_token_ids` must stay well below 1 MiB; add a
   unit fixture at the high end of a real chunk to lock that in. If soak ever hits false
   positives, raise the limit — do not remove it.
5. Optional follow-up (same PR or a sibling): bound the JSON compatibility `io.ReadAll` path in
   `HTTPClient.Send` (`client.go:272`) with the same budget. Out of the critical path for this
   report, but the same participant could abuse it if any non-SSE response is still accepted.

Tests (`transport/client_test.go`):
- regression: a stream that sends `data: ` + >limit bytes without `\n` aborts with
  `ErrSSEEventTooLarge` after at most `limit+ε` bytes buffered (not the full attacker payload);
- a well-formed stream with a near-limit but legal event still parses receipt / `[DONE]`;
- oversize mid-stream after a valid receipt still fails the send (no silent success);
- existing `parseSSEResponse` tests keep passing.

This step can land before or after Steps 5–11; it should land **before** Step 13's default-on
flip so the soak does not widen the attack window.

### Step 13 — Enable by default, soak, dashboards

1. Flip `ForceUpstreamStreaming` default to `true`.
2. Confirm on the soak environment:
   - `devshard_gateway_participant_first_content_seconds` and
     `devshard_host_first_token_seconds` are populated for essentially all chat requests,
   - `devshard_gateway_timeout_actions_total` shows no `response_timeout_reduced_max_tokens`
     actions,
   - `devshard_inference_missing_usage_total` stays at zero,
   - `devshard_gateway_sse_event_too_large_total` stays at zero outside intentional fault tests,
   - p50/p95 end-to-end latency for `stream: false` clients does not regress,
   - payload-store growth rate against the D7 estimate.
3. Update the gateway dashboard so the TTFT panels are no longer implicitly
   streaming-only (`gateway_dashboard_test.go` guards the metric aliases).

### Step 14 — devshardd streaming-only (optional, after Step 13 is stable)

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

### Step 15 — In-flight chat stream join (optional, after Step 13)

Proposal: [proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md).

Today the gateway chat cache is **post-completion only**: `chatCache.Set` runs after
`ServeHTTP` returns (`gateway.go:1461-1465`). While request A is still streaming, an identical
request B (same `chatCacheKey`) always misses and starts a **second** ML path. A completed-cache
hit dumps the entire body at once (`serveCachedChatResponse`, `response_cache.go:176-198`) —
there is no “replay prefix, then follow the live tail.”

Step 15 closes that gap for concurrent / retried identical clients:

1. Keep an **in-flight registry** keyed by the same cache key as Step 8
   (`chatCacheKey(model, body, clientStream)`), so stream and non-stream shapes stay isolated.
2. First miss creates a `liveStream` (capture buffer + subscribers + completion). Later same-key
   requests **join**: replay bytes already flushed to subscribers, then follow the live tail —
   without a second `RunInference` / host execution.
3. On completion, promote the full body into the normal TTL `chatResponseCache` so a third
   identical request hits today's completed-entry path.
4. Non-stream (`stream: false`): coalesce by waiting for the primary and serving one aggregated
   JSON (no tail).
5. Define disconnect policy when the primary client leaves but joiners remain (keep draining via
   existing meta-drain vs cancel when no subscribers). Attribution for joiners follows
   [chat-cache-attribution.md](./proposals/chat-cache-attribution.md) (joining request's escrow /
   request ids, not the primary's).

Depends on Steps 5–8 (aggregator + intent-aware cache key) and benefits from always-stream
upstream (one streamed shape to fan out). Distinct from Step 4 (same-nonce **gateway↔host**
reconnect for one request) and from client-facing `Last-Event-ID` resume
([stream-resume-pre-proposal.md](./stream-resume-pre-proposal.md)).

Tests / acceptance (from the proposal): two overlapping identical streaming requests → one ML
generation; the second client gets already-sent prefix + live tail; after completion a third hit
uses the normal cache; joiners do not inherit another request's accounting.

### Step 16 — Reconnect / winner-continuity observability spans

**Prerequisite:** merge of branch `ak/devshard-observability-e2e` (and any follow-ups it depends
on). That branch introduces the gateway attempt span tree used here:

- `gateway.attempt` (`StartGatewayAttempt`)
- phase children `attempt.dispatch` → `attempt.prefill` → `attempt.stream`
  (`attempt_spans.go`, `observability/gateway_attempt.go`)
- stall events on the innermost open phase (`AddStallDetected` / `AddStallRecovered`)

Do **not** invent a parallel tracing stack. Extend that model after Step 4's reconnect ladder
exists, so soak can measure whether same-nonce resume is winning vs escalating to a new nonce.

Instrument the reconnect / winner-continuity path with the following (names illustrative; keep
them in `devshard/observability` next to the existing `SpanNameAttempt*` constants and cover them
in `attrs_contract_test.go` / dashboard lint):

1. **Reconnects** — open a child span `attempt.reconnect` under the interrupted
   `gateway.attempt` for each same-nonce resend try. Attributes: try index, drop reason
   (`eof_transport` / …), `delivered_events` / `delivered_partial` at drop, protocol version,
   result (`resumed` / `budget_expired` / `receipt_only` / `error` / `skipped_protocol`). End the
   span when that try finishes (resume starts forwarding, or the try fails).
2. **Time to first byte after reconnect** — on the reconnect span (or a linked histogram
   `devshard_gateway_attempt_reconnect_ttfb_seconds`), record the duration from reconnect send
   start until the first byte of the resumed upstream stream is observed (including skipped
   prefix bytes that are read but not forwarded).
3. **Time to first *new* chunk after reconnect** — duration from reconnect send start until the
   first upstream event **past** the delivered offset is forwarded to the client (the first
   byte the user would not have already seen). Distinct from (2) when the host replays from
   event 0 and the gateway skips `deliveredEvents`.
4. **Switch to another nonce attempt** — when the reconnect reservation is released and a
   **different** nonce crowns or is started after the reconnect budget
   (`startAdditionalInflight` / secondary crown), emit a clear signal on the parent
   `gateway.request` / original `gateway.attempt`: either a span event
   `attempt.winner_switched` or a short child span `attempt.failover`, with
   `from_nonce`, `to_nonce`, `reason` (`reconnect_budget_expired` / `reconnect_failed` /
   `stream_reset`), and whether any content had already been delivered to the client.

Also keep the Prometheus counters from the reconnect plan's Step 6
(`devshard_gateway_attempt_reconnect_total`, `winner_continuity_total`, …) labeled so they can be
joined to these spans in Jaeger / Grafana.

Tests: unit tests with an OTel span recorder (same style as `attempt_span_test.go` on the
observability branch) asserting the four signals fire on resume, on budget-expired failover, and
do **not** fire for ≤v4 / `skipped_protocol` paths. Extend the gateway observability dashboard
panels once the spans exist.

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
| ML drain | Dead client writer does not stop event accumulation; drain deadline is bounded and counted | `cmd/devshardd/inference/*_test.go` |
| ML drain (e2e) | Mid-stream disconnect still publishes finish with non-zero tokens; reconnect replays in full | `testenv/citest` |
| Attempt reconnect | Same-nonce resume, winner continuity, fallback after budget | [gateway-attempt-reconnect-plan.md](./gateway-attempt-reconnect-plan.md) |
| SSE event size cap | Oversize no-newline stream aborts under limit; legal near-limit event still parses | `transport/client_test.go` |
| In-flight join | Concurrent identical streams share one ML path; joiner gets prefix + live tail | [proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md) |
| Reconnect spans | `attempt.reconnect`, TTFB, first-new-chunk, winner-switched; skipped on ≤v4 | `cmd/devshardctl/attempt_span_test.go` (after observability e2e merge) |
| Corner cases | Malformed JSON, host down, post-finalize (existing) | `e2e/non_streaming_corner_cases_test.go` |

Run with the workspace Go cache prefix:

```bash
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test ./devshard/cmd/devshardctl/... ./devshard/transport/... ./common/completionapi/...
```

## Rollout & risks

| Risk | Mitigation |
|---|---|
| Aggregated JSON differs subtly from vLLM's own non-streaming output (tool calls, `stop_reason`, unusual fields) | Differential test in Step 9; single revertible flag; manual diff against a real vLLM node before Step 13 |
| Token accounting now always depends on the final usage chunk | Step 1 makes a missing usage chunk a hard failure with a counter, landed before the flip |
| Payload-store growth (D7) | Measured in Step 13; Step 14's optional aggregation is the fix |
| Larger upstream traffic from per-chunk forced logprobs / `top_logprobs: 5` | Measured in Step 13; the alternative is reducing `TopLogprobsForcedValue`, which is a validation decision, not a streaming one |
| Escalation flip makes the gateway more aggressive on previously-quiet requests | Step 11 is behind the same flag; watch `devshard_gateway_timeout_actions_total` and per-participant attempt counts during soak |
| Cache-entry collapse serving the wrong content type | Step 8 lands before Step 9; guarded by the two existing E2E isolation tests |
| Step 3's drain keeps ML work running after a host loses its client | Absolute drain deadline plus graceful-drain state; counted so retained work is visible |
| Malicious executor grows unbounded SSE lines in `parseSSEResponse` | Step 12 hard-caps event size (default 1 MiB), aborts with typed error, counters; land before Step 13 default-on |
| In-flight join fans out a failed primary or wrong escrow attribution | Step 15: explicit disconnect policy + H6 attribution tests; optional until after Step 13 soak |
| Reconnect span work races the observability e2e branch | Step 16 waits for `ak/devshard-observability-e2e` merge; reuse its attempt/phase APIs only |
| Older devshardd hosts | No protocol change; the aggregator's passthrough branch handles a host that still answers with a single `chat.completion` |

## Task checklist

- [x] Step 1 — streamed `usage` mandatory, counter + tests
- [x] Step 2 — `replaySSEBody` unwraps `{"events":[…]}`
- [x] Step 3 — devshardd drains ML independently of the client connection
- [ ] Step 4 — same-nonce reconnect before escalation, gated to protocol ≥ v5 ([separate plan](./gateway-attempt-reconnect-plan.md); host Steps 1–2 landed)
- [ ] Step 5 — `aggregateSSEStream` + unit tests
- [ ] Step 6 — wire into `handleNonStreaming`, drop `assembleSSEChunks`
- [ ] Step 7 — `streamClientIntent` + `chatRequest.StreamOptions`
- [ ] Step 8 — cache key and capture read intent
- [ ] Step 9 — force `stream` / `stream_options` upstream behind `ForceUpstreamStreaming`
- [ ] Step 10 — suppress forced usage chunk for streaming clients
- [ ] Step 11 — unify escalation on streaming timeouts (+ separate `InterChunkStallTimeout` cleanup)
- [ ] Step 12 — cap gateway SSE event size (`parseSSEResponse` unbounded `ReadBytes`)
- [ ] Step 13 — default on, soak, dashboards
- [ ] Step 14 — devshardd streaming-only deprecation (optional)
- [ ] Step 15 — in-flight chat stream join (optional; [proposal](./proposals/chat-stream-inflight-join.md))
- [ ] Step 16 — reconnect / winner-continuity OTel spans (after `ak/devshard-observability-e2e` merge)

## Related

- [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md) — the proposal
- [gateway-attempt-reconnect-plan.md](./gateway-attempt-reconnect-plan.md) — Step 4's reconnect and
  winner-continuity design, which builds on Steps 2 and 3; parent Step 16 feeds that plan's
  Step 6 OTel work (after its Step 5 E2E)
- [stream-resume-pre-proposal.md](./stream-resume-pre-proposal.md) — why client-facing stream resume
  is a separate, larger problem than Steps 2–4
- [proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md) — Step 15:
  duplicate clients join an in-flight stream (prefix + live tail)
- [proposals/chat-cache-attribution.md](./proposals/chat-cache-attribution.md) — response
  cache attribution, touches the same cache keyed in Step 8 / Step 15
- Branch `ak/devshard-observability-e2e` — gateway.attempt / phase spans that Step 16 extends
