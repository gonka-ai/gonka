# Proposal: signed stream checkpoints and empty-stream evidence

**Status:** Draft for discussion
**Issues:** #1808 (per-chunk stream signatures), #1841 (stream chunk signatures and empty stream detection) — milestone Devshard v7
**Consumer:** #2000 path 2 (gateway-started count vote, vitaly-andr)
**Base:** `devshard-0.2.x-v6` @ `3c924cbf6` (line numbers below are at this commit)

## Summary

The executor signs two things on an inference: the receipt (`MsgConfirmStart.ExecutorSig`) and the Finish (`MsgFinishInference.ProposerSig`, which covers `ResponseHash` and `ServedHash`, the whole served body). **The stream between them carries no signature** (`cmd/devshardctl/redundancy.go:4223` says so outright). That leaves two gaps:

- **A cut stream leaves the gateway with nothing.** It holds a prefix that no signature covers, and `ReceivedResponseHasher.Sums()` returns nil without `[DONE]` or `devshard_meta` (`common/completionapi/served_projection.go:86`). This is what #2000 path 2 needs when the executor withholds the stored body.
- **No one counts what was served.** The Finish's `InputTokens`/`OutputTokens` set the payment (`state/machine.go:1372`, capped at the reservation), and nothing compares them with the body. An empty stream is the extreme case: the host is paid for a body that carries no answer at all.

**An empty stream is not by itself a fault.** #1972 (`00578912e`, in this base) removed the always-on `min_tokens = 64` floor, so an honest engine returns a complete empty stream whenever EOS comes first. A client can also force that, with `stop`, `logit_bias`, `stop_token_ids`, a grammar, or the thinking knobs. An earlier version of this proposal turned a complete empty stream into a `MsgErrorMiss`, with a request-bound carve-out. It doesn't survive #1972: no request field separates an honest empty stream from a host that returned nothing. This revision withdraws that design.

**What replaces it.** The fault is not an empty body. It is a host claiming output tokens it did not generate, which is exactly #2000's dispute. Item 3's job is to give that dispute the token-level evidence it lacks:

