# Proposal: Account host/ML-node inference errors as a miss

**Status:** Draft / proposal
**Implementation:** [error-finish-miss-protocol-plan.md](../error-finish-miss-protocol-plan.md)

This is the protocol design. Step-by-step implementation lives in the plan
linked above.

---

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
- **No executor contact.** Verifiers vote off the executor-signed Finish the
  gateway relays to them. No payload fetch, no RPC to the executor, no
  dependence on the failing host being reachable.
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

The only other proto change is one field on `TimeoutVoteContent` to bind the vote
to the body it judged (see [Binding the vote to the body
hash](#binding-the-vote-to-the-body-hash)). No new messages, no new transaction
types, no new wire fields on any `Msg`.

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

`MsgTimeoutInference` and `TimeoutVote` are untouched, and every existing field
number is preserved, so serialized snapshots and `REFUSED` / `EXECUTION`
signatures stay byte-identical.

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

## State machine

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

Finish always runs first. `applyFinishInference` does not look at the body; it
only looks at token counts in `MsgFinishInference` and treats them as successful
work:

1. **At Start** the client's escrow is locked: `Balance -= ReservedCost`.
2. **At Finish** the host is paid as if the job completed:
   - `ActualCost = tokenPrice × (input + output)`, capped at `ReservedCost`
   - surplus `ReservedCost - ActualCost` goes back to `Balance`
   - `HostStats.Cost += ActualCost` — this is the settlement payout accumulator
   - status becomes `StatusFinished`

`HostStats.Cost` is what settlement pays the executor (`chain_tx_encode.go`). If
the `ERROR` timeout then only flipped status to timed-out and incremented
`Missed`, the host would be **paid and marked missed** for the same inference,
and the client would keep paying `ActualCost`.

Existing `applyTimeout` (refused / execution) never hits this because those
reasons require `Pending` or `Started` — Finish has not run, `HostStats.Cost`
was never credited, and the full `ReservedCost` is still locked, so
`Balance += rec.ReservedCost` is correct.

`ERROR` is the first timeout that applies **after** Finish. The reservation is
already split, so the branch cannot reuse the refused/execution refund. Mirror
the `StatusInvalidated` unwind (`state/machine.go:1353`):

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

| Ledger | After Finish | What ERROR must do |
| --- | --- | --- |
| `Balance` | only surplus returned | add `ActualCost` so the client gets the full reservation back |
| `HostStats.Cost` | `+= ActualCost` | subtract `ActualCost` so the host is not paid |
| Status / miss | `Finished` | `TimedOut`, `Missed++` |

Net after Finish + ERROR timeout, for one inference:

- escrow is whole again (`surplus` at Finish + `ActualCost` here = `ReservedCost`)
- host Cost is back to its pre-Finish value
- `Missed` is incremented, `Invalid` is not
- the record is `StatusTimedOut`, so it is not sampled for validation and is not
  billed as finished

Worked numbers: reserve 100, Finish reports tokens worth 30.

- Finish: `Balance += 70`, `Cost += 30`
- ERROR timeout: `Balance += 30`, `Cost -= 30`, `Missed++`
- client paid 0; host earned 0; miss recorded

If we instead did `Balance += ReservedCost` here, the 70 already returned at
Finish would be refunded again. That is why the snippet adds `ActualCost`, not
`ReservedCost`.

The `if hs.Cost < rec.ActualCost { hs.Cost = 0 }` line is not a policy choice.
`Cost` is a `uint64` session accumulator copied from the invalidate path. Saturating
avoids underflow if the books are inconsistent; in a correct Finish→ERROR
sequence it never triggers.

`Missed` vs `Invalid`: this is a miss, not an invalid completion. Invalid is
"the host produced a body that failed validation." Error-miss is "the host
produced an error envelope, which we refuse to treat as a completion." Same
money outcome as invalidate (client refunded, host unpaid), different counter.

If the error payload has `usage.completion_tokens == 0` (the classifier requires
that) but still has prompt tokens, Finish would still credit input-token cost.
Unwind takes that credit back too: crashing after reading the prompt is not
billable work.

If both token counts are 0, `ActualCost` is 0, Finish already returned the full
reservation as surplus, and the Cost/Balance lines are no-ops. The only
accounting that remains is `Missed++` and `StatusTimedOut`. That is still the
correct outcome.

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
| State root | Vote content is not part of the root, so neither the enum value nor `response_hash` moves any hash. New sessions get different roots only because they carry a new protocol tag (see rollout), which is inherent to shipping under a new approved name. |

## Flow

Messages, and who talks to whom. The only new message on the wire is the
`reason=ERROR` timeout; everything else already exists.

```text
ML node        Executor host          Gateway            Verifier hosts
   |                |                   |                     |
   |  200 + SSE     |                   |                     |
   |  {"error":…}   |                   |                     |
   |--------------->|                   |                     |
   |                |  proxied stream   |                     |
   |                |------------------>|  (client gets       |
   |                |                   |   hostApplicationError,
   |                |                   |   unchanged)        |
   |                |                   |                     |
   |         [1] signs MsgFinishInference{                     |
   |             response_hash = sha256(error payload),        |
   |             input_tokens, output_tokens,                 |
   |             proposer_sig }                                |
   |                |                   |                     |
   |                |  [2] devshard_meta SSE event (mempool)   |
   |                |------------------>|                     |
   |                |                   |                     |
   |                |  [3] VerifyTimeout{inference_id,         |
   |                |      reason=ERROR, finish_tx,            |
   |                |      response_payload}                   |
   |                |                   |-------------------->|
   |                |                   |                     |
   |                |                   |     [4] check proposer_sig on
   |                |                   |         finish_tx, then
   |                |                   |         sha256(response_payload)
   |                |                   |           == finish.response_hash,
   |                |                   |         then classify payload
   |                |                   |         NO call to executor
   |                |                   |         NO payload fetch
   |                |                   |                     |
   |                |                   |  [5] TimeoutVote{accept,             |
   |                |                   |      sig over {…, response_hash}}    |
   |                |                   |<--------------------|
   |                |                   |                     |
   |         [6] diff N = [ MsgFinishInference, MsgTimeoutInference{ERROR, votes} ]
   |                |<------------------|-------------------->|
   |                |                   |                     |
   |         [7] applyFinishInference then applyTimeout(ERROR):
   |             StatusTimedOut, Missed++, cost unwound
```

| Step | Message | Direction | New? |
| --- | --- | --- | --- |
| 1 | `MsgFinishInference` | executor signs | existing, unchanged |
| 2 | `devshard_meta` SSE event carrying the mempool | executor → gateway | existing (`transport/server.go:507`) |
| 3 | `VerifyTimeoutRequest{reason=ERROR, finish_tx, response_payload}` | gateway → verifiers | existing endpoint, new reason + two new fields |
| 4 | — | verifier, no outbound calls | new logic |
| 5 | `VerifyTimeoutResponse` + `TimeoutVote` | verifier → gateway | existing, vote content gains `response_hash` |
| 6 | `Diff{Finish, Timeout(ERROR)}` | gateway → all hosts | existing envelope, new reason |
| 7 | — | every host applies | new reason branch |

## Verification: the verifier never contacts the executor

**Gossip is disabled**, so a verifier does not have the Finish in its own
mempool. The executor hands it to the *gateway* in the `devshard_meta` SSE event
that closes the inference stream (`transport/server.go:507`), which is where the
gateway's `inf.resp.Mempool` — the thing `HasMsgFinish` already reads — comes
from. So the gateway is the only party holding the artifact when the vote starts,
and it must carry it to the verifiers in the request.

That is safe because the artifact is executor-signed. The gateway is an
**untrusted courier**: it cannot forge `ProposerSig`, cannot alter any signed
field without invalidating it, and cannot make a verifier accept anything the
executor did not sign. Delivery by an untrusted party costs nothing when the
payload authenticates itself.

What this buys is exactly the property that matters: verification is a pure
local computation over bytes already in hand. No verifier ever calls the
executor, so a miss cannot be blocked by the failing host being unreachable.

The Finish is what binds the executor to a specific body:

```go
MsgFinishInference{
    ResponseHash: sha256(response payload),  // pins the body
    InputTokens:  n,
    OutputTokens: m,                         // host-reported, not trusted
    ProposerSig:  ...,                       // executor's signature over all of it
}
```

### Token counts are not the criterion

`OutputTokens == 0` is host-reported. An executor that returns an error body
while claiming `OutputTokens = 7` would evade a token-only rule, and the codebase
already treats host-reported usage as untrustworthy for exactly this reason:
`isModelBurnEmpty` is deliberately scoped to the reasoning route because
"the completion_tokens signal is host-reported, so honoring it on non-reasoning
models would let any host dodge empty-stream quarantine by faking usage"
(`redundancy.go:3386`).

So the vote is decided by **the body, pinned by the executor's own hash**. Token
counts are corroborating detail, never the deciding test.

### The body travels with the request, pinned by the signed hash

The gateway relays the response payload alongside the Finish:

```go
// transport/types.go VerifyTimeoutRequest
FinishTx        []byte `json:"finish_tx,omitempty"`        // proto DevshardTx, executor-signed
ResponsePayload []byte `json:"response_payload,omitempty"` // canonical {"events":[...]} bytes
```

Both are **required** for `reason=ERROR`. Neither is trusted:
`sha256(ResponsePayload)` must equal `ResponseHash` inside the executor-signed
Finish. That single comparison is what makes gateway delivery safe — a gateway
cannot produce bytes matching a hash the executor signed over a different body,
and an executor cannot disown the body its own signature pins.

This is reproducible because the payload serialization is deterministic. For a
streamed response it is `json.Marshal(SerializedStreamedResponse{Events: lines})`
(`completionapi/completionresponse.go:227`, and the identical
`ExecutorResponseProcessor.GetResponseBytes`), a struct with one `[]string`
field: fixed field order, canonical string escaping. Given the same lines, any
party computes byte-identical bytes and therefore the same hash. The gateway has
those lines — they are what the host streamed to it, after the host's own id
injection.

Implementation requirement this creates: the gateway must retain the host's
emitted `data:` lines **verbatim** for an error attempt — post id-injection, and
before any gateway-side rewriting such as `rewriteStreamingPayload` — and must
exclude the `devshard_receipt` / `devshard_meta` protocol events. The existing
`errorBodySample` is capped by `bodySampleForLog` and so cannot be used;
reconstruction needs the full line set. If reconstruction is off by a single
byte the hash check fails, the verifier rejects, and behaviour degrades to
today's (no miss). It fails closed.

`transport/server.go:593` `HandleVerifyTimeout` gains an `ERROR` case:

```go
case types.TimeoutReason_TIMEOUT_REASON_ERROR:
    accept, err = host.VerifyErrorTimeout(st, req.InferenceID,
        req.FinishTx, req.ResponsePayload, localMempool)
```

Note the signature: no `ctx`, no `executorClient`, no `payloadFetcher`, no
`config`, no clock. `host.VerifyErrorTimeout` steps, all failures → reject
(`accept=false`):

1. **No deadline check.** Unlike `VerifyRefusedTimeout` (`host/timeout.go:83`)
   and `VerifyExecutionTimeout` (`host/timeout.go:157`), this function reads no
   clock and no timeout config. The signature is the evidence.
2. **No network.** Unlike `VerifyExecutionTimeout`, which falls back to
   `ExecutorClient.GetMempool` (`host/timeout.go:170`), this function makes no
   outbound call at all. The body arrives in the request and is authenticated by
   the hash, so there is nothing to fetch from the executor.
3. **Decode the Finish** from `req.FinishTx`. Prefer an already-applied record or
   a locally-known copy when present, but do not require one: with gossip off,
   the request is normally the only source. Reject if there is no Finish from any
   source — a verifier with no artifact must not vote.
4. **Match the record.** `msg.InferenceId == req.InferenceID`,
   `msg.EscrowId == state.EscrowID`, and `msg.ExecutorSlot == rec.ExecutorSlot`.
   The record must be `StatusStarted` (Finish not yet applied, the common case)
   or `StatusFinished`; if `StatusFinished`, also require
   `msg.ResponseHash == rec.ResponseHash`.
5. **Verify the executor `ProposerSig`** against `slotToAddress[rec.ExecutorSlot]`
   using the same logic as `applyFinishInference` (`state/machine.go:1170`).
   Export a thin `StateMachine.VerifyFinishProposerSig(msg)` helper rather than
   duplicating it.
6. **Pin the body:** `sha256(req.ResponsePayload) == msg.ResponseHash`. Reject on
   mismatch or empty payload.
7. **Classify the pinned body** with `IsTerminalErrorResponse` (below). This is
   the actual verdict: an error envelope with no content anywhere.
8. If all pass → sign a `TimeoutVoteContent{escrow, inference_id, reason=ERROR,
   accept=true, response_hash}` via `signTimeoutVote`
   (`transport/server.go:696`), which gains a `responseHash` argument. The hash
   signed is the one from the Finish this verifier just authenticated.

Why this is sound with the gateway as courier:

- Every fact a verifier votes on is covered by `ProposerSig`, directly or through
  the hash it pins. The gateway contributes delivery, not authority.
- A gateway that alters, truncates or fabricates `finish_tx` fails step 5; one
  that alters the body fails step 6; one that withholds either gets no votes.
  None of these produce a wrong miss.
- A malicious executor cannot escape by inflating `OutputTokens`, because tokens
  are not tested. It would have to produce a body that is *not* an error envelope
  yet hashes to what it signed — which is the collision resistance of SHA-256.
- The executor cannot deny it later: `ProposerSig` covers `ResponseHash`, and the
  vote itself is signed over that same hash.

Ordering note: step 4 accepts `StatusStarted` because with gossip off the
verifier usually has not applied the Finish yet — it arrives in the same diff
that carries the timeout. The strict `StatusFinished` requirement belongs in
`applyTimeout`, where the Finish has provably already been applied, not here.

Remaining residual, much narrower than the token-based rule: an executor that
streams genuine content and *then* appends an error event is not eligible, since
the classifier requires no content anywhere. That attempt stays on the normal
validation path, which is the correct outcome — the client got tokens.

### If gossip is ever re-enabled

Nothing changes. A verifier that already holds the Finish locally can skip
decoding `finish_tx`; the checks and the vote are identical either way. Treat the
local copy as an optimisation, never as a precondition. The body still has to
come from the gateway, since only the executor and the gateway ever see it.

## Binding the vote to the body hash

Without this, a `reason=ERROR` vote asserts only "inference N failed with an
error" — it does not name *which* body the verifier looked at. Add the binding:

```proto
// devshard/proto/devshard/v1/diff.proto
message TimeoutVoteContent {
  string escrow_id      = 1;
  uint64 inference_id   = 2;
  TimeoutReason reason  = 3;
  bool   accept         = 4;
  bytes  response_hash  = 5;  // set for reason=ERROR only; empty otherwise
}
```

**No new wire field is needed on `MsgTimeoutInference`.** `applyTimeout`
reconstructs the signed content locally rather than reading it off the wire
(`state/machine.go:1423`), so it sources the hash from state:

```go
voteContent := &types.TimeoutVoteContent{
    EscrowId:    sm.state.EscrowID,
    InferenceId: msg.InferenceId,
    Reason:      msg.Reason,
    Accept:      vote.Accept,
}
if msg.Reason == types.TimeoutReason_TIMEOUT_REASON_ERROR {
    voteContent.ResponseHash = rec.ResponseHash // set by the Finish applied earlier
}
```

`rec.ResponseHash` is populated because `applyFinishInference` ran first in the
same diff. So the verifier signs the hash of the body it actually classified, the
applier reconstructs the hash the executor committed to, and any disagreement
fails `RecoverAddress` → `ErrInvalidVoteSig` → the diff is rejected. The gateway
cannot influence this: it neither signs votes nor supplies the hash the applier
uses.

This closes the loop with the body check. A verifier accepts only after
`sha256(payload) == ResponseHash`, then signs over that same `ResponseHash`, and
the state machine independently re-derives it from the applied Finish. The body
the group judged and the body the executor committed to are provably the same
bytes at every step.

What this buys: a vote can no longer be lifted from one error body onto another.
That is defence in depth rather than a live hole today — `applyFinishInference`
requires `StatusStarted`, so exactly one body can ever be committed per
inference — but it makes the vote self-describing, which is what makes it usable
later as evidence outside this code path.

### Compatibility of the field addition

The field addition alone is signature-compatible for the existing reasons: with
`response_hash` empty, proto3 omits it, so `REFUSED` / `EXECUTION` vote contents
marshal to byte-identical bytes and all existing signatures still verify. Only
`reason=ERROR` populates it, and that reason exists only in the new binary.

`TimeoutVoteContent` is **not** part of the state root — the root covers
balance, `HostStats`, inferences, warm keys, fees, phase and the version tag
(`ComputeStateRoot` / `ComputeStateRootFromRestHash`, `state/seal.go:134`). Vote
content is verified transiently inside `applyTimeout` and never hashed into the
root. So this change does not by itself alter any root.

It is still protocol-breaking, because a host that does not populate
`response_hash` for a `reason=ERROR` vote produces a signature its peers reject.
That is what forces a new approved version name (see rollout), not a root-layout
change.

One cleanup while here: `applyTimeout` marshals vote content with
`deterministicMarshal.Marshal` (`state/machine.go:1429`) while `signTimeoutVote`
uses plain `proto.Marshal` (`transport/server.go:703`). Both must use
`deterministicMarshal`, or signer and verifier can disagree on encoding. It
happens to work for the current all-scalar message; adding a `bytes` field is a
good moment to make it explicit rather than incidental.

## The shared classifier

This predicate is the consensus rule, so every host must compile against one
implementation. Divergence is not a safety bug — it only causes insufficient
votes, degrading to today's behaviour — but it must be deterministic to be
useful. Put it in `common/completionapi`:

```go
// IsTerminalErrorResponse reports whether a stored response payload is an
// error envelope carrying no usable completion.
func IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool)
```

Accept only when all hold, parsed via
`NewCompletionResponseFromLinesFromResponsePayload`:

- some event carries a top-level `error` object (the `{"error":{code,message,type}}`
  shape, matching `sseChunkErrorPayload`);
- no content anywhere — no `delta.content`, `delta.reasoning_content`,
  `delta.tool_calls`, `message.content`, `choice.text`;
- `usage.completion_tokens == 0`.

A gateway that misclassifies cannot cause a wrong miss: it can only start a vote
that verifiers then reject, because the Finish reports non-zero output tokens.
The consensus rule is the backstop, and it is the stricter of the two.

The "no content anywhere" condition is what stops this becoming an escape hatch.
A host that streams real tokens and appends a trailing error event fails the
predicate, so it cannot dodge validation by decorating a good response. Because
the predicate runs on hash-pinned bytes rather than on self-reported usage, the
host cannot influence the outcome by misreporting token counts either.

## Gateway

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
4. **Forward the artifacts — both required.** Extend
   `TimeoutVerifier.VerifyTimeout` / `CollectTimeoutVotes` to pass:
   - `finish_tx`, from `inf.resp.Mempool` (the `devshard_meta` tail the gateway
     already parses for `HasMsgFinish`) or from `pendingTxs`;
   - `response_payload`, the canonical
     `json.Marshal(SerializedStreamedResponse{Events: lines})` over the host's
     verbatim `data:` lines for that attempt.

   With gossip off these are the verifiers' only sources, so a round missing
   either collects zero accepts.
5. **Retain the full error body.** `inf.errorBodySample` is capped by
   `bodySampleForLog` and cannot be used to rebuild the payload. Capture the
   attempt's complete `data:` line set when `errorSource` is first set — verbatim
   as the host emitted them (post id-injection, pre `rewriteStreamingPayload`),
   excluding `devshard_receipt` / `devshard_meta`. Bound the retention to error
   attempts so this does not become a memory sink on the happy path.
6. **Fix the local finished view.** `nonceStates[n].finished` is set from
   `HasMsgFinish` (`session.go:624`), so gateway-local `IsNonceFinished` would
   still report finished after an accepted error miss, disagreeing with chain
   state. Clear it when the `ERROR` timeout applies, so
   `completeAccountingRequest` and the debug/stats views do not bill a
   timed-out inference.
7. **Retire the exemption TODO** in `recordPostContentWinnerFailureOnce`
   (`redundancy.go:3610`). Recommendation: keep gateway *perf/quarantine*
   scoring decoupled from the on-chain miss for the first release — the miss is
   the protocol penalty, quarantine remains a latency/health signal — but make
   that a deliberate, commented decision instead of the current
   "until hosts submit Finish for errors" placeholder, which this change resolves.
8. **Client response unchanged.** `hostApplicationError` still surfaces with its
   original status/body (`proxy.go:404`). Only accounting changes.

## Rollout: ship as a new approved version

An old host applying `reason=3` hits `applyTimeout`'s `default` branch and
returns `ErrInvalidTimeoutReason`, which **rejects the whole diff** and wedges
the session. A host that ignores `response_hash` in the vote content produces
signatures its peers reject. Both make this a protocol-breaking change.

`devshard/docs/upgrade.md` already prescribes the mechanism:
**protocol-breaking changes require a new `approved_versions` name.** Governance
adds the next name (say `v5`) with the new binary's URL and sha256; versiond
downloads and serves it under `/devshard/v5/*`. There is no constant to bump and
no capability flag to negotiate.

Why this needs no additional gate:

- **The version *is* the gate.** A session binds one protocol name on the first
  owner request to `/devshard/<name>/*` and keeps it for life. Storage pins one
  version per escrow (`ErrSessionVersionConflict`, `storage/interface.go:30`) and
  a host whose `boundVersion` differs refuses to attach
  (`session/manager.go:644`). So every participant in a `v5` session is running
  `v5` code — the error-miss path exists for all of them or none of them.
- **Old sessions are untouched.** A `v4` session never sees `reason=ERROR`,
  because the gateway serving it is the `v4` binary, which cannot emit it.
  Sessions are not migrated; `v4` sessions keep today's behaviour (error stream
  served as `hostApplicationError`, no miss) until they settle.
- **Nothing to check at runtime.** No reading
  `state.StateRootAndProtocolVersion` to decide whether to emit, and no polling
  peers for capability.

Two things an earlier draft of this plan got wrong, recorded so they are not
reintroduced:

- **`types.DevshardStateRootAndProtocolVersion` (`"v2"`) is not the governance
  knob.** It is only the fallback tag for builds with no link stamp — plain
  `go test` and local runs (`types/protocol_version.go:19`). Release binaries get
  the tag from `-X …buildStateRootProtocolVersion` (`DEVSHARD_VERSION`), resolved
  into `EffectiveStateRootAndProtocolVersion` and stamped at session creation
  (`state/machine.go:234`). Editing the constant would change only local test
  builds, and production is already past it. Likewise `types.ProtocolV3` in
  `ParseProtocolVersion` is a different, unrelated enum — not this tag.
- **`/versions` is not a capability probe.** It is dapi's list of
  governance-approved binaries that versiond polls in order to download and run
  them (`upgrade.md`, "Target flow"). Using it to infer whether a peer
  understands `reason=ERROR` would be both racy and meaningless.

Remaining rollout rules:

- Ship the whole change — accept, verify, apply *and* emit — in the one `v5`
  binary. Splitting host and gateway sides across two names buys nothing here,
  since a session can never mix names.
- Keep a kill switch (`DEVSHARD_ERROR_MISS_ENABLED`, default off for the first
  release) on the **emit** side only, so a bad classifier can be silenced without
  a governance round trip. Apply and verify must stay unconditional in `v5`: if
  emit is on for any gateway, every host must be able to apply the result.
- State roots differ from `v4` for new sessions simply because the tag is an
  input to the root. That is the normal consequence of a new name, needs no
  migration, and settlement already carries the tag per session
  (`state/settlement.go:35`) so `v4` and `v5` sessions settle side by side.
- Validate on a release-candidate name with `VERSIOND_FORCE=<name>` before the
  governance proposal, per `upgrade.md` "Operator overrides".

## Alternatives rejected

| Alternative | Why not |
| --- | --- |
| New `MsgErrorInference` message | Needs a new `DevshardTx` oneof field, a new apply path, new snapshot/settlement plumbing. `MsgTimeoutInference` already carries "executor owes a miss, here are the votes". |
| Suppress the Finish; emit only an execution timeout | Loses the signed `ResponseHash`, so the miss becomes unattributable and the executor can deny it. Also a lie: the executor did confirm and did answer. |
| Host refuses to publish Finish on an error body | Then the error can never be proven, and every EngineCore crash falls back to `TIMEOUT_REASON_EXECUTION` — a **32-minute** `ExecutionTimeout` wait per crash (`types/config.go:51`) with the escrow reservation locked for the duration. |
| Keep the Finish but wait out `ExecutionTimeout` before voting | Pointless: the executor already answered, so the deadline adds no evidence. It would also never trigger, since `VerifyExecutionTimeout` rejects as soon as it sees the Finish (`host/timeout.go:163`). |
| Vote on `OutputTokens == 0` from the Finish alone | Host-reported, so a malicious executor returning an error body while claiming non-zero output tokens escapes the miss entirely. The codebase already distrusts this signal for the same reason (`isModelBurnEmpty`, `redundancy.go:3386`). Adopted as corroborating detail only. |
| Verifier fetches the response payload from the executor to classify the body | Adds a network round trip per verifier per miss and makes the vote depend on the failing host being reachable — an executor could block a miss it caused by going dark. Relaying hash-pinned bytes through the gateway gives identical evidence with no such dependency. |
| Serve the error as a successful completion | Charges the client for a crash, and any sampled validation would invalidate it (no logprobs → `InvalidInferenceResult`). |

## Observability

- New `logInferenceStage` stage `error_miss` with `inference_id`, `host`,
  `error_type`, `error_code`, `response_hash`, `votes`, `accepted`.
- Extend the timeout-reason label set (`RecordInferenceTimeout`,
  `timeoutReasonLogLabel` at `session.go:2239`) with `"error"`.
- Counter for rejected error-miss verifications by cause
  (`no_finish_tx`, `no_payload`, `sig`, `hash_mismatch`, `not_error_body`). A
  spike in `hash_mismatch` is the alarm that matters: it means the gateway's
  payload reconstruction has drifted from the executor's serialization, which
  silently disables the whole path. Alert on it.
- Update `docs/inference-lifecycle.md` (status transitions) and
  `proposals/gateway-observability/observability.md` (the `error_stream` row now
  has a follow-up outcome).

## Acceptance tests

State machine (`devshard/state/machine_test.go`):

- `Finish` then `ERROR` timeout in one diff → `StatusTimedOut`, `Missed == 1`,
  `HostStats.Cost` back to pre-finish, balance restored by full `ReservedCost`.
- `ERROR` timeout against `StatusStarted` / `StatusPending` → `ErrInvalidTimeoutReason`.
- `ERROR` timeout with `acceptCount <= threshold` → `ErrInsufficientVotes`, no mutation.
- Wrong-order diff (`Timeout` before `Finish`) → rejected, state unchanged.
- Post-`ERROR` `MsgValidation` / `MsgValidationVote` → rejected or no-op.
- **Hash binding:** a vote signed over a *different* `response_hash` than
  `rec.ResponseHash` → `ErrInvalidVoteSig`, diff rejected, state unchanged.
- **Cross-body replay:** a vote harvested from inference A cannot be applied to
  inference B, even with a matching reason and voter slot.
- **Existing-reason signature compatibility:** `REFUSED` / `EXECUTION` vote
  contents marshal byte-identically to pre-change, and vote signatures produced
  by a pre-change signer still verify (empty `response_hash` must be omitted, not
  encoded as zero-length).
- Golden state-root vectors: unchanged. The default test tag
  (`DevshardStateRootAndProtocolVersion`) is not touched, and vote content is not
  in the root, so every existing vector must still pass byte-for-byte. A vector
  that moves means something leaked into the root that should not have.

Classifier (`common/completionapi`) — this is the consensus rule:

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
- **No-network guard:** run with a nil `ExecutorClient`, an empty local mempool
  and a payload fetcher that fails the test if called. `accept` must still be
  returned from `finish_tx` + `response_payload` alone. This is the regression
  test for both "verifiers do not contact the executor" and "gossip is not
  required".
- Valid Finish sig + payload hash match + error body, against a `StatusStarted`
  record → `accept`, vote signed over `ResponseHash`. This is the normal
  gossip-off case.
- **Lying host:** Finish reports `OutputTokens = 7` but the pinned body is an
  error envelope → `accept`. This is the regression test that keeps the verdict
  on the body rather than on self-reported usage.
- Body with real content and a trailing error event → reject, even when the
  Finish reports `OutputTokens = 0`.
- `sha256(response_payload) != msg.ResponseHash` → reject (covers a gateway that
  substitutes, truncates or re-serializes the body).
- `response_payload` absent or empty → reject.
- Tampered `finish_tx` (re-signed by a non-executor slot, or fields edited after
  signing) → reject.
- `finish_tx` absent or undecodable → reject.
- `finish_tx` naming a different inference or escrow → reject.
- Record already `StatusFinished` and `msg.ResponseHash != rec.ResponseHash` →
  reject.
- **Round-trip determinism:** payload rebuilt from the host's emitted lines by
  the gateway hashes equal to the executor's `ResponseHash` for the exact
  EngineCore case. This is the test that protects the whole scheme from a
  serialization drift.

Gateway (`devshard/cmd/devshardctl`):

- Error-stream attempt end to end: client still gets the original error status
  and body, one diff carries Finish + `ERROR` timeout, `Missed == 1`, request is
  not billed as finished.
- **No-wait assertion:** run with a realistic `ExecutionTimeout` (e.g. `32*60`,
  not the `1`s the existing proxy tests use at `proxy_test.go:709`) and assert the
  miss is applied in well under a second. Existing timeout tests pass only because
  they shrink the config; this path must not need that.
- Kill switch off → no `ERROR` timeout emitted and behaviour is exactly today's.
  (There is no version branch to test: the old behaviour lives in the old
  binary, which does not contain this code.)
- Insufficient votes → today's behaviour, request still fails cleanly.

E2E (`devshard/testenv/citest`): mock-openai fault returning `200` + SSE error
envelope (companion to the existing `a2_ml_upstream_5xx_test.go` which covers the
HTTP 5xx case). Assert `Missed` on the executor slot and no validation job.
