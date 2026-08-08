# Proposal: Always stream between gateway and devshardd

**Status:** Draft / proposal
**Scope:** `devshardctl` request filters + proxy response path + redundancy escalation policy; `devshardd` streamed-usage strictness and reconnect replay. No protocol/message-format change, no chain change.
**Plan:** [../gateway-always-stream-upstream-plan.md](../gateway-always-stream-upstream-plan.md)

This is a design note; the step-by-step implementation lives in the linked plan.

---

## Problem

The OpenAI API lets a client choose `stream: true` or `stream: false`. Today the gateway
forwards that choice verbatim all the way to the ML node: `chatRequest.Stream` is parsed in
`request_filters_parameters.go:355`, `stream` is registered as a pass-through vLLM
parameter (`request_filters_parameters.go:668`), and the normalized body — including
`"stream": false` — becomes `InferenceParams.Prompt`, which devshardd hands to vLLM.

The gateway↔devshardd hop is already SSE-framed in both cases (`transport/server.go:343`
always sets `text/event-stream`), so `stream: false` does not simplify the transport. It only
degrades it: vLLM buffers the whole completion, devshardd emits it as one
`data: {…}\n\ndata: [DONE]\n\n` event (`inference/execute.go:106`), and the gateway learns
nothing about the request until the very end. Concretely:

- **No liveness signal.** A non-streaming attempt is indistinguishable from a hung host
  until it completes. `escalationForInflight` explicitly disables first-token escalation for
  these requests (`redundancy.go:2681`) and falls back to a hardcoded 140s
  reduced-`max_tokens` retry (`redundancy.go:40`, `2484`) plus a 30-minute no-content
  timeout (`redundancy.go:2355`). A dead host burns minutes instead of ~1s.
- **No TTFT.** `devshard_gateway_participant_first_content_seconds` and the CTTFL
  per-input-token gauge are only meaningful when chunks arrive incrementally, so a large
  slice of traffic contributes nothing to the metrics used for participant selection.
- **Worse latency for the client anyway.** Aggregating chunks at the gateway is cheap;
  the client's first byte arrives no later than under `stream: false`, and the speculative
  race can crown a winner as soon as real content appears rather than at the end.
- **Winner selection is weaker.** `sseChunkContentSource` has a special branch for the
  non-streaming `message.content` shape (`redundancy.go:162`); the race can only pick a
  winner once an attempt has fully finished.

Two latent defects make the current split worse than it looks. `replaySSEBody`
(`transport/server.go:429`) wraps a cached body in a single `data:` line, but a streamed
inference is stored as `{"events":[…]}` (`completionapi/responseprocessor.go:82`), so a
reconnect replay of a streamed inference sends a non-OpenAI envelope to the client. And
`StreamedCompletionResponse.GetUsage` silently returns `PromptTokens: 0` when no usage
chunk is present (`completionapi/completionresponse.go:217`), which under-bills
`MsgFinishInference`.

## Proposed change

The gateway should decide the response shape for the **client** and always ask **upstream**
for a stream:

1. **Force streaming upstream.** Add `stream` → `ForceLiteralParameter{Value: true}` and
   `stream_options` → `ForceLiteralParameter{Value: {"include_usage": true}}` at
   `RequestFilterStagePostLimits`, exactly as `logprobs` / `top_logprobs` /
   `return_token_ids` are already forced (`request_filters_parameters.go:578-583`). The
   client's original intent is captured earlier by `ctx.DecodeRequest()`, so it survives.
2. **Aggregate for non-streaming clients.** Replace `assembleSSEChunks`
   (`proxy.go:579`, "take the last `data:` line") with a real aggregator that folds
   `chat.completion.chunk` deltas into one `chat.completion`: per-choice content,
   reasoning, refusal, tool-call argument fragments, concatenated logprobs, terminal
   `finish_reason`, and `usage` from the final chunk.
3. **Carry client stream intent explicitly**, mirroring `logprobClientIntent`
   (`stream_rewrite.go:15`), so the cache key, the request capture, and the decision to
   forward or suppress the final `usage` chunk all read intent rather than the rewritten
   body.
4. **Unify escalation policy** on the streaming timeouts, retiring the non-streaming-only
   140s reduced-`max_tokens` timer and the 30-minute no-content timeout.
5. **Fix the two latent defects** first, since forcing streaming takes them from
   "affects streaming traffic" to "affects all traffic".

Once the gateway never sends `stream: false`, devshardd can drop non-streaming ML support
entirely (deprecation metric first, rejection later).

## Out of scope

- Changing the devshardd↔gateway wire protocol, the `devshard_receipt` / `devshard_meta`
  envelopes, or anything on chain.
- Changing what devshardd stores and hashes. The stored payload becomes `{"events":[…]}`
  for every inference; `ExtractLogits` already handles both shapes and validation forces
  `stream: false` on replay (`common/validation/validation.go:345`). Storing an aggregated
  canonical body instead would change `ResponseHash` inputs and is deferred.
- Shape-agnostic response caching (one cached entry rendered as either SSE or JSON). The
  plan keeps today's stream/non-stream cache isolation.
- `/v1/completions` and the embeddings/rerank surfaces.

## Acceptance sketch

- A client request with `stream: false` produces a byte-comparable `chat.completion` for
  the same seed whether the upstream streamed or not — asserted by a differential test
  against the mock ML node and reviewed against a real vLLM node.
- `devshard_gateway_participant_first_content_seconds` is populated for 100% of chat
  requests; no request reaches the 140s reduced-`max_tokens` path.
- A dead host on a `stream: false` client request is escalated within the first-token
  budget (~1s + per-input-token lag), not after 140s.
- `MsgFinishInference.InputTokens` is non-zero for every finished inference; devshardd
  refuses to finish an inference whose streamed response carries no `usage`.
- Reconnect replay of a streamed inference yields a valid OpenAI stream, and the same
  aggregated JSON for a non-streaming client.
- Existing cache-isolation tests (`TestE2E_StreamingThenNonStreamingCacheIsolation`,
  `TestE2E_NonStreamingThenStreamingCacheIsolation`) still pass unchanged.
