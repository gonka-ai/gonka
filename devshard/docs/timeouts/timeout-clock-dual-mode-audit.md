# Timeout clock dual-mode audit (B1–B6)

**Plan:** [timeout-clock-dual-mode-plan.md](timeout-clock-dual-mode-plan.md)
**Scope:** Track B steps B1–B6 on this branch, rechecked after the legacy clock was put back on the `devshard-0.2.x-v6` wall rules.
**Status:** Closed. Implementation-review findings 1–6 and 8–12 are fixed. Finding 7 is accepted. Docker e2e still needs `DEVSHARD_E2E=1`. Citest still needs `TESTENV_CITEST=1`.

Acting clocks:

- Legacy (`HeightsyncTimeoutClock == false`): gateway refusal ages `sendTime` (a zero `sendTime` uses `StartedAt`) plus `TimeoutBuffer`. Execution ages the queued confirm, then `ConfirmedAt`, plus the same buffer. Due when now is not strictly before the deadline. Verifier `awaitVerifierLegacyDue` uses `Due` with no buffer; a signed anchor of 0 is the Unix epoch. The stamp sample is `CompareLegacyShadow`.
- Header (`HeightsyncTimeoutClock == true`): gateway `refusalDeadlineLocked` / `executionDeadlineLocked` and verifier `awaitVerifierStampDue`. Wall time does not open the challenge.
- Seal and settlement drain stay on carried stamp time against the floor. They do not follow `HeightsyncTimeoutClock`.

The acting deadline is `headertime.Due`. The legacy sample is `CompareLegacyShadow`.

## Summary

