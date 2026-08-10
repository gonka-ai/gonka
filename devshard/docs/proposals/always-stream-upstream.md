# Proposal: Always stream upstream + attempt reconnect

**Status:** Draft / proposal  
**Scope:** Gateway request filters, proxy response shape, redundancy escalation; host ML drain and same-nonce reconnect; streamed-usage strictness and well-formed streamed replay. No chain message or transport-envelope change.  
**Implementation plans:**  
[../gateway-always-stream-upstream-plan.md](../gateway-always-stream-upstream-plan.md),  
[../gateway-attempt-reconnect-plan.md](../gateway-attempt-reconnect-plan.md)

This document is **design only**. Step-by-step implementation, test matrices, and rollout checklists live in the linked plans.

---

## Problem

### Client `stream` is forwarded all the way to the ML node

The OpenAI API lets a client choose `stream: true` or `stream: false`. Today the gateway
forwards that choice verbatim into the normalized prompt that reaches vLLM. The
gateway↔devshardd hop is already SSE-framed either way, so `stream: false` does not
simplify the transport — it only hides progress:

- **No liveness signal.** A non-streaming attempt looks hung until it finishes.
  First-token escalation is disabled; the gateway falls back to a long
  reduced-`max_tokens` retry (~140s) and a 30-minute no-content timeout.
- **No TTFT for that traffic.** First-content and CTTFL metrics only work when chunks
  arrive incrementally.
- **Weaker winner selection.** The speculative race can only crown a non-streaming
  attempt once it has fully finished.
- **Worse client latency for no gain.** Aggregating at the gateway is cheap; first
  byte need not wait for the full completion.

Two latent defects become universal once every request streams:

- **B1 — streamed reconnect replay is malformed.** A streamed inference is stored as
  `{"events":[…]}`; today's replay emits that wrapper as a single `data:` line.
- **B2 — streamed usage is silently zeroed.** Missing usage chunks become
  `PromptTokens: 0` on `MsgFinishInference`, under-billing the inference.

### Mid-stream drops throw away a resumable generation

When a participant SSE stream breaks mid-request:

- Escalation starts a **new nonce on another host** — an independent generation.
- If the attempt had **already streamed content**, the request fails with
  `winner_failed_after_content`; a secondary may keep running for protocol settlement
  but its tokens never reach the client.
- On the host, a gateway disconnect often **aborts ML mid-generation**, so there is
  nothing durable to reconnect to (`completedResponses` never populated, no finish).

So a transient TCP drop either costs a full regeneration or a truncated user-visible
failure — even when the same generation is still alive on the host.

---

## Goals

1. **Always stream upstream.** The client's `stream` flag is only a response-shape
   decision at the gateway↔client boundary:
   - client `stream: true` → forward SSE chunks,
   - client `stream: false` → aggregate chunks into one `chat.completion` JSON.
   Upstream (gateway → devshardd → vLLM) is always streamed with `include_usage`.
2. **Survivable mid-stream drops.** Before escalating to a new nonce, try a
   **same-nonce reconnect** to the same host and continue from the delivered prefix
   (including a partial last chunk). Keep the interrupted attempt as winner while it
   is resumable.
3. **Host drain independent of the client writer.** Losing the gateway↔host HTTP
   connection must not abort ML; drain to completion, persist, then serve reconnects
   from memory (live) or storage (after finish).

---

## Out of scope

- Changing the gateway↔devshardd wire protocol, `devshard_receipt` / `devshard_meta`,
  or any chain message.
- Changing what is hashed/stored as the canonical response body (optional later;
  would change `ResponseHash`).
- Shape-agnostic response cache (one entry rendered as SSE or JSON). Stream and
  non-stream client shapes stay separate cache entries.
- Mid-generation resume inside vLLM (no KV reattach).
- Client-facing resume API (`Last-Event-ID` / `starting_after`) — see
  [stream-resume-pre-proposal.md](../stream-resume-pre-proposal.md).
- Fan-out of one in-flight generation to a *different* end-user request — see
  [chat-stream-inflight-join.md](./chat-stream-inflight-join.md).
