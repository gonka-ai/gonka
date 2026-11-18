# Implementation plan: unified gateway challenge path

**Status:** not started  
**Branch:** `feat/devshard-unified-gateway-challenge`  
**Design:** [proposals/unified-gateway-challenge.md](./proposals/unified-gateway-challenge.md)  
**Depends on:** [signature-verification.md](./signature-verification.md), [chat-frame-sparse-signature-plan.md](./chat-frame-sparse-signature-plan.md) (esp. `ChatFrameEvidence`), [public-validation-assignment-plan.md](./public-validation-assignment-plan.md) (§ Chat-frame mismatch votes)

**Open concerns:** [unified-gateway-challenge-open-concerns.md](./unified-gateway-challenge-open-concerns.md) lists 16 unresolved items (C1–C16), each tied to the step it blocks. Read it before starting Steps 2b, 6, 8 and 12; C1, C2 and C3 must be settled before the code for Steps 8 and 12 is written.

**Not in this tree (checked):** #2088 (`MsgChallenge`, `VerifyChallenge`, `emptyBodyVerdict`) and #2093 (`ChainContextLimits`, `MaxModelLen`) are absent from the branch. Nothing here may assume they landed. Step 11 re-implements `MaxModelLen` parsing itself; it does not "port" it.

Each step lands alone. **Do not start the next step until that step’s tests are green.** The summary table is the source of truth for progress.

## Decisions fixed by this plan

