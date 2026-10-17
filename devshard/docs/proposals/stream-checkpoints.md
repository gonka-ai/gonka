# Proposal: signed stream checkpoints and empty-stream misses

**Status:** Draft for discussion
**Issues:** #1808 (per-chunk stream signatures), #1841 (stream chunk signatures and empty stream detection) — milestone Devshard v7
**Consumer:** #2000 path 2 (gateway-started count vote, vitaly-andr)
**Base:** `devshard-0.2.x-v6` @ `95b95d2a0` (line numbers below are at this commit)

## Summary

The executor signs two things on an inference: the receipt (`MsgConfirmStart.ExecutorSig`) and the Finish (`MsgFinishInference.ProposerSig`, which covers `ResponseHash` and `ServedHash`, the whole served body). **The stream between them carries no signature** (`cmd/devshardctl/redundancy.go:4193-4202` says so outright). That leaves two gaps:

- **An empty stream is free.** The Finish applies, the host is paid, and the only consequence is a gateway-local quarantine. Nothing on-protocol punishes it.
- **A cut stream leaves the gateway with nothing.** It holds a prefix that no signature covers, and `ReceivedResponseHasher.Sums()` returns nil without `[DONE]` or `devshard_meta` (`common/completionapi/served_projection.go:86`). This is what #2000 path 2 needs when the executor withholds the stored body.

The proposal:

