# Accounting host/ML-node inference errors as a miss

## Problem

A host can return an OpenAI-style error as the inference result and end up in a
state that is neither served, nor validated, nor charged as a miss.

Observed on the gateway:

```
stage=error_stream escrow=57577 nonce=89 host=s5ryv5le
  output_chunks=2 output_bytes=200
  error_source=error.InternalServerError error_code=500 error_type=InternalServerError
  error_message="EngineCore encountered an issue. See stack trace (above) for the root cause."
  response_body_sample="data: {\"error\":{\"code\":500,\"message\":\"EngineCore encountered an issue...\",
                        \"param\":null,\"type\":\"InternalServerError\"},\"id\":\"devshard-57577-89\"}"
```

This is an **HTTP 200 `text/event-stream`** whose body is an error envelope, not
an HTTP 5xx from the ML node. An HTTP 5xx never reaches the processor
(`ReasonHTTP5xx` closes the body and `Execute` fails, `inference/engine.go:165`),
so no `MsgFinishInference` would exist. The injected `"id":"devshard-57577-89"`
proves the host's `ExecutorResponseProcessor` accepted and proxied this body.

What the host does with such a body today (`host/host.go:1016` `RunExecution`):

1. `processExecutionHTTPResponse` succeeds — the error event is a parseable
   completion. Usage is `0/0`, `GetEnforcedTokens()` would fail.
2. The body is stored as the response payload:
   `{"events":["data: {\"error\":{...},\"id\":\"devshard-57577-89\"}","data: [DONE]"]}`.
3. `MsgFinishInference{ResponseHash: sha256(that payload)}` is signed
   (`ProposerSig`) and queued to the mempool.
4. Transport tails `devshard_meta` with that mempool (`transport/server.go:510`).

Consequences on the gateway:

| Path | Behaviour | Where |
| --- | --- | --- |
| Serve to client | `hostApplicationError`, HTTP 500. Not a success. | `redundancy.go:3817` |
| Miss vote | **Skipped** — nonce is finished | `shouldRunHandleTimeout`, `redundancy.go:3212` |
| Host perf scoring | **Skipped** — explicit TODO exemption | `recordPostContentWinnerFailureOnce`, `redundancy.go:3610` |
| Chain state | `StatusFinished`, `HostStats.Cost += ActualCost` | `applyFinishInference`, `state/machine.go:1194` |
| Validation | Eligible but only rate-sampled; if sampled → `InvalidInferenceResult` (no logprobs) | `collectValidationJobs`, `host/host.go:1160` |

So the record is *finished enough to block a miss* while not being a completion
anyone can use. The signed `ResponseHash` is a real artifact — hash the stored
payload, match `MsgFinishInference.ResponseHash`, and you have the executor's own
signature over "this error is my answer" — but nothing consumes it.

## Goal

Turn that artifact into an accountable outcome:

- The inference is **not** counted as finished (no cost credited to the host).
- The inference is **not** validated (there is nothing valid to validate).
- The executor slot gets **`Missed++`**, agreed by the group off the signed
  error body.
- **No timeout wait.** The miss is accounted the moment the error Finish is
  observed. See below — this is a design property, not an optimisation.
- The client keeps receiving today's `hostApplicationError` response. Unchanged.

## No timeout wait: the artifact replaces the deadline

Both existing timeout reasons exist because the executor said *nothing*, so the
only available evidence is "a deadline passed with no answer":

- `VerifyRefusedTimeout` gates on `nowUnix-rec.StartedAt < config.RefusalTimeout`
  (`host/timeout.go:83`) — 60s by default.
- `VerifyExecutionTimeout` gates on
  `nowUnix-rec.ConfirmedAt < config.ExecutionTimeout` (`host/timeout.go:157`) —
  **`32 * 60` seconds by default** (`types/config.go:51`).

