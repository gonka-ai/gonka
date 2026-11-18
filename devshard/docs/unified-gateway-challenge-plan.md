# Implementation plan: unified gateway challenge path

**Status:** not started  
**Branch:** `feat/devshard-unified-gateway-challenge`  
**Design:** [proposals/unified-gateway-challenge.md](./proposals/unified-gateway-challenge.md)  
**Depends on:** [signature-verification.md](./signature-verification.md), [chat-frame-sparse-signature-plan.md](./chat-frame-sparse-signature-plan.md) (esp. `ChatFrameEvidence`), [public-validation-assignment-plan.md](./public-validation-assignment-plan.md) (§ Chat-frame mismatch votes), [#2093](https://github.com/gonka-ai/gonka/pull/2093) (`fix/validation-context-limit-client-fault`) for context-limit tokenization

Each step lands alone. **Do not start the next step until that step’s tests are green.** The summary table is the source of truth for progress; each step section links back here.

## Delivery order

| Phase | What lands | Cases covered |
| --- | --- | --- |
| **A. Structure** | Proto, vote/apply, RPC, gateway collection, identity — no real kind logic yet | Scaffold for all three |
| **B. Case 1** | Evidence-only kinds: token-count mismatch, empty stream | No ML node, no executor fetch |
| **C. Case 2** | Context-length via tokenization / render (adopt #2093 onto this bus) | Tokenize input only |
| **D. Case 3** | Full validation on the same challenge path; gateway-paid trigger | Same as full validation |

---

## Implementation status

| Step | Work | Status |
| --- | --- | --- |
| [1](#step-1-challenge-protos-and-domain-tags) | Challenge protos, kinds, evidence oneof, domain tags | Not started |
| [2](#step-2-applychallenge-skeleton) | `applyChallenge` skeleton (Finished → mark / Invalidated) | Not started |
| [3](#step-3-verifychallenge-rpc-skeleton) | `VerifyChallenge` RPC + host dispatch by kind | Not started |
| [4](#step-4-gateway-collection-skeleton) | Gateway collect / filter / send `MsgChallenge` | Not started |
| [5](#step-5-durable-vote-store) | One kept vote per inference (memory / SQLite / Postgres) | Not started |
| [6](#step-6-retain-chatframeevidence-on-the-gateway) | Gateway keeps tail `ChatFrameEvidence` through Finish binding | Not started |
| [7](#step-7-case-1-token-count-mismatch) | Case 1: token-count mismatch (evidence only) | Not started |
| [8](#step-8-case-1-empty-stream) | Case 1: empty stream (evidence only) | Not started |
| [9](#step-9-case-1-integration-and-abuse) | Case 1: offline executor, forged evidence, metrics | Not started |
| [10](#step-10-case-2-request-evidence-and-context-limits) | Case 2: request evidence + governance `--max-model-len` | Not started |
| [11](#step-11-case-2-context-limit-kind) | Case 2: context-limit kind (tokenize / render, adopt #2093) | Not started |
| [12](#step-12-case-3-validation-kind-on-the-bus) | Case 3: validation kind reuses full-validation pipeline | Not started |
| [13](#step-13-case-3-gateway-paid-trigger) | Case 3: gateway-initiated paid validation | Not started |

---

## Phase A — Structure

Land the bus with stub kind handlers (`abstain` / reject `unsupported_kind`). No production trigger opens a challenge yet.

### Step 1. Challenge protos and domain tags

[Status](#implementation-status).

Add wire types for the unified path (new protocol field numbers as needed on the active `devshard/v*`; do not overload #2088 fetch semantics if that PR is present — prefer a clean evidence-carrying shape even if names overlap).

**Proto / types**

- `ChallengeKind`: `TOKEN_COUNT`, `EMPTY_STREAM`, `CONTEXT_LIMIT`, `VALIDATION` (plus `UNSPECIFIED`).
- Evidence oneof on the verify request / optional digest on the tx:
  - `ChatFrameEvidence` fields (or embed transport helper’s wire form): `escrow_id`, `inference_id`, `content_hash`, `token_count`, `sig`
  - `RequestEvidence`: request bytes (or content-addressed blob + `prompt_hash`), `model`, `max_tokens`, epoch id as needed for `--max-model-len`
  - `ValidationEvidence`: inference id + paid/owed flag (payloads stay on existing validation fetch paths)
- `ChallengeFault` / `ChallengeCheck` / `ChallengeVote` / `ChallengeVoteContent` / `MsgChallenge`
- `InferenceRecord` flag(s) for “challenged structurally” (and reuse `StatusChallenged` only for validation — do not conflate)

**Signing** ([signature-verification.md](./signature-verification.md))

- One `CanonicalSignedBytes` helper for `ChallengeVoteContent` with a unique domain (e.g. `devshard.challengevote.v1`).
- Classify in `signed_preimage_test.go`. Chat-frame evidence keeps `devshard.chatframe.v1` — do not re-encode it under another domain.
- Host vote identity via `SlotActors.Allows`; executor self-vote refused at apply.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestSignedProtoPreimagesAreNotInterchangeable` (extended) | Challenge vote preimage does not verify as validation / timeout / error-miss / chat-frame |
| `TestChallengeVoteContent_DomainAndFields` | Marshalled content has expected fields; domain prefix present |
| `TestChallengeKind_WireRoundTrip` | Kind + evidence oneof round-trip through generated pb |

**Done when:** types compile; preimage CI green; no host/gateway behaviour change yet.

---

### Step 2. `applyChallenge` skeleton

[Status](#implementation-status).

State machine applies `MsgChallenge` for a Finished record:

- Verify each vote (slot in group, one per address, signature → `SlotActors`, binds `ResponseHash`, not executor).
- Sum fault weight (any non-`NONE` fault).
- Above `VoteThreshold` → `Finished → Invalidated` with replay-like refund / `hs.Invalid++` / withdraw sampled passes if that rule is already in tree.
- Below threshold → tx still applies; set structural challenged mark; stay Finished; **`NONE` adds no SPRT pass**.
- Sealed / unfinished → refuse. Second challenge for same inference → no-op.
- Kind on the tx is recorded for observability; this step may ignore kind-specific evidence (evidence checked on the host in later steps).

**Tests**

| Test | Asserts |
| --- | --- |
| `TestApplyChallenge_AboveThresholdInvalidates` | Fault weight > threshold → Invalidated, balance restored |
| `TestApplyChallenge_BelowThresholdKeepsFinished` | Tx applies, mark set, status Finished |
| `TestApplyChallenge_NoneAddsNoPass` | `NONE` votes do not increase validated / SPRT pass counts |
| `TestApplyChallenge_RejectsExecutorVote` | Executor address vote fails the tx |
| `TestApplyChallenge_RejectsBadSigOrDuplicate` | Bad sig / duplicate address → refuse whole tx (or drop vote — pick one and test it; prefer refuse whole tx like error-miss apply) |
| `TestApplyChallenge_SecondIsNoop` | Second MsgChallenge does not re-invalidate |
| `TestApplyChallenge_SealedRefused` | Sealed record → error |

**Done when:** `go test ./state/` green for the above; no RPC yet.

---

### Step 3. `VerifyChallenge` RPC skeleton

[Status](#implementation-status).

- `SessionService.VerifyChallenge`: owner-only (SeedHeightSync style), `RPCWeightVerify`, large-message mux, proxy heavy ACL.
- Request: `inference_id`, `ChallengeKind`, evidence, catch-up diffs.
- Host: bind session, ensure Finish visible, dispatch `verifyChallenge(kind, evidence)`:
  - unknown kind → reject `unsupported_kind`
  - known but not implemented yet → reject `kind_not_ready` (Steps 1–5 only)
- Response: pending \| reject(cause) \| vote (signed). No executor client call in the skeleton.
- Follow [signature-verification.md](./signature-verification.md) §5 for signing the vote.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyChallenge_OwnerOnly` | Group member / warm key of member cannot open |
| `TestVerifyChallenge_UnsupportedKind` | Reject cause set; no vote stored |
| `TestVerifyChallenge_KindNotReady` | TOKEN_COUNT before Step 7 → `kind_not_ready` |
| `TestVerifyChallenge_CatchUpAppliesFinish` | Cold host applies catch-up then answers |
| `TestRPC_VerifyChallenge_WeightAndEndpoint` | Listed in endpoint set / weight table / traffic |

**Done when:** `go test ./transport/... ./host/` for RPC admission; still no gateway trigger.

---

### Step 4. Gateway collection skeleton

[Status](#implementation-status).

Mirror timeout / error-miss collection:

- Ask every host except executor; retry pending / 429 / 503; early-exit when outcome settled.
- Verify each vote before counting (#2056 pattern).
- Send `MsgChallenge` under settle barrier; `FeePerNonce`.
- Outcomes: `invalidated`, `below_threshold`, `none`, `incomplete`, `no_votes`, `nonce_cap`, `failed`, `unsupported_protocol`.
- **No production detector calls this yet** — only unit tests drive `RunChallenge`.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestCollectChallengeVotes_EarlyExitOnThreshold` | Stops asking once faults cannot change outcome |
| `TestCollectChallengeVotes_DropsBadSig` | Forged vote not counted |
| `TestRunChallenge_SendsMsgChallenge` | Diff contains MsgChallenge when votes kept |
| `TestRunChallenge_SettleBarrier` | Escrow not settled while collection runs |
| `TestRunChallenge_NonceCap` | Cap → `nonce_cap`, no partial apply |

**Done when:** `go test ./user/` collection tests green; metrics stubs optional.

---

### Step 5. Durable vote store

[Status](#implementation-status).

One signed verdict per `(escrow, inference)` in memory / SQLite / Postgres; first settle wins (HA). Repeated `VerifyChallenge` returns the kept vote. Late reopen of window is not required until Case 1; for structure, store is enough.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestChallengeVoteStore_RoundTrip` | Put / get on memory, sqlite, postgres (or shared store suite) |
| `TestChallengeVoteStore_FirstSettleWins` | Concurrent put: one winner |
| `TestVerifyChallenge_ReturnsKeptVote` | Second RPC returns same bytes without re-running verify |

**Done when:** store tests green. **Phase A complete** — move to Case 1 only after Steps 1–5 pass.

---

## Phase B — Case 1 (evidence only, no ML)

No ML node. No executor payload fetch. Voters verify `ChatFrameEvidence` and compare to Finish.

### Step 6. Retain `ChatFrameEvidence` on the gateway

[Status](#implementation-status).

Fix deferred review item: [chat-frame-sparse-signature-review.md §7](./chat-frame-sparse-signature-review.md#7-the-gateway-discards-the-evidence-fields).

- On stream close, keep tail `content_hash`, `token_count`, `sig` (and ids) on the attempt / served-binding retention used for Finish binding.
- Expose a helper `TailChatFrameEvidence(escrow, inference) (ChatFrameEvidence, ok)`.
- Unsigned streams: `ok == false` (Case 1 does not challenge them structurally).

**Tests**

| Test | Asserts |
| --- | --- |
| `TestGateway_RetainsTailChatFrameEvidence` | After Close + Finish bind, evidence fields still present |
| `TestGateway_EvidenceMatchesTransportHelper` | Retained fields verify with `ChatFrameEvidence.Verify` |
| `TestGateway_UnsignedStreamHasNoEvidence` | No signature → ok false |
| `TestGateway_EvidenceDroppedAfterRetention` | Past retention → gone (same window as served binding) |

**Done when:** gateway unit tests green; log may still print TokenCounts as today.

---

### Step 7. Case 1: token-count mismatch

[Status](#implementation-status).

**Verifier (pure):** given Finish + `ChatFrameEvidence`:

1. Evidence ids match escrow / inference; `Verify` against executor cold/warm via `SlotActors`.
2. If `Finish.OutputTokens > evidence.token_count` (+ agreed tolerance, default 0 or 3 — document the constant) → fault `TOKEN_COUNT_MISMATCH` / `USAGE_INFLATED`.
3. Else `NONE`.
4. Never opens a payload client.

**Host:** implement kind `TOKEN_COUNT` in `VerifyChallenge` using the pure verifier.

**Gateway trigger:** when Finish applies and retained evidence exists and `OutputTokens > token_count` (+ tolerance), call `RunChallenge(TOKEN_COUNT, evidence)`. Byte bounds may gate logging only; they must not settle without evidence.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyTokenCount_FinishAboveSignedCount` | Fault + deviation |
| `TestVerifyTokenCount_FinishWithinTolerance` | NONE |
| `TestVerifyTokenCount_BadSig` | Reject / no fault vote |
| `TestVerifyTokenCount_WrongInferenceId` | Reject |
| `TestVerifyTokenCount_NoExecutorDial` | Fake dialer unused |
| `TestHostVerifyChallenge_TokenCount` | End-to-end host vote |
| `TestGateway_TriggersTokenCountChallenge` | Finish overclaim → MsgChallenge |
| `TestApplyChallenge_TokenCountInvalidates` | Threshold → Invalidated |

**Done when:** listed tests green; Step 6 still green.

---

### Step 8. Case 1: empty stream

[Status](#implementation-status).

**Verifier:** evidence verifies; `token_count == 0` (or signed empty / only-`[DONE]` frame per sparse plan) while Finish claims paid output or receipt-backed empty observation → fault `EMPTY_STREAM`. Non-zero signed count → `NONE` (gateway cut; not empty fraud).

**Gateway trigger:** receipt applied, no content tokens observed, **and** retained evidence has `token_count == 0` (or missing content with a signed zero count). Do not challenge “gateway saw empty” without evidence.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyEmptyStream_ZeroCountWithPaidFinish` | EMPTY_STREAM fault |
| `TestVerifyEmptyStream_NonZeroCountIsNone` | NONE (cut toward gateway) |
| `TestVerifyEmptyStream_BadEvidence` | Reject |
| `TestHostVerifyChallenge_EmptyStream` | Host vote |
| `TestGateway_TriggersEmptyStreamChallenge` | Evidence-backed empty → challenge |
| `TestGateway_EmptyObservationWithoutEvidence_NoChallenge` | Unsigned / no evidence → no structural challenge |

**Done when:** tests green; Steps 6–7 still green.

---

### Step 9. Case 1: integration and abuse

[Status](#implementation-status).

Hardening for the evidence-first model:

- Executor offline during Case 1 challenge → voters still vote from evidence; **no** `PAYLOAD_UNAVAILABLE` fault; outcome not `failed` solely due to silence.
- Forged gateway evidence (bad sig) → votes rejected; no Invalid from forgery.
- Metrics: `devshard_gateway_challenges_total{kind,outcome}`, host `devshard_challenge_votes_total{kind,fault}`.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestChallenge_OfflineExecutor_TokenCountStillSettles` | Invalid or NONE from evidence; dialer never required |
| `TestChallenge_ForgedChatFrameEvidence_NoInvalid` | No Invalidated |
| `TestChallenge_MetricsLabels` | kind=token_count\|empty_stream |

**Done when:** tests green. **Phase B / Case 1 complete.**

---

## Phase C — Case 2 (tokenize input only; adopt #2093)

[#2093](https://github.com/gonka-ai/gonka/pull/2093) today hangs context-limit off #2088’s **fetch-based** empty path (`emptyBodyVerdict` + fetched request). Adopt the **check**, not the fetch: request bytes travel as `RequestEvidence` on this bus; voter tokenizes / renders; no executor body fetch.

### Step 10. Case 2: request evidence and context limits

[Status](#implementation-status).

- Wire `RequestEvidence` on `VerifyChallenge` (request bytes, model, effective `max_tokens`, epoch).
- Port `ChainContextLimits` / `MaxModelLen` from #2093 (`inference/context_limit.go`) — governance `--max-model-len` from epoch model args.
- Gateway retains the request it sent (it already has it) for challenge evidence; size-cap the blob on the RPC.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestMaxModelLen_ParsesArgs` | From #2093 |
| `TestChainContextLimits_CachesPerEpochModel` | From #2093 |
| `TestRequestEvidence_RoundTrip` | Proto / RPC |
| `TestVerifyChallenge_RequestEvidenceSizeCap` | Oversize → reject |

**Done when:** limit resolution tests green without opening Case 2 kind yet.

---

### Step 11. Case 2: context-limit kind

[Status](#implementation-status).

**Kind `CONTEXT_LIMIT`:**

1. Verify request hashes to `PromptHash` on the record (or Start).
2. Resolve `--max-model-len` for `(epoch, model)`.
3. Count prompt tokens via pinned renderer / tokenize API (**no generation**) — same outcomes as #2093: use `CountPrompt` / `ContextLimitCounter` adapted so input is `RequestEvidence`, not `ChallengeFetch.Request` from the executor.
4. Outcomes (match Vitaly’s mapping on #2093, adapted):
   - `rendered + allowance + max_tokens > limit` and zero output → **`NONE`** with check `CONTEXT_LIMIT` (honest refusal; do **not** EMPTY_STREAM-invalidate).
   - Fits limit but empty paid Finish → leave to Case 1 empty (or validation), not “context exceeded”.
   - Count failed / no renderer → abstain (`unchecked`), not a fault.
5. Optional: if Finish `InputTokens` is inflated vs render while over limit, may vote usage fault on input — only if #2093’s inflated branch is kept; document the choice in the step commit.

**Gateway trigger:** context-refusal shaped stream / engine 400 with zero output **and** local pre-check that request exceeds limit → `RunChallenge(CONTEXT_LIMIT, requestEvidence)`. Prefer challenging only when the gateway’s own rough check agrees, to save voter load.

**Tests** (port and rename from #2093)

| Test | Asserts |
| --- | --- |
| `TestContextLimit_ExceededVotesNone` | Honest refusal → NONE + CONTEXT_LIMIT check |
| `TestContextLimit_FitsKeepsEmptyPathSeparate` | Under limit + empty → not cleared by this kind |
| `TestContextLimit_OutputTokensClaimedSkips` | Non-zero output → not treated as refusal |
| `TestContextLimit_CountFailedAbstains` | No fault vote |
| `TestContextLimit_NoExecutorBodyFetch` | Dialer unused |
| `TestGateway_TriggersContextLimitChallenge` | Refusal stream → kind CONTEXT_LIMIT |
| `TestApplyChallenge_ContextLimitNoneDoesNotInvalidate` | NONE votes → Finished stays |

**Done when:** tests green; Case 1 steps still green. **Phase C / Case 2 complete.**

---

## Phase D — Case 3 (full validation on the same bus)

Reuse the existing full-validation pipeline (fetch/replay or TEE path as deployed). The challenge bus becomes how the **gateway** can start the same check and pay for it; protocol-owed validation stays free.

### Step 12. Case 3: validation kind on the bus

[Status](#implementation-status).

- `ChallengeKind_VALIDATION` on `VerifyChallenge`: host runs the same `Validate` path used for sampled / assigned validation (lease, fetch, ML).
- Prefer applying results through existing `MsgValidation` / `MsgValidationVote` and `StatusChallenged` tallies so SPRT / Phase B stay one code path. `MsgChallenge(kind=VALIDATION)` is only used if needed as a wrapper; do not fork `VotesValid` meaning.
- Protocol-owed / already-`Challenged`: answering Phase B stays free; gateway collection for owed work does not charge the paid fee.
- Structural `NONE` still never credits validation passes.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyChallenge_ValidationKind_RunsValidate` | Calls validator; produces validation tx or vote |
| `TestVerifyChallenge_ValidationKind_InvalidOpensChallenged` | Valid=false → Challenged tallies |
| `TestVerifyChallenge_ValidationKind_ValidCreditsPass` | Only ML valid credits pass |
| `TestChallenge_StructuralNone_NoValidationPass` | Regression from Case 1 |
| `TestVerifyChallenge_Validation_ReusesLeaseRules` | Same lease / eligibility as host validateAsync |

**Done when:** validation kind works for owed/challenged flows without a fee yet.

---

### Step 13. Case 3: gateway-paid trigger

[Status](#implementation-status).

- Gateway API / internal hook: `RequestValidation(inference_id)` for a Finished record not already owed.
- Debit configured fee (escrow balance / accounting — pick existing fee primitive or add `ValidationFee`); refuse if insufficient.
- Then `RunChallenge(VALIDATION, …)` or notify assigned/eligible validators via the same bus.
- Eligibility: cannot assign executor’s address; prefer public-assignment rules when that plan is live.
- Idempotent: already Challenged / already validated → no second fee.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestGateway_PaidValidation_ChargesFee` | Balance drops by fee |
| `TestGateway_PaidValidation_InsufficientFunds` | No challenge started |
| `TestGateway_PaidValidation_RunsFullValidate` | Invalid/valid outcomes match normal validation |
| `TestGateway_PaidValidation_NoDoubleCharge` | Second request no fee |
| `TestGateway_ProtocolOwedValidation_NoFee` | Challenged / assigned path free |

**Done when:** tests green. **Phase D / Case 3 complete.**

---

## Cross-cutting rules (every phase)

- **Previous step tests must stay green** before merging the next step.
- Structural kinds: **never** dial the executor for payload; silence ≠ fault.
- All new signatures follow [signature-verification.md](./signature-verification.md).
- Chat-frame evidence uses existing `ChatFrameEvidence` / `devshard.chatframe.v1` only.
- Protocol version: new tx/RPC requires an approved version bump when first structural apply ships (Step 2+); document the version name in the Step 2 commit.

## Out of scope (follow-ups)

- Prefix-mismatch vote that carries full SSE bytes (named in the design; add as Step 8b later if needed).
- Retiring #2088 fetch-based path (after Case 1 is required on the protocol version).
- TEE / private-inference packaging ([VALIDATION_PROTOCOL_PROPOSAL.md](./proposals/VALIDATION_PROTOCOL_PROPOSAL.md)).
- Missing-witness `ChallengeDue` messages ([public-validation-assignment-plan.md](./public-validation-assignment-plan.md) residuals).

## Done when (whole plan)

1. Steps 1–5: bus exists; stub kinds only.  
2. Steps 6–9: token-count and empty stream settle from evidence without ML or executor fetch.  
3. Steps 10–11: context-limit adopts #2093 checks on request evidence.  
4. Steps 12–13: full validation is reachable on the same path; gateway can pay to start it.
