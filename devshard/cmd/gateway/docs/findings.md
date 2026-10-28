# Defects outside the gateway

Seven defects the gateway runs into and cannot fix inside `cmd/gateway`. The first four and the sixth are closed, and the fifth and seventh are patched locally pending upstream; each entry states the rule that closes it and where that rule lives.

---

## 1. The execution timeouts nobody retried — closed

**The failure.** A nonce a host receipted and never finished owes an execution timeout. Voted only by `settleSteps` at the end of the race that owns it ([`engine/engine.go`](../engine/engine.go)), a round that finds no verifiers leaves the nonce owned by nobody, because no other component knows it exists.

The cost is money, not observability. In `settleLiveRecordLocked` ([`state/machine.go`](../../../state/machine.go)) a `StatusStarted` record settles as `ActualCost = ReservedCost`, credited to the executor slot: the escrow pays in full for an answer it never received, and the unposted vote also spares that host the `Missed` it earned.

**The rule.** Every escrow tick scans its own live records for ones started and past their execution deadline by a grace, counted from the executor's stamp or, when it stamped none (`ConfirmedAt` not positive), from the start, and re-votes them through the same `HandleTimeout`: [`state/started_deadline.go`](../../../state/started_deadline.go) scans, [`user/timeout_sweep.go`](../../../user/timeout_sweep.go) votes, [`registry/timeout_sweep.go`](../registry/timeout_sweep.go) walks the published and draining escrows and sweeps an escrow once more before it finalizes, and [`escrow/manager.go`](../escrow/manager.go) drives it off the tick. See [`race.md`](./race.md), "The swept vote and the retried vote", for the three properties that keep it off the hot path.

**Why it is sound.** `applyTimeout` ([`state/machine.go`](../../../state/machine.go)) has no wall-clock bound: it requires only that the record is still live and that its status matches the reason. `sealEligibleStatus` ([`state/seal.go`](../../../state/seal.go)) admits only `Finished`, `Validated`, `Invalidated` and `TimedOut`, so a `StatusStarted` record never auto-seals and stays settleable until the escrow itself settles. The retry needs nothing carried over from the original request: `VerifyExecutionTimeout` ([`host/timeout.go`](../../../host/timeout.go)) decides from the verifier's own state and the executor, and takes no payload. A sweep cannot collide with a live race either: the execution deadline is `ConfirmedAt + ExecutionTimeout` (32 minutes), or `StartedAt + ExecutionTimeout` without a stamp, and no attempt outlives `streamingHardTimeout` (20 minutes), and the configured grace is added on top of that.

**Scope note.** `StatusPending` is excluded. `settleLiveRecordLocked` settles it like `StatusStarted`, at its full reserve credited to the executor, and declines to increment `Missed`, because state cannot distinguish user censorship from host absence; only a refused timeout refunds it, and that counts a `Missed`. Sweeping it would assign blame the protocol chose not to assign, so the gateway sends every nonce it commits instead of leaving one pending ([`race.md`](./race.md), "Client departure and the drain").

---

## 2. The confirm stamp read from the cache — closed

**The failure.** A `Session.TimeoutDeadline` that reads `confirmedAt` only from the in-memory `nonceStates` map loses it across a restart. That map is written when a nonce is committed and when a response arrives, and it is empty after a restart, while the committed record carries `ConfirmedAt` and survives.

With an empty map a nonce a host already receipted yields reason `refused`, and `applyTimeout` rejects a refused timeout against such a record — `reason=refused requires pending`. The vote is not merely missed across a restart: it cannot be posted at all, and the nonce is guaranteed to settle at full reserve.

**The rule.** The committed record is the authority for the stamp and the map is a cache of it ([`user/session.go`](../../../user/session.go), `TimeoutDeadline`). A record without the stamp is still named `refused` by `TimeoutDeadline`, by design; the vote gate (`reasonTheGroupWillAccept`) switches it to an execution vote once its start-anchored deadline has passed.