`reason=ERROR` is categorically different. The executor **did** answer, and it
signed its answer: `MsgFinishInference.ProposerSig` covers
`ResponseHash = sha256(error payload)`. That signature is complete, positive
evidence of failure, available immediately. There is nothing left to wait for —
waiting could only weaken the case, never strengthen it.

So the deadline is absent on **both** sides:

- **Verifier:** `VerifyErrorTimeout` performs **no** deadline comparison. It does
  not read `RefusalTimeout`, `ExecutionTimeout`, or `TimeoutBuffer`. Reviewers
  should treat any wall-clock gate added here as a bug.
- **Gateway:** the `ERROR` branch of `HandleTimeout` skips
  `sleepUntilDeadlineWithHeartbeat` entirely (`session.go:2114`, `:2123`) and goes
  straight to `CollectTimeoutVotes`. End-to-end latency is one round of verifier
  RPCs plus one diff.

Why this matters beyond latency:

| | Without this change | With `reason=ERROR` |
| --- | --- | --- |
| Time to account the miss | never (nonce is finished, so no timeout path runs at all) | immediate |
| If the finish were suppressed instead | ~32 min of `ExecutionTimeout` per crash | n/a |
| `ReservedCost` held in escrow | until seal, credited to the host | released in the same diff |
| Host reuse for retries | crashing host keeps looking healthy | miss lands before the next request picks it |

The `TimeoutReason` name is now a slight misnomer for this value: `ERROR` is an
instant, evidence-backed miss, not an elapsed-time claim. Reusing the enum is
still the right trade — `MsgTimeoutInference` already carries exactly "executor
owes a miss, here are the votes", which is the payload we need — but the enum
comment should say so explicitly so nobody later "fixes" it by adding a deadline.

## Design: one new `TimeoutReason`, zero new messages

Reuse `MsgTimeoutInference`. Add a third enum value.

```proto
// devshard/proto/devshard/v1/tx.proto
enum TimeoutReason {
  TIMEOUT_REASON_UNSPECIFIED = 0;
  TIMEOUT_REASON_REFUSED     = 1;
  TIMEOUT_REASON_EXECUTION   = 2;
  // Executor finished with an error body. Evidence-backed and immediate:
  // verifiers check the executor-signed response hash, NOT an elapsed deadline.
  TIMEOUT_REASON_ERROR       = 3;
}
```

Nothing else in the proto changes. `MsgTimeoutInference`, `TimeoutVote` and
`TimeoutVoteContent` keep their field numbers, so existing signatures and
serialized snapshots are byte-identical.

### The diff

```
diff N: [ MsgFinishInference{id, response_hash, proposer_sig},
          MsgTimeoutInference{id, reason=ERROR, votes[]} ]
```

The **ordering** `Finish → Timeout` is the invariant, not literally "same diff".
`applyTimeout(reason=ERROR)` requires `StatusFinished`, which the Finish
produces. Same-diff is the atomic and preferred case (the gateway holds the
Finish in `pendingTxs` while collecting votes); if the Finish was already
published in an earlier diff, a later solo Timeout diff applies identically.

Tx order inside a diff is preserved: `applyCore` walks `diff.Txs` in order
(`state/machine.go:1016` `applyTx`), and `localBestEffortLocked` applies
candidates in queue order, so FIFO `pendingTxs` gives Finish before Timeout.

### Why this shape

The Finish must land on-chain, not be suppressed. It is the *only* thing that
binds the executor to the error body: `ProposerSig` covers `ResponseHash`. Drop
the Finish and you lose the proof and are back to an unattributable miss. The
Timeout immediately after it converts that proof into the accounting outcome.

## State machine changes

### `applyTimeout` — new reason branch

`state/machine.go:1383`. Add to the reason/status gate:

```go
case types.TimeoutReason_TIMEOUT_REASON_ERROR:
    if rec.Status != types.StatusFinished {
        return fmt.Errorf("%w: reason=error requires finished, got %d",
            types.ErrInvalidTimeoutReason, rec.Status)
    }
```