1. **Signed checkpoint evidence (PR B).** A running hash over the served lines, with a signed `(seq, output_tokens, head)` every 64 lines and at the end. An executor that cut the stream can no longer deny what it sent, or what the MLNode had generated up to the last checkpoint. The token count comes from the MLNode, so the gateway only hashes lines and recovers signatures.
2. **An empty body that claims tokens is a signal for #2000.** A served body with no content and a Finish claiming `output_tokens > 0` is the shape #2000's recount exists for. The gateway can hand it over with the lines it holds, without a body fetch.
3. **Frame coalescing (PR C, A K's question).** Batch the gzip flushes, not the JSON events. 52 bytes per token today → 15. Signing every chunk would take the answer to about 4x. A checkpoint every 64 lines adds about 5%.

The detailed tables and measurements are below.

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

A carve-out keyed on `stop` (the previous revision) covers one route and not the others, and the other candidate keys don't hold either:

| Key | Why it fails |
|---|---|
| `stop_reason` | Host-reported, so a host that sent nothing can copy a `stop` entry into it. |
| Per-token evidence in the served body | `token_ids` are dropped before the body is hashed (`responseprocessor.go:112`, `logprobs_compress.go:18`), and the served body drops `logprobs` as well. |
| Exempt `stop` only | The client can empty an honest stream through EOS, `stop_token_ids`, a grammar, or the thinking knobs, none of which need `stop`. |
| Exempt only long `stop` entries | Needs a per-tokenizer lower bound on characters per token, which the split-UTF-8 case puts at about 16. |

So: **an empty body proves nothing.** What a host can still be wrong about is the number of tokens it claims, and that is what #2000 disputes.

## Production data

From one production gateway's `empty_stream` log stage, over one day. Only shares are given.

| Shape | Share of empty streams |
|---|---|
| Receipt only, no body after it | about 98.5% |
| A body followed the receipt | about 1.5% |

Empty streams were somewhat under a tenth of all attempts.

- **Receipt only.** In a traced example, no token arrived for 300 s. An execution timeout vote then accepted, ending `TIMEOUT_REASON_EXECUTION`. This shape is already a miss today: no Finish, so `isEmptyStreamAttempt` runs and the timeout path accounts for it. Nothing here changes.
- **A body followed.** In a traced example, the nonce was finished and the timeout was skipped with `empty_stream_without_non_empty_winner` (`skipEmptyStreamTimeout`, `redundancy.go:4241`). The host was paid. This is the shape that needs the evidence below.
- **Two sub-shapes in that 1.5% that the logs can't explain.**
  - Almost all of it is 1-3 chunks of up to about 1 MB, with no content the gateway recognises and `usage_tokens = 0`.
  - A handful are GLM-5.3-Flash streams of thousands of chunks, with `usage_tokens` in the thousands, and no content the gateway recognises either.

  Both are worth classifying before the count vote is scoped: the second could be an honest answer in a field the content predicate doesn't know, which a recount would call a mismatch.
- The gateway logs no request parameters beyond `request_flags` (`redundancy.go:306`), so the share of traffic carrying `stop`, `logit_bias` or `stop_token_ids` is unknown. Adding `stop_count`, `stop_max_len` and `logit_bias_count` to `request_flags` would measure it, and that is now only useful for sizing the recount, not for a rule.
- The local strike has the same problem as the old rule. `recordSample` charges every empty stream to the host (`redundancy.go:5206-5218`) unless it is Kimi reasoning burn, and `isModelBurnEmpty` is scoped to a model that is retired (`:4463`). After #1972 this penalises honest empty streams. It should be fixed separately in the gateway, which holds the request and needs no vote.

## PR B: signed stream checkpoints

Unchanged in direction by the #1972 finding. It is now the evidence layer for #2000 rather than for a miss rule.

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

- one checkpoint as soon as the first token has been written, content-bearing or not, which proves generation started and pins the count so far;
- every `N = 64` lines;
- one `final` right before `[DONE]`.

An optional `T = 2 s` heartbeat while lines flow slower than that. **Open question 2.**

The first checkpoint is what an empty-with-tokens stream needs. A stream that generates tokens whose text never appears still emits chunks — cyber's reproduce has one chunk per step, `delta.content == ""`, carrying logprobs. If the first checkpoint is taken on the first token rather than the first visible content, such a stream carries a signed `output_tokens` before it ends. What it does not carry is proof that those tokens are honest: see "The case this does not settle".

### Where the token count comes from

From the MLNode, which is what A K asked for. The request rewrite forces `logprobs = true` with `top_logprobs` (`common/completionapi/request.go:46`) and `return_token_ids = true` (`:57`), so every content chunk carries one `logprobs.content` entry per generated token. The executor's `ExecutorResponseProcessor` parses those chunks anyway; summing `len(logprobs.content)` is the fallback `StreamedCompletionResponse.GetUsage` already uses (`completionresponse.go:207-212`). On the final checkpoint the count must equal the Finish's `OutputTokens`; a mismatch is a gateway-local strike and a signal #2000 can use.

The gateway does no tokenizing and no counting: it hashes lines (0.12 µs each) and recovers a signature per checkpoint.

### Negotiation (required, not optional)

A gateway on today's code forwards an unknown `devshard_*` line as inference data — the final `forward("data")` in `handleSSELine` (`transport/client.go:903`). That would leak checkpoint events to the client **and** add them to the received hash, breaking the served binding. So the host emits checkpoints only when the request asks:

- `InferenceRequest.stream_checkpoints = 6` (bool) in `proto/devshard/transport/v1/session.proto:25-31`, and the matching field in the HTTP JSON request.
- An old host ignores the field; an old gateway never sets it.

### Gateway side

- `handleSSELine` handles `devshard_checkpoint` like `devshard_meta`: not forwarded, not added to `ReceivedResponseHasher`.
- The gateway keeps its own chain over forwarded lines and at each checkpoint checks the head, the signature, `seq` against the lines received, and that `output_tokens` is non-decreasing. A failure is a strike; the attempt cannot win.
- The last valid checkpoint is kept on `inflight` and on the session for the nonce, for `servedBindingRetention` (`user/served_binding.go:115`).

## What #2000 gets

Artifact: `{request, finish, lines[0..seq), checkpoint}`. The receipt is not in it: its fields repeat `MsgStartInference`, and `h_0` already binds `escrow_id`, `inference_id` and `prompt_hash` from the Start (vitaly-andr). The Finish is needed: payment comes from it, and #2000 also disputes `input_tokens`, which only the Finish carries.

A voter:

1. Checks the request against `prompt_hash`, and the Finish signature against the executor slot.
2. Recomputes `head` from the lines and recovers the checkpoint signer.
3. Recounts the text with the model's renderer. If the signed `output_tokens` exceeds that count beyond tolerance, the executor signed an inflated claim. No body fetch is needed.
4. On a complete stream, checks that the final checkpoint's count equals the Finish's `OutputTokens`.
5. On a cut stream, knows the honest count of the prefix. Only the tail after `seq` needs the stored body. The executor can't substitute a different prefix, because the chain would no longer extend the signed head.
6. On a stream whose body carries no content, compares the checkpoint's `output_tokens` with the Finish's. A checkpoint and a Finish that both say zero is honest. A Finish above the checkpoint is a claim the host signed after its own last checkpoint, and the count vote is what judges it. A checkpoint that is itself above zero over an empty body is the shape in "The case this does not settle": the host says it generated tokens that the client never saw, and only a replay can say whether that is true.

This narrows #2000's fetch path but doesn't remove it: a tail the gateway never received can't be counted from the gateway's side.

### The case this does not settle

A stream that carries tokens which decode to nothing (the lone-byte and `</think>` cases above) looks like an honest empty body to the gateway, and to a text recount it looks like an answer with no content against a nonzero claim. So the recount will flag it, correctly by its own rule and wrongly for an honest host. Text alone can't separate it from a host that generated nothing and claimed tokens. That case needs the full replay #2000 already sketches, on a sample, not on every empty stream. This proposal supplies the prioritisation signal; the budget and the sampling stay with #2000.

## PR C: frame coalescing (A K's question)

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
- **What's missing is a time bound.** A byte cut alone holds tokens until 1,024 bytes accumulate — at 20 tokens/s, about 250 ms. PR C adds a hold limit (proposal: 50 ms) and turns the knob on.
- This applies to the peer RPC (gzip) path. The HTTP inference path appears to be uncompressed SSE; `ResponseCompressionMiddleware` (`transport/compression.go:27`) has no production caller I could find. Worth confirming before PR C.

### Caveats

The corpus is synthetic. Real traffic with tool calls and reasoning compresses differently, and real inter-token timing is what the hold limit is for. Before PR C merges, confirm with #1989's prod-like loadtest: bytes per token and TTFT, knob off vs on.

## Out of scope

- **Receipt then silence (#1823).** No body exists, so there is nothing to sign. The stall timeout (#1798) and the timeout vote cover it, and the issue itself is about a host taking the receipt under PoC load. A `T`-second heartbeat checkpoint would let the gateway tell "alive, producing nothing" from "dead"; that is open question 2.
- **Changing `MsgFinishInference`.** The final `head` could go into the Finish as field 11 to bind the chain on-chain. Nothing here needs it, and it would be a state-root change. Leave it until #2000 shows a need.
- **Gateway-side tokenization.** Per A K, the count stays on the MLNode; the recount belongs to #2000's voters.
- **A protocol miss for empty streams.** Withdrawn, see "Why an empty stream is not a miss".

## Open questions

1. **Empty-with-tokens priority.** Should the gateway mark a served body with no content and `output_tokens > 0` as a priority replay target for #2000? If so, the signal has to survive the session, not just the attempt.
2. **Cadence.** Is `N = 64` lines right? Do we want the `T`-second heartbeat checkpoint? It would also give #1823 a signed "alive, silent" signal.
3. **The two production shapes.** What are they? This decides whether the recount scoped by #2000 will see honest answers as mismatches.
4. **Artifact ownership.** Does PR B's checkpoint land in this repo for #2000, or does #2000 define it and this proposal implement the executor side?
5. **Order.** PR B needs a negotiated field, so every gateway must be upgraded before hosts emit checkpoints. PR C is independent and can go first. Is that the order?