**Why it is sound.** `ConfirmedAt` is written in the same transition that sets `StatusStarted` ([`state/machine.go`](../../../state/machine.go)), from the executor's signed receipt. A receipt that carries a zero stamp leaves `ConfirmedAt` at zero; the sweep and the session's vote gate then anchor that record on its `StartedAt`. Verifiers already accept such a timeout, since `VerifyExecutionTimeout` measures from the zero stamp and sees it as long past; the user side's start anchor is what keeps the vote off a still-running inference.

---

## 3. The unapplied timeout unnamed at its source — closed

**The failure.** `HandleTimeout` has a path where the votes suffice and the diff is sent, but the transaction does not land. There `result.Applied` is false, and an unwrapped error has the same shape a settled vote returns, so a caller separating the two by error shape reads the unsettled nonce as settled.

**The rule.** That return is wrapped in `ErrTimeoutNotApplied`, the sentinel the insufficient-votes path already uses ([`user/timeout_effect.go`](../../../user/timeout_effect.go), `timeoutSettledError`). The gateway itself never depends on the error shape — `SettleTimeout` reads `result.Applied`, which is the fact — so the sentinel corrects the reason label rather than a miscount.

**Verification note.** The path is not reachable cheaply in a test: it requires `sendPendingDiff` to succeed while returning a diff that carries no timeout transaction for that nonce. The decision it feeds is pinned directly instead ([`user/timeout_effect_test.go`](../../../user/timeout_effect_test.go)).

---

## 4. The SSE event cap below the body a host may send — closed

**The failure.** A transport cap of 1 MiB on one SSE event ([`transport/client.go`](../../../transport/client.go), `DefaultMaxSSEEventBytes`) is below what a host may send. Every request runs with logprobs, `top_logprobs` and token ids, and a host that returns them and does not stream writes its whole answer as one event, so a few thousand tokens pass such a cap. The attempt is aborted as `response_too_large` after its nonce is committed, the honest host takes the perf sample, and the attempt's answer is lost.

**The rule.** The cap is `MaxJSONResponseBytes` (16 MiB), the largest body the JSON path already reads. See [`race.md`](./race.md), "Classification and reassembly", for why a complete event never reaches the attempt budget.

**Why it is sound.** No bound grew: the JSON path already buffers a body of that size ([`transport/client.go`](../../../transport/client.go), `readBoundedResponseBody`), and the carry budget charges only an unterminated tail, which the transport never hands over, because `writeSSELine` writes an event and its terminator in one write.

---

## 5. The router-catalog probe dialed private addresses — patched pending upstream

**The failure.** `Session.WaitRouterCatalog` ([`user/catalog.go`](../../../user/catalog.go)) probes each host's public base URL. That URL is the host's on-chain `InferenceUrl`, and the registration check rejects only literal private IPs, so through a plain `http.Client` a host name resolving to `127.0.0.1`, `169.254.169.254` or an RFC1918 address sends the gateway's warmup probe into its own network. The gateway cannot wrap the client: it is built inside the call.

**The rule.** The probe goes through a package-level client carrying the `common/httpguard` dial-time guard. The patch is upstream's [gonka-ai/gonka#1853](https://github.com/gonka-ai/gonka/pull/1853) byte for byte, with its test [`user/catalog_ssrf_test.go`](../../../user/catalog_ssrf_test.go), so the local copy drops out when that PR lands.

**Why it is sound.** `DialControl` checks the resolved address on every dial, redirect hops included, and `DEVSHARD_ALLOW_PRIVATE_ADDRESSES` keeps compose and e2e stands, whose hosts resolve to Docker-internal addresses, working.

---

## 6. Every composed diff copied the live window three times — closed

