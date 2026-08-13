# Proposal: Challenge path must accept, not only ping

**Status:** Draft / proposal  
**Suggested branch:** `fix/challenge-confirm-start`  
**Related:** [PR #1460](https://github.com/gonka-ai/gonka/pull/1460) (receipt RPC must not wait on ML), [issue #1466](https://github.com/gonka-ai/gonka/issues/1466) item 1 (accept-path divergence), [`attacks.md`](../attacks.md) “Executor refuses to work”

This is a design note only; it does not change code by itself.

---

## Why

Every inference has two on-chain clocks:

1. **Accept** — `MsgConfirmStart` (signed executor receipt) moves Pending → Started.
2. **Finish** — `MsgFinishInference` is legal only from Started.

Settlement treats those states differently. Pending is refunded as “never accepted.” Started is paid as “the host committed” (actual cost if Finish landed, reserved cost if it did not).

The user path does this correctly. `signReceipt` puts `MsgConfirmStart` in the host mempool, returns the receipt, then runs ML. The gateway later sequences ConfirmStart, then Finish. Accounting works.

The timeout-challenge path is supposed to be the same accept, triggered by a verifier instead of the user: “you’re alive, here is the prompt, take the job and compute it.” What it does today is **prove liveness without accepting**.

`ChallengeReceipt` signs a receipt and starts ML. `VerifyRefusedTimeout` only checks that the receipt is non-empty, then throws it away. Nobody turns that signature into `MsgConfirmStart`. The Finish that later appears is a finish for a job that is still Pending. The sequencer tries to include it; the state machine rejects it (`expected started, got pending`); `PreviewLocalBestEffort` drops non-start failures. At settlement the record is still Pending, so the user is refunded in full and the executor is not paid.

The host did the work. The protocol never recorded that it accepted the work.

That is also why execution-timeout cannot take over after a successful challenge: `ConfirmedAt` is written only by `applyConfirmStart`. Until ConfirmStart is sequenced, the inference cannot become Started, cannot be finished, and cannot be execution-timed-out. The only remaining outcome is the Pending refund.

[#1460](https://github.com/gonka-ai/gonka/pull/1460) does not fix this. It only returns the ping before ML finishes, so a live host is not voted unreachable. After that PR the executor is *more* likely to finish work the chain will still treat as never accepted. The two changes are complementary; they must not be bundled.

---

## What is not the problem

Do **not** unify `signReceipt` and `challengeReceiptLocked` into one “accept as executor” helper.

Those functions no longer do the same job. The user path serves a request (cache replay, live attach, payload-store resume, hard `VerifyPayload`, observability). The challenge path proves liveness for a still-Pending inference (soft-fail payload, no stream, no cache tiers). Folding them together needs a pile of behavior flags and hides the remaining divergences. The v4 attempt at that helper was correctly dropped from #1460.

The one block that must not stay duplicated is the receipt → `MsgConfirmStart` step. That is the commitment. Everything else can stay on its own path.

---

## Desired behavior

When a challenge produces a receipt:

1. The same `MsgConfirmStart` the user path would have queued is in the executor mempool **before** ML starts (or is skipped as already in-flight / already finished).
2. Repeat challenges from several verifiers reuse that mempool entry. They must not mint a new `ConfirmedAt` each time.
3. The next time the sequencer sees this host’s mempool (later `HandleRequest`, catch-up, or finalize sync), ConfirmStart is absorbed first, then Finish (`txPriority` already orders them that way).
4. Apply succeeds: Pending → Started → Finished. The executor is paid actual cost. Validators can see the work.
5. If Finish never lands but ConfirmStart did, settlement pays reserved cost — the host committed.

The challenge HTTP response stays a receipt ping. The verifier is not the sequencer and must not grow a mempool in `ChallengeReceiptResponse`. Survival is the same as the user path: mempool outlives the RPC; the next contact with this host delivers the txs.

---

## How

### 1. One helper: receipt → mempool ConfirmStart

On current v5 this block is inlined in `signReceipt` (`host.go`: marshal `ExecutorReceiptContent`, sign, `mempool.Add` `MsgConfirmStart`). Extract it (name it `ensureConfirmStartLocked` if that helper is not already on the branch):

- Caller holds `h.mu`.
- If mempool already has `MsgConfirmStart` for this inference, return that `ExecutorSig` / `ConfirmedAt`.
- Else sign once and queue it, `ProposedAt = h.sm.LatestNonce()`.

`signReceipt` calls it instead of signing inline. `challengeReceiptLocked` calls it instead of its copy of marshal-and-sign.

Challenge then becomes: apply diffs → still Pending, we are the executor, payload verifies → **ensure ConfirmStart** → if already executing or Finish already in mempool, return the receipt and skip a second job → else mark `executing` and return the job.

That is the whole production change for the accounting bug.

### 2. Tests that pin the protocol, not the helper

Minimum:

- After `ChallengeReceipt` on a fresh Pending inference, mempool contains `MsgConfirmStart` for that id (and the returned receipt matches it).
- A second `ChallengeReceipt` does not add a second ConfirmStart and does not change `ConfirmedAt`.
- After challenge + `RunExecution`, mempool has ConfirmStart and Finish. Applying them in `txPriority` order yields `StatusFinished` with non-zero `ActualCost`.
- Applying Finish alone (no ConfirmStart) still fails — so the test would have failed on today’s code.

Keep #1460’s “receipt returns while Execute is in flight” test on that PR. It does not belong here.

### 3. Small adjacent gaps (same PR if cheap, else follow-ups)

These are not the accounting bug, but they sit on the same handler:

- **`CompletionRequestsEnabled`.** `HandleRequest` and `HandleVerifyTimeout` refuse new work when requests are disabled. `HandleChallengeReceipt` does not, so a draining host can still start ML from a challenge. Gate it the same way.
- **Observability.** Challenge has no `receiptOutcome`. A soft-fail `VerifyPayload` is a silent nil. At least log/count payload mismatch and “queued ConfirmStart / skipped execute” so a challenge that does real work is visible.

Do not expand tx gossip to ConfirmStart in this PR. The user path already relies on mempool return + later host contact; match that first. Gossip of ConfirmStart is a later HA concern if finalize sync is not enough.

---

## Out of scope

- Returning the challenge receipt without waiting on ML (#1460). Merge that separately.
- Same-nonce reconnect, LiveStream, payload-store resume, cross-`devshardd` HA (#1466 items 2–4, #1574).
- Changing `VerifyRefusedTimeout` to sequence txs. Verifiers vote; the user sequences.
- Putting mempool txs on `ChallengeReceiptResponse`.
- Unifying the rest of `signReceipt` / `challengeReceiptLocked`.
- `Missed++` on Pending settlement. That remains “state cannot distinguish user censorship from host absence.” After this fix the challenge path is no longer Pending, so that default is no longer the outcome.

---

## Acceptance

- A live executor that answers a refused-timeout challenge and completes ML is paid, not refunded as if it never accepted.
- ConfirmStart is in the executor mempool as soon as the challenge receipt is signed, independent of whether the RPC waits on ML.
- Repeat challenges are idempotent on `(ExecutorSig, ConfirmedAt)`.
- User SSE path behavior is unchanged (same helper, same mempool entry).
- No session stall: orphan Finish without ConfirmStart is a pre-existing apply skip, not a new withhold. After this change that Finish is no longer orphaned.
