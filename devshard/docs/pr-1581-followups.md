# PR #1581 follow-ups — spool + local fold

Status: follow-up plan for [gonka-ai/gonka#1581](https://github.com/gonka-ai/gonka/pull/1581) (`qd/obsrv`).
Source of the missing pieces: local branch `ak/only-streaming-to-gateway`.
Spool design: [spool-shared-library.md](./spool-shared-library.md).
Local always-stream plan: [gateway-always-stream-upstream-plan.md](./gateway-always-stream-upstream-plan.md).

This file is **not** a re-review of #1581's accounting, findings, chunk cadence, Kimi thinking-template, or logging work. Those can land as-is. It is the list of always-stream body-handling follow-ups that should go **on top of** #1581, taken from the local branch.

## Why a follow-up exists

#1581 and `ak/only-streaming-to-gateway` both force every host call to `stream: true` and reassemble a JSON body for clients that asked for one. They are not the same size because they are not the same stack:

| | #1581 | Local branch |
|---|---|---|
| Always-stream + assemble | `stream_only.go` (~385 loc) + `bytes.Buffer` | `stream_aggregate.go` + `spool.Buffer` |
| Host LiveStream / same-nonce reconnect | not in scope | most of the extra diff |
| Observability / accounting | in the PR | not on this local branch |

The extra local lines are mostly reconnect/HA, tests, and docs. The follow-up that belongs on #1581 is the **gateway aggregate path**: spool for the wire body, and the local fold instead of `assembleSSEBody`.

Keep from #1581 (do not undo):

- Two-pipeline split: `upstreamChatRequestPipeline()` forces stream; gateway admission/cache stays on the client-shaped body.
- Deleting the non-stream timeout / reduced-`max_tokens` ladder (dead once every upstream call is a stream).
- `replaceNonFiniteNumbers` (vLLM logprobs emit `-Infinity` / `NaN`; the local fold currently drops those chunks).

## Summary

| ID | Adopt | Heals | Gate |
|---|---|---|---|
| [U1](#u1--adopt-spoolbuffer-for-the-aggregated-wire-body) | `devshard/spool` + `aggregateResponseBuffer` | Unbounded RAM, no byte ceiling, `ReadAll` of a large SSE body | **Must** |
| [U2](#u2--replace-assemblessebody-with-the-local-fold) | `stream_aggregate.go` + logprobs NDJSON | Silent truncation, `chat.completion` short-circuit, OpenAI drift, forced-logprobs trees | **Must** |
| [U3](#u3--port-replacenonfinitenumbers-into-the-local-fold) | PR helper into `completionFolder.ingest` | Local fold dropping vLLM non-finite chunks | **Must** (with U2) |
| [U4](#u4--keep-a-kill-switch) | `ForceUpstreamStreaming` (local) | Unconditional force with no rollback | **Should** |

Spool alone does **not** heal U2. The fold is a separate layer; see [What spool does not heal](#what-spool-does-not-heal).

---

## U1 — Adopt `spool.Buffer` for the aggregated wire body

**#1581 today.** `handleNonStreaming` accumulates the winner SSE in a plain `bytes.Buffer` with no cap, then passes `buf.Bytes()` into `assembleSSEBody`. Peak RAM is the full SSE (which always carries forced `logprobs` + `top_logprobs: 5`) plus the merge tree plus the encoded JSON. The only backstop is `StreamingAttemptHardTimeout` (30m). `inf.outputBytes` is counted for logs, not enforced.

**Take from local.** `handleAggregated` writes into `aggregateResponseBuffer` → `spool.Buffer`:

- RAM until `GATEWAY_AGGREGATE_MAX_MEMORY_BYTES` (default 2 MiB).
- Then spill to an unlinked `agg-*` inode under `GATEWAY_AGGREGATE_SPOOL_DIR`.
- Per-request ceiling `GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES` (default 16 MiB) → `ErrAggregateResponseTooLarge`, not a truncated 200.
- Process cap `GATEWAY_AGGREGATE_MAX_CONCURRENT_SPOOLS` (default 64) via shared `Slots`.
- Spill failure degrades to RAM under `GATEWAY_AGGREGATE_MAX_DEGRADED_RAM_BYTES`; otherwise stay at the memory ceiling.
- Production fold reads through `OpenReader()` (line scan). Never `ReadAll` a spilled file.

Call-site sketch is in [spool-shared-library.md §6.3](./spool-shared-library.md). Package surface: `Dir`, `File`, `Buffer`, `Budget`, `Slots`. Do **not** pull `Index` / LiveStream into this follow-up.

**Tests to bring:** `aggregate_response_test.go`, `testenv/citest/gateway_aggregate_spill_test.go`.

---

## U2 — Replace `assembleSSEBody` with the local fold

This is the other must-have. Swapping the buffer (U1) and keeping `assembleSSEBody` leaves the correctness bugs below in place.

**#1581 today.** Generic `map[string]any` merge in `stream_only.go`:

- `maxAssembledEvents = 65_536` → `break`, return HTTP 200 with whatever merged so far.
- `maxTopLevelFields = 64` / `maxIndexedElements = 256` drop silently.
- An event with `"object": "chat.completion"` sets `complete` and `break`s, discarding prior deltas.
- Forced logprobs are materialized as maps, JSON-encoded, then `filterClientInternalFields` unmarshals the whole body again to `delete` keys.
- `filterClientInternalFields` omits `logprobs` rather than emitting `null`.
- `encodeCompletion` does not default `message.role` to `"assistant"`.

With forced logprobs, chunks track tokens roughly 1:1, so a long generation can hit 65k events and look complete.

**Take from local.** `aggregateSSEStreamReader` + `completionFolder` (`stream_aggregate.go`, `stream_aggregate_logprobs.go`):

| Concern | Local fold |
|---|---|
| Size limit | Shared `foldBudget` (same 2 MiB / 16 MiB ceilings) → `ErrAggregateFoldTooLarge` error body |
| `chat.completion` | Single already-complete payload is passed through; mixed chunk + completion events keep folding |
| Forced logprobs | Discarded at decode when the client did not ask (`decodeChoiceForFold`); kept as NDJSON + disk spill when they did |
| OpenAI shape | `choices[].logprobs: null` when none; `message.role` defaults to `"assistant"`; `finish_reason: null` if missing |
| Host errors | Sole error → passthrough; trailing error after terminal finish → drop + counter; mid-stream without finish → fail closed |
| Fan-out | Choice index outside `[0, 8)` / tool-calls > 64 / extras > 64 dropped and counted, not used as a token-text cap |
| Peak RAM | One SSE line (≤1 MiB) + fold state + marshaled output |

**Tests to bring:** `stream_aggregate_test.go`, `stream_intent_test.go`, `stream_usage_suppress_test.go`, `testenv/citest/gateway_forced_upstream_stream_test.go`.

After U2, `stream_only.go` / `assembleSSEBody` should go away. Do not keep both assemblers.

---

## What spool does not heal

`devshard/spool` is scratch storage (dir lifecycle, temp files, RAM-then-disk `Buffer`, byte budgets). The SSE fold is deliberately **not** in that package.

If U1 lands without U2, #1581 still:

1. **Silent truncation.** `maxAssembledEvents` `break`s and returns 200. Spool's 16 MiB ceiling may abort some huge bodies first, but a long logprob stream can stay under 16 MiB and still exceed 65k events.
2. **`chat.completion` short-circuit.** Unrelated to buffering. Harmless against well-behaved vLLM (`chat.completion.chunk`), unguarded discard of accumulated content otherwise.
3. **OpenAI drift** (`logprobs` key missing, no default `role`). Post-fold emit policy; spool never sees it.
4. **Forced-logprobs trees.** The fold still builds `map[string]any` for every token unless U2 skips them at decode.

U1 without U2 is still worth doing (DoS / RAM), but it is not a substitute for U2.

---

## U3 — Port `replaceNonFiniteNumbers` into the local fold

#1581 rewrites bare `-Infinity` / `Infinity` / `NaN` to `null` outside string literals before JSON decode, so a logprob chunk is not dropped whole.

The local `ingest` path is `json.Unmarshal(p, &raw)` into `map[string]json.RawMessage`. That rejects the same chunk, **including the content delta**, even when logprobs are being discarded.

When U2 lands, copy the PR helper (or an equivalent) onto the ingest path so a non-finite logprob cannot erase the token. Add a fold test with a content + `-Infinity` logprob chunk.

---

## U4 — Keep a kill switch

#1581 forces stream unconditionally in `upstreamChatRequestPipeline()`. Local gates it on `RedundancySettings.ForceUpstreamStreaming` (default `false`), snapshotted per request so a mid-flight admin flip cannot split `stream` from `stream_options`.

The signed prompt includes the forced `stream: true` / `include_usage`. Ship with a flag so the change can be rolled out and rolled back without a revert. Once always-stream is the only production path, the leftover non-stream ladder deletion from #1581 is the right end state.

---

## OpenAI drift (why U2 emit policy matters)

OpenAI's non-streaming choice always has:

```json
{
  "index": 0,
  "message": { "role": "assistant", "content": "Hello" },
  "logprobs": null,
  "finish_reason": "stop"
}
```

**`logprobs` omitted vs `null`.** The gateway forces logprobs upstream, then strips them for clients who did not ask. #1581 `delete`s the key. Local emits `"logprobs": null` (F10). Most SDKs treat missing and `null` as falsy; generated clients and `'logprobs' in choice` do not. Schema tests against a direct OpenAI/vLLM non-stream body will fail only on the aggregated path.

**Missing `message.role`.** vLLM usually sends `delta.role` on the first chunk; later chunks omit it. #1581 copies `delta` → `message` and does not default `role` if that first chunk never arrived (backend omit, parse drop, short-circuit). `role` is required on `ChatCompletionMessage`; some parsers `KeyError`. Local defaults `"assistant"` on emit.

Neither is a wrong-answer bug. Both are fold emit policy, not spool.

---

## Out of scope for this follow-up

Do not pull these from `ak/only-streaming-to-gateway` into a #1581 follow-up PR:

- Host `LiveStream`, disk resume, same-nonce reconnect, hop `:devshard-ts` comments.
- Cache-key intent bits — unnecessary if #1581's two-pipeline split is kept (cache still hashes the client-shaped body).
- Re-implementing accounting findings / delivery_reason / chunk cadence — already in #1581.

## Suggested split

1. **Follow-up A (U1):** `devshard/spool` + wire `aggregateResponseBuffer` behind `handleNonStreaming` / `handleAggregated`. Can still call the old assembler for a short window.
2. **Follow-up B (U2 + U3):** replace `assembleSSEBody` with `completionFolder`; port non-finite rewrite; drop `stream_only.go`.
3. **Follow-up C (U4):** `ForceUpstreamStreaming` flag + per-request snapshot, if #1581 merged without one.

A and B can be one PR if the review load is acceptable; they should not be confused for the same change.