- `/v1/completions`, embeddings, or rerank paths.
- Changing nonce allocation, picker/exclude logic for *new* attempts, or the diff chain.

---

## Design — always stream upstream

### D1. Force `stream` upstream in the filter pipeline

Force at `RequestFilterStagePostLimits` (same idiom as forced logprobs):

- `stream` → `true`
- `stream_options` → `{"include_usage": true}`

PreValidation still type-checks `stream` and sanitizes `stream_options`. Forcing at
PostLimits cannot smuggle client values through. `include_usage` is forced at the
gateway so the on-chain prompt hash matches what ran and redundancy's usage-chunk
heuristics always see a usage event.

### D2. Client intent travels separately (`streamClientIntent`)

Mirror `logprobClientIntent`:

```go
type streamClientIntent struct {
    wantsStream bool // original request had stream:true
    wantsUsage  bool // original stream_options.include_usage
}
```

Captured before rewrite; used for handler branch (SSE vs aggregate), cache key,
request capture, and suppressing a forced trailing usage chunk for streaming clients
that did not ask for it. Aggregated JSON always includes `usage` (OpenAI non-stream
shape).

### D3. Decouple response shape from escalation policy

`InferenceParams.Stream` keeps meaning “client asked for SSE.” Escalation sites use
the **unconditional streaming policy** once upstream is always streamed (first-token
budget, inter-chunk stall) — not the long non-stream timers. Behind the same rollout
flag as D5.

### D4. Real SSE aggregator, with passthrough

Replace “last `data:` line” aggregation. Fold `chat.completion.chunk` deltas into one
`chat.completion`:

- group by `choices[].index` (`n > 1` supported),
- concatenate `delta.content` / `reasoning_content` / `refusal`,
- first non-empty `delta.role` (default `assistant`),
- merge `tool_calls` by index (id/type/name + concatenated arguments),
- concatenate logprobs; last non-null `finish_reason` / `stop_reason`,
- `usage` from the last event that carries it; `id` / `created` / `model` /
  `system_fingerprint` from the first,
- emit `object: "chat.completion"` with `message`.

**Passthrough** any stream that is already a single `chat.completion` (old host,
cache replay, in-process synthetic stream). Host error payloads pass through
unchanged.

### D5. Single rollback setting

`ForceUpstreamStreaming` gates forcing (D1), escalation flip (D3), and usage-chunk
suppression together. The aggregator ships unconditionally (strictly more correct
on both inputs).

### D6. Explicit cache-key isolation by client shape

After forcing `stream: true`, normalized bodies of stream and non-stream clients
collide. The cache key (and capture logging) must take **client intent** as a
separate input so an SSE body is never served to a JSON client.

### D7. Storage growth is accepted here

Every stored payload becomes `{"events":[…]}` (~1.5–2× a single JSON completion with
forced logprobs). Measured in soak; optional later work can store an aggregated
canonical body (separate proposal — changes `ResponseHash`).

### Prerequisites that become universal

**Streamed usage mandatory (B2).** Missing or zero `PromptTokens` on a non-empty
streamed completion must not publish a zero-input finish. Strict `GetUsage` with an
explicit sentinel; lenient estimate only for non-critical readers. Counter + log.

**Well-formed streamed replay (B1).** Replaying `{"events":[…]}` emits each stored
event as its own SSE `data:` line (plus `[DONE]` if needed). Non-envelope bodies keep
single-event behavior. Wire-format only — no re-hash.

### Host: drain ML independently of the client connection

Same principle the gateway already applies to the end user: losing the writer must
not abort generation.

- Execution lifetime is detached from the gateway HTTP request context; client
  cancel means “stop proxying,” not “stop generating.”
- On write failure, stop touching that writer but keep reading ML through `[DONE]`
  so the executor still accumulates every event.
- Normal finish path still runs: usage validation, `ResponseHash`, payload store,
  `MsgFinishInference`, `completedResponses`.
- Bound orphan work with an absolute drain deadline + graceful-drain lifecycle so a
  hung vLLM cannot pin the node forever. Partial-response fields are set when drain
  ends without `[DONE]`. Metrics for detach and drain outcome
  (`completed` / `deadline` / `ml_error`).

