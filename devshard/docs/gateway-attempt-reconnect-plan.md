# Gateway Attempt Reconnect & Winner Continuity — implementation plan

Status: core landed (Steps 1–5e + 5g phase 1 + 7); deferred Steps 5f / 5g phase 2 / 6 / 8–10.
Overview (flows / timeouts / observability / e2e): [gateway-streaming-ha-overview.md](./gateway-streaming-ha-overview.md)  
Design (no steps): [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md)
Parent: [gateway-always-stream-upstream-plan.md](./gateway-always-stream-upstream-plan.md) — Step 4 references this
document. Requires that plan's Step 2 (well-formed streamed replay) and Step 3 (independent ML
drain at devshardd) to be useful.
Related: [stream-resume-pre-proposal.md](./stream-resume-pre-proposal.md),
[proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md)

## Goal

When a participant's SSE stream breaks mid-request, the gateway should first try to **continue the
same inference** (same nonce, same host) and only then fall back to a **new nonce on another host**.
The original attempt keeps winner priority while it is still resumable, so a transient TCP drop stops
costing a full regeneration and stops turning into a user-visible failure.

Three behavior changes, in order of importance:

1. **Reconnect before escalating.** A dropped attempt gets a bounded same-nonce reconnect ladder
   (default 1s budget) before a second attempt is prepared.
2. **Winner continuity.** An attempt that already streamed content to the client stays the winner
   while it is resumable; a secondary may not crown over it and may not interleave its own tokens.
3. **Resume, not restart.** A reconnect attaches to the same live generation and continues from the
   break offset (including the remainder of a partially delivered chunk), so the client never sees
   duplicated or missing bytes and never waits for ML to finish before tokens resume.

## Current state (findings)

| Concern | Today |
|---|---|
| Attempt model | One `SendOnly` per `inflight` (`redundancy.go:1918`); one nonce per attempt, allocated under session lock (`user/session.go:799-800`) |
| Additional attempt | `startAdditionalInflight` (`redundancy.go:1990`) → `prepareInflight` (`redundancy.go:1822`) → **new nonce**, participant excluded via `triedParticipants` → **different host**, fresh `MsgStartInference`, fresh ML generation |
| Same-nonce resend | **No path exists.** `PreparedInference` (`user/session.go:568-574`) is consumed by exactly one `SendOnly` (`user/session.go:869`) |
| Crowning | First **content** chunk crowns (`raceWriter.Write` → `setWinner`, `redundancy.go:1536`, `1574-1575`); not first byte, not completion |
| Loser output | Buffered in `pendingBuf` pre-crown, then discarded/suppressed (`redundancy.go:1646-1662`) — client only ever sees the winner's bytes |
| Winner dies after content | `winningInflightTerminalFailure` (`redundancy.go:2256`) → `winner_failed_after_content` (`redundancy.go:2448`) → request **fails**; `forceTreatAsFailure: true` (`redundancy.go:2444`) ignores any later successful attempt |
| Mid-stream interrupt with no content yet | `escalationForInflight` returns `attempt_failed` immediately (`redundancy.go:2636`), secondary crowns normally |
| Loser lifetime | `SecondaryWaitAfterWinner` (default 5 min, `redundancy.go:425`) then `inf.cancel()` (`redundancy.go:3154`) |
| Client-side reset primitive | `writeStreamReset` exists but is **dead code** outside tests (`proxy.go:149`) |
| Host reconnect support | `signReceipt` already branches: `alreadyExecuting` → receipt only, **no body**; `hasCached` → cached body (`host/host.go:773-822`); replay at `transport/server.go:429`. R6: live attach while draining, then drain→payload store→forget RAM; post-drain reconnects serve from storage |
| Response processing | `processInflightOnce` is a `sync.Once` per inflight (`redundancy.go:3693`) — a second response for the same nonce is not modelled |

### What the end user gets today when a second attempt is started

Worth stating explicitly, because it is the behavior this plan changes:

- The second attempt is a **new nonce on a different participant**, i.e. a **completely independent
  generation** — different sampling path, different text, its own `MsgStartInference`.
- Whichever attempt emits the first content chunk becomes the winner, and **only that attempt's
  bytes reach the client**. Losers are buffered before crowning and then dropped, so the user never
  sees interleaved or duplicated tokens.
- If attempt 1 broke **before** any content, attempt 2 crowns and the user sees only attempt 2's
  stream from the beginning — clean, and invisible to the client.
- If attempt 1 had **already streamed content** and then broke, there is **no failover**: the request
  ends in `winner_failed_after_content` and the client gets a truncated stream / mid-stream error
  event. Attempt 2 keeps running for protocol settlement, but its tokens are suppressed and never
  reach the user — so today the gateway pays for a second generation that the client cannot see.
- Cost: every attempt commits its own `MsgStartInference`; a loser that completes before being
  cancelled still finishes on-chain and is accounted as `UsageLoser`.

## Non-goals

