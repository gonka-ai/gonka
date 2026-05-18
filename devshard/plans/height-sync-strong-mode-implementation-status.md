# Height-sync Strong mode — implementation status

Tracks progress against
[`height-sync-strong-mode.md`](./height-sync-strong-mode.md)
(`LightBlock` + `VerifyCommit` + `D` band + `(C-strong)` /
`(C-hybrid)` confirmation). Each entry maps a plan step or e2e
scenario to the file(s) implementing it and its verification surface.

This file is a status checklist, **not** a design document — for
design decisions and rationale see the plan and the proposal
([`HEIGHT_SYNC_HEADERS_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md)).

Legend: ✅ implemented · 🚧 in progress · ⏳ not started · ⏸ deferred
(out of scope for this milestone).

---

## 0. Overview

| Area | Status | Notes |
| ---- | ------ | ----- |
| PoC v2 / v2.1 (anchor + courier + (C-quorum) + response signatures) | ✅ | upstream baseline — see [`height-sync-anchor-poc-implementation-status.md`](./height-sync-anchor-poc-implementation-status.md). |
| Strong mode milestone (this plan, §4 steps 1–8) | ⏳ | not started; design only. |
| `(C-strong)` + `(C-hybrid)` confirmation rules | ⏳ | wired through `ConfirmationConfig.Rule`. |
| `D` band enforcement on receivers + producer escalation | ⏳ | `D_default = 2`; configurable per deployment. |
| `LightBlock`-equivalent carrier (`blockoracle.Header` bytes; field 9) | ⏳ | wire field reserved; producer + verifier in tree. |
| Real CometBFT `tendermint.types.LightBlock` decoder | ⏸ | content-only migration after this milestone. |
| Container parity for Strong (`-tags=testenvci`) | ⏸ | follow-on — `CONTAINER_E2E_PLAN.md` §7 Phase E placeholder. |
| `MsgHeightSyncEvidence` on-chain tx + slashing wiring | ⏸ | cPoC / dispute plan; this milestone exposes evidence APIs only. |

---

## 1. Step-by-step (plan §4)

### Step 1 — `HeightSyncSection.LightBlock` + `StrongProofType` on the wire

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/anchor.go` — `LightBlock []byte` on
  `HeightSyncSection`; `StrongProofType = "cometbft-light-block-v1"`
  constant.
- `devshard/proto/devshard/v1/inference_envelope.proto` —
  `bytes light_block = 9;` on `InferenceHeightSyncSection`.
- `devshard/types/inference_envelope.pb.go` — regenerated via
  `make proto`.
- `devshard/transport/envelope.go` — map field 9 in
  `heightSyncToProto` / `heightSyncFromProto`; JSON `light_block` ↔
  base64.
- `devshard/heightsync/inbound.go` — `IsAnchorSection` accepts both
  `AnchorProofType` and `StrongProofType` (Strong is a stricter Anchor).

**Tests:**

- `transport/envelope_test.go::TestEnvelope_LightBlock_RoundTrip`
- `transport/envelope_test.go::TestEnvelope_StrongProofType_RoundTrip`
- `transport/envelope_test.go::TestEnvelope_LightBlock_RejectedForAnchorProofType`
- `heightsync/anchor_test.go::TestIsAnchorSection_AcceptsStrong`

**Verify locally:**

```bash
go test -count=1 ./heightsync/... ./transport/... -run \
  'TestEnvelope_LightBlock|TestEnvelope_StrongProofType|TestIsAnchorSection_AcceptsStrong'
```

### Step 2 — `StrongSource` + producer header cache

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/strong_source.go` (new) — `StrongSource`
  interface, `BlockOracleStrongSource` rolling cache
  `[tip − K, tip]`, `ErrLightBlockUnavailable`.
- `devshard/transport/server.go` — `WithStrongSource(src StrongSource)`
  option; `attachStrongProof(sec, h, src)` mirrors
  `attachResponseOriginSignature`.
- `devshard/transport/client.go` — `ClientConfig.StrongSource`
  optional; producer scheduler consults `StrongSource` when emitting
  Strong sections.

**Tests:**

- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_RoundTrip`
- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_OutsideWindow`
- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_CacheRetention`
- `transport/server_test.go::TestServer_AttachesStrongProof_WhenForce`
- `transport/client_test.go::TestClient_AttachesStrongProof_WhenForce`

### Step 3 — `StrongVerifier` + receiver `D` band

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/strong.go` (new) — `StrongVerifier` interface,
  `PinnedSetStrongVerifier` wrapping
  `devshard/blockoracle/verifier.Verifier`; `EpochResolver` interface
  + `NoopEpochResolver` default. Errors: `ErrStrongChainID`,
  `ErrStrongHeight`, `ErrStrongHash`, `ErrStrongValidators`,
  `ErrStrongCommit`, `ErrStrongStale`.
- `devshard/heightsync/inbound.go` — new result classes
  `ResultValidStrong`, `ResultInvalidStrongRequired`,
  `ResultInvalidStrongProof`; extended
  `ClassifyInboundRequestAnchor` / `ClassifyInboundResponseAnchor`
  with `LocalAligned int64`, `D int64`, `StrongVerifier`.
- Carry-forward Anchor outside `D` returns
  `ResultInvalidStrongRequired` regardless of freshness.

**Tests:**

- `heightsync/strong_test.go::TestVerifyLightBlock_AcceptsCanonical`
- `heightsync/strong_test.go::TestVerifyLightBlock_RejectsTamperedSig`
- `heightsync/strong_test.go::TestVerifyLightBlock_RejectsWrongChainID`
- `heightsync/strong_test.go::TestVerifyLightBlock_RejectsBlockIDMismatch`
- `heightsync/strong_test.go::TestVerifyLightBlock_RejectsWrongValidatorsHash`
- `heightsync/strong_test.go::TestVerifyLightBlock_RejectsBelowTwoThirds`
- `heightsync/inbound_test.go::TestClassify_StrongRequiredOutsideD`
- `heightsync/inbound_test.go::TestClassify_StrongProofInvalid`
- `heightsync/inbound_test.go::TestClassify_StrongCarryForwardRejectedOutsideD`

### Step 4 — Receiver wiring + audit + metrics

**Status:** ⏳

**Files (to add / change):**

- `devshard/transport/server.go::recordInboundAnchorIfAnchor` — handle
  new result classes; set `LightBlockVerified` on the audit entry.
- `devshard/heightsync/audit.go` — `AnchorAttestation.LightBlockVerified bool`;
  `TagStrong AnchorCadenceTag = "strong"`.
- `devshard/heightsync/prom_anchor.go` —
  `devshard_heightsync_strong_anchors_total{direction}`,
  `devshard_heightsync_strong_proof_invalid_total{reason}`,
  `devshard_heightsync_strong_escalation_total{direction}`,
  `devshard_heightsync_strong_required_rejected_total`.
- `devshard/transport/client.go::ingestResponseHeightSync` — Strong
  verify path on responses; drop tip + bump metric on failure.

**Tests:**

- `transport/server_test.go::TestServer_StrongAnchorRecorded`
- `transport/server_test.go::TestServer_StrongRequired_Rejected`
- `transport/server_test.go::TestServer_StrongProofInvalid_Rejected`
- `transport/client_test.go::TestClient_StrongResponse_VerifiedAndCached`
- `transport/client_test.go::TestClient_StrongResponse_InvalidProofDrops`

### Step 5 — Producer escalation (`StrongHint`, `PeerAlignedHeight`)

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/anchor.go` — `DecideHints.StrongHint
  *StrongHint`; `Decide` emits Strong when `|Δ| > D` or `Force = true`
  **and** `StrongSource` is configured.
- `devshard/transport/peer_tips.go` — `MaxAlignedFor(recipient string) int64`.
- `devshard/transport/client.go` — populates `StrongHint.PeerAlignedHeight`
  from `MaxAlignedFor`.
- `devshard/transport/server.go` — host-side `StrongHint` from inbound
  audit + `pendingUntrustedTip`.

**Tests:**

- `heightsync/anchor_test.go::TestDecide_EscalatesToStrong_WhenAboveD`
- `heightsync/anchor_test.go::TestDecide_StaysAnchor_WithinD`
- `heightsync/anchor_test.go::TestDecide_ForceStrong_OverridesD`
- `transport/peer_tips_test.go::TestPeerTips_MaxAlignedFor_TracksRecipient`
- `transport/client_test.go::TestClient_EscalatesToStrong_WhenPeerFarAhead`

### Step 6 — `(C-strong)` and `(C-hybrid)` in `ConfirmationView`

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/confirmation.go` —
  `ConfirmationRule int` (`ConfirmRuleQuorum`, `ConfirmRuleStrong`,
  `ConfirmRuleHybrid`); `ConfirmationConfig.Rule` selector;
  `ConfirmationConfig.StrongOracle` (interface with
  `LightBlockSeen(h int64) bool`); new internal
  `confirmed_heights_strong` set.
- `ConfirmationIndex.RecordStrong(h, originator, observedAt)` upsert.
- `IsStrictlyConfirmed` returns `confirmed` per the three rules from
  the plan §3.6.

**Tests:**

- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_OneVerifiedLightBlock`
- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_BelowQuorumStillConfirms`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_QuorumClearsFirst`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_StrongClearsFirst`
- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_MonotonicWithValidatorSetLoss`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_StaleOracleDoesNotRegress`

### Step 7 — DEFERRED_FAIL + Strong-grade evidence

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/audit.go` — extend `AnchorEvidence` (or sister
  struct) with optional `ReceiverLightBlock []byte`.
- `devshard/transport/server.go` — `LightBlockFor(h int64) ([]byte, bool)`
  returns cached Strong proof when available.
- Mock dispute verifier in tests: checks origin signed blob + receiver
  `LightBlock` (canonical pair).

**Tests:**

- `heightsync/strong_test.go::TestDeferredFail_StrongEvidence_Exonerates`
- `transport/server_test.go::TestServer_LightBlockFor_ReturnsCached`
- `transport/server_test.go::TestServer_LightBlockFor_AbsentOutsideWindow`

### Step 8 — Forced turn `StrongRequired`

**Status:** ⏳

**Files (to add / change):**

- `devshard/heightsync/anchor.go` —
  `EscrowHeightSyncHints.StrongRequired bool`; `Decide` honours it
  when set.
- `devshard/transport/server.go` — reject Anchor (no `LightBlock`)
  during a forced turn whose `StrongRequired == true`.

**Tests:**

- `heightsync/anchor_test.go::TestDecide_ForcedTurn_StrongRequired_Emits`
- `transport/server_test.go::TestServer_ForcedTurn_StrongRequired_RejectsAnchor`

---

## 2. In-process e2e scenarios (plan §5)

All scenarios live in `devshard/testenv/scenarios/`.

| Scenario | Plan ref | Status | Test name |
| -------- | -------- | ------ | --------- |
| S1 — Cold-start Strong escalation when peer far ahead | §5/S1 | ⏳ | `TestHeightSyncStrong_E2E_ColdStartEscalation` |
| S2 — Anchor stays inside `D` | §5/S2 | ⏳ | `TestHeightSyncStrong_E2E_NoEscalationInsideD` |
| S3 — Strong response verified by courier | §5/S3 | ⏳ | `TestHeightSyncStrong_E2E_StrongResponseVerifiedByCourier` |
| S4 — Tampered Strong proof rejected | §5/S4 | ⏳ | `TestHeightSyncStrong_E2E_TamperedProofRejected` |
| S5 — `(C-strong)` confirms on one proof | §5/S5 | ⏳ | `TestHeightSyncStrong_E2E_CStrongOneProofConfirms` |
| S6 — `(C-hybrid)` either path clears | §5/S6 | ⏳ | `TestHeightSyncStrong_E2E_CHybridEitherPathClears` |
| S7 — `(C-strong)` monotonic across validator-set loss | §5/S7 | ⏳ | `TestHeightSyncStrong_E2E_CStrongMonotonicAcrossSetLoss` |
| S8 — Carry-forward outside `D` rejected | §5/S8 | ⏳ | `TestHeightSyncStrong_E2E_CarryForwardOutsideD_Rejected` |
| S9 — Epoch-bound validator set (Step 3b) | §5/S9 | ⏳ | `TestHeightSyncStrong_E2E_EpochBoundValidatorSet` |
| S10 — Stale follower + Strong escalation | §5/S10 | ⏳ | `TestHeightSyncStrong_E2E_StaleFollowerStillVerifiesStrong` |
| S11 — DEFERRED_FAIL upgraded to Strong evidence | §5/S11 | ⏳ | `TestHeightSyncStrong_E2E_DeferredFail_StrongEvidence` |
| S12 — Forced turn with `StrongRequired = true` | §5/S12 | ⏳ | `TestHeightSyncStrong_E2E_ForcedTurnStrongRequired` |
| Neg — Strong disabled (`D = 0`, no source) falls back to Anchor | §5 neg | ⏳ | `TestHeightSyncStrong_E2E_DisabledStrong_FallsBackToAnchor` |
| Neg — Unavailable `LightBlock` outside cache window omits | §5 neg | ⏳ | `TestHeightSyncStrong_E2E_UnavailableLightBlock_FallsBackOmitOutsideSyncTurn` |

### Strong wire & verification (plan §3.1–3.4)

| Concept | On wire? | Verified? |
| ------- | -------- | --------- |
| `proof_type = "cometbft-light-block-v1"` | Yes (Strong only) | Yes — drives Strong classification |
| `light_block` bytes (field 9) | Yes (Strong only) | Yes — `StrongVerifier.VerifyLightBlock` (chain-id, header vs claims, validator-set Merkle, `BlockID`, `>2/3` commit) |
| `validators_hash` inside header | Yes (in `light_block`) | Yes — Merkle root match (Step 3); optional epoch match (Step 3b) |
| `commit.signatures` (ecrecover-bound) | Yes (in `light_block`) | Yes — `blockoracle/verifier.Verifier.Verify` (`>2/3` voting power) |
| `D` band on receivers | n/a | Enforced — `|H_peer − local_aligned| > D` ⇒ Strong required |
| Carry-forward outside `D` | n/a | INVALID (`reason = strong_required`) regardless of freshness |
| `(C-strong)` / `(C-hybrid)` | n/a | `ConfirmationConfig.Rule` selects per-deployment |

### Threat coverage (Strong-mode-specific)

| Threat | Status | Tests |
| ------ | ------ | ----- |
| Peer claims `(H, hash)` far above receiver's aligned height | ⏳ Step 3 + S1, S8 | `TestClassify_StrongRequiredOutsideD`, `TestHeightSyncStrong_E2E_CarryForwardOutsideD_Rejected` |
| Tampered commit signature in `light_block` | ⏳ Step 3 + S4 | `TestVerifyLightBlock_RejectsTamperedSig`, `TestHeightSyncStrong_E2E_TamperedProofRejected` |
| Wrong `validators_hash` (set substitution) | ⏳ Step 3 + S9 | `TestVerifyLightBlock_RejectsWrongValidatorsHash`, `TestHeightSyncStrong_E2E_EpochBoundValidatorSet` |
| `BlockID` mismatch between claim and proof | ⏳ Step 3 | `TestVerifyLightBlock_RejectsBlockIDMismatch` |
| Below-`>2/3` voting power in commit | ⏳ Step 3 | `TestVerifyLightBlock_RejectsBelowTwoThirds` |
| Stale local follower blocks `(C-quorum)` | ⏳ Step 6 + S10 | `TestConfirm_RuleHybrid_StaleOracleDoesNotRegress`, `TestHeightSyncStrong_E2E_StaleFollowerStillVerifiesStrong` |
| Validator-set rotation (epoch change) | ⏳ Step 3 (Step 3b) + S9 | `TestHeightSyncStrong_E2E_EpochBoundValidatorSet` |
| `(C-strong)` confirmation flips back on set loss | ⏳ Step 6 + S7 | `TestConfirm_RuleStrong_MonotonicWithValidatorSetLoss`, `TestHeightSyncStrong_E2E_CStrongMonotonicAcrossSetLoss` |
| Forced sync turn opens without Strong when required | ⏳ Step 8 + S12 | `TestServer_ForcedTurn_StrongRequired_RejectsAnchor`, `TestHeightSyncStrong_E2E_ForcedTurnStrongRequired` |
| DEFERRED_FAIL needs canonical proof for blame | ⏳ Step 7 + S11 | `TestDeferredFail_StrongEvidence_Exonerates`, `TestHeightSyncStrong_E2E_DeferredFail_StrongEvidence` |

### How `(C-strong)` relates to `(C-quorum)` in v2

| Property | `(C-quorum)` (v2) | `(C-strong)` (this milestone) |
| -------- | ----------------- | ----------------------------- |
| Confirmation source | ≥ `Q` distinct **host** originators within `F` | ≥ 1 **mainnet-validator-quorum-verified** `LightBlock` |
| Quorum unit | Subnet host roster | Mainnet validator set (`>2/3` voting power) |
| Replay resistance | Freshness `F` on originator timestamp | Cryptographic commit + canonical fork |
| Failure mode | Suspicion only (audit + reputation) | Slashable evidence (after dispute layer lands) |
| `(C-hybrid)` | n/a | Either path clears; recommended for production |

---

## 3. PoC v2 carry-overs (kept, not redone)

Strong mode does **not** revisit these v2 / v2.1 artefacts; it builds
on top of them.

| v2 area | Where it lives | Why kept |
| ------- | -------------- | -------- |
| Sync-turn cadence (`AnchorScheduler.shouldEmit`) | `devshard/heightsync/anchor.go` | unchanged; Strong is an addition, not a replacement |
| `HeightSyncSection` JSON/protobuf wire | `devshard/transport/envelope.go`, `devshard/proto/devshard/v1/inference_envelope.proto` | extended with field 9 (`light_block`) only |
| Carry-forward on user side (`HeightSyncPeerTips`) | `devshard/transport/peer_tips.go` | API extended (`MaxAlignedFor`); existing helpers retained |
| Audit ring | `devshard/heightsync/audit.go`, `_test.go` | gains `LightBlockVerified` + `TagStrong` |
| Forced-sync-turn plumbing (`MsgForceHeightSyncTurn`) | `devshard/heightsync/anchor.go`, state-machine messages | gains optional `StrongRequired` |
| Prom counters | `devshard/heightsync/prom_anchor.go`, `devshard/transport/*` | unchanged; new Strong counters added in Step 4 |
| `(C-quorum)` confirmation | `devshard/heightsync/confirmation.go` | unchanged; new rules sit alongside |
| Response-leg origin signatures (Step 8 v2.1) | `devshard/heightsync/origin_signing.go` | unchanged; Strong proof is `LightBlock` itself |

---

## 4. Deferred — out of scope for this milestone

| Item | Owner / driver | Notes |
| ---- | -------------- | ----- |
| Migration from `blockoracle.Header` carrier to real `tendermint.types.LightBlock` | separate plan | Wire field stays `bytes`; verifier abstraction localises the switch. |
| Full per-epoch validator-set resolver | FINALIZATION_COLLECTOR work | Step 3b plumbed via `EpochResolver`; default is `NoopEpochResolver`. |
| `MsgHeightSyncEvidence` on-chain tx + slashing | cPoC / dispute plan | Strong delivers evidence APIs; tx wiring is owned downstream. |
| Container parity for Strong (`-tags=testenvci`) | [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md) §7 Phase E | follow-on milestone. |
| Cross-chain Strong (multiple mainnet `chain_id`s in one session) | future revision | Verifier rejects non-matching `chain_id`. |
| Hardening: replay policy on old commits, `local_tip − max_lag_blocks` recency | follow-on hardening plan | `ErrStrongStale` reserved; default `max_lag_blocks` is `0` (no recency check). |

---

## 5. Done definition

This milestone is complete when, for every row in §1 and §2 above:

- Status is ✅.
- Linked test names exist and pass on a clean checkout.
- Files referenced exist in the tree and pass `gofmt` / `go vet` /
  the project's standard lint targets.
- The proposal's `Status` line reads "PoC v2 + Strong mode
  implemented; deferred items: real CometBFT `LightBlock` decoder
  migration, on-chain `MsgHeightSyncEvidence` tx".
- `CONTAINER_E2E_PLAN.md` §7 Phase E is opened (status ⏳, no work
  started) so the container milestone has a tracking row.
- `ConfirmationConfig.Rule` defaults documented; deployments that
  enable Strong opt into `Hybrid` via README guidance.

---

## 6. How to verify locally

```bash
# Unit
go test -count=1 -v ./heightsync/... ./transport/... ./blockoracle/...

# In-process e2e (no docker); held-response timing scenarios use -tags=dev
go test -tags=dev -count=1 -v ./testenv/scenarios -run '^TestHeightSyncStrong_E2E_'

# Strong-specific unit selectors
go test -count=1 ./heightsync/... ./transport/... -run \
  'TestVerifyLightBlock|TestClassify_Strong|TestServer_Strong|TestClient_Strong|TestConfirm_RuleStrong|TestConfirm_RuleHybrid|TestDecide_(EscalatesToStrong|StaysAnchor|ForceStrong|ForcedTurn_StrongRequired)'

# Compile-check container scenarios (sanity; Phase E not started)
go test -tags=testenvci -c -o /dev/null ./testenv/scenarios/container/...
```

---

## 7. Reading order for new contributors

1. **Proposal §"Sync modes"** — what Anchor / Strong / Omit mean on
   the wire.
2. **Proposal §"CometBFT `LightBlock` verification"** — the seven
   verifier steps.
3. **Proposal §"Validation pipeline"** steps 5 + 6 — `D` band +
   Strong classification.
4. **Proposal §"Confirmation API for downstream consumers"** —
   `(C-strong)` / `(C-hybrid)`.
5. **Plan §3** — architecture diff against PoC v2.
6. **Plan §5** — the e2e scenarios; S1, S3, S4, S5 are the smoke
   tests of the milestone.
7. **`devshard/blockoracle/verifier`** — the in-tree primitive used
   by `PinnedSetStrongVerifier`. Strong mode does not bring in a new
   crypto library.
