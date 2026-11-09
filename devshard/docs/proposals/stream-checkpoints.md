# Proposal: signed Chat frames and empty-stream handling

**Status:** Updated 2026-10-26 to match the implementation. It replaces the earlier revisions that put signed checkpoints into the SSE stream and turned an empty stream into a `MsgErrorMiss`.
**Issues:** #1808 (per-chunk stream signatures), #1841 (stream chunk signatures and empty stream detection), milestone Devshard v7
**Base:** `devshard/v7`

## Where each piece is

| Piece | PR | State |
|---|---|---|
| Chat gzip at level 2 | #2095 | merged into `devshard/v7` |
| Signed `ChatFrame`s, two gateway knobs, gateway verification | #2102 | open |
| `EMPTY_STREAM` challenge reason (vitaly-andr) | #2088 | open |
| The gateway-local empty-stream strike waits for `EMPTY_STREAM` | #2137 | open, stacked on #2088 |

## Summary

Item 3 was "sign stream chunks and detect empty streams". Three decisions came out of the review on this PR.

1. **An empty body proves nothing on its own.** After #1972 dropped the always-on `min_tokens = 64` floor, an honest engine returns a complete empty stream in several ways the client controls. An empty stream is not a `MsgErrorMiss`. That revision is **withdrawn**.
2. **#2088 detects empty streams.** Its voters judge the stored body by its generated positions, not its text. The gateway-local strike no longer punishes on its own: it waits for that verdict (#2137).
3. **The executor signs the peer RPC Chat frames**, following A K's spec from 2026-10-21: gzip level 2, a raw 64-byte signature as a `ChatFrame` protobuf field outside the gzip body, the first output in its own frame, then one signed frame per 5 events. The gateway verifies and only logs.

## Background: what was signed before

```
client ◄── SSE ── gateway(devshardctl) ◄── Chat frames ── executor(devshardd) ◄── MLNode(vLLM)
```

The executor signed the receipt (`MsgConfirmStart.ExecutorSig`) and the Finish. Nothing in between was signed. Payment comes from the Finish's `InputTokens`/`OutputTokens` alone, capped at the reservation, and only a sampled GPU replay (`state.ShouldValidate`) compares them with the body.

The gateway's only binding covers the whole stream. After the stream ends, it hashes what it received and compares that with the Finish's `ServedHash` (`user/served_binding.go`). A cut stream leaves the gateway holding an unsigned prefix.

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

The gateway starts a challenge with the inference id and a reason. Every voter fetches the request and the stored body from the executor and checks them against the signed Finish. A voter votes `EMPTY_STREAM` only when the stored body matches the Finish and has **no generated positions** (`logprobs.content` entries). No text is not enough (`host/challenge.go`). A body with tokens and no text gets `NONE`. That covers all three honest cases above, each of which keeps one stored position per generated token. The executor forces `logprobs` and `return_token_ids` on every request, and the logprob compression drops fields of each entry but never the entry itself. The model `isModelBurnEmpty` names is the one exception: its documented outcome is no positions with completion tokens claimed, so the voter votes `NONE` there too.

This branch adds nothing to the vote.

### The gateway-local strike (#2137)

`recordSample` struck every empty stream (`ObserveEmptyStreamForModelWithStats`), and three strikes put the participant into a 30-minute shadow quarantine. The only exemption, `isModelBurnEmpty`, matches a retired model. Since #1972, a client can therefore push honest hosts out of rotation.

#2137 holds the strike when all three of these hold:

- the session runs the challenge,
- the Finish applies,
- the received stream hashes to the Finish's response or served hash.

The strike is cleared only if the challenge ends `none`. Every other outcome keeps it: either the voters found a fault, or the check never happened, and the gateway then does what it did before. The strike and the outcome wait for each other for up to an hour. `Stop` applies every strike still held. A stream that was cut, that doesn't bind to its Finish, or that ran on an older protocol is struck at once, as before. The new code is in `cmd/devshardctl/empty_stream_strike.go`, and it changes only the gateway.

Known limitation: a stream that stays open longer than `ChallengeOpenStreamGrace` after its Finish is challenged as `STREAM_UNBOUND` first. If that verdict arrives before the strike is held, it is dropped, and the strike lands at expiry as it does today. The #2137 description has the details.

### Production shape

From one production gateway's `empty_stream` log stage over one day, as shares only:

| Shape | Share of empty streams |
|---|---|
| Receipt only, no body after it | about 98.5% |
| A body followed the receipt | about 1.5% |

