# Proposal: empty-stream evidence, signed checkpoints and frame coalescing

**Status:** Draft for discussion. Updated after #2000's design was rewritten to a gateway-started challenge protocol (vitaly-andr, 2026-10-20).
**Issues:** #1808 (per-chunk stream signatures), #1841 (stream chunk signatures and empty stream detection) — milestone Devshard v7
**Consumer:** #2000, whose PR 1 now contains the empty-stream verdict
**Base:** `devshard-0.2.x-v6` @ `3c924cbf6` (line numbers below are at this commit)

## Summary

Item 3 was "sign stream chunks and detect empty streams". The design below reached three conclusions, in the order they were learned.

1. **An empty body proves nothing on its own.** #1972 (`00578912e`, in this base) removed the always-on `min_tokens = 64` floor, so an honest engine returns a complete empty stream whenever EOS comes first, and a client can force one through `stop`, `logit_bias`, `stop_token_ids`, a grammar or the thinking knobs. Turning an empty body into a `MsgErrorMiss` — an earlier revision of this proposal — doesn't survive that. **Withdrawn.**
2. **Empty-stream detection moves to #2000's PR 1, as the `EMPTY_STREAM` reason.** The voters there fetch the stored body from the executor and hold it against the signed Finish, so the gateway contributes nothing but the challenge. What is left for this branch is the trigger, one correction to the evidence rule, and a separate gateway-local fix.
3. **Signed chunks are not needed by that design.** #2000's voters take nothing from the gateway but the inference id. Signed checkpoints are held in this document for an A K decision on #1808; they close one case the challenge can't settle without a GPU (see "Held: signed checkpoints").

