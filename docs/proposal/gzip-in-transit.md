# Proposal: gzip Between the Gateway and a devshard Host

Companion to [compressed-payloads.md](compressed-payloads.md). Operators want [v6-deploy-test-plan.md](../../devshard/docs/v6-deploy-test-plan.md) instead: this is the design record. That proposal made the artifacts smaller by dropping fields and by compressing them at rest and on the payload route. This one compresses the two legs that carry every inference: the request the gateway sends a host, and the SSE answer the host streams back.

## Goal / Problem

Both legs travel uncompressed. Two things make that expensive beyond the obvious.

**The response.** After the four-field strip, what remains is almost entirely per-chunk housekeeping — `id`, `object`, `created`, `model` and the `choices` wrapper — repeated once per token. That is the most compressible shape a stream can have, and it is paid on every inference rather than on a sampled fraction.

**The request.** `PayloadJSON.Prompt` is `[]byte`, so the normalized chat body travels base64 inside the envelope: a flat 1.33x on every prompt, before any content-dependent redundancy.

## Proposal

**1. Compress the response, negotiated.** The inference route serves gzip when the caller asks. No client changes: the transport's `http.Transport` sets no `Accept-Encoding` of its own and leaves `DisableCompression` off, so Go already asks for gzip and unwraps it.

**2. Decompress the request, ahead of authentication.** The signature is `sha256(escrow_id || body || ts_be8)` over the body with no canonicalisation, and the same bytes feed the height-sync request-leg evidence digest a host retains for offline dispute. So the sender signs the plaintext and compresses after; the host decompresses before auth reads the body. Then no hash in the protocol can tell that compression happened.

**3. Bound what a stream decodes to.** `MaxSSEStreamBytes` (256 MiB) caps a whole inference stream. The per-line cap does not see a stream that stays small per line and never ends, and compression makes that cheap for an untrusted executor.

## Impact

Measured on a stand that replays the real logprob corpus in `common/validation/testdata`, counting bytes on the socket with headers and chunked framing included.

**Response leg**, one chunk per token:

| tokens | shape | plain | gzip | |
|---:|---|---:|---:|---:|
| 1024 | after the strip | 223.3 KiB | 45.8 KiB | 4.88x |
| 4096 | after the strip | 892.5 KiB | 181.8 KiB | **4.91x** |
| 4096 | before the strip | 2.3 MiB | 637.0 KiB | 3.77x |

gzip does better *after* the strip than before it: the two steps multiply rather than compete, because what the strip leaves behind is the repeated housekeeping.

**Request leg**, 256 KiB prompts:

| prompt | plain | gzip | |
|---|---:|---:|---:|
| source code | 372.4 KiB | 125.4 KiB | 2.97x |
| HTML-heavy | 455.2 KiB | 118.6 KiB | 3.84x |
| prose | 346.7 KiB | 167.8 KiB | 2.07x |
| incompressible | 341.9 KiB | 242.7 KiB | 1.41x |

The last row is the floor, not an anomaly: gzip reclaims the base64 tax whatever the content.

**Per inference**, a 4096-token answer against a 256 KiB prompt: 1.2 MiB to 307.3 KiB, **4.12x**. Over 100 000 inferences, 120.63 GiB to 29.30 GiB.

## Cost

gzip level 1, single core: 8.8 ms per MiB on the request leg. Level 6 reaches 4.07x against level 1's 3.22x for 23.3 ms per MiB — not worth it on the leg a gateway pays for every request at once. Set against an inference that occupied a GPU for seconds, neither is material.

## Compatibility

| direction | result |
|---|---|
| old client <- new host | works — gzip is negotiated by `Accept-Encoding` |
| new client <- old host | works — the header is ignored and the answer arrives plain |
| old host <- new sender compressing | **fails** — the host verifies the compressed bytes, recovers a stranger, and answers 403 |

That last row is why the write side ships off. `DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES` turns it on once every host in the roster carries the read side; the response leg has no such constraint and needs no gate.

The failure mode deserves naming because it does not look like what it is: ECDSA recovery never fails on the wrong bytes, it recovers a different address. A sender compressing to a host that cannot decompress sees **403 sender-not-in-group**, which reads as a key or registration problem.

## Non-goals

**The flush cadence is not changed.** Flushing per token forces a deflate block boundary per token, and most of what the stream could compress to goes into block headers: the same 4096-token answer reaches 12.3x flushing every 4 tokens and 4.91x flushing every one. Keeping the opening tokens unbatched and batching the tail measured 17x with time-to-first-byte unchanged. That trades against the race — the crown goes to the first attempt producing content — so it belongs in its own change, with its own evidence.

**The gateway's own client leg is not compressed.** The gateway answers clients through `net/http` rather than echo, so it needs its own wrapper.

## What this costs an unauthenticated caller

Decompression has to run above authentication, and the rate limiter lives inside it, so an unnamed caller now buys the work of inflating up to the 10 MiB body cap for as little as ~10 KiB on the wire. The work per request is bounded exactly as before — the cap is enforced on the decoded bytes, so nothing inflates past it — but the bandwidth an attacker spends to buy it drops by about three orders of magnitude. The edge `client_max_body_size` still bounds what arrives. This is a deliberate trade, named here because the ratio changed even though the bound did not.

## Verification

- The signature survives: a request signed as plaintext and sent compressed verifies through the real routes and yields the signer; the same request to a host without decompression yields a stranger.
- The bound is on the decoded body: a bomb one byte over the 10 MiB cap, under 1 MiB on the wire, is refused 413.
- The stream is not held back: with one write and one flush per frame, the bytes on the wire at the first flush already decode to the first frame and not the second. Asserted against a recorder that snapshots at each flush.
- A body that claims gzip and is not answers 400 — for a wrong magic number and for a header cut short. This middleware runs before any caller is named, so it must not let an unnamed one spend the host's 5xx budget. An empty body carries no header to be wrong about and is passed through to be judged by auth like any other unsigned request.
- The full e2e suite passes with both mounts in place, including in the stub host the stand runs.

- The cadence holds through the real handler, not a stand-in: an inference driven end to end through `HandleInference` with `Accept-Encoding: gzip` has some event readable on the wire before the stream finishes. Verified by mutation — silence every flush on the path and it fails.
- A stream cut before its trailer is reported as a truncation rather than a generic transport error. Compression makes that reachable: an answer whose headers were flushed and whose body never came is an unterminated gzip stream, which reads as `unexpected EOF`. That is a naming fix, not a scoring one — `Send` returns a stream-parse error without observing it, so no quarantine strike was ever at stake — but `sse_truncated` says what happened and `eof_transport` does not.

Two mutations did not kill anything, and both are recorded rather than papered over. Raising the compressor's `MinLength` leaves the streaming test green, because echo's `Flush` forces the threshold regardless. And `TestInferenceRouteLeavesAPlainClientAlone` pins echo's content negotiation rather than any line of ours; it is kept as a statement of the negotiated contract, not counted as coverage.