- **Receipt only.** No Finish, so the timeout path decides, as today (#1798 stall timeout, #1823). No challenge reaches it.
- **A body followed.** The nonce finished and the host was paid. This is the shape `EMPTY_STREAM` and #2137 address. It has two sub-shapes the logs can't explain yet. Most are 1–3 chunks with no content the gateway recognises and `usage_tokens = 0`. A handful are GLM-5.3-Flash streams of thousands of chunks with `usage_tokens` in the thousands. That second shape is why reading positions instead of text matters.

## Signed Chat frames (#2102)

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
- 16 KiB of pending bytes force a flush, so a stream with no blank line can't stall.
- `Every` is capped at 64. A joined group holds tokens, and the gateway cancels a winner that is silent past its inter-chunk stall timeout, so a group must fill well inside that.

### Two knobs on the request

The gateway sends the schedule as `chat_frames: {every, separate}` in the inference request, from its own environment, so a schedule can be tried without a host release:

- `GATEWAY_CHAT_FRAME_EVENTS`: `Every`. `0` means the executor's default (5).
- `GATEWAY_CHAT_FRAME_SEPARATE`: one flush per event, with a signature every `Every` events.

Joining saves body bytes. Separate keeps per-token delivery.

### Gateway

The verifier (`ChatFrameVerifier`, `transport/chat_frame_sign.go`) sits in the Chat read path (`chatFrameReader` and `countingReader`, `transport/rpc_chat.go`). It hashes every inflated byte in order and checks each signature once the stream reaches exactly the byte count that signature names. Byte counts must strictly increase. At most 64 signatures can wait ahead of the inflated stream.

State holds the executor's addresses, not its key, and a 64-byte signature recovers to one key per recovery id. Only the signer's key also verifies the next signature, so the two candidates narrow by themselves as signatures arrive. When the stream ends, the gateway checks the remaining key once against the slot's cold key or a bound warm key (`frameSignerAllowed`, `user/session.go`), by the same rule as state signatures. Nothing that can reach the chain runs while tokens flow, and the gateway never asks about the other recovered key. Asking would cost a bridge call and leave a permanent `warmChecks` entry on every inference. `TestChatFrameVerifier_AsksOnlyAboutTheSigner` and `_DoesNotAskWhileTheStreamIsRead` pin this.

The rest of the gateway side, per A K:

- An invalid signature is logged as `chat_frame_signature_invalid`, and nothing else happens. Acting on it means another participant challenging the stream, which is a separate protocol change.
- A stream with no signed frame, from a host that predates signing, logs nothing.
- `send_completed` logs `chat_frames` and `chat_frames_signed`.
- Gaps between frames are observability only. `maxChunkGap` goes to metrics and the accounting record, and feeds no QoS or punishment decision. A gap of a few events is the batch, not a stall.

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

Same completion, level 2. The bytes are the marshalled `ChatFrame` messages, so each signature costs its 64 bytes plus the field tags and the `stream_bytes` varint.

| Schedule | Frames | Signed | Bytes/token |
|---|---|---|---|
| unsigned, one frame per event (old host on #2095) | 105 | 0 | 18.8 |
| **signed, joined every 5 (default)** | 24 | 23 | **26.2** |
| signed, joined every 10 | 14 | 13 | 18.2 |
| signed, separate, every 5 | 105 | 23 | 34.7 |
| signed, separate, every 64 | 105 | 4 | 21.5 |

The production stream before #2095 was 56.4 bytes/token on this answer. With signing and level 2 together it is 26.2. Executor CPU for the whole answer, gzip and signing included, is about 0.5 ms. On the gateway, a 64-byte signature costs at most two recoveries (about 32 µs) until the key is known, and about 12 µs per verification after that.

In the full testenv stack (`make citest-peerrpc-chat`), 9 of 10 frames per inference carried a signature the gateway verified, with no `chat_frame_signature_invalid`. The unsigned frame is the role delta. The first output's signature covers it.

## Out of scope

- **Token throughput on the gateway, and a dynamic join window** that moves `Every` with it. A K's further proposal. It waits on the throughput measurement (@qdanik).
- **A frame signature as challenge evidence.** One case in #2000's table needs it: a host streams a short text, cuts, then stores and signs a self-consistent long body. The newest valid frame signature proves the served prefix, so a voter could show the stored body diverges from it without a GPU. That is a protocol change on #2000's side.
- **Receipt then silence (#1823).** No Finish, so no challenge applies. The stall timeout (#1798) and the timeout vote cover it.
- **Changing `MsgFinishInference`.** Binding the final stream hash in the Finish would be a state-root change. Nothing here needs it.
- **Gateway-side tokenization.** Per A K, the count stays on the MLNode, and the recount belongs to #2000's CPU voters.

## Open questions

1. **Order.** #2102 is independent and can go now. #2137 waits on #2088. Is that the order?
2. **The two production shapes** with a body but no recognised content. Do the GLM bodies carry positions? If they do, #2137 stops striking them once #2088 votes `NONE`.
3. **The HTTP inference path.** It stays unsigned. Is that acceptable for v7, or should a gateway that wants signatures always use peer RPC?