| Topic | Decision |
| --- | --- |
| Empty chunk | In a **signed** stream, one empty message, or one message that is only `[DONE]`, is invalid. No tolerance. |
| Evidence is a *span*, not a whole body | Proof of a bad chunk or a wrong signed count is the bytes between two signatures plus the SHA-256 midstate at the earlier one (see [Span evidence](#span-evidence)). No full body, no prompt. |
| Two fault grades | **Strike** (applied on chain, invalidates) and **observe** (challenge runs, votes collected, **not applied**, logged at WARN, metric). |
| Finish vs signed count | Signed count must equal `Finish.OutputTokens`. Any difference is logged at WARN and runs an **observe** challenge. It is a **strike** only when `Finish.OutputTokens * 10 >= signed_count * 12` (×1.2, constant `TokenCountStrikeNum/Den = 12/10`) **and** the stream was complete. |
| Span proofs are strikes | Empty chunk, and "signed count N but span holds fewer content tokens", are exact proofs under the shared classifier. They are strikes with no ratio. |
| Below-strike result | **Not applied on chain.** `applyChallenge` refuses the tx unless fault weight > `VoteThreshold` (same as `applyErrorMiss`). So there is no mark flag, no lock-out, and a gateway can retry. `InferenceRecord` gets no new field; only per-host strike counters are added ([Step 2b](#step-2b-protocol-gate-and-settlement-enrichment)). |
| Strike gating | Strikes reach the chain only on sessions at or after `ChallengeProtocolVersion`; before it every grade is `OBSERVED`. Settlement carries the structural counters. |
| Empty | Empty content in a `data:` event, or a bare `[DONE]` (no `finish_reason` / error before it in the span). See Step 8 for what the executor hashes. |
| Tokenizer | Case 2 branch A uses the **MLNode tokenize endpoint**. A host whose MLNode lacks it votes `NONE` (against punishing), never a strike. |
| Cut stream | Never evidence for a Finish-vs-count challenge. Only a complete signed stream qualifies. |
| Case 2 payload | Branch A (claimed sum > limit) re-verifies `InputTokens`: the request goes to voters for tokenizing only. Branch B (sum fits, signed context error) carries no prompt. Case 1 never carries a prompt. |

---

## Delivery order

| Phase | What lands | Cases covered |
| --- | --- | --- |
| **A. Structure** | Proto, strike-only apply, RPC, gateway collection (strike + observe), identity, vote store | Scaffold for all |
| **B. Case 1** | Evidence-only kinds: empty chunk, signed-count-vs-span, Finish-vs-signed-count | No ML node, no executor fetch |
| **C. Case 2** | Context length: recount input tokens (A) / signed-error contradiction (B) | Tokenize only, no generation |
| **D. Case 3** | Full validation reached from the same RPC; gateway-paid trigger | Same as full validation |

---

## Implementation status

| Step | Work | Status |
| --- | --- | --- |
| [1](#step-1-challenge-protos-and-domain-tags) | Challenge protos, kinds, evidence oneof, domain tags | Not started |
| [2](#step-2-applychallenge-skeleton) | `applyChallenge` (strike-only; Finished → Invalidated) | Not started |
| [2b](#step-2b-protocol-gate-and-settlement-enrichment) | Protocol gate for strikes; settlement carries structural strike counters | Not started |
| [3](#step-3-verifychallenge-rpc-skeleton) | `VerifyChallenge` RPC + host dispatch by kind | Not started |
| [4](#step-4-gateway-collection-skeleton) | Gateway collect / filter / send; strike and observe modes | Not started |
| [5](#step-5-durable-vote-store) | One kept vote per inference (memory / SQLite / Postgres) | Not started |
| [6](#step-6-gateway-evidence-capture) | Gateway captures tail evidence, span evidence, stream-complete flag | Not started |
| [7](#step-7-align-the-signed-token-count-with-completion-tokens) | Executor signs a count equal to `completion_tokens` | Not started |
| [8](#step-8-case-1-empty-chunk) | Case 1: empty chunk (span evidence) | Not started |
| [9](#step-9-case-1-signed-count-vs-span-content) | Case 1: signed count N, span holds fewer tokens | Not started |
| [10](#step-10-case-1-finish-vs-signed-count) | Case 1: Finish vs signed count (observe, strike at ×1.2) | Not started |
| [11](#step-11-case-1-integration-and-abuse) | Case 1: offline executor, forged / replayed evidence, metrics | Not started |
| [12](#step-12-case-2-context-limit) | Case 2: governance `--max-model-len`, input recount, signed-error contradiction | Not started |
| [13](#step-13-case-3-validation-kind-on-the-bus) | Case 3: validation kind reuses full-validation pipeline | Not started |
| [14](#step-14-case-3-gateway-paid-trigger) | Case 3: gateway-initiated paid validation | Not started |

---

## Span evidence

The sparse signature covers a **running hash of the whole uncompressed prefix** plus a cumulative token count. A chunk cannot be checked alone against it, and one signature may cover up to `SignEvery` chunks (default 13, max 4096). So a proof of "this chunk is bad" is:

| Part | Why |
| --- | --- |
| `prev`: previous verified `ChatFrameEvidence` (or "stream start": count 0, hash = `newChatStreamHash(escrow, inference, promptHash)` digest) | Anchors the span start. Verified like any frame. |
| `prev_midstate`: `encoding.BinaryMarshaler` state of the SHA-256 at `prev` | Lets the voter continue hashing without the earlier bytes. Voter checks `midstate.Sum() == prev.content_hash`; it cannot be forged without breaking SHA-256. |
| `span_bytes`: uncompressed bytes from `prev` to `cur` | The only content sent. Capped (`MaxSpanEvidenceBytes`, default 256 KiB; larger spans are logged, not challenged). |
| `cur`: the signed `ChatFrameEvidence` that ends the span | The executor’s signed claim about the span. |

Voter: verify both signatures via `SlotActors`; restore midstate, check its digest equals `prev.content_hash`; hash `span_bytes`, check equals `cur.content_hash`; then run the **shared** classifier (`scanSSE`, same code the signer and gateway use) on the span. No executor dial, no prompt (the prompt hash is already on the record).

Finish-vs-count (Step 10) needs no span: tail `ChatFrameEvidence` only.

---

## Phase A — Structure

Land the bus with stub kind handlers. No production trigger opens a challenge yet.

### Step 1. Challenge protos and domain tags

[Status](#implementation-status).

**Proto / types** (new field numbers on the active `devshard/v*`)

- `ChallengeKind`: `EMPTY_CHUNK`, `SIGNED_COUNT_MISMATCH`, `TOKEN_COUNT`, `CONTEXT_LIMIT`, `VALIDATION` (plus `UNSPECIFIED`).
- `ChallengeFault`: `NONE`, `OBSERVED` (below strike grade; never on chain), `STRIKE`. A vote carries the grade it computed so apply stays deterministic.
- Evidence oneof on the verify request:
  - `ChatFrameEvidence` wire form (`escrow_id`, `inference_id`, `content_hash`, `token_count`, `sig`) — tail evidence.
  - `ChatFrameSpanEvidence` (`prev`, `prev_midstate`, `span_bytes`, `cur`) — see [Span evidence](#span-evidence).
  - `ValidationEvidence`: inference id + paid/owed flag only.
  - `RequestEvidence` (Case 2 branch A only): request bytes, `model`, epoch; size-capped.
  - `ContextErrorEvidence` (Case 2 branch B): `ChatFrameSpanEvidence` containing the error event, plus `input_tokens`, `max_tokens`, `model` (all re-checked against the log).
- `ChallengeVote` / `ChallengeVoteContent` / `MsgChallenge`. `MsgChallenge` carries only strike votes.
- **No** `InferenceRecord` field. A challenge changes state only by `Finished → Invalidated`.

**Signing** ([signature-verification.md](./signature-verification.md))

- One `CanonicalSignedBytes` helper for `ChallengeVoteContent`, unique domain (e.g. `devshard.challengevote.v1`). The vote content binds `kind`, `fault`, `inference_id`, `ResponseHash`.
- Classify in `signed_preimage_test.go`. Chat-frame evidence keeps `devshard.chatframe.v1`; do not re-encode it.
- Host vote identity via `SlotActors.Allows` (apply path: `hostSignerAllowedLocked`); executor self-vote refused at apply.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestSignedProtoPreimagesAreNotInterchangeable` (extended) | Challenge vote preimage does not verify as validation / timeout / error-miss / chat-frame |
| `TestChallengeVoteContent_DomainAndFields` | Expected fields; domain prefix present |
| `TestChallengeKind_WireRoundTrip` | Kind + evidence oneof round-trip |
| `TestSpanEvidence_WireRoundTrip` | Midstate and span survive pb round-trip |

**Done when:** types compile; preimage CI green; no behaviour change.

---

### Step 2. `applyChallenge` skeleton

[Status](#implementation-status).

State machine applies `MsgChallenge` for a `Finished` record, **strike-only**:

- Verify each vote: slot in group, one per address, signature recovered then `hostSignerAllowedLocked`, binds `ResponseHash`, voter is not the executor, `fault == STRIKE`. Any bad vote → refuse the whole tx (like `applyErrorMiss`).
- Sum strike weight (`addressToSlotCount`). `<= VoteThreshold` → refuse (`ErrInsufficientVotes`). **Nothing is recorded**, so the gateway may retry with more votes.
- `> VoteThreshold` → `Finished → Invalidated`, with exactly this accounting (single rule, the same as `resolveChallengeTalliesLocked` plus the pass unwind from `applyErrorMiss`):
  - `Balance += ActualCost`; `hs.Cost -= ActualCost` (clamped at 0);
  - `hs.Invalid++`;
  - `hs.Validated -= rec.ValidatedBy.Count()` (clamped at 0), because earlier passes no longer stand;
  - `hs.Finished` is **not** decremented (matches the existing Challenged → Invalidated path).
- Sealed / unfinished / already `Invalidated` / `Challenged` / `Validated` → refuse. (A challenged record is already owed validation; do not stack.)
- Kind is recorded in the log line only.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestApplyChallenge_AboveThresholdInvalidates` | Strike weight > threshold → Invalidated, balance restored |
| `TestApplyChallenge_AccountingMatchesRule` | `Cost`, `Invalid`, `Validated`, `Finished` deltas exactly as above |
| `TestApplyChallenge_BelowThresholdRefusedAndRetriable` | Refused, record unchanged, a later tx with more votes applies |
| `TestApplyChallenge_ObservedVoteRejected` | `OBSERVED` / `NONE` votes in a tx → refused |
| `TestApplyChallenge_RejectsExecutorVote` | Executor address vote fails the tx |
| `TestApplyChallenge_RejectsBadSigOrDuplicate` | Whole tx refused |
| `TestApplyChallenge_NotFinishedRefused` | Started / Challenged / Validated / Invalidated → error |
| `TestApplyChallenge_SealedRefused` | Sealed record → error |
| `TestApplyChallenge_StateRootUnchangedOnRefuse` | Refused tx leaves root identical |

**Done when:** `go test ./state/` green; no RPC yet. Document the protocol version name in the commit (new tx type needs an approved bump).

---

### Step 2b. Protocol gate and settlement enrichment

[Status](#implementation-status).

Strikes take effect on the inference-chain side through the settlement message, so both the gate and the message belong here.

**Protocol gate**

- A new named constant `ChallengeProtocolVersion`. `MsgChallenge` is refused (`ErrUnsupportedProtocol`) on any session whose stamped protocol version is older. Hosts on an older session answer `unsupported_protocol`; the gateway never builds a strike for it.
- **Before the gate:** every vote grade is capped at `OBSERVED` (the gateway may still log and observe on hosts that support the RPC). **From the gate:** `STRIKE` applies.
- Step 7’s aligned signed count becomes mandatory at the same version. Steps 8–12 strikes are gated by it; observe results are not.
- The version name is documented in this step’s commit (a new tx type needs an approved bump).

**Settlement enrichment** (`MsgSettleDevshardEscrow`, `cmd/devshardctl/chain_tx_encode.go`, inference-chain handler)

Today a host’s stats on settlement are `slot, missed, invalid, cost, required/completed validations, validated, finished` (fields 1–8). A structural strike is indistinguishable from an ML-validation invalid. Add to `HostStats` / `HostStatsProto` / `SettlementJSON` / the chain `HostStats` proto:

- `invalid_structural` (total) and per-kind counts: `empty_chunk`, `signed_count`, `token_count`, `context_limit`.
- `applyChallenge` increments the existing `Invalid` (so a chain that has not upgraded still penalises) **and** the structural counters.
- Counters are encoded only when non-zero, and only for sessions at or after `ChallengeProtocolVersion` (the same pattern `Validated` / `Finished` use). Sessions before the gate keep byte-identical roots and settlement bytes.
- Rollout order: the inference-chain module learns the new fields first; the devshard version that emits them is enabled after.
- This **does** change the state hash for gated sessions (`HostStats` is in state); `state/hash.go`, `types/snapshot_codec.go`, the settlement JSON and the chain proto change together. `InferenceRecord` still gets no field.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestApplyChallenge_RefusedBeforeProtocolGate` | Old session → `ErrUnsupportedProtocol` |
| `TestVerifyChallenge_UnsupportedProtocolOnOldSession` | Host answers `unsupported_protocol` |
| `TestGateway_NoStrikeOnOldSession` | Gateway never builds a `MsgChallenge` |
| `TestVoteGradeCappedBeforeGate` | STRIKE becomes OBSERVED before the gate |
| `TestHostStats_StructuralCountersBumpedByStrike` | `Invalid` and the kind counter both increment |
| `TestStateRoot_UnchangedForPreGateSessions` | Pre-gate roots and snapshots byte-identical |
| `TestSettlement_EncodesStructuralCountersOnlyWhenNonZero` | New fields omitted when zero or pre-gate |
| `TestSettlement_ChainRoundTrip` | Chain handler decodes the new fields (mock chain) |

**Done when:** state, settlement-encode and chain-mock tests green.

---

### Step 3. `VerifyChallenge` RPC skeleton

[Status](#implementation-status).

- `SessionService.VerifyChallenge`: owner-only (SeedHeightSync style), `RPCWeightVerify`, large-message mux, proxy heavy ACL. Evidence size caps enforced at admission (span cap, request cap). Request bytes are accepted only for `CONTEXT_LIMIT` branch A.
- Request: `inference_id`, `ChallengeKind`, evidence, catch-up diffs.
- Host: bind session, ensure Finish visible, dispatch per kind. Each kind has a registered stub that returns `kind_not_ready` until its own step lands; unknown kind → `unsupported_kind`.
- Response: pending \| reject(cause) \| vote (signed; fault = `NONE` \| `OBSERVED` \| `STRIKE`). Observed/none votes are also signed so the gateway can verify them before logging.
- Follow [signature-verification.md](./signature-verification.md) §5.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyChallenge_OwnerOnly` | Group member / warm key of member cannot open |
| `TestVerifyChallenge_UnsupportedKind` | Reject cause; no vote stored |
| `TestVerifyChallenge_KindNotReady_PerKind` | Each unimplemented kind → `kind_not_ready`; implemented kinds are not |
| `TestVerifyChallenge_CatchUpAppliesFinish` | Cold host applies catch-up then answers |
| `TestVerifyChallenge_OversizeEvidenceRejected` | Span over cap → reject |
| `TestRPC_VerifyChallenge_WeightAndEndpoint` | In endpoint set / weight table / traffic |

**Done when:** `go test ./transport/... ./host/` green.

---

### Step 4. Gateway collection skeleton

[Status](#implementation-status).

Mirror timeout / error-miss collection, with two modes:

- **strike mode:** ask every host except executor; retry pending / 429 / 503; early-exit when the outcome is settled **on slot weight** (not host count: stop when strike weight > `VoteThreshold`, or when remaining unasked weight cannot reach it). Verify each vote before counting (#2056 pattern). Send `MsgChallenge` under settle barrier; `FeePerNonce`.
- **observe mode:** same collection, **no tx**. Result is logged at WARN with the vote tally and counted in metrics.
- Outcomes: `invalidated`, `observed`, `none`, `incomplete`, `no_votes`, `nonce_cap`, `failed`, `unsupported_protocol`. `incomplete` is never an accusation, and a later retry is allowed.
- **No production detector calls this yet.**

**Tests**

| Test | Asserts |
| --- | --- |
| `TestCollectChallengeVotes_EarlyExitByWeight` | Stops once strike weight cannot change outcome; multi-slot hosts weighted correctly |
| `TestCollectChallengeVotes_DropsBadSig` | Forged vote not counted |
| `TestRunChallenge_StrikeSendsMsgChallenge` | Diff contains MsgChallenge when strike weight > threshold |
| `TestRunChallenge_ObserveSendsNoTx` | Observe mode never emits a diff |
| `TestRunChallenge_RetryAfterIncomplete` | Second run may send after a first `incomplete` |
| `TestRunChallenge_SettleBarrier` | Escrow not settled while collection runs |
| `TestRunChallenge_NonceCap` | Cap → `nonce_cap`, no partial apply |

**Done when:** `go test ./user/` collection tests green.

---

### Step 5. Durable vote store

[Status](#implementation-status).

One signed verdict per `(escrow, inference, kind)` in memory / SQLite / Postgres; first settle wins (HA). Repeated `VerifyChallenge` returns the kept vote. Keying by kind lets a later, different kind get its own verdict.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestChallengeVoteStore_RoundTrip` | Put / get on memory, sqlite, postgres |
| `TestChallengeVoteStore_FirstSettleWins` | Concurrent put: one winner |
| `TestChallengeVoteStore_KeyedByKind` | Two kinds on one inference coexist |
| `TestVerifyChallenge_ReturnsKeptVote` | Second RPC returns same bytes without re-verifying |

**Done when:** store tests green. **Phase A complete.**

---

## Phase B — Case 1 (evidence only, no ML, no payload)

### Step 6. Gateway evidence capture

[Status](#implementation-status).

Fix [chat-frame-sparse-signature-review.md §7](./chat-frame-sparse-signature-review.md#7-the-gateway-discards-the-evidence-fields).

- Tail: keep `content_hash`, `token_count`, `sig`, ids of the last verified frame on the attempt / served-binding retention.
- **Stream-complete flag:** set only when `finish_reason`, `[DONE]` and a signed tail covering the end were all read, and the stream was not cut by the gateway, the client or a transport error. Add it to the retained record.
- **Span capture:** `ChatFrameVerifier` keeps, since the last verified signature, the SHA-256 midstate (`MarshalBinary`) and the uncompressed bytes (bounded by `MaxSpanEvidenceBytes`; past the cap it drops the span and marks it oversized). On each verified signature it rolls `prev` forward.
- **Violation snapshot:** the moment a span fails a check (empty-content `data:` event, bare `[DONE]`, count mismatch), snapshot `{prev, midstate, span_bytes, cur}` into retention. Do **not** wait for the tail: the offending span is usually not the last one.
- Helpers: `TailChatFrameEvidence(escrow, inference) (ChatFrameEvidence, complete bool, ok bool)` and `SpanViolations(escrow, inference) []ChatFrameSpanEvidence`.
- Unsigned streams: `ok == false`. Case 1 does not challenge them structurally.
- Retention window: **at least the live-state horizon** (the record must still be un-sealed when the challenge is sent). Pick one named constant and assert it in a test against the seal horizon; evidence that outlives the live record is useless, and evidence that dies earlier is lost.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestGateway_RetainsTailChatFrameEvidence` | Fields present after Close + Finish bind |
| `TestGateway_EvidenceMatchesTransportHelper` | Retained fields verify with `ChatFrameEvidence.Verify` |
| `TestGateway_StreamCompleteFlag` | Set on full stream; **unset** on gateway cut, client cut, missing `[DONE]`, unsigned tail |
| `TestGateway_SpanCapturedAtViolation` | Mid-stream bad span retained, not lost by later frames |
| `TestGateway_SpanMidstateVerifies` | `midstate.Sum() == prev.content_hash`; hashing span reaches `cur.content_hash` |
| `TestGateway_SpanOverCapDropped` | Oversize span flagged, not retained |
| `TestGateway_UnsignedStreamHasNoEvidence` | ok false |
| `TestGateway_EvidenceRetentionCoversSealHorizon` | Retention ≥ seal horizon |

**Done when:** gateway unit tests green; existing log lines unchanged.

---

### Step 7. Align the signed token count with completion tokens

[Status](#implementation-status).

Today the signed count is the number of SSE events classified `sseContent`, while `Finish.OutputTokens` is the engine’s `usage.CompletionTokens` (`cmd/devshardd/inference/recover.go`). They can differ (stop token, multi-token events, tool-call / reasoning deltas, non-JSON data lines). The comparison in Step 10 is meaningless until they agree.

- Define the count once: **the final signed `token_count` equals `completion_tokens`** that Finish reports for a complete stream. Choose the mechanism in this step’s commit (executor adds the unaccounted tokens to the final frame’s count, or the classifier counts per engine token); record the choice here.
- Intermediate frames keep the event-based count, so Step 9’s span check still has an exact rule. **The rule for intermediate frames and for the final frame must be spelled out and share one function** used by signer, gateway verifier and voter.
- Any residual difference between final signed count and Finish is logged by the gateway at WARN (it is the Step 10 observe case).

**Tests**

| Test | Asserts |
| --- | --- |
| `TestSignedCount_FinalEqualsCompletionTokens` | Stop token, multi-token event, tool-call delta, reasoning delta, non-JSON data line each counted; equality holds |
| `TestSignedCount_SignerGatewayVoterAgree` | One classifier; same result on the same bytes |
| `TestSignedCount_IntermediateFramesStayEventBased` | Span rule from Step 9 still exact |

**Done when:** tests green; Step 6 still green.

---

### Step 8. Case 1: empty chunk

[Status](#implementation-status).

**What the executor actually hashes (rechecked in `transport/chat_gzip.go`, `scheduleSignedEvent`).** An honest executor never puts an empty event on the wire: an event the classifier calls `sseIgnore` (empty `data:`, empty `content`, role-only / empty delta, comment) hits the `default` branch and is **dropped, not hashed**, unless part of it was already released; a `[DONE]` with nothing pending, no held finish event and nothing sent is also dropped, and `flushChunk` sends nothing when `pending == 0`. So an empty stream yields **no bytes and no signature**, and a zero-byte chunk never enters the hash. Consequence: the only provable empty-chunk fault is an executor that **hashed and signed** such an event. That is exactly what span evidence can show, and it is a fault, because the honest sink never emits it.

**Definition of empty (fixed):**

1. **Empty content in data:** an SSE event with a `data:` line that the shared classifier classes as `sseIgnore`: empty payload, `content` / `reasoning` / `tool_calls` all empty, role-only delta, empty delta. A bare comment line (no `data:`) is **not** a strike (it is not "content in data"; logged at DEBUG).
2. **Bare `[DONE]`:** `[DONE]` with no `finish_reason` and no error object earlier in the same span. This is the existing `DoneWithoutFinish` check. The finish event is held until `[DONE]` and committed in the same tail span (`commitFinish`), so an honest executor always has it in the same span; a stream that is only `finish_reason` + `[DONE]` with zero content tokens is a legitimate empty completion and is **not** a strike.

One such event is enough. Evidence is a [span](#span-evidence); the offending bytes are in it by construction, because they were hashed.

**Verifier (pure):** given `ChatFrameSpanEvidence`:

1. Ids match; verify `prev` (unless stream start) and `cur` via `SlotActors`; midstate digest equals `prev.content_hash`; span hashes to `cur.content_hash`.
2. Classify every event in the span with the shared classifier. Rule 1 or rule 2 above present → `STRIKE`; otherwise `NONE`.
3. Never opens a payload client or reads a prompt.

**Host:** implement `EMPTY_CHUNK` with the verifier.

**Gateway trigger:** the gateway verifier hashes every decompressed byte it receives and `NoteEvent` classifies each event. When it sees a rule 1 or rule 2 event inside a span that a signature later covers, it snapshots the span (Step 6) and runs `RunChallenge(EMPTY_CHUNK, spanEvidence)` in strike mode. Independent of stream completeness.

**No-frame empty stream** (receipt applied, nothing signed, nothing forwarded) is exactly what an honest-looking executor produces for an empty output. It has no evidence and is not a strike: WARN, metric, left to validation sampling.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestChatFrameSink_DropsIgnoredEventsFromHash` | Characterization: empty-data / role-only / comment events and a bare `[DONE]` never enter the hashed stream or a signature (pins the claim above) |
| `TestVerifyEmptyChunk_EmptyContentDataEventInSignedSpan` | Signed span with an empty-content `data:` event → STRIKE |
| `TestVerifyEmptyChunk_RoleOnlyAndEmptyDelta` | STRIKE |
| `TestVerifyEmptyChunk_BareDoneWithoutFinish` | STRIKE |
| `TestVerifyEmptyChunk_FinishThenDoneZeroContentIsNone` | Legitimate empty completion not struck |
| `TestVerifyEmptyChunk_CommentLineIsNotStrike` | NONE |
| `TestVerifyEmptyChunk_FinishAndUsageEventsAreNotEmpty` | Not flagged |
| `TestVerifyEmptyChunk_BadSigOrBrokenHashChain` | Reject, no fault vote |
| `TestVerifyEmptyChunk_ForgedMidstate` | Digest ≠ `prev.content_hash` → reject |
| `TestVerifyEmptyChunk_NoExecutorDial` | Fake dialer unused |
| `TestHostVerifyChallenge_EmptyChunk` | End-to-end host vote |
| `TestGateway_TriggersEmptyChunkChallenge` | Violation → MsgChallenge |
| `TestGateway_NoFrameEmptyStream_NoStrike` | WARN logged; no challenge sent |
| `TestApplyChallenge_EmptyChunkInvalidates` | Threshold → Invalidated |

**Done when:** tests green; Steps 6–7 still green.

---

### Step 9. Case 1: signed count vs span content

[Status](#implementation-status).

**Fault:** a signature says token count `N` at `cur`, `prev` says `p`, but the span holds fewer than `N − p` content tokens by the shared classifier (and the reverse: more). The executor signed a false statement about bytes it also hashed — an exact proof, so `STRIKE`, no ratio.

**Verifier:** same checks as Step 8 (signatures, midstate, hash chain), then compare `cur.token_count − prev.token_count` to the classifier’s count over the span. Any difference → `STRIKE`; equal → `NONE`.

**Gateway trigger:** `ChatFrameVerifier` already detects `TokenCountMismatch`; it now snapshots the span (Step 6) and runs `RunChallenge(SIGNED_COUNT_MISMATCH, spanEvidence)` in strike mode.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifySignedCount_SpanHasFewerTokens` | STRIKE |
| `TestVerifySignedCount_SpanHasMoreTokens` | STRIKE |
| `TestVerifySignedCount_Exact` | NONE |
| `TestVerifySignedCount_FirstFrameAnchorsAtStreamStart` | prev = start (count 0) works |
| `TestVerifySignedCount_BadSigOrChain` | Reject |
| `TestHostVerifyChallenge_SignedCountMismatch` | Host vote |
| `TestGateway_TriggersSignedCountChallenge` | Mismatch → MsgChallenge |
| `TestApplyChallenge_SignedCountInvalidates` | Threshold → Invalidated |

**Done when:** tests green; Steps 6–8 still green.

---

### Step 10. Case 1: Finish vs signed count

[Status](#implementation-status).

**Verifier (pure):** given Finish and tail `ChatFrameEvidence` (no span, no content):

1. Evidence ids match; signature verifies via `SlotActors`.
2. Equal counts → `NONE`.
3. Different → `OBSERVED`, unless `Finish.OutputTokens * 10 >= signed_count * 12` → `STRIKE`. The constant is named and lives next to the proto.
4. Never opens a payload client.

**Gateway trigger — only on a complete stream** ([Step 6](#step-6-gateway-evidence-capture)): when Finish applies, `complete == true`, and `Finish.OutputTokens != signed_count`:

- log at **WARN** (`token_count_mismatch`, with both numbers and the ratio);
- run `RunChallenge(TOKEN_COUNT, tailEvidence)` — **observe mode** by default, **strike mode** when the ratio is ≥ 1.2.
- A cut stream (`complete == false`) never triggers this kind, even at ratio ≥ 1.2; log at DEBUG with the reason.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyTokenCount_EqualIsNone` | NONE |
| `TestVerifyTokenCount_MismatchBelow12IsObserved` | OBSERVED, not STRIKE |
| `TestVerifyTokenCount_Ratio12IsStrike` | Boundary: exactly ×1.2 STRIKE, just below OBSERVED (integer math) |
| `TestVerifyTokenCount_BadSig` / `_WrongInferenceId` | Reject |
| `TestVerifyTokenCount_NoExecutorDial` | Dialer unused |
| `TestHostVerifyChallenge_TokenCount` | End-to-end host vote |
| `TestGateway_TokenCountMismatchWarnsAndObserves` | WARN emitted; observe challenge; **no** MsgChallenge |
| `TestGateway_TokenCountStrikeAtRatio12` | Complete stream, ×1.2 → MsgChallenge |
| `TestGateway_CutStreamNeverChallenges` | Honest executor, cut at 80% (ratio ≥ 1.25) → no challenge of any kind |
| `TestApplyChallenge_TokenCountInvalidates` | Threshold → Invalidated |

**Done when:** tests green; Steps 6–9 still green.

---

### Step 11. Case 1: integration and abuse

[Status](#implementation-status).

- Executor offline during a Case 1 challenge → voters still vote from evidence; no `PAYLOAD_UNAVAILABLE` fault; outcome not `failed` solely due to silence.
- Forged evidence (bad sig) → rejected; no Invalid.
- **Replay:** a valid frame / span from the same escrow but a **different inference** (or the same inference’s earlier frame presented as `cur`) → rejected on id / chain binding.
- Metrics: `devshard_gateway_challenges_total{kind,outcome}` (outcome includes `observed`), host `devshard_challenge_votes_total{kind,fault}`.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestChallenge_OfflineExecutor_StillSettles` | Strike or none from evidence; dialer never required |
| `TestChallenge_ForgedChatFrameEvidence_NoInvalid` | No Invalidated |
| `TestChallenge_ReplayedFrameFromOtherInference_Rejected` | Same escrow, other inference id → reject |
| `TestChallenge_StaleFrameAsCur_Rejected` | Earlier frame presented as span end → reject |
| `TestChallenge_MetricsLabels` | kind × outcome incl. `observed` |

**Done when:** tests green. **Phase B / Case 1 complete.**

---

## Phase C — Case 2 (context length)

Two branches, split by the executor's own claimed numbers. The **input token count is always re-verified when the claimed sum exceeds the limit**; the prompt reaches voters for that branch only.

Chain-visible values (voters read them from their own copy of the log, never from the attachment alone):

- `max_tokens` / reservation from `MsgStartInference` (gateway-signed; verify exact field names in this step),
- `Finish.InputTokens` (executor-signed),
- `model` and epoch, hence governance `--max-model-len` for `(epoch, model)`.

| Branch | Condition | What the gateway proves | Prompt to voters? |
| --- | --- | --- | --- |
| **A. Recount** | `Finish.InputTokens + max_tokens > limit` | The input count the executor charged is wrong | **Yes**, tokenize only, no generation |
| **B. Contradiction** | `Finish.InputTokens + max_tokens <= limit` and the executor returned a context-window-exceeded error | The executor’s own signed error contradicts its own signed count | No |

### Step 12. Case 2: context limit

[Status](#implementation-status).

**Common**

- Resolve `--max-model-len` from epoch model args (implement `MaxModelLen` parsing and a per-`(epoch, model)` cache in this tree; #2093 is not present to port). Limit unknown → abstain.
- Kind `CONTEXT_LIMIT` with two evidence variants. The gateway chooses the variant from the chain values; a voter recomputes the branch itself and rejects evidence of the wrong variant.
- **State effect:** a `STRIKE` invalidates like any other. `NONE` has no state effect. A refund for provably-refused requests is a separate decision.

**Branch A — recount input tokens (`RequestEvidence`)**

- Gateway trigger: Finish applied and `Finish.InputTokens + max_tokens > limit` (whether or not the executor produced output).
- Evidence: the request bytes the gateway sent, capped (`MaxRequestEvidenceBytes`), plus model and epoch.
- Voter: request hashes to the record’s `PromptHash`; count prompt tokens with the **host’s MLNode tokenize endpoint** (tokenize only, no generation, no executor dial); discard the request after voting. We expect the MLNode to work and be the same stack as the engine, so there is no separate local renderer pin.
  - **Endpoint missing or failing** (older MLNode): the host votes `NONE` with reason `tokenizer_unavailable`. It never strikes and never counts toward a strike; this is voting against punishing, not abstaining. Counted in a metric.
  - **Capability probe:** the host checks the endpoint at startup and periodically, exposes the result in its status, and logs a WARN when it is missing. Validators are expected to upgrade their MLNodes; until they do, their votes can only protect the executor.
  - `rendered == Finish.InputTokens` → `NONE` (claimed count is right; the refusal is honest if `rendered + max_tokens > limit`).
  - `Finish.InputTokens > rendered` beyond tolerance (default 0, same MLNode stack) → `STRIKE` (input overbilled; this also covers a refusal justified by an inflated count).
  - `Finish.InputTokens < rendered` → `OBSERVED` (underbilled; no harm to the gateway).
- Privacy: this branch **does** fan the prompt out to hosts. It is started only by the escrow owner who owns the prompt, size-capped, never persisted or logged by voters (the vote store keeps the vote, not the request), and disabled for private / TEE sessions where plaintext fan-out is not allowed.

**Branch B — context-window error contradicts the signed count (`ContextErrorEvidence`)**

- Gateway trigger: sum fits the limit but the stream carried a context-window-exceeded error object.
- Evidence: span evidence ([Span evidence](#span-evidence)) whose span contains the error event and ends at a signed frame, plus attached `input_tokens`, `max_tokens`, `model` (each must equal the voter’s own on-chain value, otherwise reject).
- Voter: verify the span; classify the error with the shared classifier (extend it with a pinned `context_length_exceeded` match: error code, with message text as fallback). Then `Finish.InputTokens + max_tokens <= limit` → `STRIKE` (executor signed both the error and a count that makes it impossible); otherwise `NONE`.
- An error outside any signed span is not provable: WARN + `OBSERVED` only, no strike.
- **Scope:** only an error that arrives in-stream, on a record that reached `Finished`. An executor that hides the error behind a pre-stream HTTP 400 leaves no signed evidence. A challenge on that unfinished stream is **out of scope** here. Instead the gateway **reconnects the stream** (reuse the existing retry / hedge path; this step verifies what it does on a 400 context error today, and does not send such a failure to challenge or error-miss as a strike).

**Tests**

| Test | Asserts |
| --- | --- |
| `TestMaxModelLen_ParsesArgs` | Parses governance args |
| `TestChainContextLimits_CachesPerEpochModel` | Cache keyed by epoch and model |
| `TestContextLimit_BranchSelectedByChainValues` | Wrong-variant evidence rejected |
| `TestContextLimitRecount_MatchesFinishIsNone` | Rendered equals Finish → NONE |
| `TestContextLimitRecount_FinishInflatedIsStrike` | Finish above rendered → STRIKE |
| `TestContextLimitRecount_FinishLowIsObserved` | Finish below rendered → OBSERVED |
| `TestContextLimitRecount_RequestMustMatchPromptHash` | Hash mismatch → reject |
| `TestContextLimitRecount_TokenizerUnavailableVotesNone` | Endpoint missing / failing → NONE `tokenizer_unavailable`, never STRIKE |
| `TestHost_TokenizerCapabilityProbe` | Probe at startup and on interval; status and WARN reflect it |
| `TestContextLimitRecount_OldMLNodesCannotReachStrike` | If all hosts lack the endpoint, no strike weight reaches the threshold |
| `TestContextLimitRecount_NoExecutorDial_NoRetention` | Dialer unused; request not stored after vote |
| `TestContextLimitRecount_DisabledForPrivateSession` | Gateway refuses to send the request |
| `TestContextLimitRecount_RequestSizeCap` | Oversize → reject |
| `TestContextLimitError_SignedErrorContradictsFit` | Sum fits + signed error → STRIKE |
| `TestContextLimitError_SumExceedsLimitIsNone` | Sum over limit → NONE |
| `TestContextLimitError_AttachedValuesMustMatchChain` | Edited `input_tokens` / `max_tokens` / `model` → reject |
| `TestContextLimitError_UnsignedErrorIsObservedOnly` | Error outside signed span → OBSERVED |
| `TestContextLimitError_NoPromptInRequest` | Branch B carries no request bytes |
| `TestGateway_PreStream400ContextError_Reconnects` | 400 before the stream → reconnect, no challenge |
| `TestGateway_TriggersContextLimitRecount` | Sum > limit → Branch A |
| `TestGateway_TriggersContextErrorChallenge` | Fits + error → Branch B |
| `TestApplyChallenge_ContextLimitStrikeInvalidates` | Threshold → Invalidated |

**Done when:** tests green; Case 1 steps still green. **Phase C complete.**

---

## Phase D — Case 3 (full validation on the same bus)

Reuse the existing full-validation pipeline. The challenge bus is how the gateway starts the same check and pays for it; protocol-owed validation stays free.

### Step 13. Case 3: validation kind on the bus

[Status](#implementation-status).

- `VALIDATION` on `VerifyChallenge` is a **request for the host to run the normal validation path** (lease, fetch, ML) and submit the usual proposer-signed `MsgValidation` / `MsgValidationVote`. The RPC response is an **ack** (`accepted` \| `reject(cause)`), **not** a `ChallengeVote`: the vote formats differ and a gateway cannot build a `MsgValidation` from a `ChallengeVote`.
- All results apply through existing `MsgValidation` / `StatusChallenged` tallies, so SPRT and Phase B stay one code path. `MsgChallenge` is not used for this kind.
- Protocol-owed / already `Challenged`: free, no gateway fee.
- Structural `NONE` / `OBSERVED` never credit validation passes.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestVerifyChallenge_ValidationKind_RunsValidate` | Validator invoked; usual tx emitted |
| `TestVerifyChallenge_ValidationKind_AckNotVote` | Response is an ack |
| `TestVerifyChallenge_ValidationKind_InvalidOpensChallenged` | Valid=false → Challenged tallies |
| `TestVerifyChallenge_ValidationKind_ValidCreditsPass` | Only ML valid credits a pass |
| `TestChallenge_StructuralNone_NoValidationPass` | Regression from Case 1 |
| `TestVerifyChallenge_Validation_ReusesLeaseRules` | Same lease / eligibility as `validateAsync` |

**Done when:** validation kind works for owed / challenged flows without a fee.

---

### Step 14. Case 3: gateway-paid trigger

[Status](#implementation-status).

- **Prerequisite decision (no fee primitive exists in the tree):** add `Config.ValidationFee` and a tx that debits escrow and marks the inference "validation owed". If "owed" needs a record field it changes the state hash and requires a protocol version bump; decide and document it before coding.
- Gateway API: `RequestValidation(inference_id)` for a `Finished` record not already owed; refuse if balance < fee.
- Then notify assigned / eligible validators with `VerifyChallenge(VALIDATION)`. Executor’s address is ineligible; follow public-assignment rules when that plan is live.
- Idempotent: already Challenged / validated / owed → no second fee.
- Reconcile the **fee** with the settle barrier: a debit must not race escrow settlement.

**Tests**

| Test | Asserts |
| --- | --- |
| `TestGateway_PaidValidation_ChargesFee` | Balance drops by fee |
| `TestGateway_PaidValidation_InsufficientFunds` | No challenge started |
| `TestGateway_PaidValidation_RunsFullValidate` | Outcomes match normal validation |
| `TestGateway_PaidValidation_NoDoubleCharge` | Second request no fee |
| `TestGateway_PaidValidation_VsSettleBarrier` | Debit never lands after settle |
| `TestGateway_ProtocolOwedValidation_NoFee` | Challenged / assigned path free |

**Done when:** tests green. **Phase D / Case 3 complete.**

---

## Cross-cutting rules (every phase)

- **Previous step tests must stay green** before merging the next step.
- **Strike grade exists only from `ChallengeProtocolVersion`** ([Step 2b](#step-2b-protocol-gate-and-settlement-enrichment)); earlier sessions cap everything at `OBSERVED`.
- Structural kinds: **never** dial the executor for payload, carry a prompt **only** for the Case 2 input recount; silence ≠ fault.
- **Only `STRIKE` reaches the chain.** `OBSERVED` and `NONE` are local: WARN log, metric, no tx, no state.
- Every stream-derived judgement uses the **one shared classifier** (`scanSSE` family).
- All new signatures follow [signature-verification.md](./signature-verification.md).
- Chat-frame evidence uses existing `ChatFrameEvidence` / `devshard.chatframe.v1` only; span evidence adds hashes, not a new signature.
- Protocol version: the new tx / RPC requires an approved version bump when Step 2 ships; name it in the Step 2 commit.

## Out of scope (follow-ups)

- A challenge on an **unfinished** stream (pre-stream HTTP 400 hidden by the executor). The gateway reconnects instead.
- Retiring #2088’s fetch path (it is not in this tree; no action needed unless it lands).
- Refund for provably-refused context-overflow requests (separate decision).
- TEE / private-inference packaging ([VALIDATION_PROTOCOL_PROPOSAL.md](./proposals/VALIDATION_PROTOCOL_PROPOSAL.md)).
- Missing-witness `ChallengeDue` messages ([public-validation-assignment-plan.md](./public-validation-assignment-plan.md) residuals).

## Done when (whole plan)

1. Steps 1–5: bus exists; stub kinds only.
2. Steps 6–11: empty chunk and signed-count-vs-span strike from span evidence; Finish-vs-count observes and strikes at ×1.2 on complete streams only; none needs ML, an executor fetch or a prompt. Step 12 recounts input tokens (prompt to voters, tokenize only) or proves a signed-error contradiction (no prompt).
3. Step 12: context limit settled by recounting input tokens or by a signed-error contradiction.
4. Steps 13–14: full validation reachable on the same path; gateway can pay to start it.