Vote counting, dedup-by-address, weight-by-slot-count and the `VoteThreshold`
check are reused verbatim.

### `applyTimeout` — unwind the finish accounting

`applyFinishInference` already ran, so it credited the host and released only the
surplus. The `ERROR` branch must undo the credited part. Mirror the
`StatusInvalidated` unwind (`state/machine.go:1353`):

```go
if msg.Reason == types.TimeoutReason_TIMEOUT_REASON_ERROR {
    sm.state.Balance += rec.ActualCost          // surplus was already released
    hs := sm.state.HostStats[rec.ExecutorSlot]
    if hs.Cost < rec.ActualCost { hs.Cost = 0 } else { hs.Cost -= rec.ActualCost }
} else {
    sm.state.Balance += rec.ReservedCost        // existing refused/execution path
}
rec.Status = types.StatusTimedOut
sm.state.HostStats[rec.ExecutorSlot].Missed++
```

Invariant to assert in tests: after an `ERROR` timeout the escrow balance is
restored by the **full `ReservedCost`** (`surplus` at finish + `ActualCost`
here), identical to the refused/execution paths, and `HostStats.Cost` is back to
its pre-finish value.

### Everything downstream is already correct

| Behaviour | Why no change is needed |
| --- | --- |
| Not validated | `collectValidationJobs` only samples `rec.Status == StatusFinished` (`host/host.go:1160`); `StatusTimedOut` is skipped. |
| Not counted as finished | Status is `StatusTimedOut`; cost unwound above. |
| Sealing | `StatusTimedOut` is already in `sealEligibleStatus` and `terminalAutoSealStatus` (`state/seal.go:256`, `:268`). |
| Settlement | `HostStats.Missed` is already serialized to chain (`chain_tx_encode.go:89`, `state.proto` field 2). |
| Late validation votes | `applyValidation` / `applyValidationVote` status gates already reject or no-op post-terminal records. |
| State root | Enum value addition, no new field, no changed encoding. |

## Verification: how a verifier agrees the body was an error

This is the only genuinely new logic. A verifier must not accept the gateway's
word for it; it must check the executor's own signature.

`transport/server.go:593` `HandleVerifyTimeout` gains an `ERROR` case:

```go
case types.TimeoutReason_TIMEOUT_REASON_ERROR:
    accept, err = host.VerifyErrorTimeout(ctx, st, req.InferenceID, req.FinishTx,
        localMempool, executorClient, payloadFetcher)
```

`host.VerifyErrorTimeout` steps, all failures → reject (`accept=false`):

0. **No deadline check.** Unlike `VerifyRefusedTimeout` (`host/timeout.go:83`)
   and `VerifyExecutionTimeout` (`host/timeout.go:157`), this function reads no
   clock and no timeout config. The executor's signature is the evidence.
1. **Record state** — `rec.Status == StatusFinished`. If still `Started`, this is
   an execution timeout, not an error finish.
2. **Obtain the Finish tx.** Prefer the local mempool / applied record; else the
   executor's own mempool via the existing `ExecutorClient.GetMempool`
   (`host/timeout.go:170`); else the gateway-supplied `finish_tx` in the request.
3. **Verify the executor signature** over that Finish msg against
   `slotToAddress[rec.ExecutorSlot]`, using the same `verifyProposerSig` logic as
   `applyFinishInference` (`state/machine.go:1170`). Export a thin
   `StateMachine.VerifyFinishProposerSig(msg)` helper rather than duplicating it.
   Also require `msg.ResponseHash == rec.ResponseHash`.
4. **Fetch the response payload** from the executor and hash-check it. Reuse the
   validator fetch path unchanged: `commonvalidation.FetchPayloadsHTTP` +
   `VerifyExecutorPayloadSignature` + `sha256(responsePayload) == ResponseHash`
   (exactly `inference/validate.go:107-138`). This is why no error body needs to
   cross the wire from the gateway: the executor serves its own payload, signed,
   and the hash is pinned by the Finish.