This is what makes same-nonce reconnect (below) useful.

### Cap gateway SSE event size

The transport SSE parser must not grow unboundedly on a line without `\n` (DoS /
OOM). Hard default max event size (e.g. 1 MiB); oversize aborts with a typed error
and attempt failure / escalation — never silent truncate. Always-stream makes every
chat request hit this path.

---

## Design — attempt reconnect and winner continuity

Depends on well-formed streamed replay and independent ML drain. Ship reconnect code
in the gateway-v4 binary; activate only on sessions bound to protocol **≥ v5**.

### R0. Protocol gate (≥ v5)

Effective predicate:

```text
AttemptReconnectEnabled
AND ProtocolVersionAtLeast(sessionVersion, ProtocolV5)
```

≤v4 sessions keep today's escalate-to-new-nonce / `winner_failed_after_content`
behavior. The gate is per-session (homogeneous escrow protocol version), not
per-host. Not a setting an operator can override into an incompatible host set.

### R1. Same-nonce resend is a reconnect, not a new inference

Re-issue the existing `PreparedInference` (same nonce, catch-up, payload). Host
`signReceipt` already branches on executing vs cached.

Protocol hazards:

- **Duplicate `MsgConfirmStart`.** A second receipt must not become a second mempool
  entry; repeat confirm for an already-confirmed inference is a no-op (or the first
  receipt is reused).
- **Double `ProcessResponse`.** Merge reconnect `HostResponse` into the same
  inflight — one `ProcessResponse` per nonce (prefer the response that carries
  receipt / mempool).

### R2. Resume offset = upstream events + partial bytes

On the winning inflight:

```go
deliveredEvents  int64 // complete upstream data events already forwarded
deliveredPartial int64 // bytes of event N already forwarded
```

Upstream-side, not client-side (rewrites and framing differ). Aggregating clients
need none of this — nothing was written yet.

On resume the host continues from that cursor. If the host ever re-emits a prefix,
the gateway still drops the already-delivered events/partial as a safety net.

### R3. Reconnect ladder, then escalate

On mid-stream transport failure (or EOF before `devshard_meta`):

1. Immediately same-nonce reconnect to the **same host**.
2. Budget `AttemptReconnectBudget` (default **1s**) from the **first** drop, up to
   `AttemptReconnectMaxTries` (default 2) with short backoff. Both are **per attempt,
   not per drop**: a resumed stream that drops again spends the next try on the same
   window, and the ladder is one-shot per nonce so a flapping host cannot chain
   budget windows and defer escalation forever.
3. Fail a try on error, disconnect, receipt-only when neither executing nor cached,
   or no content before budget ends.
4. When the budget expires, escalate as today (**new nonce**, next participant) **and
   keep the reconnect ladder running** — they race under R4.

Pre-content drops keep today's timing: reconnect and `attempt_failed` escalation can
start together (no delivered prefix to protect).

### R4. Winner continuity

While a reservation is held (`reservedWinner` / `reservedUntil`):

- No other nonce may crown; secondaries buffer in `pendingBuf` as today.
- Reservation releases on resume, ladder exhaustion, or confirmed-dead with nothing
  to resume from.
- After release, a secondary may take over only if nothing was delivered yet, or
  (opt-in R5) it has strictly more content than the winner delivered **and** stream
  reset is enabled.

A winner transport failure with a resumable nonce enters reservation instead of
immediately becoming terminal `winner_failed_after_content`.

**Quality feedback (not mid-stream switch).** When a winner needed reconnect,
do not splice the secondary onto the client. Record a **reconnect blip** for
future host selection (see R8). Blips are a degradation signal with a short
memory — not a `Responsive: false` failure sample and not an immediate
shadow quarantine.

### R5. Switching generations after bytes were delivered is opt-in

Default: streaming client with delivered prefix and exhausted resume → fail
(truncated stream / resume-exhausted reason). Optional `AllowStreamResetOnFailover`
writes a stream reset and continues from the secondary. Aggregating clients may
always switch (nothing delivered).

### R6. Live attach from offset; then drain → disk → forget

**Not** wait-for-ML-completion before tokens resume.

