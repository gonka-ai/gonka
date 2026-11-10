# Proposal: Compressed Inference Payloads

Two words are used precisely here. **Compressing a payload** means dropping fields nothing reads, leaving readable JSON. **zstd** and **gzip** are named explicitly wherever byte-level compression is meant. They are separate steps and their effects multiply.

## Goal / Problem

Offchain payloads moved inference artifacts off the chain into per-executor storage, fetched over HTTP by a validator when an inference is sampled. The artifacts themselves were never reduced: an executor stores and serves the serving engine's response verbatim.

A response is almost entirely logprobs. Measured on a 4096-token answer (`MiniMaxAI/MiniMax-M2.7`, top_k=5, 123 187 prompt tokens):

| Part | Size | Share |
|---|---|---|
| whole response | 1467.8 KB | 100% |
| `choices[].logprobs` | 1272.9 KB | **86.7%** |
| answer text (`content` + `reasoning`) | 16.8 KB | 1.1% |

Inside that block, 318 bytes per position carry 246 bytes of fields validation reads. The rest — 35% — is fields no validator opens. JSON syntax accounts for a further 56% of the block: on 20 480 alternatives per answer, the keys `"token"`, `"logprob"`, `"bytes"`, `"top_logprobs"` are repeated in full.

Payloads are retained for three epochs and served uncompressed both on disk and on the wire.

## Proposal

Three independent changes.

**1. Store only the fields validation reads.** Three fields are dropped from `choices[].logprobs.content[]` before the response is hashed and stored:

| Field | Why it is redundant |
|---|---|
| `top_logprobs[].bytes` | the ASCII of its own `token` — `"258"` is stored as `[50,53,56]` |
| `logprobs[].bytes` | the decoded token text; no validator reads it |
| `logprobs[].logprob` | equals `top_logprobs[rank].logprob` for the same token |

Verified across 100 responses / 409 600 positions: zero exceptions. Every position is checked before its fields are dropped: an alternative whose bytes are not its own token's digits, or a position whose logprob no alternative explains, aborts the rewrite and the executor stores that response whole. A serving engine that stops holding the invariant costs bytes, never data.

The remaining shape is OpenAI's own, with the same field names, and parses back into the existing `completionapi.Logprob` with no special case.

**2. Stop sending the gateway what it discards.** Four fields — `logprobs`, `token_ids`, `prompt_token_ids`, `prompt_logprobs` — are removed from each chunk the executor forwards. The gateway strips all four on arrival, so carrying them buys nothing. The executor keeps every chunk whole for its own accumulation, so what it stores and hashes is unaffected: the strip applies only to the bytes leaving for the gateway.

**3. Apply zstd at rest and gzip in transit.** Files are written zstd-encoded (`{inferenceId}.json.zst`); both suffixes are read, so files written by earlier versions stay readable. Writing is gated by `DEVSHARD_PAYLOAD_ZSTD_ENABLED`, default off, because a node that writes `.zst` hides those payloads from an older binary reading the same directory. The payload route serves gzip, negotiated by `Accept-Encoding` — the validator's Go client already asks for it and unwraps it, so no fetcher changes.

## Impact

Measured on a real request/response pair: 533 KB prompt, 1321 KB response, 123 187 prompt / 4096 completion tokens.

**Disk, per inference:**

| | Size | Reduction |
|---|---|---|
| today | 2414 KB | 1.0× |
| compressed | 1771 KB | 1.4× |
| compressed + zstd | **358 KB** | **6.7×** |

**Network, executor to gateway, per inference.** This is the only figure here paid on every inference rather than on a sampled fraction. Measured on the SSE stream the same answer produces — one chunk per token, each repeating the chunk housekeeping OpenAI's format requires:

| | Size | Reduction |
|---|---|---|
| today | 2313.0 KB | 1.0× |
| four fields removed | **768.1 KB** | **3.0×** |

| Inferences | Before | After | Saved |
|---|---|---|---|
| 10 000 | 22.1 GiB | 7.3 GiB | **14.7 GiB (67%)** |
| 100 000 | 220.6 GiB | 73.2 GiB | **147.3 GiB (67%)** |

What remains after the strip is almost entirely per-chunk housekeeping — `id`, `object`, `created`, `model` and the `choices` wrapper, repeated 4096 times. That is OpenAI's streaming format, not something this proposal changes.

**Network, per validator payload fetch:**

| | Size | Reduction |
|---|---|---|
| today | 2414 KB | 1.0× |
| compressed | 1771 KB | 1.4× |
| compressed + gzip | **454 KB** | **5.3×** |

**On the logprobs block alone**, across 20 responses / 81 920 positions:

| | Per position | Reduction |
|---|---|---|
| raw JSON | 340.2 B | 1.0× |
| zstd only, fields untouched | 35.1 B | 9.7× |
| compressed only | 209.6 B | 1.6× |
| compressed + zstd | **20.0 B** | **17.0×** |

The two steps multiply rather than compete: zstd removes byte-level repetition, dropping fields removes redundancy no compressor can see, because it cannot know one field is a function of another.

**At volume**, from the same per-inference figures:

| Retained inferences | Disk before | Disk after | Saved |
|---|---|---|---|
| 10 000 | 23.0 GiB | 3.4 GiB | **19.6 GiB (85%)** |
| 100 000 | 230.2 GiB | 34.1 GiB | **196.1 GiB (85%)** |

| Payload fetches | Network before | Network after | Saved |
|---|---|---|---|
| 10 000 | 23.0 GiB | 4.3 GiB | **18.7 GiB (81%)** |
| 100 000 | 230.2 GiB | 43.3 GiB | **186.9 GiB (81%)** |

Disk counts inferences held in the three-epoch retention window at any moment. Network counts payload fetches, which happen only when an inference is sampled for validation, not once per inference.

Absolute figures come from one large request; the ratios hold across sizes, since prompt and logprobs respond to zstd at similar rates.

## Cost

Per inference, on the same pair (Apple M-series, single core):

| Step | Time | Throughput | Allocations | Runs |
|---|---|---|---|---|
| compress the response | 30.0 ms | 44 MB/s | 25.2 MB / 531 275 | once per inference, on the executor |
| zstd encode | 7.1 ms | 257 MB/s | 2.6 MB / 11 | once per inference, on the executor |
| zstd decode | 2.4 ms | 748 MB/s | 1.8 MB / 2 | once per payload read |
| gzip encode | 9.7 ms | 187 MB/s | 2.3 MB / 33 | once per payload fetch |

Set against an inference that occupied a GPU for seconds, 37 ms of executor CPU is not material. The allocation figure is: compressing walks the payload as `map[string]any` with `json.Number`, which allocates one object per position and per alternative — 531 275 for a 4096-token answer, against 11 for zstd beside it. At sustained throughput that is the dominant garbage this change introduces.

Decoding the logprobs into their typed struct instead of a generic map removes almost all of it. The cost is that numbers would then be re-emitted as Go formats a float64 rather than with the digits the engine sent — values identical, digits not guaranteed. That trade is available and not taken here.

## Compatibility

| Direction | Result |
|---|---|
| old validator ← new executor (compressed payload) | works — hash covers the stored bytes, dropped fields have no readers |
| new validator ← old executor (full payload) | works — extra fields are ignored |
| new binary reads files written before zstd | works — both suffixes are read |
| fetcher that does not send `Accept-Encoding` | works — gzip is negotiated |
| gateway receives chunks without the four fields | works — it strips all four on arrival and reads none of them |
| **old binary reads files written after zstd** | **fails** — `.json.zst` is not found, returns `ErrNotFound` |

The last row is a rollback hazard, bounded by the three-epoch retention window: a node downgraded after writing zstd files cannot serve payloads it wrote while upgraded, and fails validations drawn against them.

It is not a concurrency hazard. `versiond` permits two devshardd versions to overlap only when the storage mode is `postgres`, where payloads do not live in files; in `sqlite` and `hybrid` mode overlap is refused. Two versions never read one payload directory at the same time.

If rollback across this boundary must be supported, the standard two-phase rollout applies: ship the read side first, enable writing in a later release.

## Non-goals

**Validation is not modified.** Every consumer of logprobs reads `Token` and `TopLogprobs[].{Token, Logprob}` only — `GetEnforcedTokens` for both response shapes, `CompareLogits`, `positionDistance`, `HasNonNumericTokens`, `IsEmptySentinelTokens`. The dropped fields have no readers, so the algorithm, its thresholds and its verdicts are untouched.

**Nothing is approximated.** Values are dropped whole or kept exactly. An earlier draft quantised logprobs to float16 — 4.9e-04 relative error, 2.3e-05 on the verdict — and was discarded: a lossless scheme reaches 20.0 B/pos against 10.3 B/pos lossy, which does not justify introducing an error into the number that decides an inference's fate.

**gzip is not applied to the inference stream.** It is scoped to the payload route; on a streamed response it would buffer chunks.

## Verification

- Round-trip: every token, every alternative, every order, every float identical.
- Verdict: `CompareLogits` returns bit-identical similarity from compressed and full content, across 4096 positions × 3 noise levels × 2 sentinel shares. Asserted as exact equality, not tolerance.
- End to end: `ExecuteValidation` run against a compressed payload — parse, enforced tokens, replay, compare, threshold — and the enforced tokens the replay is pinned to are the executor's own ids.
- Redundancy: all 100 responses / 409 600 positions in the reference corpus pass the pre-drop check.
- Bounds: a payload file that inflates past 256 MiB is refused rather than read, asserted with a real bomb. The bound turns an unbounded decompression into one failed read rather than an OOM; the largest legitimate payload is ~90 MiB, a 10 MiB request at the body cap plus 300k output tokens.
- Divergence: an inference driven through a streaming stub asserts both outputs at once — the gateway receives no logprobs, and the payload stored from the same stream still replays the executor's token path with its alternatives intact.
- Mutation testing: 13 mutants, 11 killed. The two survivors both concern gzip being scoped to one route rather than the whole group; echo leaves a handler-written body uncompressed either way, so no test can distinguish them. That scoping rests on where the middleware is written, not on a test.
