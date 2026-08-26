# Proposal: Account host/ML-node inference errors as a miss

**Status:** Draft / proposal
**Implementation:** [error-finish-miss-protocol-plan.md](../error-finish-miss-protocol-plan.md)

This is the protocol design. Step-by-step implementation lives in the plan
linked above.

**Encoding.** Error-miss is not a timeout. The classifier treats a signed
Finish over an error envelope as a miss (including content-then-error and
host-reported `completion_tokens`), and treats unparseable `data:` JSON as a
miss (junk cannot veto). On-chain message is `MsgErrorMiss { inference_id,
votes[] }` with no payload; the hash is re-derived from `rec.ResponseHash`
after Finish. Vote RPC is `POST .../verify-error-miss`. `TimeoutReason` keeps
`reserved 3` / `"TIMEOUT_REASON_ERROR"` so the abandoned first encoding cannot
return. Timeouts stay `REFUSED` / `EXECUTION` only. See plan §12.

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

`MsgErrorMiss` is categorically different. The executor **did** answer, and it
signed its answer: `MsgFinishInference.ProposerSig` covers
`ResponseHash = sha256(error payload)`. That signature is complete, positive
evidence of failure, available immediately. There is nothing left to wait for —
waiting could only weaken the case, never strengthen it.

So the deadline is absent on **both** sides:

- **Verifier:** `VerifyErrorMiss` performs **no** deadline comparison. It does
  not read `RefusalTimeout`, `ExecutionTimeout`, or `TimeoutBuffer`. Reviewers
  should treat any wall-clock gate added here as a bug.
- **Gateway:** `HandleErrorMiss` (not a branch of `HandleTimeout`) skips
  `sleepUntilDeadlineWithHeartbeat` and goes straight to collecting error-miss
  votes. End-to-end latency is one round of verifier RPCs plus one diff.

Why this matters beyond latency:

| | Without this change | With `MsgErrorMiss` |
| --- | --- | --- |
| Time to account the miss | never (nonce is finished, so no timeout path runs at all) | immediate |
| If the finish were suppressed instead | ~32 min of `ExecutionTimeout` per crash | n/a |
| `ReservedCost` held in escrow | until seal, credited to the host | released in the same diff |
| Host reuse for retries | crashing host keeps looking healthy | miss lands before the next request picks it |

Timeouts remain elapsed-time claims (`REFUSED` / `EXECUTION`). Error-miss is an
instant, evidence-backed miss. That is why it is a new message, not a third
`TimeoutReason`: putting it on `MsgTimeoutInference` invited a later "fix" that
added a deadline, and mixed hash-bound body votes into a path that has none.

## Design: new `MsgErrorMiss`; timeouts stay deadlines

Error-miss is not a timeout. Keep `MsgTimeoutInference` for refused and
execution deadlines only. Add `MsgErrorMiss` — votes over the executor-signed
Finish body, no payload on the message. `TimeoutReason` reserves the abandoned
`ERROR` value so it cannot come back.

```proto
// proto/devshard/v1/tx.proto
enum TimeoutReason {
  TIMEOUT_REASON_UNSPECIFIED = 0;
  TIMEOUT_REASON_REFUSED     = 1;
  TIMEOUT_REASON_EXECUTION   = 2;
  reserved 3;
  reserved "TIMEOUT_REASON_ERROR";
}

message MsgErrorMiss {
  uint64 inference_id = 1;
  repeated ErrorMissVote votes = 2; // { voter_slot, accept, signature }
}

// proto/devshard/v1/diff.proto
message DevshardTx {
  oneof tx {
    // … existing fields 1–11 …
    MsgErrorMiss error_miss = 14; // 12/13 reserved for cPoC
  }
}

message TimeoutVoteContent {
  string escrow_id     = 1;
  uint64 inference_id  = 2;
  TimeoutReason reason = 3;
  bool   accept        = 4;
  reserved 5; // was ERROR-only response_hash
}

message ErrorMissVoteContent {
  string escrow_id     = 1;
  uint64 inference_id  = 2;
  bool   accept        = 3;
  bytes  response_hash = 4;
}
```