```text
Before drop (gateway → client):
  event 0 … event N-1   (complete)
  event N               (partial: deliveredPartial bytes already streamed)

Reconnect WHILE still draining ML:
  event N               (remaining: payload[deliveredPartial:])
  event N+1 …           (live as ML produces)
  … [DONE] / meta

Reconnect AFTER drain finished (body in payload storage):
  stream from storage from the resume cursor
```

Host buffer lifecycle:

```text
ML producing  →  in-memory event buffer (live attach)
      ↓ drain completes
persist        →  payload storage + completedResponses
      ↓
forget RAM     →  drop live buffer; later reconnects use storage only
```

Rules:

1. Reconnecting HTTP request **joins** the in-flight execution; first post-reconnect
   byte must not wait for `[DONE]`.
2. Per-inference buffer holds complete SSE events + the currently forming event
   while draining.
3. Resume cursor `(deliveredEvents, deliveredPartial)` on transport / `HostRequest`
   only (not a chain message). Cursor past buffer → resume failure → escalate.
4. On drain complete: normal finish path, then drop the live buffer. Later reconnects
   use `hasCached` / payload storage + well-formed `replaySSEBody`.
5. **`InflightReplayBufferTTL`** (aligned with the streaming hard timeout): stranded live
   buffers are **detached, not closed** — closing would discard a still-producing
   generation's remaining output. So the TTL bounds attachability, not RAM. After prune:
   storage if present, otherwise resume failure. Prefer drain→persist→forget; TTL is a
   safety net. RAM itself is bounded by soft-cap → head-trim (default) or optional
   spill; see reconnect-plan Step 5 / R9.
6. No second vLLM call for the same nonce.
7. **Bound the live log (reconnect-plan Step 5):** reconnect-only
   `ErrSubscriberLagged` plus a primary **write deadline** (a stalled primary must not
   pin the log); head-trim to the RAM cap, with cursors older than the retained window
   failing as `ErrResumeCursorPast`. A partial body is **never** published into
   `completedResponses` / payload store mid-flight — cached replay synthesizes `[DONE]`
   and would silently truncate the client.