Frame coalescing (A K's gzip question) is independent of both and still stands: **52 bytes per token today → 15** when the gzip flush is batched, while a signature on every event would take it to 4x.

## Background: what is signed today

```
client ◄── SSE ── gateway(devshardctl) ◄── SSE ── executor(devshardd) ◄── MLNode(vLLM)
```

An executor's stream to the gateway (`transport/inference.go:39` `ServeInference`):

```
data: {"devshard_receipt": ...}          signed: MsgConfirmStart.ExecutorSig
data: {"choices":[{"delta":{"content":"Hello"}}]}     unsigned, one line per event
...
data: [DONE]
data: {"devshard_meta": {mempool}}       carries the signed Finish
```

Payment comes from the Finish's `InputTokens`/`OutputTokens` alone (`state/machine.go:1372` `checkFinishLocked`, capped at the reservation). Nothing compares them against the body the client received except a sampled GPU replay: `state.ShouldValidate` (`state/validation.go:80`) picks about a tenth of finished inferences. #2000 has the multipliers.

The gateway's only binding is whole-stream: after the stream ends it hashes what it received and compares with the Finish's `ServedHash` (`user/served_binding.go:40`). A cut stream produces no sums at all (`served_projection.go:86`).

## Why an empty stream is not a miss

An earlier revision of this proposal made a complete empty stream a `MsgErrorMiss`: the Finish signs the body, the empty body hashes to it, and today the `MsgErrorMiss` verifier rejects it only because it requires an error envelope (`host/timeout.go:416-467`). The safety argument was `min_tokens = 64`.

The argument is gone:

- **The floor is gone.** #1972 (`00578912e`) rewrote the request (`EnforceTokenBudget`, `common/completionapi/request.go:169`) to keep only a caller's `min_tokens`, and to drop even that under a grammar (`GrammarConstrained`, `:182`). `stop_token_ids` are now kept after a vocabulary check (`ValidateStopTokenIDs`, `:226`). With no floor, an honest engine returns a complete empty stream whenever EOS comes first.
- **A caller's `min_tokens` doesn't close it either.** A host that forces a lone-byte token (e.g. id 159 on DeepSeek-V4-Flash, byte 0xE0) leaves the decoded text ending in U+FFFD, which the incremental detokenizer does not emit (`vllm/v1/engine/detokenizer.py:211-247` in gonka-ai/vllm `befecf605`). Nothing is streamed, and the Finish says `completion_tokens = max_tokens`.
- **Thinking knobs do the same.** An honest `</think>` followed by EOS streams nothing, because the parser engine treats the delimiter as closing reasoning only. The `min_tokens` that rules this out is model-dependent.

A carve-out keyed on `stop` covers one route and not the others, and the other candidate keys don't hold either:

| Key | Why it fails |
|---|---|
| `stop_reason` | Host-reported, so a host that sent nothing can copy a `stop` entry into it. |
| Per-token evidence in the served body | `token_ids` are dropped before the body is hashed (`responseprocessor.go:112`, `logprobs_compress.go:18`), and the served body drops `logprobs` as well. |
| Exempt `stop` only | The client can empty an honest stream through EOS, `stop_token_ids`, a grammar, or the thinking knobs, none of which need `stop`. |
| Exempt only long `stop` entries | Needs a per-tokenizer lower bound on characters per token, which the split-UTF-8 case puts at about 16. |

So: **an empty body proves nothing.** What a host can still be wrong about is the number of tokens it claims, and that is what #2000 disputes.

## Where this goes now: #2000's challenge protocol

#2000 was rewritten (2026-10-20) to a gateway-started challenge: the gateway sends `VerifyChallenge(inference_id, reason)` with no evidence, every host fetches the request and the stored body from the executor, checks them against the signed Finish, and votes. Above `VoteThreshold` the record is Invalidated and the payment is refunded. Its PR 1 is the protocol, PR 2 adds CPU token counting, PR 3 raises the replay probability for challenged inferences.

Empty-stream detection is **item 8 of PR 1**, under the `EMPTY_STREAM` reason: a signed receipt, then a stream that ended with no content. The voters' evidence is the stored body, not what the gateway received.

### What this branch still owns

**1. The trigger.** The gateway knows when to send `EMPTY_STREAM`: the attempt has a receipt, ended with no content, and holds a signed Finish. That is `isEmptyStreamAttempt` (`redundancy.go:4445`) combined with the Finish artifacts the ErrorMiss path already collects. The state machine rejects the challenge for a non-Finished record, so a stream with no Finish stays on the timeout path as today.

**2. One correction to the evidence rule.** #2000's wording is "the stored body is missing or has no content either". Read as *text*, that flags honest hosts. In all three client-forceable cases above the stored body has no text:

| Case | Served and stored text | Stored per-token positions |
|---|---|---|
| `stop` match at offset 0 | empty | one per generated token |
| lone-byte token (U+FFFD) | empty | one per generated token |
| `</think>` then EOS | empty | one per generated token |

The rule has to read *content* as **generated token positions**, not as text: `EMPTY_STREAM` only when the stored body has no positions either, or is missing. That is the same quantity #2000's PR 2 counts, so it is a clarification rather than a new mechanism. The executor already forces the data: `logprobs = true` and `return_token_ids = true` (`common/completionapi/request.go:46,57`). The stored body keeps one `logprobs.content` entry per generated token; `responseprocessor.go:112` drops `token_ids`, and the logprob compression drops `logprob` and `bytes` per entry (`logprobs_compress.go:13`) but never the entry itself.

**3. The gateway-local strike, separately.** `recordSample` charges every empty stream to the host (`redundancy.go:5206-5218`) unless it is Kimi reasoning burn, and `isModelBurnEmpty` is scoped to a model that is retired (`:4463`). After #1972 that penalises honest empty streams with a 30-minute shadow quarantine. The gateway holds the request and doesn't need a vote, so this can be fixed on its own, in this branch, ahead of anything else. **This is the one change here that ships regardless of #2000.**

## Production data

From one production gateway's `empty_stream` log stage, over one day. Only shares are given.

| Shape | Share of empty streams |
|---|---|
| Receipt only, no body after it | about 98.5% |
| A body followed the receipt | about 1.5% |

Empty streams were somewhat under a tenth of all attempts.

- **Receipt only.** In a traced example, no token arrived for 300 s. An execution timeout vote then accepted, ending `TIMEOUT_REASON_EXECUTION`. This shape is already accounted for: no Finish, so the timeout path decides, and #2000 can't challenge it. Nothing here changes.
- **A body followed.** In a traced example, the nonce was finished and the timeout was skipped with `empty_stream_without_non_empty_winner` (`skipEmptyStreamTimeout`, `redundancy.go:4241`). The host was paid. This is the shape `EMPTY_STREAM` targets.
- **Two sub-shapes in that 1.5% that the logs can't explain.**
  - Almost all of it is 1-3 chunks of up to about 1 MB, with no content the gateway recognises and `usage_tokens = 0`.
  - A handful are GLM-5.3-Flash streams of thousands of chunks, with `usage_tokens` in the thousands, and no content the gateway recognises either.

  The second is the reason the positions-based reading matters: it may be a body with thousands of positions and no text the current predicate knows, which the gateway calls empty and the voters would call honest.
- The gateway logs no request parameters beyond `request_flags` (`redundancy.go:306`). Adding `stop_count` and `stop_token_ids_count` there would size the challenge volume, since a client-forced empty body is a challenge that ends `NONE`.

## Held: signed checkpoints

**Status:** held, pending an A K decision on whether #1808 stays in v7. #2000's voters take nothing from the gateway, so the count vote doesn't need these.

There is one case they would settle that the challenge cannot, from #2000's own table: *streams a short text, cuts, stores a self-consistent long body and signs it*. The voters see matching hashes and a count that matches the stored body, so it stands, with only the raised replay probability of PR 3 against it. A signed chain over the served lines would let the voters prove the served prefix was shorter than the stored body, **without a GPU**.

### Chain

The executor keeps a running hash over the lines it writes to the gateway, in the exact form the gateway receives them (the forwarded lines — the ones `ReceivedResponseHasher.Add` sees, `transport/client.go:791`).

```
h_0 = SHA256("devshard.streamchain.v1" ‖ escrow_id ‖ inference_id ‖ prompt_hash)
h_i = SHA256(h_{i-1} ‖ uint32_be(len(line_i)) ‖ line_i)
```

`prompt_hash` comes from `MsgStartInference` (`tx.proto:15`), so a chain cannot be replayed into another request. The length prefix removes line-boundary ambiguity. Blank separator lines and `devshard_*` protocol lines stay out of the chain.

### Checkpoint event

At each checkpoint the host writes one extra event:

```
data: {"devshard_checkpoint":{"seq":128,"output_tokens":131,"head":"<b64 32B>","sig":"<b64 65B>"}}
```

The signature is over a new proto message with its own domain (`types/signed_preimage.go`, alongside `devshard.errormissvote.v1`):

```proto
message StreamCheckpointContent {   // domain "devshard.streamcheckpoint.v1"
  string escrow_id     = 1;
  uint64 inference_id  = 2;
  uint32 executor_slot = 3;
  uint64 seq           = 4;  // lines committed at this point
  uint64 output_tokens = 5;  // cumulative, as the MLNode counted them
  bytes  head          = 6;  // h_seq
  bool   final         = 7;  // the checkpoint before [DONE]
}
```

Signed with the same key as the Finish (`h.signer`, the warm key on joins) and verified the same way: cold key, or a bound or resolvable warm key (`VerifyFinishProposerSig`, `state/machine.go:1796`).

### Cadence

- one checkpoint as soon as the first token has been written, content-bearing or not;
- every `N = 64` lines;
- one `final` right before `[DONE]`.

An optional `T = 2 s` heartbeat while lines flow slower than that.

### Negotiation (required, not optional)

A gateway on today's code forwards an unknown `devshard_*` line as inference data — the final `forward("data")` in `handleSSELine` (`transport/client.go:903`). That would leak checkpoint events to the client **and** add them to the received hash, breaking the served binding. So the host emits checkpoints only when the request asks:

- `InferenceRequest.stream_checkpoints = 6` (bool) in `proto/devshard/transport/v1/session.proto:25-31`, and the matching field in the HTTP JSON request.
- An old host ignores the field; an old gateway never sets it.

### Gateway side, and what a voter gains

- `handleSSELine` handles `devshard_checkpoint` like `devshard_meta`: not forwarded, not added to `ReceivedResponseHasher`.
- The gateway keeps its own chain over forwarded lines, checks head, signature, `seq` and a non-decreasing `output_tokens` at each checkpoint, and keeps the last valid one for `servedBindingRetention` (`user/served_binding.go:115`).
- A challenge carries the prefix: `{lines[0..seq), checkpoint}`. The voter recomputes the head and compares the checkpoint's count with the stored body's. A stored body shorter than the prefix is a direct fault; a stored body longer than the prefix where the served text stopped is the case from #2000's table.

## Frame coalescing (A K's question)

### Setup

Synthetic corpus: mixed English and Chinese split into BPE-like pieces of up to 4 bytes, rendered as DeepSeek-style `chat.completion.chunk` events after the logprobs strip. gzip BestSpeed, one window, a sync flush per frame, the way `ChatGzipEmitter` works (`transport/chat_gzip.go:41`). secp256k1 via go-ethereum, signing 11.0 µs, recover 14.4 µs (one Apple M5 core). 300 and 2,000 token runs agree within 2%; the table shows 2,000.

### Cost per token

| Framing | Bytes/token | vs. today |
|---|---|---|
| Today: one event per token, flush per event | 52.2 | 100% |
| Flush every 3 events (events unchanged) | 22.1 | 42% |
| Flush every 5 events (events unchanged) | 15.2 | 29% |
| Flush every 10 events (events unchanged) | 10.4 | 20% |
| 3 tokens merged into one event | 18.4 | 35% |
| 5 tokens merged into one event | 11.9 | 23% |
| 10 tokens merged into one event | 7.1 | 14% |
| Today + a signature on every event | 209.8 | 402% |
| Today + a checkpoint every 16 lines | 60.4 | 116% |
| Today + a checkpoint every 64 lines | 54.6 | 105% |
| Flush every 5 + a checkpoint every 64 | 17.7 | 34% |

### Findings

- What costs bytes is the **sync flush per event**: every flush closes a deflate block and pads to a byte boundary. The gzip window already removes most of the repeated JSON.
- Signing every event is not viable — about 4x the bytes, and ~22 ms of signing per 2,000-token answer. Periodic checkpoints are the design that survives contact with the wire.
- Merging tokens into one event saves another 20-30%, but it changes the stored body (`{"events":[...]}`) that validators replay token by token, and it changes what streaming clients see. Not worth 3-4 bytes per token.
- The knob already exists: `SetChatGzipCoalesce(ChatGzipCoalesceBytes)`, 1024 bytes ≈ 5 tokens (`transport/chat_gzip.go:18-31`). Only tests call it, so production flushes per token. The first token leaves alone (`firstTokenReady`, `chat_gzip.go:197`), so TTFT is unchanged.
- **What's missing is a time bound.** A byte cut alone holds tokens until 1,024 bytes accumulate — at 20 tokens/s, about 250 ms. The change adds a hold limit (proposal: 50 ms) and turns the knob on.
- This applies to the peer RPC (gzip) path. The HTTP inference path appears to be uncompressed SSE; `ResponseCompressionMiddleware` (`transport/compression.go:27`) has no production caller I could find. Worth confirming first.

### Caveats

The corpus is synthetic. Real traffic with tool calls and reasoning compresses differently, and real inter-token timing is what the hold limit is for. Before this merges, confirm with #1989's prod-like loadtest: bytes per token and TTFT, knob off vs on.

## Out of scope

- **Receipt then silence (#1823).** No Finish, so no challenge reaches it; the stall timeout (#1798) and the timeout vote cover it.
- **Changing `MsgFinishInference`.** Binding the final chain head in the Finish would be a state-root change. Nothing here needs it.
- **Gateway-side tokenization.** Per A K, the count stays on the MLNode and the recount belongs to #2000's CPU voters.
- **A protocol miss for empty streams.** Withdrawn, see "Why an empty stream is not a miss".

## Open questions

1. **`EMPTY_STREAM` on positions, not text.** Confirm the change with vitaly (:2000 item 8). Without it the reason invalidates honest hosts on a client-forced empty body.
2. **Does #1808 stay in v7?** Signed checkpoints now buy exactly one row of #2000's table without a GPU (self-consistent stored body after a cut). That is an A K decision on scope.
3. **The two production shapes.** What are they? The positions-based reading depends on knowing whether the GLM bodies carry positions without text.
4. **Order.** The gateway-local strike fix is independent and can go now. The `EMPTY_STREAM` trigger waits on #2000 PR 1's protocol. Coalescing is independent of both. Is that the order?