5. **Classify** the payload with the shared deterministic predicate below.
6. If all pass → sign a `TimeoutVoteContent{escrow, inference_id,
   reason=ERROR, accept=true}` via the existing `signTimeoutVote`
   (`transport/server.go:696`) — no signing change.

Transport request gains one optional field (JSON only, not consensus):

```go
// transport/types.go VerifyTimeoutRequest
FinishTx []byte `json:"finish_tx,omitempty"` // proto DevshardTx, executor-signed
```

### The shared classifier

Put it in `common/completionapi` (or `common/validation`) so executor, verifier
and gateway compile against one implementation. Divergence between hosts is not
a safety bug — it only causes insufficient votes, which degrades to today's
behaviour — but it must be deterministic to be useful.

```go
// IsTerminalErrorResponse reports whether a stored response payload is an
// error envelope carrying no usable completion.
func IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool)
```

Accept only when all hold, parsed via
`NewCompletionResponseFromLinesFromResponsePayload`:

- some event carries a top-level `error` object (the `{"error":{code,message,type}}`
  shape, matching the gateway's `sseChunkErrorPayload`, `redundancy.go:265`);
- no content anywhere — no `delta.content`, `delta.reasoning_content`,
  `delta.tool_calls`, `message.content`, `choice.text`;
- `usage.completion_tokens == 0`.

The last two conditions are what stop this becoming an escape hatch: a host that
streams real tokens and appends a trailing error event is *not* eligible for an
error miss, so it cannot dodge validation by decorating a good response.

## Gateway changes

All in `devshard/cmd/devshardctl` + `devshard/user/session.go`.

1. **Let the path run.** `shouldRunHandleTimeout` (`redundancy.go:3212`) returns
   false for any finished nonce. Add an exception: run when
   `isErrorStreamAttempt(inf)` even if the nonce finished.
2. **New reason branch in `HandleTimeout`** (`session.go:2081`), taken *before*
   the deadline logic. Today the execution path sees a pending Finish and returns
   early after publishing it (`session.go:2135-2147`). For an error attempt:
   **do not** early-return, and **do not** call
   `sleepUntilDeadlineWithHeartbeat` — no `RefusalTimeout`, no `ExecutionTimeout`,
   no `TimeoutBuffer`. Go straight to `CollectTimeoutVotes(reason=ERROR)`.
   Structurally this means the `ERROR` case is decided at the top of
   `HandleTimeout`, not inside the existing `confirmedAt > 0` branch that owns the
   32-minute wait.
3. **Same-diff composition.** Keep the Finish in `pendingTxs`, then
   `AddPendingTimeoutTx(nonce, ERROR, votes)` (`session.go:1907`) and one
   `SendPendingDiff`. FIFO ordering gives Finish → Timeout in a single diff.
4. **Forward the artifact.** Extend `TimeoutVerifier.VerifyTimeout` /
   `CollectTimeoutVotes` to pass the pending Finish tx through to the new
   `finish_tx` request field.
5. **Fix the local finished view.** `nonceStates[n].finished` is set from
   `HasMsgFinish` (`session.go:624`), so gateway-local `IsNonceFinished` would
   still report finished after an accepted error miss, disagreeing with chain
   state. Clear it when the `ERROR` timeout applies, so
   `completeAccountingRequest` and the debug/stats views do not bill a
   timed-out inference.
6. **Retire the exemption TODO** in `recordPostContentWinnerFailureOnce`
   (`redundancy.go:3610`). Recommendation: keep gateway *perf/quarantine*
   scoring decoupled from the on-chain miss for the first release — the miss is
   the protocol penalty, quarantine remains a latency/health signal — but make
   that a deliberate, commented decision instead of the current
   "until hosts submit Finish for errors" placeholder, which this change resolves.
7. **Client response unchanged.** `hostApplicationError` still surfaces with its
   original status/body (`proxy.go:404`). Only accounting changes.

## Rollout: this is a breaking diff for old hosts

An old host applying `reason=3` hits `applyTimeout`'s `default` branch and
returns `ErrInvalidTimeoutReason`, which **rejects the whole diff** and wedges
the session. Gating is mandatory, not optional.

- Emit `ERROR` timeouts only when every group member reports a runtime version
  that supports the reason. The gateway already tracks per-participant versions
  (`phase_gate.go`, `versions_cache`, and the `/v1/versions` poll) and already
  has precedent for version-gated behaviour.
- Ship the *host* side (accept + verify + apply) at least one release before the
  *gateway* starts emitting.
- Until the gate opens, behaviour is exactly today's: error stream served as
  `hostApplicationError`, no miss.
- Add a kill switch (`DEVSHARD_ERROR_MISS_ENABLED`, default off) so the emit
  side can be turned off in production without a rollback.

No state-root or protocol-version bump is required: an added enum value changes
no existing encoding.

## Deliberately deferred

**Binding the vote to the body hash.** `TimeoutVoteContent` has no
`response_hash`, so a vote says only "inference N failed with an error". That is
sufficient today because Finish applies at most once per inference
(`applyFinishInference` requires `StatusStarted`), so there is exactly one body a
`reason=ERROR` vote can refer to. Adding `bytes response_hash = 5` would harden
it, but `TimeoutVoteContent` is signed content, so that change requires a
state-root/protocol-version bump (`types.DevshardStateRootAndProtocolVersion`,
currently `v2`). Do it in the next version bump if we want defence in depth, not
in this change.

## Alternatives rejected

| Alternative | Why not |
| --- | --- |
| New `MsgErrorInference` message | Needs a new `DevshardTx` oneof field, a new apply path, new snapshot/settlement plumbing. `MsgTimeoutInference` already carries "executor owes a miss, here are the votes". |
| Suppress the Finish; emit only an execution timeout | Loses the signed `ResponseHash`, so the miss becomes unattributable and the executor can deny it. Also a lie: the executor did confirm and did answer. |
| Host refuses to publish Finish on an error body | Then the error can never be proven, and every EngineCore crash falls back to `TIMEOUT_REASON_EXECUTION` — a **32-minute** `ExecutionTimeout` wait per crash (`types/config.go:51`) with the escrow reservation locked for the duration. |
| Keep the Finish but wait out `ExecutionTimeout` before voting | Pointless: the executor already answered, so the deadline adds no evidence. It would also never trigger, since `VerifyExecutionTimeout` rejects as soon as it sees the Finish (`host/timeout.go:163`). |
| Gateway inlines the error body in `VerifyTimeout` | The gateway does not hold the executor's canonical `{"events":[...]}` serialization (it sees raw SSE lines) and would have to reconstruct it byte-exactly. Fetching from the executor's payload API is both exact and already implemented. |
| Serve the error as a successful completion | Charges the client for a crash, and any sampled validation would invalidate it (no logprobs → `InvalidInferenceResult`). |

## Test plan

State machine (`devshard/state/machine_test.go`):

- `Finish` then `ERROR` timeout in one diff → `StatusTimedOut`, `Missed == 1`,
  `HostStats.Cost` back to pre-finish, balance restored by full `ReservedCost`.
- `ERROR` timeout against `StatusStarted` / `StatusPending` → `ErrInvalidTimeoutReason`.
- `ERROR` timeout with `acceptCount <= threshold` → `ErrInsufficientVotes`, no mutation.
- Wrong-order diff (`Timeout` before `Finish`) → rejected, state unchanged.
- Post-`ERROR` `MsgValidation` / `MsgValidationVote` → rejected or no-op.
- Golden state-root test: an unrelated session's root is byte-identical to
  pre-change (enum addition must not move any hash).

Classifier (`common/completionapi`):

- The exact EngineCore payload from this report → `ok == true`.
- Deterministic 4xx (`BadRequestError`) → `ok == true`.
- Real content followed by a trailing error event → `ok == false`.
- Error event with `completion_tokens > 0` → `ok == false`.
- Empty stream (role + `[DONE]`, no error object) → `ok == false`
  (that stays the empty-stream path).

Verifier (`devshard/host`, `devshard/transport`):

- **No-wait guard:** a state whose `ConfirmedAt` is "now" (far inside both
  `RefusalTimeout` and `ExecutionTimeout`) still yields `accept`. This is the
  regression test that keeps a deadline from being reintroduced; today's
  `VerifyExecutionTimeout` would reject the same state.
- Valid Finish sig + payload hash match + error body → `accept`, signed vote.
- Tampered `finish_tx` (re-signed by a non-executor slot) → reject.
- Payload hash mismatch (`ErrHashMismatch`) → reject.
- Executor serves a *content* payload for that hash → reject.
- Executor payload API unreachable → reject (fail closed; a miss must be proven,
  unlike execution timeout where unreachability supports the claim).

Gateway (`devshard/cmd/devshardctl`):

- Error-stream attempt end to end: client still gets the original error status
  and body, one diff carries Finish + `ERROR` timeout, `Missed == 1`, request is
  not billed as finished.
- **No-wait assertion:** run with a realistic `ExecutionTimeout` (e.g. `32*60`,
  not the `1`s the existing proxy tests use at `proxy_test.go:709`) and assert the
  miss is applied in well under a second. Existing timeout tests pass only because
  they shrink the config; this path must not need that.
- Version gate closed → today's behaviour exactly, no `ERROR` timeout emitted.
- Insufficient votes → today's behaviour, request still fails cleanly.

E2E (`devshard/testenv/citest`): mock-openai fault returning `200` + SSE error
envelope (companion to the existing `a2_ml_upstream_5xx_test.go` which covers the
HTTP 5xx case). Assert `Missed` on the executor slot and no validation job.

## Observability

- New `logInferenceStage` stage `error_miss` with `inference_id`, `host`,
  `error_type`, `error_code`, `response_hash`, `votes`, `accepted`.
- Extend the timeout-reason label set (`RecordInferenceTimeout`,
  `timeoutReasonLogLabel` at `session.go:2239`) with `"error"`.
- Counter for rejected error-miss verifications by cause
  (`sig`, `hash_mismatch`, `not_error_body`, `payload_unreachable`) — this is the
  signal that hosts disagree on the classifier.
- Update `docs/inference-lifecycle.md` (status transitions) and
  `proposals/gateway-observability/observability.md` (the `error_stream` row now
  has a follow-up outcome).

## Work breakdown

| # | Change | Files |
| --- | --- | --- |
| 1 | `TIMEOUT_REASON_ERROR` + regenerate | `proto/devshard/v1/tx.proto`, `types/tx.pb.go` |
| 2 | Shared error classifier + tests | `common/completionapi` |
| 3 | `applyTimeout` reason branch + cost unwind + tests | `state/machine.go` |
| 4 | Export `VerifyFinishProposerSig` | `state/machine.go` |
| 5 | `VerifyErrorTimeout` | `host/timeout.go` |
| 6 | `ERROR` case + `finish_tx` field | `transport/server.go`, `transport/types.go`, `transport/client.go` |
| 7 | `HandleTimeout` error branch, vote plumbing, finished-view fix | `user/session.go` |
| 8 | `shouldRunHandleTimeout` exception, exemption cleanup, version gate + kill switch | `cmd/devshardctl/redundancy.go` |
| 9 | Metrics, stages, docs | `observability/`, `docs/` |
| 10 | E2E fault case | `testenv/mockopenai`, `testenv/citest` |

Steps 1–5 are host-side and shippable independently; 6–8 are the gateway emit
side and must stay behind the version gate until hosts have 1–5.
