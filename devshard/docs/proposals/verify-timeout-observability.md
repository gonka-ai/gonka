# VerifyTimeout observability — proposal

**Component:** gateway only (`devshard/user`, `logging.Stage`). No host change.
**Status:** implemented (see `devshard/docs/verify-timeout-observability-plan.md`)
**Related incident:** escrow `65405` nonce `12707` — `timeout_vote_queue_expired` on 4 verifiers after 120s, then `errors=4` / insufficient votes. A request-filtered dump could not show who held `SharedVerifierQueue`.

Execution-timeout verify is a FinishInference mempool probe (`host.VerifyExecutionTimeout`). The queue still serializes those POSTs per verifier address (`MaxConcurrentVerifierRPCs`, default 10). When acquire fails, today's expire line includes who is in flight. Occupying RPCs live under **other** `request=` IDs; `inflight_snapshot` copies them onto the blocked request.

---

## Goal

From gateway logs of the **blocked** request alone:

1. See every `VerifyTimeout` / `VerifyErrorMiss` **currently in flight** to that verifier, including **when each was sent**.
2. See when **this** round actually sent (distinct from “requested”, which is pre-queue).
3. Log every RPC that **timed out without a vote**, and every RPC that **returned in more than 15s** (accept, reject, or error).

No Prometheus in this pass. Stage lines are enough to grep a production dump.

---

## Decisions

| | Choice |
|---|---|
| Where occupancy lives | `verifierHostQueue` (process-wide, same object as the semaphore) |
| “In flight” | token acquired **and** HTTP call started. Waiters are not inflight. |
| Snapshot on expire | **that verifier address only** (the one we failed to send to) |
| `timeout_vote_requested` | keep: intent, before `acquire` |
| New `timeout_vote_sent` | HTTP POST is about to start (`sent_at` unix ms) |
| Slow threshold | `VerifyTimeoutSlowLog = 15 * time.Second` |
| Timed out without vote | RPC was sent, then `err != nil` and the error is a deadline/cancel/transport timeout — **no** accept/reject vote |
| Early quorum cancel | after send: log as `rpc_timeout` only if `errors.Is(err, context.DeadlineExceeded)` or a transport timeout. Parent `Canceled` because another verifier already met weight is **not** a hung verifier; skip the extra slow/timeout line if `elapsed ≤ 15s` |
| Error-miss | same occupancy + stages (`reason=error`). Today `CollectErrorMissVotes` does not even log `timeout_vote_queue_expired` |
| Snapshot size | all inflight count; detail the **oldest 8** (highest `sent_age_ms`) so a line stays grepable at cap 32 |
| Occupancy fields on the waiter’s line | copy holder `request=` / escrow / nonce onto the **blocked** request so a filtered dump is enough |

---

## Occupancy record

On successful acquire, **before** `VerifyTimeout` / `VerifyErrorMiss`:

```go
type inflightVerify struct {
    RequestID string    // logging.RequestID(ctx); empty if unset
    Escrow    string
    Nonce     uint64
    Reason    string    // execution | refused | error
    SentAt    time.Time // when the HTTP call starts, not enqueue time
}
```

`release` removes that entry. Tests inject `WithVerifierQueue(newVerifierHostQueue())`; occupancy is on the same queue instance.

`acquire` stays `(ctx, addr)`. A follow-up `beginInflight(addr, rec)` / `endInflight(addr, rec)` keeps the semaphore ignorant of metadata. `snapshot(addr) (inflight []inflightVerify, waiters int)`.

Waiter count is optional but cheap (increment around the `select` in `acquire`). Include `waiters` on expire so a storm (many queued) is distinguishable from one hung RPC.

---

## Stage lines

All via `logging.Stage`. Existing `timeout_vote_requested` / `dequeued` / `result` / `tally` stay.

### 1. `timeout_vote_sent` — this round actually sent

Emitted once per verifier, immediately before `VerifyTimeout` / `VerifyErrorMiss`.

```
stage=timeout_vote_sent escrow=65405 nonce=12707 reason=execution host=qsy8ts3e
  sent_at_ms=1788006267000
```

`timeout_vote_requested` without a later `timeout_vote_sent` for the same `(request, host, nonce)` means the RPC never left the gateway.

### 2. `timeout_vote_queue_expired` — cannot send; dump holders

Extend the existing stage (error-miss must emit it too):

```
stage=timeout_vote_queue_expired escrow=65405 nonce=12707 reason=execution host=qsy8ts3e
  wait_ms=120186 wait_timeout_ms=120000 error="context deadline exceeded"
  inflight=2 waiters=5
  inflight_snapshot="request=req-aaa escrow=65401 nonce=12003 reason=execution sent_age_ms=174000; request=req-bbb escrow=65405 nonce=12690 reason=execution sent_age_ms=81000"
```

`inflight_snapshot` is oldest-first, `; `-separated, max 8 entries. `sent_age_ms` is `now - SentAt` at expire time (age is more useful in a dump than a raw unix ms).

