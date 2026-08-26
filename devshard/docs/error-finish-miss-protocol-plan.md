# Implementation: account host/ML-node inference errors as a miss

Protocol design: [proposals/error-finish-miss.md](proposals/error-finish-miss.md)

Steps 1–10 land in the same binary — a session cannot mix protocol names, so
there is nothing to stage across releases. Build under a release-candidate name
and exercise with `VERSIOND_FORCE`; step 11 promotes that name into
`approved_versions`. Until the proposal passes, the code is unreachable in
production, which is the gate.

Mark a box when the step is done, including its tests.

## 1. Proto

- [ ] Add `TIMEOUT_REASON_ERROR = 3` to `TimeoutReason` in `proto/devshard/v1/tx.proto`. Comment: evidence-backed and immediate; verifiers check the executor-signed response hash, not an elapsed deadline.
- [ ] Add `bytes response_hash = 5` to `TimeoutVoteContent` in `proto/devshard/v1/diff.proto` (set for `reason=ERROR` only; empty otherwise).
- [ ] Regenerate `types/*.pb.go`.

## 2. Shared classifier (the consensus rule)

Files: `common/completionapi`

Every host must run one implementation: this predicate, over hash-pinned bytes,
is what verifiers agree on. Token counts are never the criterion.

- [ ] Add `IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool)`.
- [ ] Accept only when all hold, parsed via `NewCompletionResponseFromLinesFromResponsePayload`:
  - some event carries a top-level `error` object (`{"error":{code,message,type}}`, matching `sseChunkErrorPayload`)
  - no content anywhere (`delta.content`, `delta.reasoning_content`, `delta.tool_calls`, `message.content`, `choice.text`)
  - `usage.completion_tokens == 0`
- [ ] Tests:
  - [ ] Exact EngineCore payload from the report → `ok == true`
  - [ ] Deterministic 4xx (`BadRequestError`) → `ok == true`
  - [ ] Real content followed by a trailing error event → `ok == false`
  - [ ] Error event with `completion_tokens > 0` → `ok == false`
  - [ ] Empty stream (role + `[DONE]`, no error object) → `ok == false`

## 3. State machine: apply + unwind + hash-bound votes

Files: `state/machine.go`, `state/machine_test.go`

- [ ] `applyTimeout` reason/status gate: `TIMEOUT_REASON_ERROR` requires `StatusFinished`.
- [ ] Unwind Finish accounting on `ERROR`: `Balance += ActualCost`, `HostStats.Cost -= ActualCost` (saturate at 0), then `StatusTimedOut` and `Missed++`. Do **not** add `ReservedCost` (surplus already released at Finish).
- [ ] Reconstruct `TimeoutVoteContent.ResponseHash` from `rec.ResponseHash` when `reason=ERROR`.
- [ ] Tests:
  - [ ] Finish then `ERROR` timeout in one diff → `StatusTimedOut`, `Missed == 1`, `HostStats.Cost` back to pre-finish, balance restored by full `ReservedCost`
  - [ ] `ERROR` timeout against `StatusStarted` / `StatusPending` → `ErrInvalidTimeoutReason`
  - [ ] `ERROR` timeout with `acceptCount <= threshold` → `ErrInsufficientVotes`, no mutation
  - [ ] Wrong-order diff (Timeout before Finish) → rejected, state unchanged
  - [ ] Post-`ERROR` `MsgValidation` / `MsgValidationVote` → rejected or no-op
  - [ ] Vote signed over a different `response_hash` than `rec.ResponseHash` → `ErrInvalidVoteSig`
  - [ ] Vote harvested from inference A cannot be applied to inference B
  - [ ] `REFUSED` / `EXECUTION` vote contents marshal byte-identically to pre-change (empty `response_hash` omitted, not encoded as zero-length)
  - [ ] Golden state-root vectors still pass byte-for-byte

## 4. Signature helpers

Files: `state/machine.go`, `transport/server.go`

- [ ] Export `StateMachine.VerifyFinishProposerSig(msg)` (same logic as `applyFinishInference`).
- [ ] Unify `signTimeoutVote` onto `deterministicMarshal.Marshal` (today it uses plain `proto.Marshal`; `applyTimeout` already uses deterministic).

## 5. Verifier: `VerifyErrorTimeout`

Files: `host/timeout.go`

Local computation only. Signature takes no `ctx`, no `ExecutorClient`, no payload
fetcher, no `SessionConfig`, no clock. Gossip is disabled, so `finish_tx` and
`response_payload` from the request are the evidence.

- [ ] Implement `VerifyErrorTimeout(st, inferenceID, finishTx, responsePayload, localMempool)`.
- [ ] No deadline check — do not read `RefusalTimeout`, `ExecutionTimeout`, or `TimeoutBuffer`.
- [ ] No outbound call — no `ExecutorClient.GetMempool`, no payload fetch.
- [ ] Steps, all failures → `accept=false`:
  1. Decode the Finish from `finishTx` (prefer an applied record or local copy when present, but do not require one); reject if no Finish from any source
  2. `msg.InferenceId`, `msg.EscrowId`, `msg.ExecutorSlot` match the record; record is `StatusStarted` or `StatusFinished`; if `StatusFinished`, require `msg.ResponseHash == rec.ResponseHash`
  3. Verify executor `ProposerSig`
  4. `sha256(responsePayload) == msg.ResponseHash` — this is what makes gateway delivery safe
  5. `IsTerminalErrorResponse(responsePayload)` — the verdict. Do not test token counts.
