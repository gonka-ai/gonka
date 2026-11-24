# Proposal: signed Chat frames and empty-stream handling

**Status:** Aligned with the new `cmd/gateway` merged by #2166 into `devshard/v7`. The table below separates merged behavior from pending protocol and gateway changes. Signed SSE checkpoint events and an empty-stream `MsgErrorMiss` rule are withdrawn.
**Issues:** #1808 (per-chunk stream signatures), #1841 (stream chunk signatures and empty stream detection), milestone Devshard v7
**Base:** `devshard/v7`

## Where each piece is

| Piece | PR | State |
|---|---|---|
| Chat gzip at level 2 | #2095 | merged into `devshard/v7` |
| New gateway executable | #2166 | merged; `cmd/devshardctl` is deprecated |
| Signed `ChatFrame`s and shared RPC verification | #2102 | open; host/protocol and shared-client support only |
| New gateway schedule settings, request wiring and journal observations | #2317 | draft; depends on #2102 |
| `EMPTY_STREAM` challenge reason (vitaly-andr) | #2088 | open; its gateway integration still targets `cmd/devshardctl` |
| The gateway-local empty-stream strike waits for `EMPTY_STREAM` | #2137 | open, stacked on #2088; new gateway migration pending |

## Summary

Item 3 was "sign stream chunks and detect empty streams". Three decisions came out of the review on this PR.

1. **An empty body proves nothing on its own.** After #1972 dropped the always-on `min_tokens = 64` floor, an honest engine returns a complete empty stream in several ways the client controls. An empty stream is not a `MsgErrorMiss`. That revision is **withdrawn**.
2. **#2088 detects empty streams.** Its voters judge the stored body by its generated positions, not its text. The proposed gateway-local strike waits for that verdict (#2137); this is not implemented in the merged new gateway.
3. **The executor signs the peer RPC Chat frames**, following A K's spec from 2026-10-21: gzip level 2, a raw 64-byte signature as a `ChatFrame` protobuf field outside the gzip body, the first output in its own frame, then one signed frame per 5 events. The gateway verifies and only logs.

## Background: what was signed before

```
client ◄── SSE ── gateway(cmd/gateway) ◄── Chat frames ── executor ◄── MLNode(vLLM)
```

The executor signed the receipt (`MsgConfirmStart.ExecutorSig`) and the Finish. Nothing in between was signed. Payment comes from the Finish's `InputTokens`/`OutputTokens` alone, capped at the reservation, and only a sampled GPU replay (`state.ShouldValidate`) compares them with the body.

Without #2102, the gateway's binding covers the whole stream. After the stream ends, it hashes what it received and compares that with the Finish's `ServedHash` (`user/served_binding.go`). A cut stream leaves the gateway holding an unsigned prefix.

## Empty streams

### Why an empty stream is not a miss

The withdrawn revision relied on `min_tokens = 64`. These three facts break it:

- **The floor is gone.** #1972 keeps only a caller's `min_tokens`, and drops even that under a grammar (`EnforceTokenBudget`, `GrammarConstrained` in `common/completionapi/request.go`). `stop_token_ids` survive a vocabulary check. An honest engine returns an empty complete stream whenever EOS, or a requested stop token, comes first.
- **A `stop` match at offset 0 empties the text.** vLLM holds back `max(len(stop)) - 1` characters from each delta. A `logit_bias` that forces a repeated token, together with a long `stop` string built from it, streams `""` deltas and ends with `finish_reason: "stop"` and a non-zero `completion_tokens` (cyberdelamain's trace on this PR).
- **A caller's `min_tokens` doesn't close it either.** A forced lone-byte token leaves the decoded text ending in U+FFFD, which the incremental detokenizer never emits. An honest `</think>` followed by EOS streams nothing on a reasoning model.

A carve-out keyed on any one request field covers one route and misses the others:

| Key | Why it fails |
|---|---|
| `stop_reason` | The host reports it, so a host that sent nothing can copy a `stop` entry into it. |
| Exempt requests with `stop` | EOS, `stop_token_ids`, a grammar or the thinking knobs need no `stop`. |
| Exempt only long `stop` entries | Needs a per-tokenizer lower bound on characters per token. The split-UTF-8 case puts it near 16. |

What a host can still lie about is the number of tokens it claims. That is #2000's count dispute.

### `EMPTY_STREAM` in #2088

This section describes the pending challenge protocol, not a capability already merged into the new gateway.

The gateway starts a challenge with the inference id and a reason. Every voter fetches the request and the stored body from the executor and checks them against the signed Finish. A voter votes `EMPTY_STREAM` only when the stored body matches the Finish and has **no generated positions** (`logprobs.content` entries). No text is not enough (`host/challenge.go`). A body with tokens and no text gets `NONE`. That covers all three honest cases above, each of which keeps one stored position per generated token. The executor forces `logprobs` and `return_token_ids` on every request, and the logprob compression drops fields of each entry but never the entry itself. The model `isModelBurnEmpty` names is the one exception: its documented outcome is no positions with completion tokens claimed, so the voter votes `NONE` there too.

This branch adds nothing to the vote.

### The gateway-local strike (#2137)

The legacy gateway's `recordSample` applied an empty-stream strike through `ObserveEmptyStreamForModelWithStats`; three strikes put the participant into a 30-minute shadow quarantine. #2137 changes that legacy path. Its current implementation remains in `cmd/devshardctl/empty_stream_strike.go` and must be adapted to the new gateway's own outcome, performance and crowning boundaries before it can affect `cmd/gateway`.

At #2137 head `799e0524b`, the strike is held only when the received stream binds an applied Finish or a queued Finish the session can actually apply. Only a no-fault verdict for that same Finish clears it. A verdict for another Finish, a fault or an unsuccessful check retains the strike. Cut or unbound streams and sessions without challenge support retain the immediate path.

The pending implementation handles both arrival orders, including an early `STREAM_UNBOUND` result, and honors outcomes already owned by finalizers during shutdown. Expiry follows the protocol's vote-collection deadline plus one hour of fallback grace. The early-verdict loss and fixed one-hour lifetime described in an earlier revision are therefore not current limitations of #2137. Its shared SSE receipt-field fix is transport support and must stay separate from a gateway-only migration.

The merged gateway already classifies empty answers, narrows participant windows and gates crowning (`cmd/gateway/engine/outcome.go`, `cmd/gateway/engine/crown_strikes.go`). That policy is not the legacy three-strike quarantine and is not yet conditioned on #2088's challenge verdict. This proposal does not claim those mechanisms are equivalent.

### Production shape

Historical observations from a legacy production gateway's `empty_stream` log stage over one day, as shares only; these are not measurements of the new gateway:

| Shape | Share of empty streams |
|---|---|
| Receipt only, no body after it | about 98.5% |
| A body followed the receipt | about 1.5% |

- **Receipt only.** No Finish, so the timeout path decides (#1823). In the new gateway, receipt and first-token deadlines, the streaming backstop and timeout votes are distinct from the inter-chunk stall; a stream with no first event has no stall anchor. No Finish-bound challenge can decide it.
- **A body followed.** The nonce finished and the host was paid. This is the shape `EMPTY_STREAM` and #2137 address. It has two sub-shapes the logs can't explain yet. Most are 1–3 chunks with no content the gateway recognises and `usage_tokens = 0`. A handful are GLM-5.3-Flash streams of thousands of chunks with `usage_tokens` in the thousands. That second shape is why reading positions instead of text matters.

## Signed Chat frames (#2102 and #2317)

### Wire

`ChatFrame` (`proto/devshard/transport/v1/session.proto`) gets two fields. Both sit outside the gzip body, so they don't disturb the dictionary:

```proto
message ChatFrame {
  bytes  chunk        = 1; // opaque gzip-stream bytes
  bytes  sig          = 2; // 64-byte R||S, empty on an unsigned frame
  uint64 stream_bytes = 3; // uncompressed bytes sig covers
}
```

The signature is raw bytes on the protobuf frame. It is not base64 inside the SSE JSON, so a gateway that doesn't know the field never forwards it to the client and never hashes it into `ServedHash`. The 64-byte form drops the recovery id. The gateway's side of that is under "Gateway" below.

### What is signed

```proto
message ChatFrameSignatureContent {   // domain "devshard.chatframe.v1"
  string escrow_id    = 1;
  uint64 inference_id = 2;
  uint64 seq          = 3; // 1 for the first signed frame
  bytes  stream_hash  = 4;
  uint64 stream_bytes = 5; // uncompressed bytes stream_hash covers
}
```

`stream_hash` is a running SHA-256 over every **uncompressed** byte of the Chat stream, from its first byte up to the end of the frame. It is seeded with length-prefixed domain, escrow id, inference id and the started record's prompt hash, so a signature can't be moved to another inference or another prompt:

```
h = SHA256( lp("devshard.chatframe.v1") ‖ lp(escrow_id) ‖ u64be(inference_id) ‖ lp(prompt_hash) ‖ stream bytes… )
```

Each signature covers the whole prefix and states its length, so after a cut the newest valid signature is all the gateway needs. Because the hash is over uncompressed bytes, it lines up with `ServedHash` and with what #2000 hashes, and the gzip level can change without touching the signature.

The executor signs with the same key as the Finish (`Signer()`, the warm key on joins), and only when it is the inference's executor and its payload matched the started record. Catch-up, Finalize and any other host keep unsigned per-event frames. Signing starts before the receipt is written (`startFrameSigning`, `transport/inference.go`), so the first signature covers the receipt too.

### Schedule

`ChatFrameSink` (`transport/chat_gzip.go`) counts finished SSE events:

| Mode | Flush | Signature |
|---|---|---|
| joined (default, `Every = 5`) | every `Every` events | every flush |
| separate | every event | every `Every`-th event |

- Before the first output, every finished event leaves alone, and the first output is always signed. Output means content, reasoning, a tool call, an error or `[DONE]`. A role-only delta does not count. Time to first token and winner selection don't wait for a group.
- The tail (`[DONE]`, `devshard_meta`) leaves signed on `Close`. No prefix is signed twice.
- A 16 KiB pending-byte limit forces a flush and bounds unflushed bytes even without an SSE event boundary.
- The host caps `Every` at 64. Joining holds events until the next flush, so it changes delivered-content timing. The new gateway does not cancel a crowned winner at the inter-chunk stall deadline; its existing escalation and hard-backstop policy is described below. The frame schedule does not add a new cancellation or punishment rule.

### Two knobs on the request

#2317 reads strict startup settings in `cmd/gateway/env`, validates them in `config` and passes a fresh `host.ChatFrameSchedule` through the API dispatch path. The schedule travels as `chat_frames: {every, separate}` in the host request envelope; client prompt JSON cannot override it. A schedule can be tried without a host release once the host supports #2102:

- `GATEWAY_CHAT_FRAME_EVENTS`: integer from 1 through 64, default 5. An explicit 0 is rejected at gateway startup. The shared host protocol still interprets a missing schedule or `every: 0` as its default; that compatibility rule is not the gateway environment contract.
- `GATEWAY_CHAT_FRAME_SEPARATE`: boolean, default false; true flushes each event separately while signing every `Every` events.

Joining saves body bytes. Separate keeps per-token delivery.

### Shared verification and gateway reporting

#2102 owns the protocol and shared client code because host binaries also execute those packages. #2317 owns the executable configuration and reporting under `cmd/gateway`; neither PR adds behavior to `cmd/devshardctl`.

The verifier (`ChatFrameVerifier`, `transport/chat_frame_sign.go`) sits in the Chat read path (`chatFrameReader` and `countingReader`, `transport/rpc_chat.go`). It hashes every inflated byte in order and checks each signature once the stream reaches exactly the byte count that signature names. Byte counts must strictly increase. Signatures must be exactly 64 bytes before entering the queue; at most 64 signatures can wait ahead of the inflated stream.

State holds the executor's addresses, not its key, and a 64-byte signature recovers to one key per recovery id. Only the signer's key also verifies the next signature, so the two candidates narrow by themselves as signatures arrive. When the stream ends, the gateway checks the remaining key once against the slot's cold key or a bound warm key (`frameSignerAllowed`, `user/session.go`), by the same rule as state signatures. Nothing that can reach the chain runs while tokens flow, and the gateway never asks about the other recovered key. Asking would cost a bridge call and leave a permanent `warmChecks` entry on every inference. `TestChatFrameVerifier_AsksOnlyAboutTheSigner` and `_DoesNotAskWhileTheStreamIsRead` pin this.

The shared client returns `HostResponse.ChatFrames`. In #2317, `api.Sessions.WithChatFrameNarrator` forwards that observation to `journal.ChatFramesChecked`, including a partial response returned beside a transport error:

- Signed and unsigned peer RPC streams emit `chat frames checked` with `chat_frames` and `chat_frames_signed`; unsigned frames are compatible and do not produce a signature warning. An absent observation emits no line.
- Signature failures emit `chat_frame_signature_invalid` with the counts and error. The journal uses its bounded progress lane; it does not alter the response, routing, participant windows or challenge voting. Using a frame signature as challenge evidence remains a separate protocol change.
- `MaxChunkGap` remains an observation, not an input to routing or punishment. This does not disable the engine's separate stall timer over delivered content. A batch can create delivery gaps; signature verification does not establish that such a gap was an executor fault.

### Relation to the stall migration (#1917)

#1917 changes only the deprecated gateway. Its cancellation algorithm is not a drop-in port: the new gateway separates deadline observations, escalation, concurrent-attempt limits and cancellation. The behavior comparison is against merged `devshard/v7` commit `b8eb22696` and legacy #1917 head `38214e068`.

| Legacy #1917 concern | Merged new gateway behavior |
|---|---|
| A role-only first event must wake and arm the timer | The first event emits `AttemptFirstToken`; the coordinator records the anchor and replans its timer. `TestARoleChunkFollowedBySilenceFallsBackOnTheStall` covers fallback. |
| Role/empty/error drips must not keep moving the deadline | Only `facts.Content` emits later `AttemptChunk` events. Role, error and terminator-only traffic do not refresh that anchor; reasoning and tool-call content do. |
| `[DONE]` without an answer must not disarm fallback | `[DONE]` does not clear the coordinator's stall deadline. A contentless attempt can still trigger a rescue when the attempt budget permits. |
| A successful answer waiting after `[DONE]` must survive the stall window | Stall marks the attempt but does not cancel it. A successful Finish after the window can still complete the winner; the hard backstop remains armed. |
| Disabled cancellation / expired log deadlines must not spin | The new public `engine_inter_chunk_stall_ms` requires a positive value; internally a zero policy arms no stall. There is no separate legacy log deadline, and a marked stall is not re-armed. |
| A failure arriving as the last attempt ends must still permit retry | Attempt completion is applied by the coordinator before it plans the next deadline. `TestRunRaceEscalatesAfterItsLastAttemptFailed` verifies the replacement. |
| A slow downstream write must not become a failed executor | Stall alone does not cancel the winner. The stream records silence after writes/flushes return; `TestAClientHoldingAWriteIsNotBookedAsTheWinningHostsSlowChunk` covers successful completion and excludes client wait from the longest-gap observation. |
| A stalled attempt must release capacity for a later host | **Not equivalent.** Stalled attempts stay live. The default two-live-attempt budget can prevent a healthy third host from being tried when the first two remain open. A total started-attempt cap of three does not free either occupied slot. |

The last difference is material: with two role-only hosts that stay open and a healthy third host queued, both stall deadlines can pass without dispatching the third. With no client departure or host completion, the streaming backstop ends the race instead. The new gateway's production backstop is at most 20 minutes and stays inside the protocol execution deadline; it is not the legacy 60-second stall cancellation. Changing this requires a gateway policy decision about cancelling or retiring unfinished attempts and their committed nonces. #1917 must not be declared fully superseded solely because the new gateway has stall tests, and this signing proposal makes no such policy change.

The comparison was checked with the engine/config race suites and local virtual-clock cases for repeated role/error/terminator events, DONE-tail completion, the winner's stall-versus-backstop boundary, zero policy and the occupied-budget case. The error-drip and winner-deadline checks fail when the corresponding behavior is deliberately reversed. See [the gateway race design](../../cmd/gateway/docs/race.md) and `engine/deadline.go`, `engine/attempt.go`, `engine/report.go` for the implementation boundaries.

### Compatibility

- An old host ignores `chat_frames` and sends unsigned per-event frames.
- An old gateway ignores `sig` and `stream_bytes`, which are unknown protobuf fields. It can still read a joined frame, because frames are opaque slices of one gzip stream.
- `SetChatGzipCoalesce` / `ChatGzipCoalesceBytes` had no production caller, and the event schedule replaces them.
- Only the peer RPC Chat path is signed. The HTTP inference path writes plain SSE with no frame, so it has nowhere to carry a signature and stays unsigned.

## Size and cost

### Why gzip level 2 (#2095)

An earlier revision of this proposal put the per-token cost on the sync flush itself. That was wrong. `gzip.BestSpeed` resets its window on every `Flush`, so each token can only match within its own frame and pays again for the repeated `id`, `object`, `created` and `model`. Level 2 is the lowest level that keeps the 32 KiB window across a sync flush. `Flush` still ends the block, so the gateway can read each frame immediately. A K's measurement on a real `QwQ-32B` completion (100 content tokens, 104 SSE events):

| Writer | Flush | Bytes/token |
|---|---|---|
| BestSpeed (production before #2095) | every event | 58.1 |
| Level 2 | every event | 17.0 |
| Level 2 | every 5 events | 10.0 |
| Level 2 | at the end | 7.0 |

The open level-2 writer takes less heap than BestSpeed (about 794 KiB against 1,176 KiB per open Chat).

### Signed frames (#2102)

Historical protocol measurement on the same completion, level 2; this is not a new gateway throughput benchmark. The bytes are the marshalled `ChatFrame` messages, so each signature costs its 64 bytes plus the field tags and the `stream_bytes` varint.

| Schedule | Frames | Signed | Bytes/token |
|---|---|---|---|
| unsigned, one frame per event (old host on #2095) | 105 | 0 | 18.8 |
| **signed, joined every 5 (default)** | 24 | 23 | **26.2** |
| signed, joined every 10 | 14 | 13 | 18.2 |
| signed, separate, every 5 | 105 | 23 | 34.7 |
| signed, separate, every 64 | 105 | 4 | 21.5 |

The production stream before #2095 was 56.4 bytes/token on this answer. With signing and level 2 together it is 26.2. Executor CPU for the whole answer, gzip and signing included, is about 0.5 ms. On the gateway, a 64-byte signature costs at most two recoveries (about 32 µs) until the key is known, and about 12 µs per verification after that.

The historical `make citest-peerrpc-chat` run reported 9 signed frames out of 10, with no signature warning. New-gateway validation is in #2317: `TestE2E_GatewayVerifiesSignedChatFrames` exercises real peer-RPC hosts in joined and separate modes, with streaming and buffered requests. Each response must produce a signed observation from a newer nonce in the same escrow, and no `chat_frame_signature_invalid` warning. The old frame-count measurement is not asserted as a property of the new gateway.

## Out of scope

- **Token throughput on the gateway, and a dynamic join window** that moves `Every` with it. A K's further proposal. It waits on the throughput measurement (@qdanik).
- **A frame signature as challenge evidence.** One case in #2000's table needs it: a host streams a short text, cuts, then stores and signs a self-consistent long body. The newest valid frame signature proves the served prefix, so a voter could show the stored body diverges from it without a GPU. That is a protocol change on #2000's side.
- **Receipt then silence (#1823), and #1917 cancellation-policy migration.** No Finish means no Finish-bound challenge. Receipt/first-token escalation, the streaming backstop and timeout votes remain the engine and protocol's responsibilities; the occupied-attempt-budget difference above remains separate work.
- **Changing `MsgFinishInference`.** Binding the final stream hash in the Finish would be a state-root change. Nothing here needs it.
- **Gateway-side tokenization.** Per A K, the count stays on the MLNode, and the recount belongs to #2000's CPU voters.

## Open questions

1. **Integration order.** #2102 supplies host/protocol and shared verification; #2317 depends on it for the new gateway. #2137 depends on #2088, and both still need their legacy gateway integration adapted before the new gateway can use the challenge verdict.
2. **The two production shapes** with a body but no recognised content. Do the GLM bodies carry positions? If they do, #2137 stops striking them once #2088 votes `NONE`.
3. **The HTTP inference path.** It stays unsigned. Is that acceptable for v7, or should a gateway that wants signatures always use peer RPC?
