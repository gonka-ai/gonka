# Chat frame signatures and chunking

**Status:** design. The code on this branch is [PR #2102](https://github.com/gonka-ai/gonka/pull/2102) (`2d0ebf5`). The behavior below is the design this branch implements on top of that. Steps and status: [chat-frame-sparse-signature-plan.md](./chat-frame-sparse-signature-plan.md).

A Chat stream is cut into chunks. Some chunks carry a signature. The signature covers every uncompressed byte from the start of the stream through the end of that chunk, and the number of content tokens in that same prefix. A later dispute sends the token count, the hash, and the signature. It does not send the gzip body.

## Chunks

One vLLM event is one token: a `data:` line ended by a blank line. The executor drops an event that has no content, no reasoning, and no tool call. A role-only delta and an empty delta are not tokens. They do not open a chunk, do not fill one, and do not add to the token count. A `data:` line that is not JSON still counts as a token, so a junk line cannot skip the schedule.

A chunk is sent only once the next content token has already arrived. That next token is not part of the chunk. It opens the following one.

- The first chunk is the first content token. It is sent when the second content token arrives. It does not wait to fill a group. Time to the first byte on the wire is time to the second token.
- Each later chunk gathers `Join` content tokens (default 5) and is sent when the content token after that group arrives.
- `[DONE]` is not a content token and does not open a chunk. It is written onto the chunk already held, and that chunk is sent immediately, even when it has fewer than `Join` tokens. One content token followed by `[DONE]` is one chunk. `[DONE]` with nothing held is not sent.
- An error event closes and signs the held chunk the same way `[DONE]` does.

A chunk with no content token, or a chunk that is only `[DONE]`, is a miss. A correct executor does not produce one. This design does not open a challenge from it. #2102 logs a bad signature and stops there, and that boundary stays.

`Join` holds tokens, so it is capped at 64. That is the inter-chunk stall budget from #2102.

## Signatures

`SignEvery` counts chunks, not tokens. Default 13: chunks 13, 26, 39, and so on are signed. The tail is always signed. The tail is the chunk that contains `[DONE]` or an error, and the bytes still open at `Close`.

Chunks between signatures carry gzip only. `sig` is empty and there is no token count. The next signature's hash includes them, because the hash runs over every uncompressed byte from the start.

Thirteen chunks is the earlier "sign about every 65 tokens" interval, counted in chunks so the signature lands on a frame boundary. The first chunk is one token, so chunk 13 attests `1 + 12×5 = 61` content tokens under the defaults. The number in the signature is that real total.

`SignEvery` does not hold tokens. Its cap is 4096.

The signed bytes are the domain string followed by the protobuf message. `||` here is concatenation.

```text
"devshard.chatframe.v1" || protobuf(ChatFrameSignatureContent{
  escrow_id,
  inference_id,   // inference nonce, not the diff nonce
  content_hash,   // running SHA-256 of the uncompressed prefix
  token_count     // content tokens in that prefix
})
```

`content_hash` is seeded with the domain, the escrow, the inference, and the prompt hash, then every uncompressed byte through the end of this chunk. The content token held back to open the next chunk is not hashed and not counted. `seq` and `stream_bytes` are not in the message. `stream_bytes` may stay on the frame as an unsigned cursor. A cursor that names the wrong prefix fails the hash check.

The signature itself is 64 bytes, `R` then `S`. With the protobuf tag and length, a signed chunk grows by about 68 bytes. On #2102's measured QwQ answer, signing every joined group made that about half of 26.2 bytes per token. Signing every 13th chunk, plus the tail, is where that cost goes.

`token_count` is also a cleartext field on the signed frame. The verifier copies it into the preimage. Changing it without re-signing fails verification. The gateway does not recount the SSE in order to accept the frame. The signature freezes the executor's count.

The verifier still does what #2102 does around the key: a non-empty signature that is not 64 bytes is rejected before it is queued, each signature must cover a strictly longer prefix, and the recovered key is checked against the executor once, at the end of the stream. An unsigned stream is not a failure.

## Evidence

Someone who already knows which inference this is checks the signature without the gzip body.

| Piece | Role | Size |
| --- | --- | --- |
| `token_count` | cumulative content tokens in the hashed prefix | uint64 |
| `content_hash` | identity of that prefix | 32 bytes |
| `sig` | executor signature over the message below | 64 bytes |
| `escrow_id`, `inference_id` | inside the signed protobuf, supplied by the vote or the request | string, uint64 |

The verifier rebuilds the protobuf, prefixes `devshard.chatframe.v1`, and checks `sig`. The ids are inside the signed message so the same count, hash, and signature cannot be shown for a different inference. The hash seed also contains them, but a verifier without the chunk never recomputes that seed.

`ChatFrameEvidence` in `devshard/transport/chat_frame_sign.go` holds those five values. `Preimage()` returns the signed bytes. `Verify` checks `sig` against a public key. The gzip chunk is not an input.

## Example

`Join = 2`, `SignEvery = 3`, seven content tokens, then `[DONE]`.

| When | What is sent | Signed | `token_count` |
| --- | --- | --- | --- |
| token 1 arrives | nothing; it is held | | |
| token 2 arrives | chunk 1: token 1 | | |
| tokens 3–4 | held as tokens 2–3 | | |
| token 4 arrives | chunk 2: tokens 2–3 | | |
| tokens 5–6 | held as tokens 4–5 | | |
| token 6 arrives | chunk 3: tokens 4–5 | yes, chunk 3 | 5 |
| token 7, then `[DONE]` | chunk 4: tokens 6–7 and `[DONE]` | yes, tail | 7 |

Chunk 3's hash is every uncompressed byte of chunks 1–3. Token 6 is not in it. Chunk 4's hash adds tokens 6–7 and `[DONE]`, and the signature covers that whole prefix. Chunks 1 and 2 have an empty `sig`.

The same rules at the defaults (`Join = 5`, `SignEvery = 13`), ten tokens, then `[DONE]`:

| Chunk | Body | Signed |
| --- | --- | --- |
| 1 | token 1, sent when token 2 arrives | |
| 2 | tokens 2–6, sent when token 7 arrives | |
| 3 | tokens 7–10 and `[DONE]` | yes, tail. `token_count` 10 |

No chunk is signed before the tail, because chunk 13 is never reached.

## Where this sits in the code

#2102, now on this branch, is the mechanism this design changes.

| Piece | Where it is now | What the design does with it |
| --- | --- | --- |
| Frame on the wire | `ChatFrame` in `devshard/proto/devshard/transport/v1/session.proto`: `chunk`, `sig`, `stream_bytes`, `token_count` | `token_count` is set on a signed frame. `stream_bytes` stays an unsigned cursor. |
| Signed message | `ChatFrameSignatureContent` in `devshard/proto/devshard/v1/diff.proto`: `escrow_id`, `inference_id`, `content_hash`, `token_count` | The verifier rebuilds that preimage. Each verified `token_count` is on `ChatFrameCheck.TokenCounts`. Changing the count without re-signing fails verification. |
| Domain | `devshard.chatframe.v1` in `devshard/types/signed_preimage.go` | Unchanged. |
| Running hash | `newChatStreamHash` in `devshard/transport/chat_frame_sign.go` | Unchanged. The held-back token is not fed in until its chunk is sent. |
| Schedule | `ChatFrameSchedule.Join` and `SignEvery` in `devshard/host/host.go`. Gateway env `GATEWAY_CHAT_FRAME_JOIN` and `GATEWAY_CHAT_FRAME_SIGN_EVERY`. | The sink applies both. `sign()` takes the content-token count, and that count is what the preimage binds. |
| Cutting and signing | `ChatFrameSink` in `devshard/transport/chat_gzip.go` | Empty choices are dropped. One content token is held back. Chunk `SignEvery`, `2×SignEvery`, and the tail are signed. |
| Evidence | `ChatFrameEvidence` in `devshard/transport/chat_frame_sign.go` | `Preimage()` is the signed bytes. `Verify` checks `sig` from the two ids, the hash, and the count. The gzip chunk is not an input. |

Gzip stays level 2, one member across flushes. The warm-key check at the end of the stream stays.