- [ ] Tests:
  - [ ] No-wait guard: `ConfirmedAt` is "now" (inside both timeouts) still yields `accept`
  - [ ] No-network guard: nil `ExecutorClient`, empty local mempool, payload fetcher that fails the test if called → still `accept`
  - [ ] Valid Finish sig + payload hash match + error body, `StatusStarted` record → `accept`, vote signed over `ResponseHash`
  - [ ] Lying host: Finish reports `OutputTokens = 7`, pinned body is an error envelope → `accept`
  - [ ] Body with real content plus trailing error event → reject, even with `OutputTokens == 0`
  - [ ] `sha256(response_payload) != msg.ResponseHash` → reject
  - [ ] `response_payload` absent or empty → reject
  - [ ] Tampered `finish_tx` (non-executor signer, or fields edited after signing) → reject
  - [ ] `finish_tx` absent or undecodable → reject
  - [ ] `finish_tx` naming a different inference or escrow → reject
  - [ ] Applied `StatusFinished` record with `msg.ResponseHash != rec.ResponseHash` → reject
  - [ ] Round-trip determinism: gateway-rebuilt payload for the EngineCore case hashes equal to the executor's `ResponseHash`

## 6. Transport: verify request + vote signing

Files: `transport/server.go`, `transport/types.go`, `transport/client.go`

- [ ] `HandleVerifyTimeout` `ERROR` case calling `host.VerifyErrorTimeout`. Do not thread `executorClient` or a payload fetcher into it.
- [ ] `VerifyTimeoutRequest.FinishTx []byte` (`json:"finish_tx,omitempty"`) — required for `reason=ERROR`.
- [ ] `VerifyTimeoutRequest.ResponsePayload []byte` (`json:"response_payload,omitempty"`) — required for `reason=ERROR`; authenticated by `sha256 == ResponseHash`, never trusted directly.
- [ ] `signTimeoutVote` takes `responseHash`; populated for `reason=ERROR` with the hash this verifier fetched and classified.

## 7. Gateway session: emit path

Files: `user/session.go`

- [ ] `HandleTimeout` `ERROR` branch at the top, before deadline logic. No early-return on pending Finish. No `sleepUntilDeadlineWithHeartbeat`.
- [ ] Same-diff composition: keep Finish in `pendingTxs`, then `AddPendingTimeoutTx(nonce, ERROR, votes)`, one `SendPendingDiff`.
- [ ] `CollectTimeoutVotes` / `TimeoutVerifier.VerifyTimeout` forward both artifacts — required. `finish_tx` from `inf.resp.Mempool` (the `devshard_meta` tail) or `pendingTxs`; `response_payload` as `json.Marshal(SerializedStreamedResponse{Events: lines})` over the host's verbatim lines. Without either, a vote round collects zero accepts.
- [ ] Clear `nonceStates[n].finished` when the `ERROR` timeout applies so local `IsNonceFinished` matches chain state.

## 8. Gateway redundancy: let the path run + kill switch

Files: `cmd/devshardctl/redundancy.go`

- [ ] `shouldRunHandleTimeout`: run when `isErrorStreamAttempt(inf)` even if the nonce finished.
- [ ] Retain the attempt's full `data:` line set when `errorSource` is first set — verbatim as the host emitted them (post id-injection, pre `rewriteStreamingPayload`), excluding `devshard_receipt` / `devshard_meta`. `inf.errorBodySample` is capped by `bodySampleForLog` and cannot be used. Scope retention to error attempts only.
- [ ] Emit kill switch `DEVSHARD_ERROR_MISS_ENABLED`, default off. Apply and verify stay unconditional.
- [ ] Retire the exemption TODO in `recordPostContentWinnerFailureOnce`. Keep gateway perf/quarantine scoring decoupled from the on-chain miss; replace the placeholder comment with that decision.
- [ ] Client response unchanged: `hostApplicationError` still surfaces with original status/body.
- [ ] Tests:
  - [ ] Error-stream attempt end to end: original error status/body, one diff carries Finish + `ERROR` timeout, `Missed == 1`, request not billed as finished
  - [ ] No-wait: realistic `ExecutionTimeout` (e.g. `32*60`), miss applied in well under a second
  - [ ] Kill switch off → no `ERROR` timeout emitted, behaviour exactly today's
  - [ ] Insufficient votes → today's behaviour, request still fails cleanly

## 9. Observability and docs

- [ ] `logInferenceStage` stage `error_miss` with `inference_id`, `host`, `error_type`, `error_code`, `response_hash`, `votes`, `accepted`.
- [ ] Timeout-reason label `"error"` (`RecordInferenceTimeout`, `timeoutReasonLogLabel`).
- [ ] Counter for rejected error-miss verifications by cause (`no_finish_tx`, `no_payload`, `sig`, `hash_mismatch`, `not_error_body`). Alert on `hash_mismatch`: it means payload reconstruction drifted and the path is silently dead.
- [ ] Update `docs/inference-lifecycle.md` (status transitions).
- [ ] Update `proposals/gateway-observability/observability.md` (`error_stream` row follow-up outcome).

## 10. E2E

Files: `testenv/mockopenai`, `testenv/citest`

- [ ] mock-openai fault: HTTP 200 + SSE error envelope (companion to `a2_ml_upstream_5xx_test.go`).
- [ ] Assert `Missed` on the executor slot and no validation job.

## 11. Governance

- [ ] Build and exercise under a release-candidate name with `VERSIOND_FORCE=<name>`.
- [ ] Governance proposal adding the new name to `approved_versions` (URL + sha256).