If `inflight=0` on expire, that is a bug (token leak or wait cancelled for another reason) — still log it.

### 3. `timeout_vote_rpc_timeout` — sent, no vote, deadline/transport

After the HTTP call, if there is no vote and the error is a wait on the **RPC** (not the queue):

```
stage=timeout_vote_rpc_timeout escrow=65405 nonce=12707 reason=execution host=qsy8ts3e
  elapsed_ms=180012 error="context deadline exceeded"
```

Classify: `errors.Is(err, context.DeadlineExceeded)` or `transport.IsTransientWriteError` after the retry, or client timeout wrapped by `post`. Queue expiry must **not** use this stage.

### 4. `timeout_vote_slow` — returned in > 15s (vote or not)

If `elapsed > 15s` **and** we did send:

```
stage=timeout_vote_slow escrow=65405 nonce=12707 reason=execution host=qsy8ts3e
  elapsed_ms=16200 outcome=accept
```

`outcome` is `accept` | `reject` | `error` | `timeout`. A timed-out RPC that also took >15s may emit **both** `timeout_vote_rpc_timeout` and `timeout_vote_slow` (timeout line is the reason; slow line is the duration gate). That is intentional: grepping `timeout_vote_slow` lists every long call; grepping `timeout_vote_rpc_timeout` lists hung calls with no vote.

Fast accepts (`elapsed ≤ 15s`) stay on `timeout_vote_result` only.

### 5. `timeout_vote_tally` — name the error class

Add `error_classes=queue_expired:4` (or `rpc_timeout:2,queue_expired:2`). Today `errors=4` does not say whether the host was reached.

Add `VoteErrorQueueExpired = "queue_expired"` in `classifyVoteError` for `errors.Is(err, context.DeadlineExceeded)` **before** the RPC (acquire wait). RPC deadline after send stays a separate class `VoteErrorRPCTimeout = "rpc_timeout"` (or keep `verifier_unreachable` for RPC and only split queue). Prefer two classes: queue vs RPC. Accounting already uses this string as a counter key — two new bounded names, not addresses.

---

## Flow (where to hook)

`collectTimeoutVotes` and `CollectErrorMissVotes` share the same shape:

```
timeout_vote_requested          // existing, before acquire
acquire(waitCtx)
  fail → snapshot + timeout_vote_queue_expired  // enrich; add on error-miss
  ok   → defer release + endInflight
timeout_vote_dequeued           // existing, if wait_ms > 0
if parent ctx done → return (no sent line)
beginInflight(...)
timeout_vote_sent
t0 := now
VerifyTimeout / VerifyErrorMiss  (+ existing write retry)
elapsed := since t0
endInflight on defer with release
if sent && no vote && rpc timeout → timeout_vote_rpc_timeout
if sent && elapsed > 15s → timeout_vote_slow
timeout_vote_result             // existing
```

Do not start inflight until after the parent-ctx check that skips the RPC (otherwise a cancelled waiter would look like a holder).

---

## Tests (`devshard/user`)

Reuse `concurrencyMockVerifier` / dual `CollectTimeoutVotes` from `session_test.go`.

| Test | Assert |
|---|---|
| `TestQueueExpired_LogsInflightSnapshot` | First collection holds VerifyTimeout; second expires; `snapshot` contains first’s escrow/nonce; expire log fields include `inflight=1` and the holder nonce |
| `TestTimeoutVoteSent_OnlyAfterAcquire` | No `timeout_vote_sent` when wait expires |
| `TestVerifyTimeout_SlowLog` | Mock sleeps 16ms with `VerifyTimeoutSlowLog` overridden to 10ms; slow stage fires on accept |
| `TestVerifyTimeout_RPCTimeoutLog` | Mock returns `context.DeadlineExceeded` after “send”; `timeout_vote_rpc_timeout`; not `queue_expired` |
| `TestClassifyVoteError_QueueExpired` | acquire deadline → `queue_expired`; post-send deadline → `rpc_timeout` |
| Error-miss | expire path logs `timeout_vote_queue_expired` (today silent) |

Capture stages with a test logger, or assert `snapshot()` directly plus a `bytes.Buffer` on `log.SetOutput` if Stage still uses `log.Print`.

---

## Out of scope

- Raising `MaxConcurrentVerifierRPCs` further (default is 10)
- Metrics / gauges (`verifier_queue_inflight_age_ms`)
- Host-side `HandleVerifyTimeout` logs
- Changing `VerifierQueueWaitTimeout` or the 3m client `VerifyTimeout`

---

## Implementation order

1. Occupancy on `verifierHostQueue` + unit tests for snapshot/begin/end.
2. `timeout_vote_sent` + inflight begin/end in both collect paths.
3. Enrich `timeout_vote_queue_expired` (and add it on error-miss).
4. `timeout_vote_rpc_timeout` + `timeout_vote_slow` after the RPC.
5. `classifyVoteError` split + `error_classes` on tally.
6. Tests in the table above.

Each step is greppable on its own in a gateway dump.