- No mid-generation resume inside vLLM for the **same-process** reconnect path (Steps 1–9).
  "Resume" there means **replay / continue the same stored generation and skip the delivered
  prefix**. Cross-instance HA that reattaches a *different* `devshardd` to a still-running ML job
  is a separate deferred track ([Step 10](#step-10--cross-instance-devshardd-ha--ml-reattach-deferred);
  [issue #1466](https://github.com/gonka-ai/gonka/issues/1466) §4) and requires MLNode changes.
- No client-facing resume API (`Last-Event-ID` / `starting_after`). The end user still retries a whole
  request; that is the pre-proposal's scope.
- No in-flight fan-out to a *different* user request — that is `chat-stream-inflight-join.md`.
- No change to nonce allocation, the diff chain, `devshard_receipt` / `devshard_meta`, or chain
  messages.
- No change to which participant a *new* attempt goes to (picker/exclude logic stays as is).

## Design decisions

### R0. Gate reconnect on session protocol ≥ `v5` (ship code in gateway-v4, stay dormant on ≤v4)

Same-nonce reconnect is only correct against hosts that implement the parent plan's Step 2
(well-formed streamed replay) and Step 3 (independent ML drain). Those land with the **`v5`
protocol** host; `v4` and earlier abort ML on client disconnect and cannot answer a mid-generation
reconnect usefully. So:

1. **Ship the reconnect code in the gateway-v4 release**, behind `AttemptReconnectEnabled`, so the
   binary already knows how to do it.
2. **Do not apply it to sessions bound to protocol ≤ `v4`.** On those escrows, keep today's
   escalate-to-new-nonce / `winner_failed_after_content` behavior unchanged.
3. **Activate only when the session's bind tag is ≥ `v5`.** Sessions are homogeneous — every host
   in an escrow shares `EscrowState.StateRootAndProtocolVersion`
   (`types/domain.go:127-130`, stamped at create / recovery) — so the gate is per-session, not
   per-host. Read it from `e.session.StateMachine().SnapshotState().StateRootAndProtocolVersion`
   (or a thin `Session.ProtocolVersion()` helper) at the point a drop would enter the reconnect
   ladder.
4. **Effective predicate:**

   ```text
   AttemptReconnectEnabled
   AND ProtocolVersionAtLeast(sessionVersion, ProtocolV5)
   ```

   If either side is false, skip the ladder entirely and run today's escalation path. No
   reservation, no same-nonce resend, no winner continuity.

5. **Types work:** add `ProtocolV5 = "5"` to `types/domain.go`, accept `"v5"` / `"5"` in
   `ParseProtocolVersion`, and add a small `ProtocolVersionAtLeast(a, b)` (numeric major compare on
   the normalized form). Do **not** change `DefaultProtocolVersion` (stays `ProtocolV4`) until the
   v5 rollout itself.

Rollout implication: gateway-v4 can be published and soak with reconnect code present but inert on
all current escrows. When v5 hosts are approved and escrows start binding to `v5`, those sessions
automatically get the reconnect path without another gateway redeploy (aside from flipping
`AttemptReconnectEnabled` if it is still default-off).

### R1. A same-nonce resend is mechanically a reconnect, not a new inference

`PreparedInference` is immutable after prepare, so re-issuing `SendOnly` with the same value sends a
byte-identical `HostRequest` (`user/session.go:869-880`): same `Nonce`, same `catchUp` diffs, same
payload. The host then re-runs `signReceipt`, which **already** has the two branches a reconnect
needs (`host/host.go:773-822`): `alreadyExecuting` (execution in progress) and `hasCached`
(completed, body in `completedResponses`).

Two protocol hazards must be handled explicitly, because nothing exercises them today:

- **Duplicate `MsgConfirmStart`.** `signReceipt` stamps a fresh `confirmedAt = time.Now()`
  (`host/host.go:781`) and the host mempool is keyed by tx hash (`host/mempool.go:29`), so a second
  receipt for the same inference is a *different* hash and lands as a **second** mempool entry.
  The reconnect must either reuse the first receipt or the state machine must treat a repeat
  `MsgConfirmStart` for an already-confirmed inference as a no-op. Decide and test this before
  shipping; it is the one place a reconnect can corrupt protocol state.
- **Double `ProcessResponse`.** `processInflightOnce` guards one response per inflight
  (`redundancy.go:3693`). A reconnect yields a second `HostResponse` for the same nonce, and
  `processResponse` queues receipt + mempool txs (`user/session.go:530-545`). The reconnect response
  must be merged into the same inflight (one `ProcessResponse` per nonce, using the **last** response
  that carries a receipt / mempool), not processed twice.

### R2. Resume offset is measured in upstream events (+ partial bytes), not client bytes

At the moment of the drop the gateway may already have forwarded complete upstream events
`0 … N-1` and a **partial** event `N` (some but not all of its payload bytes) to the client. Track
that on the winning `inflight`:

```go
deliveredEvents  int64 // complete upstream data events already forwarded
deliveredPartial int64 // bytes of event N already forwarded (0 if the break was on an event boundary)
```

The offset is upstream-side, not client-side: client bytes are rewritten
(`rewriteStreamingPayload`, `filterClientInternalFields`, forced-usage suppression), and live vs
replay framing differ (`line + "\n"` vs `data: …\n\n`). `raceWriter`'s fragment reassembly
(`classifyPartial`, `redundancy.go:1356-1374`) is the natural place to maintain the counters.

On resume (see R6): the host continues the **same** generation from that cursor — it does **not**
wait for ML to finish. The gateway receives the remainder of event `N`
(`payload[deliveredPartial:]`), then subsequent events as they are produced, and forwards them
without re-sending what the client already has. If the host ever re-emits a prefix (completed
cache replay from event 0), the gateway still drops `deliveredEvents` and the partial prefix as a
safety net.

**Aggregating (non-streaming) clients need none of this.** Nothing has been written to the client, so
a resume simply re-reads / re-aggregates. Ship the offset machinery only for the streaming path.

### R3. Reconnect ladder with a 1s budget, then escalate as today

On a mid-stream transport failure (or EOF before `devshard_meta`):

1. Immediately issue a same-nonce reconnect to the **same host**.
2. Budget the ladder with `AttemptReconnectBudget` (default **1s**, matching `FirstTokenTimeoutCap`,
   `redundancy.go:418`), measured from the **first** drop. Within the ladder, allow up to
   `AttemptReconnectMaxTries` (default 2) resume tries with a short backoff.

   Both limits are **per attempt, not per drop.** A resumed stream that drops again spends the next
   try against the same budget window; neither is re-armed. The ladder itself is one-shot per nonce
   (`reconnectLadderUsed`, `redundancy.go:2346`), so a host flapping every few hundred ms cannot
   hold the client stream and the winner reservation across an unbounded series of 1s windows — and
   blips stay 1:1 with ladder runs for the Step 8 metrics. Budget expiry starts the escalation
   hedge; it does not end the ladder (step 4).
3. Reconnect is considered failed if it errors, disconnects again, returns a receipt-only response for
   an inference that is neither executing nor cached, or produces no content before the budget ends.
4. When the budget expires without a resumed stream, run today's escalation
   (`startAdditionalInflight`, new nonce, next participant) **and keep the reconnect ladder running**
   — the two race per R4.

Escalation for an attempt that broke *before* any content keeps today's timing exactly: reconnect and
`attempt_failed` escalation start together, because there is no delivered prefix to protect and the
secondary is free to crown.

### R4. Winner continuity: a delivered prefix owns the client stream

Add a **reservation** to `raceGroup` (`redundancy.go:1140`):

```go
reservedWinner uint64    // nonce whose delivered prefix must be honored
reservedUntil  time.Time // reconnect ladder deadline
```

**Hard rule once any winner bytes reached the client:** that attempt owns the end-user stream for
the rest of the request. A secondary (new nonce / different host) is a *different generation* — it
must never be spliced onto the already-streamed prefix, even if it is faster or has produced more
chunks. "B has more content than A delivered" is **not** a takeover signal for streaming clients.

Concrete scenario this forbids:

```text
1. Host A streams a prefix, then drops
2. Gateway reconnects A; after AttemptReconnectBudget also starts Host B
3. A resumes but is slow; B races ahead in pendingBuf
4. WRONG: crown B and write B's tokens to the client  → breaks A's nearly-finished response
5. RIGHT: keep forwarding A's resume to the client; keep B suppressed for client I/O;
   settle B as a loser / cancel per existing SecondaryWaitAfterWinner paths;
   record A as degraded for *future* host selection (see below)
```

Rules:

- `setWinner` refuses any other nonce while a live reservation is held, so a secondary cannot crown
  over the interrupted attempt and cannot write to the client.
- A secondary keeps buffering into `pendingBuf` exactly as it does pre-crown today
  (`redundancy.go:1656-1662`) — hedge for protocol settlement / pre-content failover only; its
  bytes stay off the client while a delivered prefix exists.
- The reservation is released when the winner resumes (it keeps the crown), the ladder is exhausted
  with the winner confirmed dead / unresumable, or resume is impossible (no cache / no live attach).
  Release does **not** mean "promote whoever has more chunks."
- **Client-stream takeover after a delivered prefix is only via R5** (explicit stream reset), never
  by comparing `secondary.contentChunks` to `winner.deliveredEvents`.
- **Pre-content path unchanged:** if nothing was delivered yet, the secondary may crown freely
  (today's `attempt_failed` behavior) — there is no prefix to protect.
- **Slow-but-alive resumed winner:** keep streaming from A until it finishes or the client/
  settle deadline fires. Do not switch the client writer to B because A is slow. Instead **record a
  timed reconnect blip** for A (see R8) so operators and metrics can see which hosts drop
  mid-stream. The blip is deliberately *not* wired into `Decide` / the picker: reacting to a
  transient drop by forcing an immediate secondary buys a second full generation and a second
  `MsgStartInference` per request, which costs more than the drop. Quarantine stays on real
  failure / stall samples.

`winningInflightTerminalFailure` (`redundancy.go:2256`) must therefore stop being terminal on its own:
a winner failure with a resumable nonce enters the reservation state instead of going straight to
`winner_failed_after_content` (`redundancy.go:2448`) with `forceTreatAsFailure`
(`redundancy.go:2444`).

### R5. Switching generations after bytes were delivered stays opt-in

If the client already received content from attempt 1, attempt 2's text is a *different* completion.
Splicing it on is semantically wrong (contradictory or repeated prose), which is exactly why today's
code fails instead — and why R4 no longer has a "B is ahead ⇒ takeover" rule. Keep that default:

- **Streaming client, prefix delivered, A still resumable (even if slow)** → stay on A; never write
  B to the client; penalize A for future picks (R4).
- **Streaming client, prefix delivered, resume impossible / ladder exhausted** → fail as today
  (truncated stream + `winner_failed_after_content`), with a new failure reason distinguishing
  "resume exhausted". Do **not** silently failover to B.
- Optional, default **off**: `AllowStreamResetOnFailover` — only then may the gateway write a
  stream reset to the client and restart from the secondary. `writeStreamReset` (`proxy.go:149`)
  is the existing primitive and would finally get a caller. This is an explicit UX break
  (client must handle reset), not a silent splice.
- **Aggregating client** → always allowed to switch; nothing was delivered to the wire.

### R6. Reconnect attaches to the live generation and continues from the break offset

Today `alreadyExecuting` returns a receipt with no body (`host/host.go:813-816`), so a reconnect
arriving mid-generation yields nothing to resume from. The required behavior is **live continue**,
not wait-for-completion — plus a clear handoff from RAM to durable storage once ML is done:

```text
Before drop (gateway → client):
  event 0 … event N-1   (complete)
  event N               (partial: first deliveredPartial bytes already streamed)

After same-nonce reconnect WHILE still draining ML (host → gateway → client):
  event N               (remaining bytes only: payload[deliveredPartial:])
  event N+1 …           (as ML produces them — live)
  … [DONE] / meta

After same-nonce reconnect AFTER drain finished (body on disk / in payload storage):
  stream from storage from the resume cursor (same bytes as a completed replay;
  gateway still applies R2 skip as a safety net)
```

**Lifecycle of the host-side buffer (preferred path):**

```text
ML producing  →  in-memory event buffer (live attach target)
      ↓ drain completes ([DONE] / usage validated)
persist body  →  payload storage + completedResponses (same as today's finish path)
      ↓
forget RAM    →  drop the live in-flight buffer; reconnects use storage only
```

Concretely:

1. **Do not wait for the ML node to finish before streaming.** Parent-plan Step 3 keeps draining ML
   after the first client writer dies; a reconnecting HTTP request **joins** that in-flight
   execution as a new subscriber / `ResponseWriter` and continues from the resume cursor
   immediately. It must not block until `completedResponses` is filled.
2. **Host keeps a per-inference byte/event buffer** of what has already been produced (complete SSE
   events + the currently forming event) while ML is still draining. Independent of whether anyone
   is currently reading.
3. **Resume cursor** — the gateway passes `(deliveredEvents, deliveredPartial)` on the reconnect
   `HostRequest` (HTTP / transport layer only; not a chain message). The host starts emitting at
   that cursor: remainder of the partial event, then live tail. If the cursor is past what the host
   has buffered (should not happen if Step 3 drained into the same buffer), treat as resume failure
   so the gateway can escalate.
4. **On drain complete → disk, then forget memory.** When the ML stream finishes, run the normal
   finish path (usage validation, `ResponseHash`, **payload store**, `MsgFinishInference`,
   `completedResponses[inferenceID]`). Then **drop the live in-flight buffer** for that nonce from
   RAM — the durable copy is the source of truth. A later same-nonce reconnect must **not** need
   the live buffer; it streams from payload storage / `completedResponses` via today's
   `hasCached` + `replaySSEBody` path (from the resume cursor, or full body with gateway R2 skip).
5. **Pruning timeout for stranded attach state.** Bound how long a live buffer stays *attachable*
   if drain never completes cleanly (deadline, ML error, host restart edge). After
   `InflightReplayBufferTTL` (aligned with the streaming hard timeout), detach the attach-map
   entry — without closing the log, so a still-producing generation keeps buffering toward its
   durable body. A reconnect after prune with **no** durable body is a resume failure (gateway
   escalates); with a persisted body it still serves from storage. Prefer the
   drain→persist→forget path so the TTL is a safety net, not the happy path.
6. **No regeneration.** Live attach and storage replay both reuse the same ML forward pass /
   same stored events — never a second vLLM call for the same nonce.

This is the same fan-out shape as [chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md),
scoped to one inference's reconnecting gateway connection rather than a second end-user.

The Step 2→4 landing already replaced the fan-out channel with an append-only
`LiveStream` log and independent readers (primary as subscriber #0). **R9 /
Step 5** below is no longer “build the log”; it finishes the RAM / lag / single-
body story that the current hub still leaves open.

### R7. Settings AND the v5 protocol gate (R0)

Add to `RedundancySettings` (`redundancy.go:428`), plumbed through `ApplyRedundancySettings` and the
admin endpoint like the existing knobs:

| Setting | Default | Meaning |
|---|---|---|
| `AttemptReconnectEnabled` | `false` initially, `true` after soak | Master switch for R3/R4; still ANDed with R0 |
| `AttemptReconnectBudgetMS` | `1000` | Ladder budget before escalating |
| `AttemptReconnectMaxTries` | `2` | Reconnect sends per attempt (total, across all drops of that nonce) |
| `AllowStreamResetOnFailover` | `false` | R5 opt-in |
| `InflightReplayBufferTTL` (host) | `ExecutionTimeout` (default 32m) = gateway hard timeout | How long a live per-nonce buffer stays **attachable**; on expiry it is detached, not closed (R6 §5 / R9) |
| `LiveStreamRingBytes` (host) | `256 KiB` | Trailing RAM window kept per generation. A cache of the spool's tail, **not** the resume horizon — sized for one reader write stall (Step 5c) |
| `LiveStreamMaxRAMBytes` (host) | `4 MiB` | Hard ceiling, reached only in degraded mode (no usable spool). Past it, readers below the trim point are dropped so the ceiling holds (Step 5c) |
| `LiveStreamWriteChunkBytes` (host) | `256 KiB` | Max bytes per subscriber write, so a large backlog is many bounded writes rather than one write racing one deadline (Step 5a) |
| Live-stream spool dir (host) | `{DataDir}/livestream-spool` | Scratch `.log` + `.idx` per generation; emptied at startup, removed on release. Absent ⇒ mid-flight reconnect refused with `ErrLiveStreamResumeUnavailable` (Step 5c) |
| `LiveStreamReaderStallTimeout` (host) | `60s`, must exceed `AttemptReconnectBudget` | How long a **non-primary** subscriber may make **zero** offset progress while bytes are pending before it is closed with `ErrSubscriberLagged` (Step 5a). Progress, however slow, keeps the reader alive |
| `LiveStreamPrimaryWriteTimeout` (host) | `30s` | Per-write deadline for **every** reader, enforced inline via `SetWriteDeadline`. Breach on the primary = client detach (drop reader, keep producing); on a reconnect = `ErrSubscriberLagged` (Step 5a) |
| `SoftSignalPersistIntervalMS` | `60000` | How often gateway soft signals (reconnect blips, rolling stream bps) are flushed to the gateway store (Step 6). `0` disables persist |
| `ReconnectBlipWindowMS` | `300000` | Sliding window for in-memory + persisted blip timestamps (R8) |

`AttemptReconnectEnabled` alone must never activate reconnect on a ≤v4 session. The protocol gate
is not a setting — it is a hard compatibility check so an operator cannot accidentally enable
reconnect against hosts that abort ML on disconnect.

### R8. Accounting: a reconnect must not look like an attempt

A reconnect reuses the nonce, so it must **not** call `recordGatewayAttemptStarted` /
`accounting.RealSend` again (`redundancy.go:2886-2916`) — that would double-count sends for one
inference. Model it as a new *transport try* on the existing attempt: its own counter and its own
latency histogram, but the same `inflight`, the same nonce, and one terminal record.

**Reconnect blips are observability, not routing.** One blip per ladder run per participant
(`RecordReconnectBlip`, aged out by `ReconnectBlipWindow`, default 5m). A blip writes no
`RequestSample`, never quarantines, and never reaches `Decide` / the picker — reacting to a
transient drop by forcing an immediate secondary would buy a second full generation and a second
`MsgStartInference` on every later request to that host. Because the signal is *only* useful if it
is visible, blips must be exported (Step 8) and periodically persisted to the
gateway store (Step 6), not just logged.

**Stream speed (bytes/sec) is observability, not routing (yet).** Today's perf model
estimates time-to-first-token (`AvgReceiptTimeMs + AvgCTTFL × inputTokens`) but does
not measure how fast a crowned stream delivers bytes after content starts. That gap
matters for reconnect / winner-continuity: a host that dribbles tokens can look fine
on TTFB, never trip the 30s stall *log*, and still hold the client for up to the
hard timeout. Measure sustained throughput from existing inflight counters, persist
a rolling summary with blips (Step 6), and export Prometheus series in Step 8 —
do **not** wire it into `Decide` / quarantine in this plan (same rationale as
blips: a soft signal must not buy a second generation by default).

Definition (gateway, per attempt that forwarded content):

```text
stream_bps = outputBytes / max(elapsed_since_first_content, ε)
```

- `outputBytes` — already tracked on `inflight` (`raceWriter.Write`).
- Clock starts at first content chunk (`firstTokenAt` / first `contentChunks > 0`),
  not at send/receipt, so prompt-processing delay does not dilute the rate.
- Clock ends at attempt terminal (success, stall abort, hard timeout, client cancel,
  or reconnect-ladder exit). Mid-ladder: attribute bytes to the same attempt; do not
  invent a second sample.
- Exclude probes and attempts that never emitted content (`contentChunks == 0`).
- Prefer **winner / client-forwarded** bytes for the user-visible series; optionally
  also record a suppressed-loser series for capacity diagnosis (label `visibility`).

### R9. LiveStream is an append-only log with independent readers

ML drain must never wait on a gateway, and a reconnect subscriber must never
silently lose bytes.

**Principle (already landed in Steps 2–4):** `LiveStream` is an **append-only
byte log with independent readers**, not a multicast push bus. Primary is
subscriber #0; reconnects `Subscribe` from a wire cursor converted once to an
absolute byte offset; producer `Write` only appends under a short lock and never
performs network I/O.

```text
ML.Execute
   └─ LiveStream.Write(p)           // lock → append → wake → unlock
         │
         ├─ primary reader (offset 0)     ──► first gateway ResponseWriter
         └─ reconnect readers (offset C)  ──► AttachLiveStream / Subscribe
```

**What Step 5 had to close:**

1. **Triple retention.** During drain the same tokens live in the response
   processor body, in `LiveStream.events` (one alloc per line), and then again in
   `completedResponses` / payload store. Soft cap only refused new attaches.
   *Status: the live-log copy is now `O(ring)`; the processor copy is 5h.*
2. **Soft cap was not a RAM bound.** Past `LiveStreamMaxRAMBytes` the producer
   kept appending, and head-trim could only drop bytes every reader had already
   consumed — so one pinned reader disabled the bound entirely. *Status: fixed by
   5c; trim now answers to the spool, not to readers.*
3. **No subscriber stall policy.** A reconnect whose socket stopped consuming
   pinned its offset forever while the log grew. *Status: fixed by 5a.*
4. **No escape from a large live log** other than finish → persist → forget, and
   a stalled primary reader could block even that. *Status: fixed by 5c.*

**Storage tiers:**

1. **Hot RAM window** (`LiveStreamRingBytes`) — a bounded cache of the spool's
   tail, sized for one reader write stall. Not the resume horizon.
2. **Spool on disk** (`.log` + `.idx`, per generation) — the log itself. Head-trim
   answers only to how much of it is written, so no reader can pin RAM, and a
   cursor older than the window still resolves. Scratch: no `fsync`, removed on
   release, swept at startup. Spool I/O never runs under the producer lock.
3. **Durable RAM cache** `completedResponses` (already exists) — populated
   **only on ML completion**, then the live log is forgotten. Mid-flight
   publishing is **forbidden**: see the partial-publish hazard below. Note this
   tier is short-lived: it is evicted as soon as `MsgFinishInference` /
   `MsgTimeoutInference` lands in an applied diff (`reconcile.go:118-125`).
4. **Payload store on disk** — the engine persists prompt + response body for
   validation, retained until the inference is sealed
   (`InferenceSealGraceSeconds`, default 3600s / `InferenceSealGraceNonces` =
   10×groupSize). Wired as a resume source in Step 5e, which is what makes cache
   eviction safe. **Write-once and hash-bound**: never the mid-flight tier — that
   is the spool's job (5c §6).

**Never publish a partial body as the durable one.** `completedResponses[id]` is
consumed by `signReceipt`'s `hasCached` branch *before* the live branch, and
transport replays it with `replaySSEBodyFromCursor`. A partial body published
mid-flight would therefore be replayed to a client that has no way to tell it
apart from a finished one. Replay no longer synthesizes a trailing `data:
[DONE]` for a streamed envelope that lacks one — it emits
`partialStreamErrorLine` (`type: incomplete_stream`) instead, so truncation is
explicit — but that is a last line of defence, not a licence to publish
partials: the payload-store copy must also hash-match
`MsgFinishInference.ResponseHash`, so a partial store breaks validation.
Mid-flight RAM relief is head-trim against the spool; the durable body stays a
completion-time artifact.

The marker occupies exactly **one event slot past the stored events** in cursor
space (the gateway counts it like any forwarded event), so a reconnect whose
cursor already covers it replays as fully-delivered rather than
`ErrResumeCursorPast`.

**Backpressure (retain + stall error, with primary asymmetry):**

Producer is always non-blocking. Pressure only affects readers / new attaches.
Silent drop and blocking `Append` on a slow `Write` are both forbidden.

The trigger is **lack of progress, not depth of backlog.** A reader that is
merely slow is a healthy reader: it is still delivering bytes the client needs
and it will still finish. Only a reader whose offset does not move at all is
unrecoverable, and only that case has to be converted into a drop.

| Reader | When it is slow but still advancing | When it stops advancing | When it falls behind the RAM window |
|---|---|---|---|
| **Primary (sub #0)** | Kept. Never failed with `ErrSubscriberLagged` — that would turn a slow client TCP into a reconnect ladder on a healthy generation. | Per-write deadline (`LiveStreamPrimaryWriteTimeout`) breached → treat exactly as today's write error: set `ClientDetached`, drop the reader, keep producing into the durable body. | Served from the spool. Head-trim proceeds regardless of where it sits. Degraded (no spool): dropped at `LiveStreamMaxRAMBytes` |
| **Reconnect** | Kept. Byte lag alone never closes it — a resume that is behind by tens of MiB but still draining is exactly the case reconnect exists to serve. | No offset progress for `LiveStreamReaderStallTimeout` while bytes are pending → close that sub with `ErrSubscriberLagged` (gateway escalates / retries). Generation stays available. | Served from the spool; `ErrResumeCursorPast` only past the spool tip. Degraded (no spool): attach refused with `ErrLiveStreamResumeUnavailable` |

`ErrSubscriberLagged` means: “this generation is still available, but this TCP
client has stopped consuming.” Same class as cursor-past for the gateway ladder.
Prefer failing one dead reconnect over corrupting the stream or stalling ML.

**Why every reader still needs a stall bound.** RAM is no longer the reason —
5c's spool decouples the window from reader offsets — but a reader parked on a
black-holed socket is still a liveness problem: it holds a gateway connection and
a ladder slot while delivering nothing. A write deadline
(`http.NewResponseController().SetWriteDeadline`, already used in this codebase
for Flush) converts that into an ordinary detach.

**A stall bound is not a RAM bound, and must not be used as one.** A reader that
keeps advancing, just slower than ML produces, keeps `[reader.offset, tip)`
undelivered by definition. Before 5c that pinned hot RAM; now those bytes live in
the spool and the reader reads them from disk, so the answer to "RAM is high
because a healthy reader is behind" is *nothing* — it no longer happens. Closing
a still-advancing reader to reclaim RAM stays explicitly rejected: it trades a
working stream for a resume failure.

**Two-copy reality (not three).** For a streamed inference the durable body is a
`{"events":[…]}` envelope built by the response processor, i.e. essentially the
same event text the live log holds. So during drain there are two live copies
(processor-retained events + `LiveStream.events`) plus a transient marshal at
finish — not three independent full bodies. Step 5b's goal is to collapse those
two live copies into one shared allocation set; the bytes turn out **not** to be
identical sequence-for-sequence (see 5b), so the sharing is per-line payload with
separate framing rather than wholesale aliasing.

Cursor semantics (blank-line merging, event vs byte offset) must stay identical
between the live log and `replaySSEBodyFromCursor`, or post-trim / post-finish
reconnect will desync. That is a hard acceptance criterion.

**Cursor model:**

- **Wire:** keep `(delivered_events, delivered_partial)` for gateway↔host.
- **Internal readers:** convert once at subscribe to an **absolute byte offset**,
  then only advance that.

**Lifecycle / limits (current + Step 5):**

- `InflightReplayBufferTTL` == `StreamingAttemptHardTimeout` == `ExecutionTimeout` (default 32m): on expiry
  **detach, do not close** the log. Prune means “live attach unavailable,” not
  “abort finish.” Attach surfaces `ErrLiveStreamGone`; `ErrLiveStreamPruned`
  remains `Subscribe`'s defensive check for direct holders. Transport maps both
  (plus resume-unavailable / lagged / cursor errors) to `ReasonCachedReplayErr`.
- TTL alone does **not** bound RAM — 5c's head-trim against the spool does, and
  unlike the earlier head-trim it does so regardless of reader behaviour.
- Caps: `LiveStreamRingBytes` (RAM window), `LiveStreamMaxRAMBytes` (degraded-mode
  ceiling), `LiveStreamWriteChunkBytes`, `LiveStreamReaderStallTimeout`
  (reconnect-only), `LiveStreamPrimaryWriteTimeout` (all readers), max
  subscribers (1–2).

---

## Step-by-step implementation plan

### Step 1 — Track the delivered prefix on the winning attempt ✅

`devshard/cmd/devshardctl/redundancy.go`.

1. Add `deliveredEvents` / `deliveredPartial` counters to `inflight` (`redundancy.go:844`), updated in
   `raceWriter.Write` (`redundancy.go:1536`) only on bytes actually forwarded to `race.w`.
2. Count complete upstream data events, using the existing fragment reassembly so a split line is
   counted once; record the partial payload length when a write covers only part of an event.
3. No behavior change yet — this step is pure instrumentation and can land alone.

Tests (unit): fragmented writes across event boundaries produce the right counts; suppressed losers
and probes count nothing; pre-crown buffered bytes count nothing until flushed.

### Step 2 — Host: live-attach reconnect from resume cursor (R6) ✅

`devshard/host/host.go`, `devshard/transport/server.go`, `devshard/cmd/devshardd/inference/`.

1. Keep the `hasCached` / payload-storage branch as is — well-formed replay via parent-plan Step 2
   (`replaySSEBody` unwrap). Prefer serving from storage whenever a durable body exists.
2. Replace the `alreadyExecuting` receipt-only branch with a **live attach** to the in-flight
   execution (requires parent-plan Step 3 drain so ML is still running / buffered):
   - Maintain a per-inference event buffer (complete SSE events + currently forming event) while
     ML is drained.
   - Accept a resume cursor `(deliveredEvents, deliveredPartial)` on the reconnect request
     (transport/`HostRequest` only — not a chain message).
   - Emit from that cursor immediately: remainder of the partial event, then follow the live ML
     tail as new bytes arrive. Do **not** wait for `[DONE]` before starting to write.
3. **Drain complete → persist → forget:** when ML drain finishes, write the body through the
   existing finish path (payload store + `completedResponses`), then drop the live in-flight
   buffer from memory. Subsequent same-nonce reconnects must hit storage / `hasCached`, not the
   live-attach path.
4. **Pruning timeout:** if the live buffer is still registered past `InflightReplayBufferTTL`
   (aligned with the streaming hard timeout), detach it from the attach map without closing it —
   the producer keeps buffering so a long generation still reaches its durable body. Reconnect
   after prune uses storage if present; otherwise resume failure.
5. Make a repeat `MsgConfirmStart` for an already-confirmed inference a no-op (R1) rather than a
   second mempool entry with a fresh `confirmedAt`.

Tests (unit / e2e):

- Mid-generation reconnect with a partial last event delivers only `payload[deliveredPartial:]`
  then live chunks (no gap / no duplication; first post-reconnect byte arrives **before** ML
  finishes).
- **Post-drain storage reconnect:** drop mid-stream, let the host finish draining ML and persist
  the body (payload store + `completedResponses`), assert the live buffer is gone from memory,
  then same-nonce reconnect with a resume cursor — body is streamed **from storage**, not from a
  live attach, and the client-visible stream is continuous / non-duplicated from the break offset.
- Completed reconnect still full-replays; cursor past buffer → resume error for gateway escalation.
- Buffer past `InflightReplayBufferTTL` → detached from the attach map; reconnect fails cleanly
  with `ErrLiveStreamGone` while the producer keeps buffering (no ML output discarded).
- Repeat receipt does not duplicate protocol txs.

### Step 3 — Gateway: same-nonce reconnect send path with cursor ✅

`devshard/cmd/devshardctl/redundancy.go`, `devshard/user/session.go`.

1. Add a resend that reuses the existing `PreparedInference` and `inflight` — no new nonce, no picker
   call, no `triedParticipants` mutation.
2. Pass `(deliveredEvents, deliveredPartial)` on the reconnect so the host can start at the break
   (R2 / R6).
3. Merge the reconnect's `HostResponse` into the same inflight so exactly one `ProcessResponse` runs
   per nonce (R1), preferring the response that carries a receipt / mempool txs.
4. Defensive skip of any unexpected re-prefix still applies if the host sends more than the
   remainder (R2 safety net).
5. Record reconnect tries separately from attempts (R8).

Tests (unit): drop at event boundary, mid-event (partial bytes already at client), and last event
→ exact original client-visible sequence with no duplication or loss; first new byte after
reconnect does not wait for ML completion; non-streaming path re-aggregates correctly.

### Step 4 — Winner continuity and reconnect-first escalation ordering ✅

`devshard/cmd/devshardctl/redundancy.go`, `devshard/types/domain.go`.

1. Add `ProtocolV5` + `ProtocolVersionAtLeast` (R0). Expose the session bind tag to redundancy
   (thin helper on `Session` reading `StateRootAndProtocolVersion`).
2. Factor a single `attemptReconnectAllowed(sessionVersion)` predicate =
   `AttemptReconnectEnabled && ProtocolVersionAtLeast(sessionVersion, ProtocolV5)`. Every entry
   into the ladder / reservation must go through it; a false result falls straight through to
   today's `attempt_failed` / `winner_failed_after_content` paths.
3. Add the `raceGroup` reservation (R4) and gate `setWinner` on it. Once
   `deliveredEvents > 0` (or any content was forwarded), the reserved nonce is the only writer to
   the client for this request unless R5 reset is enabled and resume is exhausted.
4. On winner transport failure, enter the reservation and start the ladder instead of returning
   `winner_failed_after_content` immediately — **only when** `attemptReconnectAllowed` is true.
5. Start the secondary when the budget expires (R3) while the ladder continues. The secondary is a
   **hedge** (protocol / pre-content failover): it must stay suppressed for client I/O while a
   delivered prefix exists. A resumed (even slow) winner keeps the crown; do **not** promote the
   secondary because `secondary.contentChunks > winner.deliveredEvents`.
6. Keep the pre-content path unchanged: no delivered prefix means the secondary crowns freely.
7. Release the reservation on resume, ladder exhaustion with unresumable winner, or
   confirmed-dead-with-no-cache. Settle losers through the existing
   `SecondaryWaitAfterWinner` / `finishRaceWhenPendingDone` paths (`redundancy.go:2997`, `3154`)
   with no change to client bytes.
8. **Quality feedback (recorded, not routed on):** when a winner needed a reconnect ladder,
   record one timed blip for Host A (`RecordReconnectBlip`, aged out by `ReconnectBlipWindow`).
   No `RequestSample` failure, no quarantine, and no `Decide` / picker effect — the blip is an
   observability signal for Steps 6–8. Do not change who is writing to the current client.

Tests (unit): a drop after content resumes and the client receives one continuous stream while the
secondary is suppressed — including the case where the secondary has *more* buffered content than
the winner delivered and the winner is slow after resume (client must still see only A's bytes); a
drop whose reconnect never succeeds falls back to today's failure (no silent B splice); a
secondary that crowns before any content is delivered is unaffected; the reservation never
deadlocks when both attempts die; reconnect/slow-resume records a blip without switching the
client writer or changing `Decide`. **Version gate:** with `AttemptReconnectEnabled=true` on a `v4` (or
earlier) session, a mid-stream drop must **not** same-nonce resend and must follow today's
escalation / `winner_failed_after_content` contract; the same drop on a `v5` session enters the
reconnect ladder.

### Step 5 — Host: bound the live log (R9 finish) ✅ (5a–5e; 5f–5h open)

`devshard/host/livestream.go`, `devshard/host/spool.go`, `devshard/host/host.go`,
`devshard/cmd/devshardd/session/manager.go`, `devshard/cmd/devshardd/app.go`,
transport error mapping.

**Status of the original Step 5 list:** the append-only log, “never Write under
`mu`,” primary-as-subscriber-#0, wire→byte-offset conversion, TTL align, and
refuse-attach soft cap are **already landed** in Steps 2–4. Do not rebuild them.

This step closes the gaps those landings left open: unbounded / duplicated RAM,
missing stall policy, and no escape hatch when the cap is hit mid-flight. 5c in
particular replaced the original head-trim-plus-optional-spill plan, whose bound
collapsed whenever a reader stopped consuming — see the rewrite below.

#### Goals

1. Hot RAM per in-flight generation is `O(LiveStreamRingBytes)` **independent of
   reader behaviour** — not merely "attach-refused past a soft cap", and not
   contingent on every reader having consumed the head.
2. No mid-flight partial body is ever published as the durable one.
3. A **stalled** reconnect (zero progress) is failed with `ErrSubscriberLagged`;
   a merely slow or far-behind one is kept and served from disk. The primary is
   never failed for lag, but is droppable as a reader on a hard write stall.
   No reader is ever closed for being behind while it is still advancing.
4. Trim and disk I/O never run under `LiveStream.mu` in a way that blocks ML
   `Write`.
5. Resume cursor semantics stay identical to `replaySSEBodyFromCursor` whether
   the cursor resolves against RAM, the spool, or a finished body.

#### Incremental path

**5a — Reader stall policy (small, land first)**

**Governing rule: a reader is dropped for making no progress, never for being
behind.** Backlog depth is a RAM problem (5c); a frozen offset is a liveness
problem, and only the latter can pin the log forever. Closing a slow-but-draining
reader would convert a stream that was going to succeed into a resume failure,
which is the opposite of what R3/R4 are for.

- Track, per subscriber: `sub.offset`, `totalBytes - sub.offset` (lag bytes, for
  reporting), and the timestamp of the last **advance** of `sub.offset`. Producer
  appends do not reset that timestamp; only a successful `writeReplay` that moves
  the offset does.
- **Non-primary sub:** if bytes are pending (`sub.offset < totalBytes`) and the
  offset has not advanced for `LiveStreamReaderStallTimeout`, unsubscribe it and
  return `ErrSubscriberLagged`. Transport maps it like cursor-past / over-cap
  (`ReasonCachedReplayErr`). A sub that is caught up and idle-waiting on the
  producer is **not** stalled — the clock only runs while there is undelivered
  data.
- **Byte lag never closes a reader.** A reconnect that is 50 MiB behind but still
  advancing is exactly the blackout case reconnect exists to serve; killing it
  burns a ladder try and truncates the client for no gain. With 5c's spool such a
  reader is simply reading from disk and costs no hot RAM at all.
- **Primary (sub #0) is never failed with `ErrSubscriberLagged`** — that would
  force a reconnect ladder for a healthy generation. Instead give the write a
  **deadline** (`http.NewResponseController(w).SetWriteDeadline`, the same
  controller pattern already used for Flush). On breach, treat it exactly like
  today's write error: set `ClientDetached`, drop the reader, keep producing.
- **The deadline applies to every reader, and the write runs inline on the drain
  goroutine.** An earlier shape raced the write in a helper goroutine against a
  timer; on breach the subscriber was dropped while that goroutine stayed blocked
  on the dead socket, holding its chunk — a goroutine/buffer leak under reconnect
  churn, and worse, a second writer that could still touch the primary response
  after `WaitPrimary` had released it. Inline + `SetWriteDeadline` is the fix:
  the transport aborts the write, `WaitPrimary` is exact, and nothing is
  abandoned. A writer that does not implement `SetWriteDeadline` is logged once —
  it is genuinely un-interruptible, and pretending otherwise was the bug.
- The two mechanisms are the same policy at different layers: the per-write
  deadline catches a stall inside one blocked `Write`; the stall timeout catches
  a socket that accepts bytes so slowly that the offset never moves between
  writes.
- Threshold sizing: `LiveStreamReaderStallTimeout` must exceed
  `AttemptReconnectBudget` (and should be in the same range as the gateway's
  `InterChunkStallTimeout`, not the 1s ladder budget), otherwise a transient
  pause burns ladder tries instead of resuming. Plumb through host settings /
  admin.
- **Explicit non-goal:** 5a does not bound hot RAM for a slow-but-alive reader —
  its unread window is undeliverable-yet-needed by construction. That is 5c's
  job, and it must not be worked around by lowering a stall threshold until it
  starts hitting healthy readers.
- **Chunk the writes.** One wakeup must not turn a large backlog into a single
  multi-MiB write racing one deadline: a reconnect on a healthy-but-slow link
  would breach it and be failed as lagged even though it was progressing fine.
  Cap each write at `LiveStreamWriteChunkBytes` so the progress clock advances
  per chunk.

**5b — Remove the duplicate event copy (only if provably safe)**

During drain the same event text is retained twice: by the response processor
(which builds the `{"events":[…]}` durable envelope) and by `LiveStream.events`
(one alloc per line).

**Byte-identity: verified, and the answer is "no".** Measured against the drain
loop in `cmd/devshardd/inference/proxy.go` and `ExecutorResponseProcessor`, the
two retained sequences diverge in both directions:

| Case | Processor retains | Log retains |
|---|---|---|
| `data: {…}`, rewrite OK | `updatedLine`, no `\n` | `updatedLine` + `"\n"` |
| `data: [DONE]` / non-data | `line`, no `\n` | `line` + `"\n"` |
| Blank separator (`line == ""`) | **nothing** (processor is skipped by the `line != ""` guard) | `"\n"`, merged into the previous event |
| Rewrite error | `line` (appended *before* the error returns) | same raw `line` + `"\n"` (proxy forwards the fallback so live log and durable body stay event-aligned for resume cursors) |

Also note `updatedLine` is not the upstream text: `addOrReplaceIdValue`
round-trips through `map[string]interface{}`, so keys come back alphabetically
sorted and whitespace is normalized. So wholesale aliasing is out. What *is*
shareable is the payload bytes of successfully-rewritten lines — the
overwhelming majority of volume — with each structure keeping its own framing.

- Share via a **slab**: write the rewritten line once, have `LiveStream.events[i]`
  index `slab[start:end+1]` (with `\n`) and the processor's element be a string
  view of `slab[start:end]` (without). Blank separators are a log-only byte;
  rewrite-error lines are forwarded raw into both sides and are rare.
- **Ordering conflict with 5c.** Go strings are immutable and `[]byte(s)` copies,
  so a string view over slab bytes requires `unsafe.String` and a slab that is
  never re-sliced, compacted, or reused — which is exactly what head-trim wants
  to do. Whoever lands both must decide slab lifetime ownership first; if that
  cannot be made clean, 5b reduces to "bound it" and 5c does the work.
- Do **not** try to derive finish bytes from the log: the durable envelope
  JSON-escapes every event, so log bytes ≠ durable bytes, and usage extraction /
  envelope shape are the processor's contract. Forking that risks a
  `ResponseHash` mismatch.

**Measured: the duplicate copy is not the hot cost — the per-chunk id rewrite is.**
`common/completionapi/idrewrite_test.go` benchmarks the rewrite in isolation
against the shipped fixtures (Apple M-series, `-benchtime 300x -count 3`).
Per chunk, on the logprobs/token-ids fixture (45 chunks, ~766 B each):

| Variant | ns/chunk | allocs/chunk | B/chunk |
|---|---|---|---|
| Today (`encoding/json` map round-trip) | ~13,800 | ~306 | ~13,500 |
| Same logic on `goccy/go-json` | ~10,800 | ~253 | ~12,700 |
| Depth-aware surgical splice | ~48 | 0 | 0 |
| Memoized byte patch (learn once per stream) | ~22 | 0 | 0 |

`BenchmarkProcessStreamedResponse` lands within noise of the round-trip alone
(~14 µs/chunk, ~307 allocs/chunk), i.e. **the rewrite is essentially the entire
cost of the processor** and the retention 5b targets is a rounding error next to
it. A 766-byte chunk currently generates ~13.5 KB of garbage.

So 5b's RAM goal stands, but the CPU/GC win is in the rewrite:

1. **`goccy/go-json` drop-in** — byte-identical output, so `ResponseHash` does not
   move (asserted by `TestIDRewriteGoccyPreservesHash`). Already in the module
   graph. But measured at only ~1.3× on the heavy fixture (~1.9× on the light
   one), so it is cheap insurance, not the prize.
2. **Surgical rewrite (splice / memoized patch)** — 280–640× on the heavy fixture
   and allocation-free, and it removes the error-line divergence above, which
   makes slab sharing simpler. Two costs: it skips the incidental JSON validation
   the round-trip performed (malformed chunks now surface at `GetResponse`
   instead of being filtered at drain), and it changes the stored byte form.

**Hash sensitivity (correction to an earlier assumption).**
`StreamedCompletionResponse.GetHash` hashes `json.Marshal` of the **raw event
lines**, not extracted content, so a formatting change *does* move
`ResponseHash`. It stays self-consistent — the hash is always recomputed from the
same stored payload (`inference/validate.go` sha256s the payload it fetched;
`payloadstorage.ComputeResponseHash` re-parses the stored envelope) and is never
compared against a body generated on another node, since validation replays with
`stream:false` and compares logits. Everything validation actually consumes is
provably unchanged: `TestIDRewriteDownstreamSemanticsUnchanged` asserts an
identical typed event stream, usage, inference id, and completion text, while
pinning the hash difference as the known migration cost. Treat the surgical
rewrite as a coordinated byte-form change, not a drop-in.

- Acceptance (RAM): for a large synthetic stream, retained bytes attributable to
  one in-flight inference stay within `LiveStreamMaxRAMBytes` + one processor
  copy, with no per-line alloc blow-up.
- Acceptance (CPU): rewrite allocations per chunk drop to ~0 and the differential
  tests in `idrewrite_test.go` keep passing — every candidate must produce the
  same document, and nested `tool_calls[].id` / id-like text inside content must
  be left untouched.

**5c — Bound hot RAM: spool-backed log with a RAM window**

The earlier plan here was "head-trim below the slowest reader, spill later if
trims start costing reconnects". That shape has a structural flaw: trim may only
drop bytes *every* live reader has consumed, so a single reader that stops
consuming pins the window for as long as it is pinned, and the cap degrades from
a bound into a hope. Sizing around that is what pushed `LiveStreamMaxRAMBytes` to
tens of MiB — the cap was really a *resume horizon* (how long a blackout can last
before a reconnect has to buy a whole new generation), and paying for a resume
horizon in hot RAM is the wrong trade.

**Invert it: disk is the log, RAM is a cache of its tail.** Not a fallback tier
and not an opt-in mode — the single storage layout, with no "spill" branch to
reason about.

```text
LiveStream.Write ──► RAM window (events + forming)      ← bounded cache of the tail
                          │
                          └─► spool pump goroutine ──► <escrow>-<inference>.log
                                                       <escrow>-<inference>.idx
readers: offset ≥ bytesBase → RAM;  offset < bytesBase → pread the .log
```

1. **`LiveStreamRingBytes` (256 KiB) is the RAM window**, sized for "one write
   stall of the fastest reader", not for a generation. `LiveStreamMaxRAMBytes`
   demotes to a hard ceiling that only matters in the degraded case below.
2. **Head-trim's only barrier is the spool offset.** Reader offsets do not enter
   into it. A reader that has fallen behind the window is served by `pread` on
   the next iteration, re-evaluated per chunk, so there is no mode latch and no
   reader can pin RAM. This is the property the old design could not provide.
3. **The pump is a separate goroutine.** `Write` appends to RAM and wakes it;
   the pump copies the complete-event tail out from under the lock and writes it.
   A stalled disk costs RAM window, never ML progress — the R9 non-blocking
   invariant is structural rather than a review rule.
4. **`.idx` is what makes disk retention the resume horizon.** Entry *k* is the
   `.log` offset where content event *k* starts, so a cursor older than the RAM
   window resolves with one `pread` instead of `ErrResumeCursorPast`. Blank SSE
   separators are merged into the preceding event exactly as in RAM, so event
   numbering matches the gateway cursor and `replaySSEBodyFromCursor` in every
   tier. The lookup runs **off** `LiveStream.mu` — index entries are immutable
   once written, so the producer never waits on a file read.
5. **The spool is scratch, not state.** No `fsync`, removed on release, whole
   directory emptied at startup. Losing it costs at most one resume, exactly like
   losing the RAM log did. It is *not* the durable artifact and must never be
   confused with one.
6. **Payload storage is not usable for this** and must not be extended to be.
   Its interface is whole-blob write-once (`Store(...prompt, response []byte)`,
   one `BYTEA` row or one JSON file), and its contents are hash-bound:
   validators re-fetch the stored payload and require
   `sha256(stored) == MsgFinishInference.ResponseHash`. A row in a partial state
   would be replayed to a client as a complete completion (R9's partial-publish
   hazard) and would fail validation. Two artifacts, two jobs: spool mid-flight,
   payload store at finish.
7. **Degraded mode (no spool).** If the spool cannot be created or fails
   mid-stream, the generation still runs and still persists; only resume is lost.
   `AttachLiveStream` is refused with `ErrLiveStreamResumeUnavailable`, trim falls
   back to the slowest live reader, and past `LiveStreamMaxRAMBytes` that reader
   is dropped (`ErrSubscriberLagged`, or `ClientDetached` for the primary) so the
   ceiling always holds. Deliberately *not* a silent fallback to a large RAM
   window: a reconnect that cannot be served correctly should fail fast and let
   the gateway escalate.
8. **Never** relieve pressure by publishing a partial body into
   `completedResponses` / payload store — see the partial-publish hazard in R9
   (the stored hash would not match the finish message, and the client would be
   served a truncated completion; replay now closes such a body with
   `partialStreamErrorLine` rather than a synthesized `[DONE]`, which makes the
   truncation visible but does not make publishing one acceptable).

Cursor identity is the acceptance bar: a reconnect at
`(delivered_events, delivered_partial)` must yield byte-identical output whether
it resolves against RAM, against the spool index, or against a finished body.

**What this does not fix.** Per-generation RAM is now `O(ring)` for the *live
log* only. `ExecutorResponseProcessor` still retains every rewritten line for the
whole generation, and finish still materialises the body twice more
(`GetResponse` parses all lines into typed structs; `GetBodyBytes` marshals the
envelope; `PayloadStore.Store` takes a `[]byte`). Folding that into the spool —
streaming the envelope out of the record-framed log — is **5h**, and it is
hash-sensitive: the streamed encoding must be byte-identical to
`json.Marshal(SerializedStreamedResponse{...})` or `ResponseHash` moves.

**5d — Forming-line bound (defensive only)**

The real producer cannot grow `forming` without bound: the proxy writes
newline-terminated lines (`fmt.Fprintln`) and upstream lines are already capped
at `maxScannerBufferSize` (1 MiB). Keep a defensive cap for non-line writers and
treat breach as a stream error — **not** as a trigger to publish a durable body.

Breach must be **loud**. `Write` still returns success, deliberately: failing it
would abort an ML drain that still owes us a durable body, and the proxy would
mis-record the failure as a client detach. But the live log is now truncated
while the durable body continues, and that divergence has to be visible rather
than hidden behind a successful return — so breach also disables resume for the
generation (attach returns `ErrLiveStreamFormingOversize`) and logs a warning
naming the truncation.

**5e — Payload store as the last resume tier**

The resume horizon today is far shorter than the durable retention, and the two
are not connected:

| Tier | Lives until | Readable by reconnect? |
|---|---|---|
| `LiveStream` RAM window | trimmed to `LiveStreamRingBytes` continuously | yes, `AttachLiveStream` |
| `LiveStream` spool (disk) | `Release` after `RunExecution` returns | yes, `AttachLiveStream` via `.idx` |
| `completedResponses` | the finish tx appears in an applied diff | yes, `hasCached` |
| payload store (disk) | inference sealed (~1h via seal grace) | **no read path** |

- Give the host a payload reader (the devshardd side already has
  `retrievePayloadsWithAdjacentEpochs`) and consult it in `signReceipt` after
  `completedResponses` misses, before declaring `ReasonInferenceDisappeared`.
- Replay it through `replaySSEBodyFromCursor` exactly like the RAM cache, so the
  resume cursor behaves identically across all three tiers.
- This is what makes cache eviction safe for a *completed* generation: an evicted
  prefix is no longer a resume cliff. Mid-flight the equivalent role is played by
  5c's spool, which is why the two must not be conflated — the payload store is
  written once, whole, at finish, and is hash-bound; the spool is scratch and
  exists only while the generation is producing.

**Timeout alignment (single source of truth):** do **not** keep independent
5m / 30m ops clocks. Derive host drain, LiveStream TTL, and gateway attempt hard
timeout from the protocol **`SessionConfig.ExecutionTimeout`** (default
**32 min** — the window after `ConfirmedAt` after which a missing
`MsgFinishInference` can start an execution-timeout / missed challenge).

| Derived clock | Role | Bound |
|---|---|---|
| Host `executionDrainTimeout` / `mlNodeHTTPTimeout` | ML generate + body drain after gateway detach | `ExecutionTimeout` |
| Host `InflightReplayBufferTTL` | LiveStream stay attachable | `ExecutionTimeout` |
| Gateway `StreamingAttemptHardTimeout` (and matching non-stream attempt caps) | How long the gateway waits on a crowned winner | `ExecutionTimeout` |

Rationale: producing, buffering, or holding a winner **past** the protocol
missed window cannot help the client and burns host/gateway resources on an
inference the chain may already mark missed. Prefer deriving from
`ExecutionTimeout` over inventing a fourth product timeout.

**5f — Metrics (feeds Step 8)**

Emit at least: RAM window bytes gauge, **trimmed bytes / trimmed events**, spool
bytes written and spool lag (`tip - spoolOffset` — the only thing that can now
push the window over its target), reads served from spool vs RAM,
`spool_unavailable` / spool failures, resume failures past the spool tip,
`subscriber_lagged` (reconnect only, stall-triggered), and write-deadline
detaches by role.

Because 5a never closes readers for depth and 5c makes depth free, `reader_lag_bytes`
(max over live subs, by role) is a pure client-health gauge: it says how far
behind a client is, not how much RAM is at risk. Alerting on it must not be wired
to any close action. Dashboard / lint in Step 8.

**5h — Stream the durable envelope from the spool (not started)**

With a record-framed spool the finish path could build `{"events":[…]}` by
streaming the log instead of from a second full in-RAM copy, retiring
`ExecutorResponseProcessor`'s per-line retention and making per-generation RAM
`O(ring)` end to end. Two hard constraints: the streamed encoding must be
byte-identical to `json.Marshal(SerializedStreamedResponse{...})` or
`ResponseHash` moves, and `PayloadStore.Store` takes a `[]byte`, so one full-body
materialisation at finish remains until the storage interface grows a streaming
write. Even so the win is real: full-body exposure drops from *every in-flight
generation* to *every finishing generation*.

**5g — Hop timestamps (gateway ↔ host ↔ ML)**

Goal: measure **full path latency** across gateway → host (`devshardd`) → ML node →
host → gateway, not only in-host buffer residency. Existing signals are one-sided: the
host has aggregate ML call duration; the gateway has `firstTokenNano` / `lastChunkAt` in
`raceWriter.Write` (`redundancy.go`) — but nothing today joins those clocks on one stream.
vLLM's chunk `created` is a once-per-request Unix second reused on every line, not a
per-chunk emit time, so it is not part of this design.

**What travels on the wire (absolute host times).** Gateway consume time does **not**
— the gateway stamps that locally when it reads each `data:` line.

| Stamp | Where | Absolute? | Stored in RAM/store? |
|---|---|---|---|
| `req_ms` | Host saw the inference HTTP request (before / as execution starts) | Yes (host wall ms) | Once per generation (receipt) |
| `ml[]` | Host read that chunk from the ML node (`scanner.Scan` in `proxy.go`) | Yes (host wall ms) | Parallel array next to `LiveStream.events`; sidecar at finish |
| `w[]` | Host **started writing** that chunk to *this* gateway connection | Yes (host wall ms) | **Not** stored in the log — stamped at emit time per subscriber write (fresh on reconnect) |
| gateway recv | Gateway SSE parser saw the `data:` line | Yes (gateway wall ms) | Local only |

That yields the hop chain operators care about:

```text
gateway_send  →  req_ms          (gateway → host)          [cross-clock]
req_ms        →  ml[i]           (host → ML → host)        [same host clock]
ml[i]         →  w[i]            (host buffer residency)   [same host clock]
w[i]          →  gateway_recv[i] (host → gateway)          [cross-clock]
```

`req_ms` is the minimum absolute anchor the host must report: without it, gateway→host
cannot be measured from the response stream. Per-chunk `ml` / `w` extend that to
TTFT and inter-chunk hops.

**Three constraints rule out the obvious designs.** Establish these first, because each
one silently breaks something that has no test today:

1. **The chunk JSON is hash-covered.** `ResponseHash` is `sha256` of
   `json.Marshal(SerializedStreamedResponse{Events: lines})`
   (`common/completionapi/completionresponse.go`), and validators re-fetch the stored
   payload and require `sha256(stored) == MsgFinishInference.ResponseHash`
   (`cmd/devshardd/inference/validate.go`). Injecting a `devshard_ts` field into each
   `data:` chunk would change the stored bytes and fail every validation. The processor
   appends the *same* string it returns for proxying
   (`common/completionapi/responseprocessor.go`), so "mutate on the way out" is not
   available there either. **Timestamps must never enter `streamedResponse` /
   `responseBody`.** Host stamps are captured alongside the line (and id rewrite);
   the stored/proxied `data:` bytes stay unchanged.
2. **A new `devshard_*` event leaks to end users on today's gateways.** An envelope that
   is neither `devshard_receipt` nor `devshard_meta` falls through to the
   forward-to-client branch (`transport/client.go`), so a pre-patch gateway would write
   raw devshard JSON into a user's SSE stream. Interleaved protocol events are therefore
   not backward compatible.
3. **Any extra line in the log shifts the resume cursor.** `deliveredEvents` counts
   non-blank data lines, and the host maps that count onto buffered bytes in
   `deliveredAbsOffsetLocked`. A new gateway would intercept a timestamp line (not
   counting it) while the host's log did count it — an off-by-N offset, i.e. exactly the
   gap/duplicate R2 exists to prevent, and only against *mixed-version* pairs.
   **Timestamps must stay out of the cursor-counted byte log.**

**Carrier: SSE comment lines, injected at the writer, batched per drain.**

SSE comments (`: …`) are already dropped silently by the gateway parser — the
non-`data:` branch in `handleSSELine` returns without forwarding and without warning
for a leading `:`. That gives a channel which is invisible to old gateways, never
reaches end users, and costs nothing to ignore.

```text
: devshard-ts {"b":128,"ml":[1712345678901,1712345678936],"w":[1712345678940,1712345678975]}
data: {"choices":[{"delta":{"content":"one"}}]}

data: {"choices":[{"delta":{"content":" two"}}]}
```

- `b` — absolute wire event index of the first event described (same counting rule as
  `deliveredEvents`: non-blank data lines; blank separators skipped).
- `ml` — host wall-clock ms when each event was read from the ML node (original times,
  including on reconnect replay). Parallel array: one entry per following `data:` event
  in this batch.
- `w` — host wall-clock ms when this subscriber write began for those events (**this
  connection**; different on a reconnect catch-up). Same length as `ml`.
- `req_ms` — absolute host wall-clock ms when the inference request was seen, sent
  **once** as an additive field on `devshard_receipt` (same compatibility story as any
  unknown receipt field).

The comment is emitted **immediately before the byte range it describes**, inside the
same subscriber write. Cap content events per write (and thus per comment) at N —
one comment ↔ one write ↔ ≤N events. A reconnect catching up on thousands of
buffered events uses multiple short writes, never one enormous comment or stacked
comments before the data (gateway pairing keeps a single pending batch).

**Mid-event attach:** a fresh `Subscribe` / cache replay whose cursor lands inside a
content event that already has `ml[]` prepends a 1-entry `:devshard-ts` for that
open event before the remainder (new HTTP body — safe). Same-connection
continuations stay data-only so a comment cannot splice into an open SSE line.

Critically, the comment is generated **at the writer**, not appended to `LiveStream`.
The log keeps holding exactly the ML data lines it holds today, so cursor arithmetic,
`eventsBase`/`bytesBase`, trim, and the hashed body are all untouched.

**Storage: parallel arrays in RAM; one-shot sidecar at finish (not a disk stream).**

`LiveStream.events` is a `[][]byte`; add an append-only `[]int64` for `ml` (absolute
content-event index) whenever a new content event is appended — a blank separator merged
into `events[last]` inherits the parent entry and does not grow `ml`. Phase 1 keeps `ml`
untrimmed so finish→cache replay still has early-event times after the RAM window moves;
live drain looks up `ml[eventsBase+i]`. Capture: host wall ms at `LiveStream` append
(same goroutine as `scanner.Scan` → `Fprintln` → `Write`; do not parse or mutate the
line for timestamps).

Do **not** stream the live buffer to disk per chunk. Keep timestamps in RAM while
draining; on finish, write them once next to the existing payload persist (same place
`response_payload` already lands — Postgres column / file JSON field), **outside**
`response_payload` so the hash is unchanged. That is a sidecar, not a second on-disk
SSE log. Phase 1 can ship RAM + live/cache tiers only; store-tier reconnects need the
sidecar (phase 2). Metrics must carry `tier` (`live` | `cache` | `store`) so
hour-old store replays do not dominate hop histograms.

`w` is not persisted in the sidecar — it is per-connection emit time.

**Both connection types, all three tiers.** Injection belongs in the subscriber drain
path (primary = subscriber #0 and every reconnect) plus `replaySSEBodyFromCursor` for
cache/store. Reconnect must re-emit stored `ml` and fresh `w`.

**Clock skew.** Absolute cross-machine hops (`gateway_send → req_ms`, `w → gateway_recv`)
include NTP/clock skew; document that and prefer distributions / comparisons under the
same deploy rather than single-sample SLOs. Same-host hops (`req_ms → ml`, `ml → w`) are
skew-free on the host clock and are the trustworthy residency / ML-wait numbers.
Optional derived relative offsets (`ml[i] - req_ms`) can still be logged for
skew-free drift charts, but the wire format carries **absolute** ms so full-path
latency is possible.

**Backward compatibility.**

| Pair | Behavior |
|---|---|
| Old gateway ← new host | Comment lines hit the `strings.HasPrefix(line, ":")` branch and are dropped silently. Extra `req_ms` on `devshard_receipt` is ignored (`json.Unmarshal` discards unknown fields) |
| New gateway ← old host | No comments / no `req_ms`; stamps absent. Record no metric — not an error, must not fail a resume |
| Either direction, reconnect | Cursor space unchanged by construction |
| Non-streaming | Out of scope for per-chunk hops; `req_ms` on receipt is still useful for gateway→host |

No protocol version bump; not gated on `v5`.

**Gateway side.** Parse the comment next to existing protocol-event handling; pair
`b + k` with the local receive time of the k-th following data line. Also record
gateway request-send time so `gateway_send → req_ms` is available. Keep this pairing
**separate from `deliveredEvents`** (client-forwarded bytes; zero for suppressed losers).

**Security.** Host timestamps are unverifiable. Same R8 rule as blips: metrics/logs
only, never `Decide` / picker / quarantine. Bound comment line and array length; treat
malformed comments as absent, not stream errors.

**Metrics (hand to 5f / Step 8).** Histograms for each hop, e.g.
`devshard_gateway_hop_seconds{hop,participant_key,model,tier}` with
`hop` ∈ `gw_to_host` | `host_to_ml` | `ml_to_host` | `host_buffer` | `host_to_gw`,
plus a coverage counter for events missing stamps while hosts roll out.

#### Non-goals for Step 5

- Replacing the append-only log or primary-as-#0 design (done).
- Client-facing resume / Last-Event-ID (separate proposal).
- Making blips affect routing (R8 — observability only).
- Retiring the response processor's per-line retention — that is 5h, and it
  changes hash-critical code.
- Any mid-flight write to `completedResponses` / payload store.
- Making the spool durable (`fsync`, crash recovery, cross-instance sharing).
  It is scratch by design; surviving a host restart is Step 10's problem.

#### Tests (unit)

- Reconnect sub whose offset stops advancing while bytes are pending gets
  `ErrSubscriberLagged` after `LiveStreamReaderStallTimeout`; primary under the
  same stall is **not** lag-failed, but a primary whose write deadline is
  breached is dropped as a reader with `ClientDetached` set while the producer
  keeps appending and finish still publishes.
- **Slow-but-alive reader is never closed:** a reconnect draining far slower than
  the producer, ending far behind the tip in bytes, runs to completion with
  byte-identical output and no `ErrSubscriberLagged` — including once it has
  fallen out of the RAM window and is being served from the spool.
- A caught-up sub parked in `cond.Wait()` for longer than the stall timeout,
  because the producer itself is slow, is **not** closed.
- **Head-trim is independent of readers:** with a reader pinned at offset 0 for a
  long burst, the RAM window still converges to `LiveStreamRingBytes` and
  `bytesBase` advances past that reader — and when it unblocks it receives every
  produced byte, contiguously, from the spool.
- Cursor **older** than the RAM window resolves through `.idx` and is
  byte-identical to an untrimmed control, at an event boundary and mid-event;
  only a cursor past the spool tip gets `ErrResumeCursorPast`.
- A large backlog is delivered as multiple bounded writes, none exceeding
  `LiveStreamWriteChunkBytes`.
- No body byte reaches a writer after `WaitPrimary` returns, including when the
  primary was dropped by a write-deadline breach (regression test for the
  abandoned-write leak).
- Degraded mode: with no spool, `Subscribe` is refused with
  `ErrLiveStreamResumeUnavailable`, and a pinned reader is dropped so the RAM
  window stays within `LiveStreamMaxRAMBytes`.
- `Release` removes both spool files; a startup sweep empties the directory.
- No partial body ever appears in `completedResponses` / payload store while
  executing — assert the map is empty for that nonce until finish, and that a
  mid-flight reconnect never takes the `hasCached` branch.
- After `completedResponses` is evicted (finish tx applied in a diff), a
  same-nonce reconnect still resumes **from the payload store** at the cursor
  instead of failing `ReasonInferenceDisappeared` (5e).
- Retained bytes for a large stream stay within the 5b bound; no per-line alloc
  blow-up.
- Producer `Write` performs no disk I/O: with the spool pump blocked, `Write`
  still returns promptly.
- Cursor: blank SSE separators do not desync wire event/partial across live,
  spooled, and finished replay.
- 5g cursor isolation: a stream carrying `: devshard-ts` comments produces
  **byte-identical** log contents and an identical resume cursor to the same stream
  without them; a reconnect at the same cursor yields the same bytes either way.
- 5g timestamp/trim alignment: after head-trim, the timestamp of a surviving event is
  still the one captured for *that* event (assert against a pre-trim control); a blank
  separator merged into the previous event adds no entry and shifts nothing.
- 5g resume fidelity: a mid-flight reconnect reports the **original** `ml` for
  replayed events, not the replay time, and a **fresh** `w` for this connection; cache
  and store tiers tagged with `tier`.
- 5g hop anchors: `req_ms` is present on receipt; gateway can form
  `gateway_send → req_ms` and `w → gateway_recv` without host-side consume stamps.
- 5g compatibility: the current gateway parser drops `: devshard-ts` silently — not
  forwarded to the client, no `sse_unexpected_line` warning; unknown `req_ms` on
  `devshard_receipt` is ignored; a stream with no comments records no hop metrics and
  no error.
- 5g hash safety: enabling timestamps leaves `ResponseHash` and the stored
  `{"events":[…]}` payload byte-identical to the same generation with them disabled.
- 5g bounds: an oversized or malformed comment is ignored rather than failing the stream,
  and a catch-up drain over thousands of buffered events splits into bounded comment
  lines instead of one huge one.

### Step 6 — Persist soft signals to the gateway store (periodic)

**Deferred** until [`ak/gateway-v4-postgres`](https://github.com/gonka-ai/gonka/tree/ak/gateway-v4-postgres)
merges into `gateway-v4`. This step targets the postgres store framework
(`GatewayStore` interface, SQLite + Postgres backends, D5 migrate + D6 sync
journal). Implementing it on the current SQLite-only `*GatewayStore` would be a
partial land that has to be rewritten after that merge — do not start here.

R8 soft signals today live only in `PerfTracker` RAM (blip timestamps in a 5m
window; stream bps not retained at all). A gateway restart — or a hybrid PG
failover that drops in-memory state — wipes operator context for the slow /
reconnect-prone hosts that Steps 4–5 deliberately refuse to route on. Persist
them, but **not on the hot path**: flush on a timer (default **1 minute**), the
same class of durability as quarantine state in
[host-health.md](./host-health.md), not as request-scoped writes.

**Framework (follow existing HA patterns — do not invent a third schema path):**

| Concern | Doc / code |
|---|---|
| Additive schema under multi-version HA | [storage-design.md](./storage-design.md) — **Schema Evolution Across Devshard Versions** (forward-only, append-only; new tables/columns with defaults; no drop/rename while older binaries may still touch the store) |
| Gateway store backends (SQLite / Postgres / hybrid + sync journal) | [gateway-postgres-backend-plan.md](./gateway-postgres-backend-plan.md) — `GatewayStore` interface, `PRAGMA table_info` / `ADD COLUMN IF NOT EXISTS` (SQLite) and `information_schema` / `ADD COLUMN IF NOT EXISTS` (PG), one-shot migrate + D6 journal |
| Session `schema_migrations` helper | `devshard/storage/migrate/` — **not** used for gateway tables; gateway keeps inline EnsureSchema at open (called out as out-of-scope for that helper in storage-design). Still obey the same **additive** contract |

**Schema.** New table `participant_soft_signals` (mirror name on SQLite + Postgres):

| Column | Type | Meaning |
|---|---|---|
| `participant_key` | TEXT PK | Same key as throttle / quarantine |
| `blip_timestamps_json` | TEXT / JSONB | Pruned list of Unix-ms blip times inside `ReconnectBlipWindow` |
| `last_blip_at` | TEXT / TIMESTAMPTZ nullable | When the most recent blip was **recorded** (event time, not flush time). Null if the window is empty |
| `stream_bps_ewma` | REAL | Rolling EWMA of terminal winner `stream_bps` (0 if none) |
| `stream_bps_p50` | REAL | Optional windowed p50; 0 if insufficient samples |
| `stream_bps_p95` | REAL | Optional windowed p95; 0 if insufficient samples |
| `stream_bps_samples` | INTEGER | Samples contributing to the rolling summary |
| `stream_bps_measured_at` | TEXT / TIMESTAMPTZ nullable | When the rolling bps summary was last **updated from a terminal sample** (event time). Null if `stream_bps_samples == 0` |
| `updated_at` | TEXT / TIMESTAMPTZ | When this row was last **flushed** by the persist ticker |

Event timestamps (`last_blip_at`, `stream_bps_measured_at`) are what operators and
TTL/prune logic should read — a 1m flush must not make a 59s-old blip look fresh.
`updated_at` is only “last successful upsert,” useful for store/journal debugging.

Wire into `GatewayStore`:

- `LoadSoftSignals() ([]ParticipantSoftSignalRow, error)`
- `UpsertSoftSignals(rows []ParticipantSoftSignalRow) error` (batch upsert; empty → no-op)
- `DeleteSoftSignals(keys []string) error` — drop rows whose windows fully expired and have no rolling summary

Add the table to the D5 one-shot SQLite→PG copy list and the D6 sync-journal
key space (`participant_key`, upsert/delete), same as
`participant_throttle_state`. Schema is additive only: land the full table in
one EnsureSchema step; later columns use `ADD COLUMN IF NOT EXISTS` with
defaults.

**Runtime.**

1. **Boot:** after `LoadParticipantThrottles` / settings load, `LoadSoftSignals`
   into `PerfTracker` (hydrate blip timestamps + rolling bps). Expired timestamps
   are pruned on load using `ReconnectBlipWindow` against `last_blip_at` /
   entries in `blip_timestamps_json`, not against `updated_at`.
2. **Hot path unchanged:** `RecordReconnectBlip` / terminal `stream_bps` observe
   stay in-memory only (they stamp `last_blip_at` / `stream_bps_measured_at` in
   RAM). No DB I/O on `Write`, ladder exit, or attempt terminal.
3. **Ticker** (`SoftSignalPersistIntervalMS`, default `60000`; `0` = disabled):
   snapshot dirty participants from `PerfTracker`, prune windows, batch
   `UpsertSoftSignals` with event timestamps copied through and `updated_at = now`.
   Participants with an empty blip window and zero bps samples are deleted rather
   than written as empty rows.
4. **Shutdown:** best-effort final flush (bounded; failure is WARN, not fatal —
   soft signals must not block drain).
5. **Still not routing:** loaded or persisted soft signals never feed `Decide`,
   the picker, or quarantine. Same R8 rule as today.

**What is *not* persisted:** Prometheus counters/histograms (Step 8 scrapes
process memory), per-attempt terminal samples, or `RequestSample` history.
Only the operator-facing rolling soft state that survives restart.

**Tests.**

- Store parity (sqlite + postgres): upsert / load / delete; additive open twice
  is a no-op; hybrid journal drain includes soft-signal upserts and deletes;
  `last_blip_at` / `stream_bps_measured_at` round-trip unchanged across a flush
  while `updated_at` advances.
- `PerfTracker`: dirty-bit / snapshot; hydrate restores blip count and EWMA;
  event timestamps older than the window are dropped on load and on flush
  (`updated_at` alone must not keep a stale row alive).
- Interval: with a fake clock / short interval, N blips in RAM produce exactly
  one batch upsert per tick (no write amplification on each blip).
- `Decide` / shadow-quarantine unchanged after hydrate of a large blip count
  (reuse `TestDecision_ReconnectBlipsDoNotChangeRouting` shape).

### Step 7 — E2E coverage ✅

**Landed (partial; see scenarios).** Harness knob `MultiConfigOpts.VersionName` +
`BootReconnectStack` stamps a v5 escrow without the full Step 9.2 rollout. Admin
`POST /v1/admin/settings` accepts `attempt_reconnect_*` (in-memory apply; DB columns
remain Step 8). Host test fault `DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES` injects a
gateway↔host drop while ML drain continues (env patched onto **every** versiond
service). The injector lives in `host/livestream_fault_testenvci.go` behind
`//go:build testenvci` and is compiled out of release binaries, so the v5 leg builds
with `DEVSHARD_BUILD_TAGS=testenvci`; `host/livestream_fault.go` is the production
no-op. Run target: `make -C devshard/testenv citest-attempt-reconnect`.

Citest coverage landed:
- `TestAttemptReconnect_AdminEnables` — admin knobs apply in-process
- `TestAttemptReconnect_V2ProtocolSkipsSameNonce` — protocol gate: PartialStream fails cleanly on v2
- `TestAttemptReconnect_V5MidStreamDetachResumesSameNonce` — mid-stream detach →
  `winner_reconnect_resumed`, one continuous completion, one finished inference

- **`skipped_protocol` does not exist yet.** Scenario 2 asserts *behavior* only; counter
  waits on Step 8.
- Scenarios 3–4, 6, 8 remain unit-covered / follow-up citest; scenario 7 unit-landed
  (`TestRunInference_V5SecondDropAfterResumeDoesNotRerunLadder`).

`devshard/testenv/citest`, `devshard/cmd/devshardctl/e2e`.

Ordering: after Steps 1–6 so behavior (including R9 lag / head-trim and soft-signal persist) is
covered before Prometheus / dashboards (Step 8) and rollout (Step 9).

**Scenarios.**

1. **Mid-stream drop, v5, reconnect enabled.** The client receives one complete, non-duplicated
   completion, and exactly **one** `MsgFinishInference` / one paid inference. Variants:
   - reconnect **after** the host finished draining ML and persisted the body — served from storage,
     live buffer already forgotten;
   - head-trim, cursor **inside** the retained window — resumes with no gap or duplicate;
   - head-trim, cursor **older** than the window — fails cleanly into escalation rather than
     delivering a truncated completion.
2. **Mid-stream drop, v4, reconnect enabled.** No same-nonce reconnect; today's escalation / failure
   contract is unchanged. (Counter assertion moves to Step 8 — see blocker above.)
3. **Host permanently down after the drop, v5.** Escalation still happens after the budget and the
   outcome matches today's contract.
4. **Streaming and non-streaming clients.** For a fixed seed, a resumed stream and an uninterrupted
   one must agree on aggregated **content and usage** — not on raw bytes, since chunk ids are
   rewritten per chunk (`idrewrite.go`) and will legitimately differ.
5. **Helpers to build on:** `killableClient` / `streamContentThenErrClient` in `proxy_test.go` at
   unit level, `mockopenai`'s `PartialStream` fault at host level.

The next three were added by the pre-Step-7 audit; each covers a contract with no end-to-end
coverage today.

6. **R4 winner continuity end-to-end.** Host A delivers a prefix and drops; the budget expires and a
   hedge on host B races ahead while A resumes slowly. The client stream must contain **only** A's
   bytes, B must settle as a loser, and one reconnect blip must be recorded for A with routing
   unchanged. This is the second of the plan's three headline behaviors and is currently only
   unit-tested (`redundancy_reconnect_test.go`), so nothing proves it end-to-end through the real
   writer chain.
7. **Second drop after a successful resume.** R3 makes the budget and try count **per attempt, not
   per drop**, and the ladder one-shot per nonce (`reconnectLadderUsed`). Assert that a stream which
   resumes and then breaks again does **not** get a fresh 1s window or a second ladder, and lands in
   `winner_failed_after_content`. Untested at every level today.
8. **Reconnect while the client is already gone.** After a client disconnect the gateway keeps
   draining for settlement. Assert the nonce still finishes exactly once, and that the delivered
   prefix does not advance over bytes the client writer swallowed — the invariant behind the
   `isClientDetached` / `clientFlag` guard in `raceWriter`.

### Step 8 — Settings, metrics, dashboards

**Deferred** until [`ak/devshard-observability-e2e`](https://github.com/gonka-ai/gonka/tree/ak/devshard-observability-e2e)
merges into `gateway-v4`. Metrics, dashboards, and OTel reconnect spans extend that branch's
`gateway.attempt` / phase-span / dashboard-lint stack (parent-plan
[Step 16](./gateway-always-stream-upstream-plan.md)) — do not invent a parallel observability
path earlier. Soft-signal gauges also wait on Step 6 (postgres store).

They instrument the same-nonce reconnect path from Step 3 and the winner-continuity path from
Step 4 once the observability base is in.

Gauges that reflect soft-signal state (`participant_reconnect_blips`, rolling
stream bps) read from the **hydrated** `PerfTracker` (Step 6), so a restarted
gateway still shows recent pressure without waiting for new traffic.

1. Add the R7 settings with conservative defaults, plumbed through the admin endpoint (including
   R9 live-log caps once Step 5 lands, and `SoftSignalPersistIntervalMS` /
   `ReconnectBlipWindowMS` from Step 6).
2. Metrics: `devshard_gateway_attempt_reconnect_total{result,protocol_version}` (`resumed`,
   `budget_expired`, `receipt_only`, `error`, and `skipped_protocol` when R0 refuses),
   `devshard_gateway_attempt_reconnect_seconds`,
   `devshard_gateway_winner_continuity_total{outcome}` (`resumed`, `reset`, `failed`), and a counter
   for reservations that blocked a secondary crown. Host-side (Step 5f): RAM window bytes gauge,
   trimmed bytes/events, spool bytes and spool lag, spool-vs-RAM read split, `spool_unavailable`,
   reconnect-only `subscriber_lagged` (stall-triggered), `reader_lag_bytes`, and primary
   write-deadline detaches.
3. **Blip observability (R8).** Today a blip only reaches a `reconnect_blip` log line, so nothing
   aggregates it. Add, at the single `recordReconnectBlipOnce` call site:
   - `devshard_gateway_reconnect_blips_total{participant_key,model,outcome}` — one increment per
     ladder run, `outcome` = `resumed` | `exhausted`, so a host that drops but always resumes is
     distinguishable from one that loses requests. Labels match the existing
     `devshard_gateway_attempts_started_total` shape.
   - `devshard_gateway_participant_reconnect_blips{participant_key,model}` — gauge of
     `ReconnectBlipCount` inside `ReconnectBlipWindow`, published like
     `devshard_gateway_participant_quarantine_state`, so operators see *current* blip pressure
     without deriving it from a counter.

   Document on the panel that these are informational: blips do not change routing, so a rising
   blip rate is a signal to investigate a host (or to reconsider the record-only policy), not an
   explanation for extra attempts. Cross-check against
   `devshard_gateway_attempt_reconnect_total{result}` — blips should track ladder runs 1:1.
4. **Stream-speed observability (R8).** Record bytes/sec on every attempt that forwarded
   content (see R8 definition). Export:
   - `devshard_gateway_stream_bytes_per_second{participant_key,model,visibility}` — histogram
     of terminal `stream_bps` (`visibility` = `winner` | `suppressed`). Observe once per
     attempt at terminalization next to the existing attempt-terminal / sample path; do not
     scrape from the hot `Write` path.
   - `devshard_gateway_stream_bytes_total{participant_key,model,visibility}` — counter of
     forwarded bytes (same labels), so operators can derive volume independently of rate.
   - Rolling summary on the participant (hydrated from Step 6): e.g. EWMA or windowed
     p50/p95 bps in `PerfTracker`, exposed as
     `devshard_gateway_participant_stream_bytes_per_second{participant_key,model,quantile}`
     for “is this host currently slow?” without PromQL over a long histogram window.

   Panel notes: informational only in this plan — do not auto-quarantine or force secondaries
   from low bps. Correlate with reconnect blips and `winner_stalled_after_content` logs: a host
   with healthy TTFB, low bps, and rising blips is the slow-but-alive case R4 deliberately
   refuses to splice away.
5. Extend the gateway dashboard; `gateway_dashboard_test.go` and the observability dashboard lint
   guard the metric names (blips + stream bps + reconnect totals).
6. **OTel spans (after parent Step 16 / `ak/devshard-observability-e2e`):** `attempt.reconnect`,
   reconnect TTFB, time-to-first-new-chunk past the delivered offset, stream bytes / bps on the
   attempt span, and `attempt.winner_switched` / `attempt.failover` when the race moves to
   another nonce. Reuse the branch's `gateway.attempt` / phase-span APIs.

### Step 9 — Rollout

1. Ship Steps 1–6 in the **gateway-v4** binary with `AttemptReconnectEnabled=false` and the R0
   protocol gate in place (dormant paths; ≤v4 sessions cannot activate even if the setting is
   flipped by mistake). Land Step 7 E2E against that binary; land Step 8 metrics/dashboards (and
   OTel once parent Step 16 is available) before soaking with the flag on.
2. Land host Steps 2–3 of the parent plan under the **v5** protocol name; approve `v5` hosts.
3. Enable `AttemptReconnectEnabled` on soak once v5 escrows exist; watch
   `devshard_gateway_attempt_reconnect_total` (including `skipped_protocol` staying dominant on
   remaining v4 traffic), per-request attempt counts on v5 (they should **drop**),
   `devshard_gateway_critical_user_failures_total` for `winner_failed_after_content`, and
   duplicate-`MsgConfirmStart` alarms.
4. Flip the default once reconnects dominate escalations for transient drops on v5 traffic.

### Step 10 — Cross-instance `devshardd` HA / ML reattach (**deferred**)

Tracks [issue #1466](https://github.com/gonka-ai/gonka/issues/1466) §4
(**HA / fault-tolerance across `devshardd` instances**). Complements same-process same-nonce
reconnect (Steps 1–9): those keep one host process alive and resume its `LiveStream` / payload
store. This step covers **process or machine loss** while generation is still running on the ML
node.

**Prerequisite topology (already exists):** multi-instance `versiond` / `devshardd` behind
`versiond-router` with sticky escrow hashing and **shared Postgres**, as documented in
[high-availability-architecture.md](./high-availability-architecture.md) (§3–4). HA citest
coverage for that stack (validation leases, rolling update, multi-replica catch-up) is already
landed under `devshard/testenv/citest` — do not rebuild the topology harness here.

**Gap today:** in-flight inference progress (live log, open ML HTTP body, RAM
`completedResponses` before persist) is still **process-local**. If the serving `devshardd`
reboots or the sticky router fails over mid-stream:

1. The gateway can same-nonce-reconnect to *another* replica (or the restarted process), but
2. That replica has no live attach target and often no durable body yet, and
3. Today's ML client tear-down on host death **cancels the vLLM / mock stream**, so there is
   nothing left to reattach to — the work must regenerate or fail.

**Required product change (MLNode + host):**

1. **MLNode keeps generating across host disconnect.** Closing or losing the host→ML HTTP
   stream must not abort the job. The node retains an in-flight generation keyed by a stable
   inference / request id (same spirit as host Step 3 drain: delivery loss ≠ work cancellation).
2. **A different `devshardd` can reattach** to that still-running job (or to a completed buffer
   on the ML side), then serve the gateway the live tail / remainder from the resume cursor.
3. **Durable handoff on the host side** still applies once drain finishes: payload store +
   `MsgFinishInference` on shared Postgres so a third failover after completion uses today's
   storage reconnect path (Step 5e) without needing the ML job.

```text
Before:  gateway ──► versiond-0 / devshardd-A ──► ML job J (streaming)
Reboot A / sticky failover
After:   gateway ──► versiond-1 / devshardd-B ──reattach──► same ML job J
                     (shared Postgres for escrow / diffs / finished payload)
```

**Gateway role:** largely unchanged — same-nonce reconnect with
`(deliveredEvents, deliveredPartial)` still applies. The sticky router may land the reconnect
on a different replica; that replica must answer via ML reattach or durable replay, not
"inference disappeared". Blips / stream-bps observability (R8 / Step 8) continue to attribute
to the **participant**, not a specific HA replica.

**Non-goals for this step**

- Replacing sticky routing or inventing gateway-side replica addressing (the gateway still
  talks to the participant URL / router).
- Cross-participant failover (that remains new-nonce escalation).
- Client-facing resume APIs.

**E2E (deferred until after `ak/devshard-observability-e2e` merges):**

That branch separates **mock ML nodes from `devshardd`**, which is the minimum harness needed
to reboot a host without killing the generation process. Until then, in-process
`mock-openai` / co-located mocks cannot prove ML reattach — killing the host kills the stream
source.

| # | Scenario | Assert |
|---|---|---|
| HA1 | Mid-stream **reboot** of the sticky `devshardd` / `versiond` child while ML keeps running | Client stream continues (gap-free from resume cursor); exactly one `MsgFinishInference`; no second ML generation for the nonce |
| HA2 | Sticky failover to another HA replica mid-stream (kill A, traffic → B) | B reattaches to the same ML job; client-visible bytes match an uninterrupted control for content + usage |
| HA3 | Reboot **after** drain+persist | B serves from shared payload store / `hasCached` (no ML reattach required); cursor skip still correct |
| HA4 | ML job actually gone (true cancel) | Clean resume failure → gateway ladder / escalate; no silent truncated `[DONE]` |

Helpers to extend once the observability-e2e mock-ML split lands: existing HA compose
(`versiond-0..N`, `versiond-router`, `devshard-postgres`), plus a kill/restart of one
`devshardd` child **without** stopping the separated mock MLNode. Do **not** schedule these
scenarios in Step 7; they block on that branch and on the MLNode keep-alive / reattach API.

**Order relative to other deferred work:** after Step 8 / parent observability merge (harness),
in parallel with or after Step 9 soak of same-process reconnect. Product MLNode work may
land on its own schedule; citest stays red-gated until both harness and MLNode support exist.

---

## Testing plan

| Area | Test | Where |
|---|---|---|
| Prefix accounting | Fragmented / partial-event writes; suppressed losers count nothing | `cmd/devshardctl/redundancy_reconnect_test.go` (new) |
| Prefix vs client rewrite | Cursor counts upstream events when `rewriteStreamingPayload` expands **and** when it shrinks; bytes swallowed after client disconnect do not advance it | same |
| Receipt-only resume (R3) | Reconnect that forwards nothing and carries no mempool is a failed try; tail-only resume carrying mempool still succeeds | same |
| One `ProcessResponse` per nonce (R1) | A receipt-only winner does not spend the one-shot while a ladder can still merge the Finish | same |
| Resume fidelity | Drop at first / middle / last event → exact original sequence | same |
| Winner continuity | Secondary cannot crown / write after a delivered prefix — even if B is ahead and A is slow; blip recorded only | same |
| Protocol gate (R0) | `AttemptReconnectEnabled=true` on v4 → no same-nonce resend; on v5 → ladder runs | same + `types/domain_test.go` |
| Fallback | Reconnect exhausted → today's failure contract unchanged | same |
| Host reconnect | Executing → live attach from cursor (partial remainder + live tail); completed → full replay | `host/host_test.go`, `transport/server_test.go` |
| Post-drain storage reconnect | After ML drain + payload persist, live buffer forgotten; same-nonce reconnect streams from storage at resume cursor | `host/host_test.go`, `transport/server_test.go` |
| Inflight buffer prune | TTL elapses → attach refused with `ErrLiveStreamGone`, log **not** closed, producer keeps buffering; reconnect is a resume failure | `host/host_test.go`, `host/livestream_test.go` |
| Live log / no-drop (R9) | Contiguous bytes to readers; producer never blocks on client Write *(landed)* | `host/livestream_test.go` |
| Live log stall (R9 / 5a) | Stalled **reconnect** (no offset progress) → `ErrSubscriberLagged`; slow-but-advancing reconnect runs to completion; stalled **primary** stays open (`ClientDetached` only) | `host/livestream_test.go` |
| Live log soft cap + trim (R9 / 5c) | Past RAM soft cap → attach refused and head trimmed below the slowest reader; cursor inside window is byte-identical, older cursor → `ErrResumeCursorPast`; no partial body in `completedResponses` | `host/livestream_test.go`, `host/host_test.go` |
| Duplicate-copy RAM (R9 / 5b) | Retained bytes ≤ RAM cap + one processor copy; no per-line alloc blow-up. Hub lines are **not** byte-identical to the processor's (blank separators are log-only, hub lines carry `\n`; on rewrite failure the proxy still forwards the processor's raw fallback so live log and durable body stay event-aligned), so sharing is per-line payload + separate framing | `host/livestream_test.go`, `cmd/devshardd/inference/proxy_test.go`, `common/completionapi/idrewrite_test.go` |
| Per-chunk id rewrite dominates drain CPU (5b) | Rewrite candidates agree semantically; `goccy` keeps `ResponseHash` stable; surgical rewrite is allocation-free and leaves nested `tool_calls[].id` and id-like content untouched | `common/completionapi/idrewrite_test.go` |
| Cursor byte-offset (R9) | Wire event/partial → absolute offset; blank SSE separators do not desync across live, **trimmed**, and finished replay | `host/livestream_test.go` |
| Protocol safety | Repeat `MsgConfirmStart` is a no-op; one `ProcessResponse` per nonce | `host/`, `user/session_test.go` |
| Accounting | A reconnect does not double-count sends; one terminal record per nonce | `cmd/devshardctl/accounting_test.go` |
| Blips (R8) | One blip per ladder run; window expiry; `Decide` / picker output unchanged at any blip count; counter + in-window gauge exported | `cmd/devshardctl/perftracker_test.go`, `proxy_test.go`, `metrics_test.go` |
| Stream speed (R8) | Terminal `stream_bps` = `outputBytes / elapsed_since_first_content`; no sample when `contentChunks == 0`; histogram + bytes counter exported; `Decide` unchanged | `cmd/devshardctl/perftracker_test.go`, `metrics_test.go`, `redundancy_reconnect_test.go` |
| Soft-signal persist (Step 6) | 1m batch upsert of blips + rolling bps; hydrate on boot; no hot-path DB I/O; `Decide` unchanged after hydrate; hybrid journal covers the new table | `cmd/devshardctl/gateway_store*_test.go`, `perftracker_test.go` |
| E2E resume | Mid-stream drop → one continuous completion, one finish message | `testenv/citest` |
| E2E fallback | Host dead after drop → escalation after budget | `testenv/citest` |
| E2E cross-instance HA (Step 10) | Reboot / sticky failover mid-stream; ML job survives; other `devshardd` reattaches (HA1–HA4). **Deferred** until after `ak/devshard-observability-e2e` mock-ML split + MLNode keep-alive | `testenv/citest` (HA compose already landed) |

```bash
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test ./cmd/devshardctl/... ./host/... ./transport/... ./user/...
```

## Rollout & risks

| Risk | Mitigation |
|---|---|
| Reconnect enabled against ≤v4 hosts that abort ML on disconnect | Hard R0 protocol gate (not a setting); unit + e2e assert v4 never enters the ladder |
| Duplicate `MsgConfirmStart` with a fresh `confirmedAt` corrupts protocol state | Step 2 makes the repeat a no-op; asserted by a state-machine test before the flag is enabled |
| Double `ProcessResponse` for one nonce queues duplicate txs | Step 3 merges responses into one inflight; unit test asserts a single `ProcessResponse` |
| Resume splices the wrong offset and duplicates or drops tokens | R2/R6 cursor on upstream events + partial bytes; mid-event drop tests; gateway defensive skip |
| Reservation blocks a secondary while the winner is actually dead | Budget-bounded reservation with explicit release on confirmed-dead; deadlock test with both attempts failing |
| Secondary "ahead" splices a different generation onto A's prefix | R4 hard rule: delivered prefix owns the client; no contentChunks takeover; only R5 reset may switch |
| Live attach races the forming event buffer | Host buffer owns the currently forming event; single producer (ML drain), multi-subscriber readers; unit-test partial-event handoff |
| Live buffer pins RAM after drain or forever on stuck ML | Drain→persist→forget happy path; TTL detaches attachability only; Step 5c trims the RAM window to `LiveStreamRingBytes` against the spool, so neither a stuck ML nor a pinned reader can grow the hot log |
| Slow reconnect silently drops live fan-out chunks | Already mitigated: append-only log + offset readers (Steps 2–4). Step 5a adds reconnect-only `ErrSubscriberLagged`, triggered by a frozen offset only |
| Stalled primary reader blocks trimming → RAM still unbounded | Step 5a: primary write deadline; breach = ordinary client detach (drop reader, keep producing), which unblocks trim |
| Lag-close of primary forces reconnect on healthy gen | Step 5a: `ErrSubscriberLagged` applies only to non-primary subscribers |
| Stall policy kills a healthy-but-slow reconnect → truncated client + wasted ladder try | Step 5a: closes only on **zero** offset progress while bytes are pending; byte lag is a metric, never a close threshold; stall timeout sized above `AttemptReconnectBudget` |
| RAM pressure "solved" by closing a slow reader | Forbidden by 5a. A slow reader's unread window is undeliverable-yet-needed; 5c moves it to the spool so it costs no hot RAM at all. The only case where a reader is dropped for RAM is degraded mode (no spool), where nothing can be reclaimed from disk |
| Partial body published mid-flight is replayed as a complete completion | Forbidden by R9 / Step 5c (cache stays empty until finish, asserted by test). Defence in depth: `replaySSEBodyFromCursor` no longer synthesizes `[DONE]` for a streamed envelope without one — it closes the replay with `partialStreamErrorLine` so the client sees the truncation |
| Trim discards bytes a live reader still needs | Trim only below what the spool already holds, so trimmed bytes are always re-readable; contiguity test with a reader pinned at offset 0 through a long burst |
| Spool file leaks or outlives its generation | `Release` (after `Close` + `WaitPrimary`) removes both files once no reader is draining them; the whole directory is emptied at startup, so a crash leaves nothing behind |
| Resume horizon much shorter than durable retention | Step 5e: payload store (retained to seal, ~1h) becomes a resume tier, so `completedResponses` eviction / head-trim is no longer a cliff for a completed generation |
| Host / gateway / TTL clocks diverge | Step 5e derives drain, LiveStream TTL, and gateway hard timeout from protocol `ExecutionTimeout` |
| Trim breaks absolute-offset cursor math | Explicit `eventsBase` / `bytesBase` for the RAM window and a `.idx` lookup below it; only cursors past the spool tip return `ErrResumeCursorPast` |
| Abandoned reader write leaks a goroutine or races the response | Step 5a: the write runs inline on the drain goroutine bounded by `SetWriteDeadline`; nothing is left blocked on a dead socket and `WaitPrimary` stays an exact fence |
| Chunk timestamps change the hashed body and fail every validation | Step 5g keeps them out of `streamedResponse` / `response_payload` entirely: parallel array in RAM, sidecar column in the store; test asserts `ResponseHash` is unchanged with the feature on |
| Chunk timestamps shift the resume cursor against a mixed-version peer | Step 5g emits them as SSE comments at the writer, never into the counted log; test asserts byte-identical log and cursor with the feature on and off |
| A new protocol event leaks devshard JSON into end-user streams on old gateways | Step 5g uses the `:` comment channel, which today's parser already drops silently; a new `devshard_*` envelope would instead be forwarded to the client |
| Host-reported timestamps are trusted and gamed | Step 5g: unverifiable half is informational only (same R8 rule as blips), never feeds `Decide` / picker / quarantine; parser bounds line and array length |
| Spool I/O under the producer lock stalls ML | Step 5c: the pump is a separate goroutine and copies out from under `mu` before writing; cursor `.idx` lookups also run off-lock. `Write` only appends and wakes |
| Spool disk fills or fails mid-stream | Resume is disabled for that generation (`ErrLiveStreamResumeUnavailable`), readers below the window are dropped, and the generation still finishes and persists normally |
| Reconnect masks a genuinely bad host from the perf tracker | Reconnect tries are recorded separately (R8), so per-participant failure rates still reflect drops |
| More host-side work retained after a drop | Depends on the parent plan's Step 3 drain, which is bounded and counted there |
| Soft-signal DB writes amplify under blip storms | Step 6: 1m batch upsert of dirty keys only; no write on each blip; empty windows deleted |
| Soft-signal hydrate changes routing after restart | Forbidden by R8 / Step 6: persisted state never feeds `Decide` / quarantine |
| New gateway table breaks hybrid migrate / journal | Step 6 adds `participant_soft_signals` to D5 copy list + D6 journal key space; store parity tests cover both backends |

## Pre-Step-7 audit

Steps 1–5 were audited before committing to e2e, on the reasoning that e2e written against a broken
contract encodes the break. Findings below; all fixes landed with regression tests that were
verified to fail without them.

### Fixed

| # | Severity | Where | Finding |
|---|---|---|---|
| 1 | Critical | `redundancy.go` `raceWriter.Write`, `proxy.go` `deferredWriter.Write` | Step 1 recorded the delivered prefix as `p[:n]`, but `n` came back from the client writer, which counts **rewritten** bytes. `rewriteStreamingDataEvent` expands one `message`-shaped event into several chunk events (measured: 123 B → 427 B), so `p[:n]` panicked with `slice bounds out of range` on the streaming hot path; internal-field filtering shrinks instead, silently under-counting the cursor and duplicating bytes on resume. `deferredWriter` now honors the `io.Writer` contract (progress reported in caller bytes) and the cursor advances over the upstream chunk, never a writer-derived slice |
| 2 | High | `redundancy.go` `reconnectInflight` | A failed live attach is indistinguishable from success: the host has already written the receipt (`transport/server.go:400`), the receipt sets `sawTerminator` (`transport/client.go:377`), and the clean EOF returns `nil`. The ladder logged `winner_reconnect_resumed` having resumed nothing, burning its one shot. R3 bullet 3 requires a receipt-only response to count as a failed try; now enforced via `prefixSkipWriter.forwarded`, with a tail-only resume that carries mempool still counted as success |
| 3 | High | `redundancy.go` `winningInflightTerminalFailure` | `processInflightOnce` ran **before** `awaitRace` decided to reconnect. On a clean mid-stream close (`err == nil`, receipt-only response) the one-shot was spent on a response that could not settle, so a Finish merged by a later successful reconnect could never reach `ProcessResponse` and the request failed despite resuming. Now skipped while a ladder can still run, gated on the response carrying no mempool and the nonce not already finished |
| 4 | Medium | `redundancy.go` `raceGroup` | The delivered prefix advanced over bytes `deferredWriter` swallows after client disconnect, because `detachClient()` had no production caller — the `isClientDetached` guard was dead outside tests. `raceGroup` now consults the request's `clientFlag` |

### Open (tracked elsewhere, not regressions of this plan)

- **Step 5e is only half done.** Drain, `InflightReplayBufferTTL`, and the gateway hard timeout all
  derive from `DefaultExecutionTimeout()` at init rather than the session's configured
  `ExecutionTimeout`, so escrows with a non-default timeout still get divergent clocks — which is
  exactly the risk row "Host / gateway / TTL clocks diverge" claims to close.
- **Parent Step 12 (unbounded SSE line read) is landed** (`transport/client.go`
  `parseSSEResponse` → `readBoundedSSELine` / `ErrSSEEventTooLarge`, default 1 MiB). Reconnect
  still widens the exposure window relative to no-reconnect; keep the cap on through this
  plan's Step 9 default-on.
- **Host live-log resource bounds.** `unsubscribe` now compacts closed readers out of
  `LiveStream.subs`, and `WriteHeader` does its primary network I/O outside `s.mu` (fenced against
  subscriber #0 by `hdrInFlight`, so the two never write one response concurrently). What remains
  for Step 5f is a **max-subscriber cap**: nothing bounds how many reconnect attempts may attach to
  one generation. Byte retention itself is bounded by 5c.
- **Per-generation RAM is `O(ring)` for the live log only.** `ExecutorResponseProcessor` still holds
  every rewritten line for the whole generation, and finish materialises the body twice more. That
  is Step 5h.
- **`AllowStreamResetOnFailover` is wired (default off).** On reconnect-ladder exhaust with the
  setting on, the gateway writes `writeStreamReset`, clears the race crown, and keeps racing so a
  hedge/secondary may take over from scratch. Default-off behavior is unchanged.
- **Cross-instance HA / ML reattach (issue #1466 §4) is not in Steps 1–9.** Multi-replica
  topology and HA citest exist, but a mid-stream `devshardd` reboot still loses the ML stream
  until Step 10 (blocked on separated mock MLNodes from `ak/devshard-observability-e2e` plus
  MLNode keep-alive).

## Task checklist

- [x] Step 1 — delivered-prefix counters on `inflight`
- [x] Step 2 — host live-attach from resume cursor; drain→persist→forget; storage reconnect + buffer TTL prune; repeat receipt is a no-op
- [x] Step 3 — same-nonce reconnect send path passing `(deliveredEvents, deliveredPartial)`
- [x] Step 4 — `ProtocolV5` + `ProtocolVersionAtLeast`; winner reservation + reconnect-first escalation; R0 gate; no mid-stream splice when B is ahead; reconnect blip recorded (no routing effect) for slow/reconnect A
- [x] Step 5a — reader stall policy: reconnect **no-progress** → `ErrSubscriberLagged`; write deadline enforced inline for all readers (no abandoned helper goroutine); chunked reader writes; byte lag signal only
- [x] Step 5b — cut per-chunk id-rewrite cost via memoized surgical splice (`idrewrite.go`); slab/alias sharing deferred
- [x] Step 5c — spool-backed log (`.log` + `.idx`) with a `LiveStreamRingBytes` RAM window; trim answers to the spool, not to readers; cursors older than the window resolve via the index; degraded mode refuses attach; never publish a partial durable body
- [x] Step 5d — defensive forming cap (`LiveStreamMaxFormingBytes`), loud on breach
- [x] Step 5e — payload store as the last resume tier; derive drain / LiveStream TTL / gateway hard timeout from `ExecutionTimeout`
- [ ] Step 5f — window/spool/lag metrics (with Step 8)
- [x] Step 5g — hop timestamps (phase 1): absolute `req_ms` on receipt; per-chunk `ml` in RAM (+
  in-memory cache); per-connection `w` at emit; gateway local recv; `: devshard-ts` comments out of
  cursor/hash space; hop histograms + coverage. Spool-trimmed ranges and payload-store tier omit
  comments until phase 2 sidecar.
- [ ] Step 5h — build the durable envelope by streaming the spool; retire the response processor's per-line retention (hash-sensitive; needs a byte-identical encoder)
- [ ] Step 6 — **deferred** until `ak/gateway-v4-postgres` merges; then persist soft signals (blips + rolling stream bps) to gateway store on a 1m ticker; hydrate on boot; additive schema via gateway-store interface / PG + SQLite / D5–D6 HA rules
- [x] Step 7 — E2E: admin reconnect settings; citest v2 protocol gate + v5 mid-stream resume
  (`TestAttemptReconnect_*`, `make citest-attempt-reconnect`); scenario 7 unit
  (`TestRunInference_V5SecondDropAfterResumeDoesNotRerunLadder`); `skipped_protocol`
  counter still Step 8; scenarios 3–4/6/8 citest follow-up
- [ ] Step 8 — **deferred** until `ak/devshard-observability-e2e` merges; then settings, metrics (incl. `skipped_protocol`, R9 live-log, blip counter + in-window gauge, stream bytes/sec histogram + bytes counter), dashboards; OTel via parent Step 16 APIs
- [ ] Step 9 — ship in gateway-v4 (dormant on ≤v4); enable on v5 soak; default-on
- [ ] Step 10 — **deferred** until after `ak/devshard-observability-e2e` (separated mock MLNodes)
  and MLNode keep-alive / reattach: cross-instance `devshardd` HA — reboot / sticky failover
  mid-stream continues the same ML job ([issue #1466](https://github.com/gonka-ai/gonka/issues/1466) §4);
  HA topology citest already landed; scenarios HA1–HA4 above

## Related

- [gateway-streaming-ha-overview.md](./gateway-streaming-ha-overview.md) — overview (flows /
  timeouts / observability / e2e)
- [proposals/always-stream-upstream.md](./proposals/always-stream-upstream.md) — consolidated design
  (always-stream + reconnect); this file is the reconnect implementation plan
- [gateway-always-stream-upstream-plan.md](./gateway-always-stream-upstream-plan.md) — Step 2 (replay
  format) and Step 3 (independent ML drain) are prerequisites; Step 4 there points here; parent
  Step 16 lands before this plan's Step 8 OTel spans (`attempt.reconnect`, etc.)
- [high-availability-architecture.md](./high-availability-architecture.md) — multi-instance
  `versiond` / `devshardd` + shared Postgres topology Step 10 builds on
- [storage-design.md](./storage-design.md) — Schema Evolution Across Devshard Versions (additive HA
  contract Step 6 follows)
- [gateway-postgres-backend-plan.md](./gateway-postgres-backend-plan.md) — gateway store backends /
  hybrid journal that Step 6 extends with `participant_soft_signals`
- [stream-resume-pre-proposal.md](./stream-resume-pre-proposal.md) — why client-facing resume is a
  separate, larger problem
- [proposals/chat-stream-inflight-join.md](./proposals/chat-stream-inflight-join.md) — same fan-out
  shape R6 uses, scoped here to one reconnecting gateway connection
- [Issue #1466](https://github.com/gonka-ai/gonka/issues/1466) — Better `devshardd` inference
  handling; §4 is Step 10
- Branch `ak/devshard-observability-e2e` — `gateway.attempt` / `attempt.*` phase spans to extend;
  also the mock-MLNode split required before Step 10 e2e