`MsgTimeoutInference` and `TimeoutVote` are untouched, and every existing field
number is preserved, so serialized snapshots and `REFUSED` / `EXECUTION`
signatures stay byte-identical. Error-miss votes sign `ErrorMissVoteContent`
(hash-bound). The hash is not a field on `MsgErrorMiss`: `applyErrorMiss`
re-derives it from `rec.ResponseHash` after Finish (see [Binding the vote to the
body hash](#binding-the-vote-to-the-body-hash)). Vote RPC is a new endpoint,
`POST .../verify-error-miss`, not a new reason on verify-timeout.

### The diff

```
diff N: [ MsgFinishInference{id, response_hash, proposer_sig},
          MsgErrorMiss{id, votes[]} ]
```

The **ordering** `Finish → ErrorMiss` is the invariant, not literally "same diff".
`applyErrorMiss` requires `StatusFinished`, which the Finish
produces. Same-diff is the atomic and preferred case (the gateway holds the
Finish in `pendingTxs` while collecting votes); if the Finish was already
published in an earlier diff, a later solo ErrorMiss diff applies the same way —
but only inside the seal window.

`sealEligibleStatus` includes `StatusFinished` (`state/seal.go:254`), so a
finished record is folded into `SealedAcc` once the nonce gate
(`InferenceSealGraceNonces`) and the state-clock grace
(`InferenceSealGraceSeconds`) both clear. After that `applyErrorMiss` hits
`isInferenceEvictedFromLive` and returns `inference %d is sealed`
(`state/machine.go`), and the miss is unclaimable for good. So the deferred
path is bounded, not equivalent: emit `MsgErrorMiss` in the same diff as
the Finish, or at least well inside the grace. This is another reason the design
has no deadline — any wait pushes the miss toward the seal boundary that
would silently discard it.

Tx order inside a diff is preserved: `applyCore` walks `diff.Txs` in order
(`state/machine.go` `applyTx`), and `localBestEffortLocked` applies
candidates in queue order, so FIFO `pendingTxs` plus `extraTxs` gives Finish
before ErrorMiss.

### Why this shape

The Finish must land on-chain, not be suppressed. It is the *only* thing that
binds the executor to the error body: `ProposerSig` covers `ResponseHash`. Drop
the Finish and you lose the proof and are back to an unattributable miss.
`MsgErrorMiss` immediately after it converts that proof into the accounting
outcome. Payload bytes stay off the message: the Finish already committed the
hash.

## State machine

### `applyErrorMiss` — status gate

`state/machine.go`. Requires Finish first:

```go
if rec.Status != types.StatusFinished {
    return fmt.Errorf("%w: error-miss requires finished, got %d",
        types.ErrInvalidTransition, rec.Status)
}
```

Vote counting, dedup-by-address, weight-by-slot-count and the `VoteThreshold`
check match `applyTimeout`. Votes sign `ErrorMissVoteContent`, not
`TimeoutVoteContent`. There is no `TIMEOUT_REASON_ERROR` branch in
`applyTimeout`; unknown reasons still hit `default` → `ErrInvalidTimeoutReason`.

### `applyErrorMiss` — unwind the finish accounting

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
`MsgErrorMiss` then only flipped status to timed-out and incremented
`Missed`, the host would be **paid and marked missed** for the same inference,
and the client would keep paying `ActualCost`.

Existing `applyTimeout` (refused / execution) never hits this because those
reasons require `Pending` or `Started` — Finish has not run, `HostStats.Cost`
was never credited, and the full `ReservedCost` is still locked, so
`Balance += rec.ReservedCost` is correct.

`MsgErrorMiss` is the first miss that applies **after** Finish. The reservation is
already split, so it cannot reuse the refused/execution refund. Mirror
the `StatusInvalidated` unwind (`state/machine.go`):

```go
sm.state.Balance += rec.ActualCost          // surplus was already released
hs := sm.state.HostStats[rec.ExecutorSlot]
if hs.Cost < rec.ActualCost { hs.Cost = 0 } else { hs.Cost -= rec.ActualCost }
rec.Status = types.StatusTimedOut
sm.state.HostStats[rec.ExecutorSlot].Missed++
```

| Ledger | After Finish | What ErrorMiss must do |
| --- | --- | --- |
| `Balance` | only surplus returned | add `ActualCost` so the client gets the full reservation back |
| `HostStats.Cost` | `+= ActualCost` | subtract `ActualCost` so the host is not paid |
| Status / miss | `Finished` | `TimedOut`, `Missed++` |

Net after Finish + `MsgErrorMiss`, for one inference:

- escrow is whole again (`surplus` at Finish + `ActualCost` here = `ReservedCost`)
- host Cost is back to its pre-Finish value
- `Missed` is incremented, `Invalid` is not
- the record is `StatusTimedOut`, so it is not sampled for validation and is not
  billed as finished

Worked numbers: reserve 100, Finish reports tokens worth 30.

- Finish: `Balance += 70`, `Cost += 30`
- ErrorMiss: `Balance += 30`, `Cost -= 30`, `Missed++`
- client paid 0; host earned 0; miss recorded

If we instead did `Balance += ReservedCost` here, the 70 already returned at
Finish would be refunded again. That is why the snippet adds `ActualCost`, not
`ReservedCost`.

The `if hs.Cost < rec.ActualCost { hs.Cost = 0 }` line is not a policy choice.
`Cost` is a `uint64` session accumulator copied from the invalidate path. Saturating
avoids underflow if the books are inconsistent; in a correct Finish→ErrorMiss
sequence it never triggers.

`Missed` vs `Invalid`: this is a miss, not an invalid completion. Invalid is
"the host produced a body that failed validation." Error-miss is "the host
produced an error envelope, which we refuse to treat as a completion." Same
money outcome as invalidate (client refunded, host unpaid), different counter.

If the error payload still has prompt tokens, Finish would still credit
input-token cost. Unwind takes that credit back too: crashing after reading
the prompt is not billable work. Host-reported `completion_tokens` do not
veto the miss (see classifier).

If both token counts are 0, `ActualCost` is 0, Finish already returned the full
reservation as surplus, and the Cost/Balance lines are no-ops. The only
accounting that remains is `Missed++` and `StatusTimedOut`. That is still the
correct outcome.

Invariant to assert in tests: after `MsgErrorMiss` the escrow balance is
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
| State root | Vote content is not part of the root, so `ErrorMissVoteContent.response_hash` moves no hash. New sessions get different roots only because they carry a new protocol tag (see rollout), which is inherent to shipping under a new approved name. |

## Flow

Messages, and who talks to whom. The only new on-chain message is
`MsgErrorMiss`; timeouts stay refused/execution.

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
   |                |  [3] POST .../verify-error-miss          |
   |                |      {inference_id, finish_tx,           |
   |                |       response_payload}                  |
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
   |                |                   |  [5] ErrorMissVote{accept,            |
   |                |                   |      sig over ErrorMissVoteContent}   |
   |                |                   |<--------------------|
   |                |                   |                     |
   |         [6] diff N = [ MsgFinishInference, MsgErrorMiss{id, votes} ]
   |                |<------------------|-------------------->|
   |                |                   |                     |
   |         [7] applyFinishInference then applyErrorMiss:
   |             StatusTimedOut, Missed++, cost unwound
```

| Step | Message | Direction | New? |
| --- | --- | --- | --- |
| 1 | `MsgFinishInference` | executor signs | existing, unchanged |
| 2 | `devshard_meta` SSE event carrying the mempool | executor → gateway | existing (`transport/server.go:507`) |
| 3 | `VerifyErrorMissRequest{finish_tx, response_payload}` | gateway → verifiers | **new** endpoint |
| 4 | — | verifier, no outbound calls | new logic |
| 5 | `VerifyErrorMissResponse` + `ErrorMissVote` | verifier → gateway | new vote content |
| 6 | `Diff{Finish, MsgErrorMiss}` | gateway → all hosts | new tx type |
| 7 | — | every host applies | `applyErrorMiss` |

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
// transport/types.go VerifyErrorMissRequest
FinishTx        []byte `json:"finish_tx,omitempty"`        // proto DevshardTx, executor-signed
ResponsePayload []byte `json:"response_payload,omitempty"` // canonical {"events":[...]} bytes
```

Both are **required**. Neither is trusted:
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

Second implementation requirement: only attempts whose stream the gateway read
to completion can be reconstructed. The gateway cancels losing speculative
attempts, so its retained lines may be a strict prefix of what the executor
hashed, and the hash check then rejects. That fails closed, which is correct,
but it means a cancelled error attempt yields no miss even though the executor
signed one. Distinguish this in metrics — a `hash_mismatch` on a cancelled
attempt is expected and must not be alerted on as serialization drift (see
Observability).

`POST .../verify-error-miss` calls `host.VerifyErrorMiss`. Verify-timeout is
refused/execution only: no `finish_tx`, no `response_payload`, no error reason.

Note the signature: no `ctx`, no `executorClient`, no `payloadFetcher`, no
`config`, no clock. `host.VerifyErrorMiss` steps, all failures → reject
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
   the actual verdict: an error envelope and/or unparseable `data:` JSON. Content
   before the error still accepts — the signed Finish is the proof.
8. If all pass → sign `ErrorMissVoteContent{escrow, inference_id, accept=true,
   response_hash}` via `signErrorMissVote`. The hash signed is the one from the
   Finish this verifier just authenticated. `signTimeoutVote` does not take a
   hash.

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
that carries `MsgErrorMiss`. The strict `StatusFinished` requirement belongs in
`applyErrorMiss`, where the Finish has provably already been applied, not here.

Content-then-error is in scope: the classifier accepts an error envelope after
usable tokens. The signed Finish hashes the whole body, including the error.
A host cannot dodge the miss by decorating a crash with prefix tokens. Happy-path
content with no error still rejects.

### If gossip is ever re-enabled

Nothing changes. A verifier that already holds the Finish locally can skip
decoding `finish_tx`; the checks and the vote are identical either way. Treat the
local copy as an optimisation, never as a precondition. The body still has to
come from the gateway, since only the executor and the gateway ever see it.

## Binding the vote to the body hash

Without this, an error-miss vote asserts only "inference N failed with an
error" — it does not name *which* body the verifier looked at. Bind it on
`ErrorMissVoteContent`, not on `TimeoutVoteContent` (field 5 there is reserved):

```proto
message ErrorMissVoteContent {
  string escrow_id     = 1;
  uint64 inference_id  = 2;
  bool   accept        = 3;
  bytes  response_hash = 4;
}
```

**No payload and no hash on `MsgErrorMiss`.** `applyErrorMiss` reconstructs the
signed content locally rather than reading it off the wire, so it sources the
hash from state:

```go
voteContent := &types.ErrorMissVoteContent{
    EscrowId:     sm.state.EscrowID,
    InferenceId:  msg.InferenceId,
    Accept:       vote.Accept,
    ResponseHash: rec.ResponseHash, // set by the Finish applied earlier
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

### Compatibility

`TimeoutVoteContent` is unchanged except `reserved 5`, so `REFUSED` /
`EXECUTION` vote contents marshal byte-identically to pre-change and existing
signatures still verify.

`ErrorMissVoteContent` is **not** part of the state root — the root covers
balance, `HostStats`, inferences, warm keys, fees, phase and the version tag
(`ComputeStateRoot` / `ComputeStateRootFromRestHash`, `state/seal.go:134`). Vote
content is verified transiently inside `applyErrorMiss` and never hashed into the
root. So this change does not by itself alter any root.

It is still protocol-breaking: an old host does not know `DevshardTx` oneof 14,
so applying `MsgErrorMiss` rejects the whole diff. That is what forces a new
approved version name (see rollout), not a root-layout change.

Sign and verify error-miss votes with `deterministicMarshal.Marshal`, matching
`applyErrorMiss`. Do not reuse `signTimeoutVote` for this content.

## The shared classifier

This predicate is the consensus rule, so every host must compile against one
implementation. Divergence is not a safety bug — it only causes insufficient
votes, degrading to today's behaviour — but it must be deterministic to be
useful. Put it in `common/completionapi`:

```go
// IsTerminalErrorResponse reports whether a stored response payload is not a
// usable completion — an error envelope and/or malformed data: JSON.
func IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool)
```

Accept when the payload is a streamed `{"events":[...]}` body and **either**:

1. some event carries a top-level `error` object (the `{"error":{code,message,type}}`
   shape, matching `sseChunkErrorPayload`), **including** after usable content
   and regardless of `usage.completion_tokens`; or
2. any `data:` payload that is not valid JSON (unparseable-only Finish).

Happy-path content with no error still rejects. The host fully controls the
signed payload: fail-closed unparseable JSON (`ok=false`) would let it dodge a
deterministic miss by appending one junk line. Junk is the proof, not a veto.

Token counts are never the criterion. Host-reported `completion_tokens` do not
veto an error envelope.

Out of scope by construction: client-fault errors. A 4xx from the ML node is
returned to the caller without rotation (`inference/engine.go:174`), but its
JSON body carries no usage block, so `JsonCompletionResponse.GetUsage()` fails
on `Usage.IsEmpty()` (`completionresponse.go:40`), `processExecutionHTTPResponse`
returns an error (`execute.go:113`), and **no Finish is ever signed**. This
design only ever sees errors that produced a Finish, so a malformed request
cannot be turned into a miss on an honest host. Keep it that way: if a future
change makes `GetUsage` tolerant of missing usage, this predicate needs an
explicit executor-fault condition, or a user could grief every host in the
group with one bad prompt.

There is no second, independent check behind this predicate. Verifiers run the
same implementation over the same hash-pinned bytes as the gateway, so they
agree with a misclassifying gateway by construction — the earlier token-based
draft had a backstop here, and the hash-pinned design deliberately does not.
The classifier is therefore the single point of truth, and its correctness is
safety-critical in one direction: a predicate that accepted a genuine
completion would refund the client and charge an honest host a miss. Bias
ambiguous cases to `ok == false` and fall through to today's behaviour — except
unparseable `data:` JSON, which must be a miss or the host can veto with junk.

Because the predicate runs on hash-pinned bytes rather than on self-reported
usage, the host cannot influence the outcome by misreporting token counts.
Content-then-error still accepts: the signed Finish is the proof, and prefix
tokens are not an escape hatch.

## Gateway

All in `devshard/cmd/devshardctl` + `devshard/user/session.go`.

1. **Let the path run.** `shouldRunHandleTimeout` returns false for any finished
   nonce. Exception: run when `errorMissEnabledFor(inf)` (`errorTerminal`) even
   if the nonce finished. `runHandleTimeout(errorMiss)` calls `HandleErrorMiss`,
   not `HandleTimeout`.
2. **`HandleErrorMiss`**, not a reason branch of `HandleTimeout`. Today the
   execution path sees a pending Finish and returns early after publishing it.
   For an error attempt: **do not** early-return, and **do not** call
   `sleepUntilDeadlineWithHeartbeat` — no `RefusalTimeout`, no `ExecutionTimeout`,
   no `TimeoutBuffer`. Go straight to collecting error-miss votes. Timeout path
   stays refused/execution only.
3. **Same-diff composition.** Pin the pending Finish (and any ErrorMiss already
   queued for that nonce) for the vote round so a concurrent compose cannot drain
   them. Pass `MsgErrorMiss` into the same locked compose as `extraTxs` after
   that Finish — do not enqueue ErrorMiss into the shared pending queue first.
   Append order puts Finish ahead of ErrorMiss.
4. **Forward the artifacts — both required.** `verify-error-miss` (not
   verify-timeout) carries:
   - `finish_tx`, from `inf.resp.Mempool` (the `devshard_meta` tail the gateway
     already parses for `HasMsgFinish`) or from `pendingTxs`;
   - `response_payload`, the canonical
     `json.Marshal(SerializedStreamedResponse{Events: lines})` over the host's
     verbatim `data:` lines for that attempt.

   With gossip off these are the verifiers' only sources, so a round missing
   either collects zero accepts.
5. **Retain the full error body.** `inf.errorBodySample` is capped by
   `bodySampleForLog` and cannot be used to rebuild the payload. Capture the
   attempt's complete `data:` line set — verbatim as the host emitted them (post
   id-injection, pre `rewriteStreamingPayload`), excluding `devshard_receipt` /
   `devshard_meta`. Bound the retention (`maxErrorStreamBytes`) so this does not
   become a memory sink on the happy path.
6. **Fix the local finished view.** `nonceStates[n].finished` is set from
   `HasMsgFinish`, so gateway-local `IsNonceFinished` would still report finished
   after an accepted error miss, disagreeing with chain state. Clear it when
   `MsgErrorMiss` applies, so `completeAccountingRequest` and the debug/stats
   views do not bill a timed-out inference.
7. **Retire the exemption TODO** in `recordPostContentWinnerFailureOnce`.
   Keep gateway *perf/quarantine* scoring decoupled from the on-chain miss —
   the miss is the protocol penalty, quarantine remains a latency/health
   signal — but make that a deliberate, commented decision instead of the
   "until hosts submit Finish for errors" placeholder, which this change resolves.
8. **Client response unchanged.** `hostApplicationError` still surfaces with its
   original status/body. Only accounting changes. Emit is always on, gated by
   `errorTerminal` (retriable capability 4xx stay off). Content before the error
   still emits.

## Rollout: ship as a new approved version

An old host applying `MsgErrorMiss` (oneof 14) does not know the transaction
and **rejects the whole diff**, which wedges the session. Both the new message
and the vote-content type make this a protocol-breaking change.

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
- **Old sessions are untouched.** A `v4` session never sees `MsgErrorMiss`,
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
  understands `MsgErrorMiss` would be both racy and meaningless.

Remaining rollout rules:

- Ship the whole change — accept, verify, apply *and* emit — in the one `v5`
  binary. Splitting host and gateway sides across two names buys nothing here,
  since a session can never mix names.
- Emit is always on in this binary. The safety valve is the fail-closed
  classifier (`errorTerminal` + empty `contentSource`); there is no env flag to
  silence emit without a new binary. Apply and verify stay unconditional: every
  host in a mixed-traffic session must be able to apply `MsgErrorMiss`.
- State roots differ from `v4` for new sessions simply because the tag is an
  input to the root. That is the normal consequence of a new name, needs no
  migration, and settlement already carries the tag per session
  (`state/settlement.go:35`) so `v4` and `v5` sessions settle side by side.
- Validate on a release-candidate name with `VERSIOND_FORCE=<name>` before the
  governance proposal, per `upgrade.md` "Operator overrides".

## Alternatives rejected

| Alternative | Why not |
| --- | --- |
| Third `TimeoutReason` on `MsgTimeoutInference` | Error-miss is not a deadline. A third reason plus `response_hash` / `finish_tx` / `response_payload` on the timeout path mixed two shapes and invited a later "fix" that added a wait. Timeouts stay `REFUSED` / `EXECUTION`; `TIMEOUT_REASON_ERROR` is reserved so it cannot return. |
| Put payload bytes or the hash on `MsgErrorMiss` | The Finish already committed `ResponseHash`. Re-derive it from `rec.ResponseHash`. Payload stays on the verify RPC, off-chain. |
| Suppress the Finish; emit only an execution timeout | Loses the signed `ResponseHash`, so the miss becomes unattributable and the executor can deny it. Also a lie: the executor did confirm and did answer. |
| Host refuses to publish Finish on an error body | Then the error can never be proven, and every EngineCore crash falls back to `TIMEOUT_REASON_EXECUTION` — a **32-minute** `ExecutionTimeout` wait per crash (`types/config.go:51`) with the escrow reservation locked for the duration. |
| Keep the Finish but wait out `ExecutionTimeout` before voting | Pointless: the executor already answered, so the deadline adds no evidence. It would also never trigger, since `VerifyExecutionTimeout` rejects as soon as it sees the Finish (`host/timeout.go:163`). |
| Vote on `OutputTokens == 0` from the Finish alone | Host-reported, so a malicious executor returning an error body while claiming non-zero output tokens escapes the miss entirely. The codebase already distrusts this signal for the same reason (`isModelBurnEmpty`, `redundancy.go:3386`). Adopted as corroborating detail only. |
| Verifier fetches the response payload from the executor to classify the body | Adds a network round trip per verifier per miss and makes the vote depend on the failing host being reachable — an executor could block a miss it caused by going dark. Relaying hash-pinned bytes through the gateway gives identical evidence with no such dependency. |
| Serve the error as a successful completion | Charges the client for a crash, and any sampled validation would invalidate it (no logprobs → `InvalidInferenceResult`). |
| Fail-closed on unparseable `data:` JSON | The host controls the signed payload. One junk line would veto a deterministic miss. Unparseable JSON is itself a miss. |

## Observability

- New `logInferenceStage` stage `error_miss` with `inference_id`, `host`,
  `error_type`, `error_code`, `response_hash`, `votes`, `accepted`.
- Extend the timeout-reason label set (`RecordInferenceTimeout`,
  `timeoutReasonLogLabel` at `session.go:2239`) with `"error"`.
- Counter for rejected error-miss verifications by cause
  (`no_finish_tx`, `no_payload`, `sig`, `hash_mismatch`, `not_error_body`),
  labelled by whether the gateway read the attempt's stream to completion.
  `hash_mismatch` on a cancelled attempt is expected (truncated prefix).
  `hash_mismatch` on a fully-read attempt is the alarm that matters: the
  gateway's payload reconstruction has drifted from the executor's
  serialization, which silently disables the whole path. Alert on that label
  only.
- Update `docs/inference-lifecycle.md` (status transitions) and
  `proposals/gateway-observability/observability.md` (the `error_stream` row now
  has a follow-up outcome).

## Acceptance tests

State machine (`devshard/state/machine_test.go`):

- `Finish` then `MsgErrorMiss` in one diff → `StatusTimedOut`, `Missed == 1`,
  `HostStats.Cost` back to pre-finish, balance restored by full `ReservedCost`.
- `MsgErrorMiss` against `StatusStarted` / `StatusPending` → `ErrInvalidTransition`.
- `MsgErrorMiss` with `acceptCount <= threshold` → `ErrInsufficientVotes`, no mutation.
- Wrong-order diff (`ErrorMiss` before `Finish`) → rejected, state unchanged.
- Post-ErrorMiss `MsgValidation` / `MsgValidationVote` → rejected or no-op.
- **Hash binding:** a vote signed over a *different* `response_hash` than
  `rec.ResponseHash` → `ErrInvalidVoteSig`, diff rejected, state unchanged.
- **Cross-body replay:** a vote harvested from inference A cannot be applied to
  inference B, even with a matching voter slot.
- **Existing-reason signature compatibility:** `REFUSED` / `EXECUTION` vote
  contents marshal byte-identically to pre-change (`TimeoutVoteContent` field 5
  reserved, not populated).
- Golden state-root vectors: unchanged. The default test tag
  (`DevshardStateRootAndProtocolVersion`) is not touched, and vote content is not
  in the root, so every existing vector must still pass byte-for-byte. A vector
  that moves means something leaked into the root that should not have.

Classifier (`common/completionapi`) — this is the consensus rule:

- The exact EngineCore payload from this report → `ok == true`.
- Real content followed by a trailing error event → `ok == true` (the Finish
  hashes the whole body, including the error).
- Error event with `completion_tokens > 0` → `ok == true` (token counts are
  host-reported and do not veto an error envelope).
- Unparseable `data:` JSON (alone or after an error) → `ok == true` (junk cannot veto).
- Empty stream (role + `[DONE]`, no error object) → `ok == false`
  (that stays the empty-stream path).
- Happy-path content, no error → `ok == false`.

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
- Body with real content and a trailing error event → `accept` (signed Finish is
  the proof).
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
  and body, one diff carries Finish + `MsgErrorMiss`, `Missed == 1`, request is
  not billed as finished.
- **No-wait assertion:** run with a realistic `ExecutionTimeout` (e.g. `32*60`,
  not the `1`s the existing proxy tests use at `proxy_test.go:709`) and assert the
  miss is applied in well under a second. Existing timeout tests pass only because
  they shrink the config; this path must not need that.
- Content-then-error still emits (`errorTerminal`); non-terminal capability error
  → no `MsgErrorMiss`. (There is no env flag to silence emit.)
- Insufficient votes → today's behaviour, request still fails cleanly.

E2E (`devshard/testenv/citest`): mock-openai fault returning `200` + SSE error
envelope (companion to the existing `a2_ml_upstream_5xx_test.go` which covers the
HTTP 5xx case). Assert `Missed` on the executor slot and no validation job.