Append-only log + independent readers (primary as subscriber #0), scoped to one
reconnecting gateway connection — same shape as in-flight chat join.

### R7. Settings (AND R0)

| Setting | Default | Meaning |
|---|---|---|
| `ForceUpstreamStreaming` | `false` → `true` after soak | Always-stream flip (D1/D3/usage suppress) |
| `AttemptReconnectEnabled` | `false` → `true` after soak | Master switch for R3/R4; still ANDed with R0 |
| `AttemptReconnectBudgetMS` | `1000` | Ladder budget before escalating |
| `AttemptReconnectMaxTries` | `2` | Reconnect sends per attempt (total, across all drops of that nonce) |
| `AllowStreamResetOnFailover` | `false` | R5 opt-in |
| `InflightReplayBufferTTL` (host) | `30m` = `StreamingAttemptHardTimeout` | How long a live per-nonce buffer stays attachable (detach on expiry, no close) |
| `ReconnectBlipWindow` | `5m` | Sliding window for per-participant reconnect blips |

### R8. Accounting

A reconnect must not look like a new attempt (no second `recordGatewayAttemptStarted`
/ `RealSend`). It is a transport try on the existing inflight: separate counters /
latency, same nonce, one terminal record.

#### Reconnect blips (timed degradation, not failure samples)

Recording a reconnect as `RequestSample{Responsive: false}` (and leaving
`sampleOnce` free so a later success also records) double-counts and can
shadow-quarantine a healthy host after two transient TCP drops. Instead:

```text
Winner TCP drop
    → reconnect ladder (same nonce)
    → on ladder EXIT (resume success or exhausted), once per ladder:
         RecordReconnectBlip(participantKey)
    → PerfTracker keeps blip timestamps per participant
         in ReconnectBlipWindow (default 5m); older timestamps drop out
```

**Blip** = one reconnect ladder run for a participant (not each try inside the
ladder).

Rules:

1. **Record on exit, not entry.** Outcome is known; a ladder that never started
   sending does not invent a blip at reservation time.
2. **No `Responsive: false` from blips.** Successful resume still records a
   normal success sample via `sampleOnce`. Exhausted resume keeps today's
   `winner_failed_after_content` / real failure-sample paths.
3. **Recorded, never routed on.** A blip changes neither `Decide` nor the
   picker, and never calls `ObserveStalledWinner`. Reacting to a transient TCP
   drop by forcing an immediate secondary costs a second full generation and a
   second `MsgStartInference` per request on that participant — far more than
   the drop itself. Quarantine remains for true failures / stalls; blips are an
   operator/metrics signal (Step 7) about which hosts drop mid-stream.
4. **Time decay.** Blips older than `ReconnectBlipWindow` no longer count, so
   `ReconnectBlipCount` reflects recent behavior rather than lifetime history.

#### Stream speed (bytes/sec)

TTFB / CTTFL do not describe post-content throughput. Record terminal
`stream_bps = outputBytes / elapsed_since_first_content` per attempt that
forwarded content (observability only in the reconnect plan — not a `Decide`
input). See reconnect-plan Step 7 for the Prometheus / OTel shape.

---

## Observability (design intent)

- Counters for missing usage, drain detach/outcome, SSE oversize, reconnect results
  (`resumed` / `budget_expired` / `receipt_only` / `error` / `skipped_protocol`),
  winner-continuity outcomes, reconnect blips, and stream bytes/sec (histogram +
  bytes counter; optional participant rolling p50/p95).
- After the gateway attempt-span model lands: child `attempt.reconnect` (try index,
  delivered offset, result), reconnect TTFB, time-to-first-*new*-chunk past the
  delivered offset, stream bytes / bps on the attempt span, and a clear signal when
  the race moves to another nonce (`attempt.winner_switched` / `attempt.failover`).

---

## Optional follow-ups (separate)

- **devshardd streaming-only** — reject non-stream ML payloads after traffic is
  always streamed.
- **Canonical aggregated storage** — undo D7 growth; changes `ResponseHash` (own
  proposal).
- **In-flight chat stream join** — duplicate clients join one live stream
  ([chat-stream-inflight-join.md](./chat-stream-inflight-join.md)); distinct from
  same-nonce gateway↔host reconnect.

---

## Acceptance sketch

- `stream: false` client → correct `chat.completion`; upstream saw a streaming
  request. Differential match vs mock (and reviewed vs real vLLM) for a fixed seed.
- First-content / TTFT metrics populated for essentially all chat traffic; no
  reliance on the 140s reduced-`max_tokens` path when always-stream is on.
- Dead host on a non-stream client escalates within the first-token budget (~1s),
  not after minutes.
- Streamed finish never publishes zero `InputTokens` on a non-empty completion.
- Streamed reconnect replay is a valid OpenAI SSE sequence (and aggregates correctly
  for non-stream clients).
- Mid-stream drop on a **v5** session: same-nonce resume yields one continuous,
  non-duplicated client stream (including mid-event break); at most one paid
  finish for that nonce when resume wins. Live attach does not wait for ML
  completion; post-drain reconnect streams from storage after the live buffer is
  forgotten.
- Same drop on **v4** (or reconnect disabled): today's escalation / failure
  contract; no same-nonce ladder.
- Cache isolation: stream vs non-stream client shapes remain separate entries.
- Streaming client without `include_usage` does not see a forced trailing usage
  chunk; aggregated path always has `usage`.
- Oversize SSE line without newline fails bounded, without retaining the attacker
  payload.

---

## Related

- [../gateway-always-stream-upstream-plan.md](../gateway-always-stream-upstream-plan.md) — implementation plan (always-stream, drain, aggregator, SSE cap, rollout)
- [../gateway-attempt-reconnect-plan.md](../gateway-attempt-reconnect-plan.md) — implementation plan (reconnect ladder, winner continuity, R6 lifecycle)
- [../stream-resume-pre-proposal.md](../stream-resume-pre-proposal.md) — client-facing resume (larger, separate)
- [chat-stream-inflight-join.md](./chat-stream-inflight-join.md) — duplicate clients join an in-flight stream
- [chat-cache-attribution.md](./chat-cache-attribution.md) — cache attribution for joiners / cache keys
