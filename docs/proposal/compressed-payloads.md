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

**2. Stop sending the gateway what it discards.** `token_ids`, `prompt_token_ids` and `prompt_logprobs` are the serving engine's own bookkeeping; the gateway strips all three on arrival and no validator opens them, so they are dropped from the chunk before either copy is made — the stored one loses them too.

As shipped, `logprobs` are treated separately from those three: the executor withholds them from the forwarded copy when the client did not ask for them, keeps them in the stored copy always, and stores an asking client's inference whole. What the gateway receives is always one of two signed views of the stored bytes — see [Two signed views](#two-signed-views).

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

**This row is the proposal's figure, not the shipped one.** It assumes `logprobs` leave the wire on every inference. They do not: a client that asks for them gets them, so only the three bookkeeping fields are unconditionally removed. Measured on four captured streamed responses at merge, the shipped saving is 390.8 KB → **379.1 KB, 1.03×**. The disk and validator-fetch figures above are unaffected, since they cover the stored copy.

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
| gateway receives chunks without the four fields | works — the gateway matches `served_hash`; see [Two signed views](#two-signed-views) |
| **old binary reads files written after zstd** | **fails** — `.json.zst` is not found, returns `ErrNotFound` |

The last row is a rollback hazard, bounded by the three-epoch retention window: a node downgraded after writing zstd files cannot serve payloads it wrote while upgraded, and fails validations drawn against them.

It is not a concurrency hazard. `versiond` permits two devshardd versions to overlap only when the storage mode is `postgres`, where payloads do not live in files; in `sqlite` and `hybrid` mode overlap is refused. Two versions never read one payload directory at the same time.

If rollback across this boundary must be supported, the standard two-phase rollout applies: ship the read side first, enable writing in a later release.

## Two signed views

The gateway reads the host's stream for two things: the answer it serves, and the proof of an **error miss** (a host that finished with a terminal error is not paid). Both need the bytes on the wire to be the bytes the executor signed, and the stored copy the validators fetch is not always what the gateway was sent.

`MsgFinishInference` therefore carries two hashes under one `proposer_sig`:

| Field | Covers | Sent to the gateway when |
|---|---|---|
| `response_hash` | the stored payload | the gateway asked for logprobs, or the logprobs optimization is off |
| `served_hash` | `StripForGateway(stored)`: every stored line, with `logprobs` removed from each JSON data line | otherwise |

The executor forwards exactly one of those two documents, never a third marshal. An inference whose gateway asked for logprobs is stored uncompressed, because a position's `bytes` is the decoded token text and cannot be rebuilt from the token id — the client gets the host's own positions.

| Party | Check |
|---|---|
| Executor | hashes the stored bytes and, incrementally, the served projection of the same chunks; signs both |
| Gateway | hashes every non-protocol line it received as the executor enveloped them (and a single relayed body bare, as it was stored), and matches either signed hash on the first Finish the session would accept (`user.CheckServedBinding`, local checks only). A mismatch strikes the host locally: the bytes already reached the client, and the gateway cannot prove what the host sent. A Finish without `served_hash` that matches nothing is a mismatch too |
| Validator | `sha256(fetched) == response_hash`, then `sha256(StripForGateway(fetched)) == served_hash`. A Finish whose `response_hash` or `served_hash` is not 32 bytes never applies: `applyFinishInference` refuses it with `ErrInvalidFinishHash`, on every host and in every session, so leaving `served_hash` out is caught deterministically rather than by sampling |
| Error-miss verifier | accepts the gateway's rebuilt payload when it hashes to either signed view; the vote still binds `response_hash` |

This closes both gaps the earlier gating opened: an executor cannot finish one answer and stream another, and an error miss is provable with the optimization on. Two error misses stay unprovable, as before: a JSON body relayed to a streaming gateway, and a reconnect replayed from the cached body. The executor stored a bare body there, while the gateway's error-miss payload is always an envelope; the binding check covers both shapes, the error-miss proof does not. The gateway judges the Finish that arrived with the stream; an executor that sends a different Finish later, through another response or gossip, is not re-checked against the one the session applies — the validator's `served_hash` check is what binds the applied Finish.

`served_hash` enters the state root through `InferenceRecordProto`, and a Finish without it does not apply, so the protocol is bumped to v6 (`DevshardStateRootAndProtocolVersion`, `DefaultProtocolVersion`). The sealed-inference rows in SQL do not carry it; they serve observability only.

## Configuration

Both compression steps ship off. The figures above are what an operator gets by opting in, not what a node does out of the box.

| Knob | Default | Effect |
|---|---|---|
| `DEVSHARD_PAYLOAD_ZSTD_ENABLED` | `false` | write payload files zstd-encoded. Reading accepts both suffixes either way, so the gate governs writing alone |
| `DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED` | `false` | the executor's own default. Off, it stores whole and forwards the stored bytes; `true` compresses what it stores and forwards the served view to a gateway that did not ask for logprobs. Either way the gateway receives a signed view |
| `GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE` | unset | what the gateway asks executors for, per inference. Unset says nothing and leaves every executor its own default |

The gateway's override also moves at runtime, without restarting the gateway or any host:

```
POST /v1/admin/settings  {"logprobs_optimization": {"enabled": false}}
POST /v1/admin/settings  {"logprobs_optimization": {}}          # clear, defer to the hosts again
```

It rides the inference request as `logprobs_optimization_override`, inside the body the gateway signs, so a host can tell who asked. A gateway older than the field sends nothing, and executors keep their own default.

## Non-goals

**Validation is not modified.** Every consumer of logprobs reads `Token` and `TopLogprobs[].{Token, Logprob}` only — `GetEnforcedTokens` for both response shapes, `CompareLogits`, `positionDistance`, `HasNonNumericTokens`, `IsEmptySentinelTokens`. The dropped fields have no readers, so the algorithm, its thresholds and its verdicts are untouched.

**Nothing is approximated.** Values are dropped whole or kept exactly. An earlier draft quantised logprobs to float16 — 4.9e-04 relative error, 2.3e-05 on the verdict — and was discarded: a lossless scheme reaches 20.0 B/pos against 10.3 B/pos lossy, which does not justify introducing an error into the number that decides an inference's fate.

**gzip is not applied to the inference stream here.** It is scoped to the payload route, on the reading that a streamed response would buffer. Measured since against echo v4.15.1 that reading does not hold, and the transit legs are compressed by [gzip-in-transit.md](gzip-in-transit.md).

## Verification

- Round-trip: every token, every alternative, every order, every float identical.
- Verdict: `CompareLogits` returns bit-identical similarity from compressed and full content, across 4096 positions × 3 noise levels × 2 sentinel shares. Asserted as exact equality, not tolerance.
- End to end: `ExecuteValidation` run against a compressed payload — parse, enforced tokens, replay, compare, threshold — and the enforced tokens the replay is pinned to are the executor's own ids.
- Redundancy: all 100 responses / 409 600 positions in the reference corpus pass the pre-drop check.
- Bounds: a payload file that inflates past 256 MiB is refused rather than read, asserted with a real bomb. The bound turns an unbounded decompression into one failed read rather than an OOM; the largest legitimate payload is ~90 MiB, a 10 MiB request at the body cap plus 300k output tokens.
- Divergence: an inference driven through a streaming stub asserts both outputs at once — the gateway receives no logprobs for a client that did not ask for them, and the payload stored from the same stream still replays the executor's token path with its alternatives intact.
- Mutation testing: 13 mutants, 11 killed. The two survivors both concerned gzip being scoped to one route rather than the whole group, which no test could distinguish at the time. That scoping has since been replaced: the inference route compresses too, and `TestInferenceRouteStreamsEachFrameAsItIsFlushed` distinguishes the mounts.