| # | Severity | Issue | Status |
|---|---|---|---|
| [1](#issue-1) | High | [Gateway legacy execution opened on a clock the verifier does not have](#issue-1) | Fixed |
| [2](#issue-2) | High | [One mismatch flag covers both timeout reasons](#issue-2) | Fixed |
| [3](#issue-3) | Medium | [Verifier mismatch increments on every call](#issue-3) | Fixed |
| [4](#issue-4) | Medium | [Legacy shadow due is not the deadline that opens](#issue-4) | Fixed |
| [5](#issue-5) | High | [Legacy restart with a zero send time never opened](#issue-5) | Fixed |
| [6](#issue-6) | Medium | [Bulk verifier tests force header mode](#issue-6) | Fixed |
| [7](#issue-7) | Medium | [B7 debug snapshot and B8 dual-run are still open](#issue-7) | Fixed |
| [8](#issue-8) | Low | [Legacy deadline polls read the header map](#issue-8) | Fixed |
| [9](#issue-9) | High | [Legacy dropped-receipt fallback cannot vote](#issue-9) | Fixed |

---

## Issue 1

[↑ Summary](#summary)

**High.** Gateway legacy execution opened on a clock the verifier does not have. **Fixed.**

`TimeoutDeadline` returns `legacyTimeoutDeadlineLocked` when the flag is false. That function does not write `time.Now()` into `outcome.confirmedAt`. A queued receipt's `confirmed_at` wins, then a started record's `ConfirmedAt`. Both add `TimeoutBuffer` through `legacyWallDeadline`. `acceptConfirmStartLocked` rejects `ConfirmedAt <= 0` on the legacy path.

The verifier uses `now.Unix() - rec.ConfirmedAt >= ExecutionTimeout` and `now.Unix() - rec.StartedAt >= RefusalTimeout`. It does not add `TimeoutBuffer`. A zero anchor is due. The gateway's extra buffer is the v6 split: verifiers pass their own deadline before the proxy fires.

Header mode still accepts a stamp-only receipt and still follows `Decide`.

---

## Issue 2

[↑ Summary](#summary)

**High.** One mismatch flag covers both timeout reasons. **Fixed.**

`claimClockMismatchLocked` keys `clockMismatchOnce` by nonce and kind (`"refused"` or `"execution"`). A refusal sample does not suppress the execution warn or `devshard_timeout_clock_mismatch_total{kind="execution"}`. A repeat of the same pair does not increment. The map is used even when `nonceStates` has no entry, and sealed nonces drop their keys with the outcome retention pass.

---

## Issue 3

[↑ Summary](#summary)

**Medium.** Verifier mismatch increments on every call. **Fixed.**

`awaitVerifierLegacyDue` records only when `claimVerifierClockMismatch` returns true. `Host.claimClockMismatch` keeps one mark per inference and reason on `answerHolds`. A retry of that pair does not increment or warn. The other reason still counts. `forgetInference` drops both marks when the record leaves the live set. Evidence that does not implement the guard still records every call.

---

## Issue 4

[↑ Summary](#summary)

**Medium.** Legacy shadow due is not the deadline that opens. **Fixed.**

`observeLegacyClockShadow` sets wall due from the same instant the vote uses: `!now.Before(anchor + legacyTimeoutDuration)`. That duration is the timeout plus `TimeoutBuffer`, and the anchor keeps `sendTime`'s sub-second part. The stamp uses that same duration from its millisecond time.

A zero `sendTime`, or a started record whose `ConfirmedAt` is zero, is not compared. There is no wall instant to put next to the stamp, so a restart does not log a mismatch by itself.

A stamp that is the same instant as the wall anchor does not increment while the buffered deadline is still ahead, and does not increment once both are due. A stamp that is actually not due while the deadline is past still counts once.

---

## Issue 5

[↑ Summary](#summary)

**High.** Legacy restart with a zero send time never opened. **Fixed**, to the v6 rule.

`user/completion.go` and the execution sweep call `HandleTimeout` with a zero `sendTime`. On the legacy clock a pending record's deadline is that zero time plus `RefusalTimeout` and `TimeoutBuffer`, so the refusal is already due. A started record uses `ConfirmedAt`, including zero, which is also already due. The verifier treats a zero anchor as due as well.

Header mode still restores the trigger from the header map. A map miss stays closed.

---

## Issue 6

[↑ Summary](#summary)

**Medium.** Bulk verifier tests force header mode. **Fixed.**

`dueEvidence.HeightsyncTimeoutClock()` follows `evidenceHeader` (`host/timeout_test.go`). The zero value is legacy, so a direct `VerifyRefusedTimeout` / `VerifyExecutionTimeout` run takes `awaitVerifierLegacyDue`. `TestVerifyTimeoutSuite_HeaderClock` sets the flag and runs the same fixtures through `awaitVerifierStampDue`. A not-due fixture sets a fresh `StartedAt` or `ConfirmedAt`, so the legacy integer check stays inside the window while the header stamp stays an AnswerWait ahead.

Legacy verifier behaviour is `host/timeout_clock_mode_test.go`:

- `TestLegacyVerifierOpensOnWallDespiteStampMiss`
- `TestLegacyVerifierDoesNotOpenWhenWallNotDue`
- `TestLegacyVerifierExecutionOpensOnWallConfirmedAt`
- `TestLegacyVerifierDuplicateChallengeMemoryUnchanged`

Header verifier behaviour in that file:

- `TestHeaderVerifierStampMissStillWaitsAnswerWait`
- `TestHeaderVerifierExecutionIgnoresWallAlone`

Gateway tests that run both clocks:

- `user/timeout_clock_mode_test.go`
- `TestHandleTimeout_DroppedReceiptFallsBackToRefusalVote` (legacy with `confirmed_at` unset; header with a stale `confirmed_at` and no queued confirm)
- `TestTimeoutDeadlineDoesNotOverflowWhenRemainingDurationIsSaturated`

Stamp-era deadline tests (`user/timeout_deadline_test.go`, `user/execution_confirm_height_test.go`, parts of `host/timeout_header_time_test.go`) set `HeightsyncTimeoutClock = true`. `TestHandleTimeout_DroppedReceiptFallsBackToRefusalVote` runs both clocks with a remembered `confirmed_at` and no queued confirm.

Acting-clock tests live in `chainoracle/blocks/headertime/protocol_due_test.go`. They pin `Due`: the deadline instant is due, a missing gateway anchor is unarmed, and a verifier wall of Unix 0 is the epoch.

---

## Issue 7

[↑ Summary](#summary)

**Medium.** B7 debug snapshot and B8 dual-run are fixed.

`devshard_timeout_clock_mismatch_total{side,kind}` and the structured warn live in `headertime.RecordClockMismatch`. That call also keeps the last sample. `GET /v1/debug/timeout-clock` returns `heightsync_timeout_clock` for one escrow, `mismatch_total`, and `last_mismatch` (`escrow`, `nonce`, `reason`, `wall_due`, `stamp_due`, `wall_anchor`, `stamp_ms`). Several escrows keep the flag on each entry and omit the process-wide mode.

Gateway and verifier tests force wall due against a stamp that is not due, check the labeled counter and the warn fields, and check that agreeing clocks and header mode do not warn. `TestDebugTimeoutClockAfterMismatch` reads the debug JSON.

B8 writes `heightsync_timeout_clock` on the mock-chain params and the seed escrow (YAML, create, and `MOCK_CHAIN_HEIGHTSYNC_TIMEOUT_CLOCK`). `SessionConfigAtBind` reads that escrow. `TestE2E_TimeoutProtocolEndToEnd`, `TestE2E_ExecutionTimeoutProtocolEndToEnd`, and `TestTimeoutEvidence` each run `legacy` and `header`. The header wall-past / stamp-not-due case and the legacy mismatch counter stay in the gateway and verifier clock tests, which do not need Docker.

---

## Issue 8

[↑ Summary](#summary)

**Low.** Legacy deadline polls read the header map under the session lock. **Fixed.**

`legacyTimeoutDeadlineLocked` copies the wall anchor, height, hash, and any carried `ConfirmedAtTimeMs` under `s.mu`, then returns. `observeLegacyClockShadow` calls `StoredAt` after that lock is released. The session lock is taken again only to record a new mismatch.

The map is read once the wall arm is due. That sample is remembered, including when the clocks agree and when the stamp is missing, so the next poll does not call `StoredAt`. `TestLegacyShadowReadsTheMapOnlyWhenWallIsDue` checks both.

`refusalDeadlineLocked` and `executionDeadlineLocked` are documented as the header-mode triggers. `TimeoutDeadline` calls them only when `HeightsyncTimeoutClock` is set.

---

## Issue 9

[↑ Summary](#summary)

**High.** Legacy dropped-receipt fallback cannot vote. **Fixed.**

`legacyTimeoutDeadlineLocked` keeps `outcome.confirmedAt` as the execution anchor only while that `ConfirmStart` is still queued or the record is `Started`. A `Pending` record with no queued confirm clears the stored time and uses the refusal arm (`sendTime + RefusalTimeout + TimeoutBuffer`). `inferenceConfirmed` then follows that refusal, and `VoteTimeout` collects the refusal vote.

A confirm that is still queued keeps the execution deadline. Header mode is unchanged: its deadline stays on refusal unless the record is started or a confirm is queued. `TestHandleTimeout_DroppedReceiptFallsBackToRefusalVote` covers both clocks with a remembered `confirmed_at` and no queued confirm.