**The failure.** `StateMachine.PreviewLocalBestEffort` ([`state/machine.go`](../../../state/machine.go)) composes every diff the gateway sends, heartbeat turns included. Without the hand-over it deep-copies the mutable state before the trial apply, `localBestEffortLocked` copies it again for its own rollback, and the preview copies it a third time to hand back the post-state. Each copy walks every live inference and every nonce the escrow ever sealed, so the cost grows with the escrow's age. A profile under production load showed 32% of CPU in those copies. The gateway cannot wrap it: the copies happen under the state machine's own lock.

**The rule.** The state machine no longer copies the state to apply a diff: an apply mutates the live maps in place under a mutation journal that saves only what each touched key held, and a trial apply detaches the journal, recording the post value of every touched key, and then undoes ([`state/journal.go`](../../../state/journal.go), `mutationJournal` and `detachJournalLocked`; `state/machine.go`, `localBestEffortJournaled`). Untouched records, committed blobs and sealed nonces are never copied, so a preview's cost follows the diff rather than the escrow's age.

**Why it is sound.** Nothing about the root changes: the same transactions are applied to the same state. The post-state is reachable only through the returned handle, because the apply is undone before the lock is released; `TestAPreviewLeavesTheLiveStateUntouchedUntilItsHandleIsCommitted` and `TestADiscardedPreviewLeavesNothingBehindForTheNextApply` ([`state/preview_isolation_test.go`](../../../state/preview_isolation_test.go)) fail if the pre-state is ever shared.

---

## 7. The auto-seal log wrote out the whole live window — patched pending upstream

**The failure.** A seal pass ([`state/seal.go`](../../../state/seal.go), `autoSealLocked`) runs once per 150 nonces of every escrow on the gateway and on every host. Building a record of each live inference, sorting it and logging it as JSON at Info costs, on a 20 000-record window, 24 ms, 29 MB and a 3.6 MB log line per pass; one participant's host logged 327 MB of these lines in four hours, 60% of its log.

**The rule.** The pass counts instead of listing: `candidates_count`, `sealed_count` and `live_inferences_count`, with the window's earliest and latest `ConfirmedAt` when it has any (`state/seal.go`, the `auto-seal evaluation` line). The same pass costs 1.4 ms and 166 KB (`BenchmarkAutoSealEvaluationOfAWindowNotYetDue`).

**Why it is sound.** Nothing but the log reads such a list. Which inferences are sealed is decided by the same nonce gate, terminal-status shortcut and clock gate whether the pass lists or counts, and the eligible ids are sorted before they fold into the sealed accumulator.

---

## Known limitations

These are not defects the gateway can fix inside `cmd/gateway`; each is recorded so a reader does not rediscover it.

- Abandoned `Pending` reservations are still paid to the host at finalize: `Finalize` pays every open record in full, so an attempt the gateway gave up on is charged when the escrow settles; the timeout sweep cannot recover it, because hosts reject a refused vote that carries no payload (`devshard/host/timeout.go`).
- An over-context prompt costs the refusing host's attempt a reservation for the whole execution timeout and gives that honest host a miss. The host relays vLLM's 400 to the gateway as a stream event, and the race stops at a trusted refusal unless the host runs shorter than the model ([race.md](./race.md), "Escalation"), so one attempt is held, not one per host. The hold and the miss are a host defect: the engine error comes after the receipt is signed and ends in `FailReceiptOrphan` with no Finish over the refusal (`devshard/host/host.go`, `RunExecution`), so only an execution-timeout vote settles the record. The scenarios pin the miss so a host fix flips it.
- A node that answers a frozen height with a fresh reply is not detected: only a chain API that stops answering is projected (`escrow deadlines read past a stale chain height`), and the projection moves the height, not the epoch.
- A row recovered from a commitment, or any row with no live session, counts as full only when its stored `amount` covers the model's slot; a row whose amount never resolved is left out of the full count and can make the guard create one escrow too many.
- A host that ends a challenge without testing the vote threshold decides whether an escrow with a challenged record stays stuck or is invalidated; the gateway handles either outcome and the scenario records which one ran.
- The four end-to-end anchors on the docker stand are not written yet.
- A13's measured attempt bound is logged, not enforced.