1. **A complete empty stream is a miss.** The Finish already signs the whole served body, and the gateway already knows whether the body binds to it. Only the `MsgErrorMiss` verifier's last check rejects it, because it requires an error envelope. Accepting an empty body turns the case into a miss with no new signing at all.
2. **A running hash over the served lines, signed at intervals.** `h_i = SHA256(h_{i-1} ‖ len(line_i) ‖ line_i)`, with a signed `(seq, output_tokens, head)` every 64 lines and at the end. Signing every chunk instead costs about 4x the wire bytes; checkpoints cost about 5% (measured below). The token count comes from the MLNode, so the gateway only hashes lines and recovers signatures.
3. **Frame coalescing** (A K's question): batch the gzip flushes, not the JSON events. 52 bytes per token today → 15. Merging tokens into one event saves another 20-30% but changes the stored body that validators replay, so it isn't worth it.

Three PRs, in this order:

| PR | What | State machine | Needs every host upgraded |
|---|---|---|---|
| A | Empty complete stream → `MsgErrorMiss` | no (verifier rule only) | yes, so it lands in the v7 runtime |
| B | Signed stream checkpoints, opt-in per request | no | no (negotiated) |
| C | Turn on gzip flush coalescing, with a time bound | no | no (host-side knob) |

## Background: what is signed today

```
client ◄── SSE ── gateway(devshardctl) ◄── SSE ── executor(devshardd) ◄── MLNode(vLLM)
```

An executor's stream to the gateway (executor→gateway, `transport/inference.go:39-264`):

```
data: {"devshard_receipt": ...}          signed: MsgConfirmStart.ExecutorSig
data: {"choices":[{"delta":{"content":"Hello"}}]}     unsigned, one line per event
...
data: [DONE]
data: {"devshard_meta": {mempool}}       carries the signed Finish
```

Payment comes from the Finish's `InputTokens`/`OutputTokens` alone (`state/machine.go:1310` `checkFinishLocked`, capped at the reservation). Nothing compares `OutputTokens` against the body the client received. #2000 has the details and the multipliers.

## Today's three stream shapes

**Complete empty stream** — receipt, role delta, `finish_reason`, `[DONE]`, meta. #1989's `receipt-then-empty-stream` fault produces exactly this.

| Layer | Today |
|---|---|
| Gateway | `isEmptyStreamAttempt` (`redundancy.go:4423`) → race another host; per-host strike; 3 strikes → 30 min shadow quarantine (`participant_limiter.go:769`) |
| Chain | Finish applies, `StatusFinished`, host paid (`state/machine.go:1334`) |
| Timeout vote | skipped, nonce already finished (`skipEmptyStreamTimeout`, `redundancy.go:4219`) |
| `MsgErrorMiss` | skipped, needs `inf.errorTerminal` (`redundancy.go:4193`) |

**Cut stream** — content arrives, connection dies before `[DONE]`. The gateway holds an unsigned prefix. If a Finish arrives later through the mempool (#2000, "When the gateway doesn't hold the signed body"), its `OutputTokens` cannot be checked against what the gateway received.

**Receipt then silence** (#1823) — no content ever. The #1798 stall timeout cancels the attempt and the timeout path decides the miss. Unchanged by this proposal; see "Out of scope".

## PR A: an empty complete stream is a miss

### Rule

`completionapi.IsTerminalErrorResponse` becomes the consensus predicate for "this body is not a usable answer". It is the right place: every host compiles the same function, the vote signs `ResponseHash`, and the verifier already re-checks the payload against that hash (`host/timeout.go:389-441`).

Add one branch: the payload is a served envelope (`{"events":[...]}`) whose events carry **no** content-bearing delta — no non-empty `delta.content`, `delta.reasoning`, `delta.reasoning_content` or `delta.tool_calls` — and no error object.

The gateway's own content predicate is the model to copy: `choiceContentSource` (`cmd/devshardctl/sse_chunk.go:80-92`), the same field order the classifier reports. Moving it into `common/completionapi` makes gateway and voters byte-identical by construction.

**One trap found while reading it.** `scanSSEChunk` treats `finish_reason == "stop"` with `usage.completion_tokens > 0` as content, under the label `message.empty_stop_completion_tokens` (`sse_chunk.go:63-67`). That exists for the non-streamed shape converted to SSE, where the message carries no delta at all. The same shape with a streamed body is precisely the fault we want to catch, so the shared predicate must key on the real delta fields and treat that synthetic label as its own case, not as content. **Open question 3.**

### Why it is safe

`EnforceTokenBudgetFloor` forces `min_tokens >= 64` on every request and `skip_special_tokens = false` (`common/completionapi/request.go:52,160`). An honest engine must emit at least 64 tokens, and with special tokens rendered they appear in `content`, `reasoning`, `reasoning_content` or `tool_calls`. A body with none of them is a host fault, not a model outcome.

The only known honest empty body was Kimi-K2.6 reasoning burn (`isModelBurnEmpty`, `redundancy.go:4441`). **Kimi-K2.6 is retired, so no per-model exemption is needed.** The marker in open question 3 is about a non-streamed shape, not a model.

### Changes

| File | Change |
|---|---|
| `common/completionapi/terminal_error.go` | add `IsEmptyResponse`; expose `IsMissResponse` = error **or** empty; keep `IsTerminalErrorResponse` for the error branch |
| `common/completionapi/served_projection.go` or a new file | move `choiceContentSource`-equivalent logic so both callers share it |
| `host/timeout.go:437` | check 7 uses `IsMissResponse`; error string becomes `not_miss_body` |
| `cmd/devshardctl/redundancy.go:4193` | `errorMissEnabledFor` also accepts `isEmptyStreamAttempt(inf)` |
| `cmd/devshardctl/redundancy.go` | keep the served lines for the empty case (today `errorStreamLines` is retained from the first write and dropped when the stream ends without an error, `releaseErrorStreamRetention` — the empty branch needs them kept and passed as `responsePayload`) |
| `cmd/devshardctl/redundancy.go:4219` | `skipEmptyStreamTimeout` must not skip when an ErrorMiss is runnable (it already short-circuits on `errorMiss`) |

Apply is untouched: `StatusTimedOut`, `Missed++`, `ActualCost` unwound (`state/machine.go:1652`).

**Rolling deploy.** An upgraded gateway driving an old group gets no votes and fails closed — the client keeps waiting, then times out. Slow, safe, and the reason PR A belongs in a runtime upgrade rather than a silent gateway push.

### Tests

- Classifier rows, from `docs/proposals/error-finish-miss.md:856`: role + `[DONE]`; role + `finish_reason` + usage; usage only; role delta with empty content. All `IsMissResponse == true`. A body with one `reasoning_content` token, and one with `tool_calls`, are both `false`.
- Lying host: Finish claiming `OutputTokens = 4096` over an empty body → accept, verdict from the body and not from usage.
- Hash binding: an empty body that does not hash to the Finish's `ResponseHash`/`ServedHash` → reject (`ErrorTimeoutRejectHashMismatch`).
- Verifier rejects `StatusPending` as `no_finish_tx` (unchanged) and can still run with a nil executor client and an empty local mempool.
- testenv: #1989's `receipt-then-empty-stream` scenario. Today it expects "P2 is quarantined for empty_stream"; with PR A it must also observe the nonce reaching `TimedOut` and P2's `Missed` increasing.

## PR B: signed stream checkpoints

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

The signature is over a new proto message with its own domain in `types/signed_preimage.go` (alongside `devshard.validation.v1`, `devshard.timeoutvote.v1`, `devshard.errormissvote.v1`):

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

Signed with the same key that signs the Finish (`h.signer`, the warm key on joins), verified the same way: cold key, or a bound or resolvable warm key (`VerifyFinishProposerSig`, `state/machine.go:1778`).

### Cadence

- one checkpoint as soon as a content-bearing line has been written, which proves output started;
- every `N = 64` lines;
- one `final` right before `[DONE]`.

An optional `T = 2 s` heartbeat while lines flow slower than that. **Open question 2.**

### Where the token count comes from

From the MLNode, which is what A K asked for. `ModifyRequestBodyWithLogprobsMode` already forces `logprobs = true` with `top_logprobs` (`request.go:42`), so every content chunk carries one `logprobs.content` entry per generated token. The executor's `ExecutorResponseProcessor` parses those chunks anyway; summing `len(logprobs.content)` is the same fallback `StreamedCompletionResponse.GetUsage` already uses (`completionresponse.go:207`). On the final checkpoint, the count must equal the Finish's `OutputTokens`; a mismatch is a gateway-local strike and a signal #2000 can use.

The gateway does no tokenizing and no counting: it hashes lines (0.12 µs each) and recovers a signature per checkpoint.

### Negotiation (required, not optional)

A gateway on today's code forwards an unknown `devshard_*` line as inference data — the final `forward("data")` in `handleSSELine` (`transport/client.go:903`). That would leak checkpoint events to the client **and** add them to the received hash, breaking the served binding. So the host emits checkpoints only when the request asks:

- `InferenceRequest.stream_checkpoints = 6` (bool) in `proto/devshard/transport/v1/session.proto:22-28`, and the matching field in the HTTP JSON request.
- An old host ignores the field; an old gateway never sets it.

### Gateway side

- `handleSSELine` handles `devshard_checkpoint` like `devshard_meta`: not forwarded, not added to `ReceivedResponseHasher`.
- The gateway keeps its own chain over forwarded lines and at each checkpoint checks the head, the signature, `seq` against the lines received, and that `output_tokens` is non-decreasing. A failure is a strike; the attempt cannot win.
- The last valid checkpoint is kept on `inflight` and on the session for the nonce, for `servedBindingRetention` (`user/served_binding.go:116`).

### What #2000 gets

Artifact: `{request, lines[0..seq), checkpoint}`.

- The voter checks the request against `prompt_hash`, recomputes `head` from the lines, recovers the signer.
- It recounts the text with the model's renderer. If the signed `output_tokens` exceeds that count beyond tolerance, the executor signed an inflated claim — no body fetch needed.
- Complete stream: the final checkpoint's count must equal the Finish's.
- Cut stream: the prefix's honest count is known. Only the tail after `seq` needs the stored body, and the executor cannot substitute a different prefix, because the chain would no longer extend the signed head.

This narrows #2000's fetch path; it does not remove it. A tail the gateway never received can't be counted from the gateway's side. Format to be agreed with vitaly before PR B fixes it. **Open question 4.**

### Signature coverage

The receipt is not in the chain. It is already signed, and the gateway verifies it before it races the attempt, so a chain that starts after it is anchored to a checked artifact. If #2000 wants both, `{request, receipt, lines, checkpoint}` is the artifact — the same shape, one more field. **Open question 4.**

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

## Open questions

1. **Miss reason for empty streams.** Is a complete empty stream a `MsgErrorMiss` (this proposal — verified body, no GPU work, payment refunded, `Missed++`), or should it keep a distinct reason so the two cases stay countable separately? The vote content is unchanged if we reuse `ErrorMiss`.
2. **Cadence.** Is `N = 64` lines right, and do we want the `T`-second heartbeat checkpoint (which is also what would give #1823 a signed "alive, silent" signal)?
3. **The synthetic content marker.** `scanSSEChunk` counts `finish_reason == "stop"` with `usage.completion_tokens > 0` as content for the non-streamed converted shape (`sse_chunk.go:63-67`). Under the shared predicate, that shape must not hide a streamed empty answer. Which shape is real in production today — does the gateway still serve non-streamed requests as SSE?
4. **Artifact format** with vitaly: `{request, lines, checkpoint}`, or does a voter need the receipt too?
5. **Order.** PR A is independently useful, changes one verifier rule, and needs no negotiation with hosts. Can it go first and land in v7, with B and C behind it?
