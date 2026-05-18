# Height-sync test scenarios

Authoritative catalog of automated tests that exercise the height-sync
subsystem. Each row maps a test to **what it proves** and the
corresponding section of the protocol spec
([`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)).

This file lives under `devshard/docs/` because tests are part of the
production tree. Development-only design notes live in `devshard/plans/`
and may be deleted once shipped — the canonical record of "what is
expected to work" is **this file** plus the protocol spec.

Legend: ✅ implemented · 🚧 in progress · ⏳ planned · ⏸ deferred to a
later milestone.

---

## Table of contents

1. [Categories and how to run](#1-categories-and-how-to-run)
2. [Unit tests — `devshard/heightsync`](#2-unit-tests--devshardheightsync)
3. [Unit tests — `devshard/transport`](#3-unit-tests--devshardtransport)
4. [In-process e2e — `devshard/testenv/scenarios`](#4-in-process-e2e--devshardtestenvscenarios)
5. [Container e2e — `devshard/testenv/scenarios/container`](#5-container-e2e--devshardtestenvscenarioscontainer)
6. [Planned: Strong mode (LightBlock + VerifyCommit)](#6-planned-strong-mode-lightblock--verifycommit)
7. [Coverage matrix per protocol section](#7-coverage-matrix-per-protocol-section)
8. [Attack-scenario coverage](#8-attack-scenario-coverage)

---

## 1. Categories and how to run

| Tier | Stack | Tag / flag | Typical runtime |
| ---- | ----- | ---------- | --------------- |
| **Unit** | Go packages only | none | < 1 s per package |
| **In-process e2e** | Four `httptest` hosts + static `BlockOracle`s in one process | `-tags=dev` for held-response timing scenarios | seconds |
| **Container e2e** | Docker compose: `heightsyncd`, `mockdapi` SSE, Loki, VictoriaMetrics, four `devshardd` hosts, `devshardctl` | `-tags=testenvci` | minutes |

Default commands (run from `devshard/`):

```bash
# All unit + in-process e2e
go test -count=1 ./heightsync/... ./transport/... ./testenv/scenarios/...

# In-process e2e with held-response variants (Step 5 / E2 / E3 / E8)
go test -tags=dev -count=1 ./testenv/scenarios/ -run '^TestHeightSyncAnchor_E2E_'

# Container e2e (separate driver script)
bash testenv/scripts/run-container-heightsync-e2e.sh
```

Slow-running tests check `testing.Short()` and skip under `-short`.

---

## 2. Unit tests — `devshard/heightsync`

| Test | File | What it proves |
| ---- | ---- | -------------- |
| ✅ `TestAnchorScheduler_SyncTurnSweepK10Slots4` | `anchor_test.go` | Cadence (`K`, `slots_num`) windows are correct and never overlap. |
| ✅ `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn` | `anchor_test.go` | Quiet oracle (no new block within `StaleAfter`) but cached tip ⇒ Anchor with `tip_stale_after_ms` on sync-turn nonces (not Omit). |
| ✅ `TestDecide_LogStaleSyncTurn` | `decide_log_test.go` | Same degraded path; exercises `heightsync: decide` logging (`decide_anchor_stale`). |
| ✅ `TestAnchorScheduler_SessionStartOverridesOmitWindow` | `anchor_test.go` | Session-start hint forces an Anchor on the first envelope. |
| ✅ `TestAnchorScheduler_ForceAnchorOverridesOmitWindow` | `anchor_test.go` | Per-message force flag emits Anchor outside cadence (legacy). |
| ✅ `TestAnchorScheduler_NonceZeroEmitsOmit` / `KZeroDefaultsToTen` / `SlotsZeroDefaultsToOne` / `SlotsOneCollapsesToCadence` / `KEqualsSlotsIsWallToWall` / `KLessThanSlotsIsRejected` | `anchor_test.go` | Boundary conditions for `K` / `slots_num`. |
| ✅ `TestAnchorScheduler_OracleErrorOmitUnlessForced` / `NilOracleHeaderHandling` / `NoOracleHandling` / `TimestampSet` | `anchor_test.go` | Producer behaves correctly on oracle errors. |
| ✅ `TestAnchorScheduler_EscrowForcedWindow` | `anchor_test.go` | Forced sync-turn window (`MsgForceHeightSyncTurn`) emits Anchor across the whole span. |
| ✅ `TestAnchorScheduler_CadenceSwallowTail` | `anchor_test.go` | Forced window that overlaps the next cadence window swallows it (no double Anchor). |
| ✅ `TestDecide_OriginatorPopulatedFromSigner` | `anchor_test.go` | Host scheduler sets `OriginatorSenderID = host_address` when emitting from local oracle. |
| ✅ `TestDecide_OriginatorOmittedInCourierMode` | `anchor_test.go` | Courier user does **not** brand itself as originator on re-emission. |
| ✅ `TestDecide_LazyEmissionOutsideSyncTurn` | `anchor_test.go` | Lazy emit fires outside sync turn when peer-tip cache has a tip not yet propagated to recipient. |
| ✅ `TestDecide_SyncTurnOverridesLastPropagated` | `anchor_test.go` | Cadence always emits, even if peer already has the height. |
| ✅ `TestDecide_LazyEmitDisabledWithoutPropagator` | `anchor_test.go` | Without a `Propagator`, lazy emit stays off (host self-attestation path). |
| ✅ `TestComputeCadenceSwallow_ScenarioC` / `ScenarioD_NoSwallow` | `cadence_test.go` | Cadence-swallow math (proposal §"Forced sync turn"). |
| ✅ `TestLocalOracleSource_ParityWithBlockOracle` / `TestPeerTipOracleSource_StaleWhenCacheEmpty` / `ReturnsFreshTip` | `source_test.go` | `OracleSource` abstraction wraps both local oracle and peer-tip cache. |
| ✅ `TestDecide_PeerTipOracleSource_ColdStart` / `WarmCachePreservesOriginator` / `TestDecide_LocalOracleSource_OracleError` | `source_test.go` | Courier-mode scheduler integrates with `PeerTipOracleSource`. |
| ✅ `TestClassifyInbound_StaleOriginRejected` | `inbound_test.go` | Originator timestamp older than `F` ⇒ `INVALID(stale_origin)`. |
| ✅ `TestClassifyInbound_LazyOutsideSyncTurn` | `inbound_test.go` | Lazy Anchor outside sync-turn windows is `VALID_LAZY_ANCHOR`. |
| ✅ `TestClassifyInbound_CarryForwardInsideSyncTurnIsCadence` | `inbound_test.go` | Carry-forward inside sync-turn is tagged `cadence`, not `lazy`. |
| ✅ `TestClassifyInbound_SyncTurnOmitInvalid` | `inbound_test.go` | Omit on a sync-turn nonce is INVALID. |
| ✅ `TestNonceInSyncTurn_K8Slots4` | `inbound_test.go` | Nonce → sync-turn-window predicate. |
| ✅ `TestAuditRing_AppendsAndListsByPeer` / `BoundedCapacityDropsOldest` / `DefensiveCopy` / `ListPeers` | `audit_test.go` | Bounded per-peer audit ring stores attestations safely. |
| ✅ `TestInboundTrust` | `audit_test.go` | Peer-aligned vs untrusted-peer trust mapping from oracle delta. |
| ✅ `TestConfirm_QuorumThreshold` | `confirmation_test.go` | `(C-quorum)` confirms at `≥ Q` distinct originators within `F`. |
| ✅ `TestConfirm_StaleWhenOracleStale` | `confirmation_test.go` | `Stale()` propagates to every query while oracle is down. |
| ✅ `TestConfirm_FreshnessAndWindowEligibility` | `confirmation_test.go` | `F` and `W_conf` jointly gate originator eligibility. |
| ✅ `TestConfirm_CompactOnTipAdvance` | `confirmation_test.go` | Index compacts on tip advance; ineligible entries dropped. |
| ✅ `TestConfirm_MonotonicityAfterPrune` | `confirmation_test.go` | A confirmed height never flips back to `pending`. |
| ✅ `TestConfirm_IndexUsesOriginatorNotCarrier` | `confirmation_test.go` | Quorum counts originator addresses, never the carrier (user). |
| ✅ `TestConfirm_LateOracleTipBelowH` | `confirmation_test.go` | Carry-forward heights ahead of the receiver's tip still count toward quorum. |
| ✅ `TestQuorumForRoster` | `confirmation_test.go` | Default `Q = ceil(2/3 × N_hosts)`. |
| ✅ `TestSignOrigin_RoundTrip` | `origin_signing_test.go` | Canonical signing input → secp256k1 signature → verify round-trip. |
| ✅ `TestVerifyOrigin_RejectsTamperedHash` | `origin_signing_test.go` | Any change to fields 1–7 invalidates the signature. |
| ✅ `TestVerifyOrigin_RejectsWrongOriginator` | `origin_signing_test.go` | Recovered address must equal `OriginatorSenderID`. |
| ✅ `TestCanonicalOriginBytes_DomainSeparated` | `origin_signing_test.go` | Domain string `heightsync.origin.v1` is bound into the signing input. |

---

## 3. Unit tests — `devshard/transport`

| Test | File | What it proves |
| ---- | ---- | -------------- |
| ✅ `TestEnvelope_OriginatorFields_RoundTrip` | `envelope_test.go` | Protobuf encodes/decodes originator fields 6–7. |
| ✅ `TestMarshalWrappedInferenceRequest_RoundTrip_Anchor` / `_Omit` | `envelope_test.go` | Anchor and Omit envelopes round-trip on the request leg. |
| ✅ `TestMarshalWrappedInferenceResponse_RoundTrip` | `envelope_test.go` | Same for the response leg. |
| ✅ `TestUnwrapInferenceRequestBody_LegacyWholeBodyJSON_OmitsHeightSync` / `Legacy_StdlibJSON` / `DeprecatedJSONEnvelopeRejected` | `envelope_test.go` | Backwards compatibility: legacy bodies parse; deprecated JSON envelopes are rejected. |
| ✅ `TestHeightSyncPeerTips_FreshnessFilter` | `peer_tips_test.go` | `MaxFresh` honours `F`. |
| ✅ `TestHeightSyncPeerTips_PerPeerPropagation` | `peer_tips_test.go` | `ShouldPropagateTo` / `MarkPropagated` are per-recipient and monotonic. |
| ✅ `TestHeightSyncPeerTips_CarryPreservesOriginator` / `CarryOverwritesOriginatorAtSameHeight` / `UpdateBackwardCompatWithoutOriginator` | `peer_tips_test.go` | Carrier never overwrites cached originator metadata. |
| ✅ `TestRecordOriginWithBlob_StoresVerbatimBlob` | `peer_tips_test.go` | Verified response blob is cached verbatim. |
| ✅ `TestMaxFresh_SkipsEntriesWithoutBlob` | `peer_tips_test.go` | With `RequireVerifiedBlob`, only verified entries are returned. |
| ✅ `TestOriginSignedBlobFor_Lookup` | `peer_tips_test.go` | Evidence API returns the cached blob by (originator, height). |
| ✅ `TestObservedHeightNow_CacheEmpty` / `FreshTip` / `NoHeightSync` | `client_test.go` | Courier user's `ObservedHeightNow()` returns `(0, false)` on cold cache and `(H, true)` when a fresh tip exists. |
| ✅ `TestHTTPClient_SeedHeightSync_RecordsOrigin` | `client_test.go` | Optional seed RPC populates the peer-tip cache before the first inference. |
| ✅ `TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit` | `client_test.go` | Outbound Anchor on a sync-turn nonce hits the wire and the audit ring. |
| ✅ `TestHTTPClient_Send_CourierLazyAnchorMarksPropagated` | `client_test.go` | Successful send updates `last_propagated` so the next nonce omits. |
| ✅ `TestHTTPClient_ParseSSE_InboundHeightSyncAudit` | `client_test.go` | Inbound SSE response attestations land in the user audit ring. |
| ✅ `TestClient_ResponseAnchor_VerifiesOriginSignature` | `client_test.go` | User verifies host's `sender_signature` before caching. |
| ✅ `TestClient_ResponseAnchor_DropsOnInvalidSig` | `client_test.go` | Invalid signature ⇒ tip dropped, metric `origin_sig_invalid_total` increments, cache stays cold. |
| ✅ `TestClient_RequestLeg_OmitsSenderSignature` | `client_test.go` | `Carry` strips `sender_signature` on the request leg (asymmetric verification). |
| ✅ `TestServer_Inference_HeightSync_OutboundAnchor` | `server_test.go` | Host emits Anchor on response inside sync turn. |
| ✅ `TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest` | `server_test.go` | Server honours per-request force flag. |
| ✅ `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns` / `MatchNoWarn` | `server_test.go` | Ahead-of-oracle peer tip is held pending; mismatch on later reconciliation logs warn. |
| ✅ `TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits` | `server_test.go` | Host emits Anchor for every response nonce inside an active forced turn. |
| ✅ `TestServer_LazyAnchorAcceptedOutsideSyncTurn` | `server_test.go` | `VALID_LAZY_ANCHOR` accepted outside sync-turn windows. |
| ✅ `TestServer_LazyAnchorInsideSyncTurn_IsCadenceAnchor` | `server_test.go` | Lazy-shaped Anchor inside sync turn is reclassified as cadence. |
| ✅ `TestServer_StaleOriginRejected` | `server_test.go` | Receiver rejects with `reason=stale_origin` past `F`. |
| ✅ `TestServer_ConfirmationView_AfterLazyInbound` | `server_test.go` | Server-side `IsStrictlyConfirmed` reaches `confirmed` via carried originator metadata. |
| ✅ `TestHandleHeightSync_DisabledReturnsNotFound` / `ForcesAnchor` | `server_test.go` | Optional seed RPC (`POST .../height-sync`) is opt-in and returns a forced Anchor on the response. |
| ✅ `TestServer_ResponseAnchor_SignedByHost` | `server_test.go` | Host signs the outbound response Anchor (asymmetric verification — response leg). |
| ✅ `TestServer_RequestLeg_DoesNotVerifyOriginSig` | `server_test.go` | Hosts accept request-leg carry-forward Anchors without inline signatures. |

---

## 4. In-process e2e — `devshard/testenv/scenarios`

Files: `heightsync_anchor_e2e_test.go`, `heightsync_anchor_e2e_courier_test.go`, `heightsync_anchor_e2e_confirm_test.go`, `heightsync_anchor_e2e_origin_sig_test.go`.

Shared cadence: four hosts, `K = 8`, `slots_num = 4`. Initial sync turn `1..4`, periodic `8..11`, `16..19`. Omit nonces `5..7`, `12..15`.

### Cadence and audit trail

| Test | What it proves |
| ---- | -------------- |
| ✅ `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail` | Initial sync turn emits Anchor on `1..4`, Omit on `5..7`, Anchor again on `8..11`; outbound request audit + inbound response audit match; cadence metric counts. |
| ✅ `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts` | Higher tip from one host is carried to the next on the next request. |
| ✅ `TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing` | A dropped response on nonce 1 does not poison subsequent cadence (self-heal on next sync turn). |
| ✅ `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn` | Per-message force flag emits Anchor on an Omit nonce; receiver classifies cadence. |
| ✅ `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits` | Forced sync turn opened via `MsgForceHeightSyncTurn`: hosts MUST Anchor every response across the span, even if the user side omits some requests; audit ring records `force_request_anchor_missing` sentinel on the missing inbounds. |

### Cheating trail and feed health

| Test | What it proves |
| ---- | -------------- |
| ✅ `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash` | User-substituted hash is stored verbatim in the audit ring (evidence for dispute). |
| ✅ `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors` | Oracle stops mid-session: sync turn Omits cleanly (no errors propagated to inference). |
| ✅ `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns` | After recovery, the next sync turn resumes Anchor. |

### Courier mode

| Test | What it proves |
| ---- | -------------- |
| ✅ `TestHeightSyncAnchor_E2E_CourierBootstrap` (E1) | Courier user with no local follower: first request is Omit; first response from each host populates the cache; subsequent requests carry the originator (host) not the user. |
| ✅ `TestHeightSyncAnchor_E2E_PipelinedCourier` (E7) | Concurrent dispatch of a whole sync turn: cold cache ⇒ all Omit; warm cache after first turn ⇒ next turn carries Anchor with originator metadata. |
| ✅ `TestHeightSyncAnchor_E2E_LazyCarryForwardOutsideSyncTurn` (E2) | Cached host tip propagates to one peer per Omit nonce; `last_propagated` prevents re-send to the same recipient. (`-tags=dev` held responses.) |
| ✅ `TestHeightSyncAnchor_E2E_StaleOriginRejected` (E3) | Backdated originator timestamp rejected at receiver with `reason=stale_origin`; audit ring records `dispute_carrier`. (`-tags=dev`.) |
| ✅ `TestHeightSyncAnchor_E2E_HeldOriginatorReplayRejected` (E8) | A 70 s wall-clock hold of a previously valid originator section ⇒ replay rejected; skipped under `-short`; requires `-tags=dev`. |

### Confirmation

| Test | What it proves |
| ---- | -------------- |
| ✅ `TestHeightSyncAnchor_E2E_IsStrictlyConfirmed_Quorum` (E4) | `(C-quorum)`: `Q = 3` of 4 honest hosts confirms; `Q = 5` stays pending; oracle stale ⇒ stale. |
| ✅ `TestHeightSyncAnchor_E2E_MixedHeights_Confirmed` (E5) | Three honest hosts at `H_new`, one cheater at `H_new` with bad hash: confirmation stays at the agreed max height; a single dishonest vote cannot un-confirm. |
| ✅ `TestHeightSyncAnchor_E2E_StaleOracle_Inconclusive` (E6) | Stale shared oracle ⇒ confirmation returns `stale` for every height; downstream consumer treats verdict as `Inconclusive`. |
| ✅ `TestHeightSyncAnchor_E2E_LateOracleHost_ConfirmedViaCourier` (E11 phases A/B/C) | Late follower host A: user reaches `confirmed` against B/C/D; A is `pending`; courier lazily delivers three distinct originators via a strictly-increasing height ladder; A reaches `confirmed` without its follower advancing. |
| ✅ `TestHeightSyncAnchor_E2E_LateOracleHost_StillPendingWithoutPropagate` | Same setup, without propagating to A ⇒ A stays `pending`. |

### Response-leg origin signatures (PoC v2.1)

| Test | What it proves |
| ---- | -------------- |
| ✅ `TestHeightSyncAnchor_E2E_ResponseOriginSignatureVerified` (E9) | Every honest host signs its response Anchor; user verifies and caches verified blobs; `origin_sig_invalid_total` does not increment. |
| ✅ `TestHeightSyncAnchor_E2E_ResponseOriginSignatureInvalidDropped` (E9 variant B) | Server hook flips a sig byte: user drops the tip, increments metric, keeps cache cold. |
| ✅ `TestHeightSyncAnchor_E2E_CarrierExculpation` (E10) | After a sync turn, the user holds a verified signed blob for the originator; `HTTPClient.HeightSyncEvidenceFor(host, h)` returns it; a fresh peer-tip cache without the blob returns `false` (carrier cannot exculpate). |

---

## 5. Container e2e — `devshard/testenv/scenarios/container`

Detailed status and rollout phases live in
[`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md).
Runbook: [`SCENARIOS.md`](../testenv/scenarios/SCENARIOS.md).
Summary mapping to **protocol behaviour proved end-to-end against a
real heightsyncd + mockdapi stack**:

| Phase | Status | Tests | What they prove |
| ----- | ------ | ----- | --------------- |
| Phase A | ✅ | `TestContainerE2E_HeightSync_Cadence` | Cadence from sync-turn lead via wave-parallel bursts (4+3+4+4+1); per-slot Loki + Prom on real stack. |
| Phase B | ✅ | `TestContainerE2E_HeightSync_LostFirstResponse`, `TestContainerE2E_HeightSync_ForceAnchorSingleMessage`, `TestContainerE2E_HeightSync_CheatingTrail` | Same scenarios as in-process, validated against Loki logs and VictoriaMetrics counters. |
| Phase C | ✅ | `TestContainerE2E_HeightSync_FeedStoppedOmits`, `TestContainerE2E_HeightSync_FeedRecovers`, `TestContainerE2E_HeightSync_Smoke` | Oracle outage / recovery proven against real SSE feed. |
| Phase D | ⏳ | Container ports of E1..E11 | Courier-mode + confirmation under container stack. |
| Phase E | ⏳ | Container ports of Strong-mode scenarios (S1..S12 below) | Real `LightBlock` proofs + `D` band against multi-process oracles. |

---

## 6. Planned: Strong mode (LightBlock + VerifyCommit)

Spec: [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)
§"Strong mode" and §"Confirmation API".

Strong mode adds `LightBlock`-equivalent proofs (validator-quorum
binding), the `|Δ| > D` escalation rule, and the `(C-strong)` /
`(C-hybrid)` confirmation rules.

### Unit (planned)

| Test | Package | What it will prove |
| ---- | ------- | ------------------ |
| ⏳ `TestEnvelope_LightBlock_RoundTrip` | `transport/envelope_test.go` | Protobuf field 9 (`light_block`) survives encode/decode. |
| ⏳ `TestEnvelope_StrongProofType_RoundTrip` | `transport/envelope_test.go` | `proof_type = cometbft-light-block-v1` round-trips. |
| ⏳ `TestEnvelope_LightBlock_RejectedForAnchorProofType` | `transport/envelope_test.go` | A non-empty `light_block` with Anchor `proof_type` is rejected. |
| ⏳ `TestIsAnchorSection_AcceptsStrong` | `heightsync/anchor_test.go` | Cadence-emit logic treats Strong as a stricter Anchor. |
| ⏳ `TestBlockOracleStrongSource_RoundTrip` / `_OutsideWindow` / `_CacheRetention` | `heightsync/strong_source_test.go` | Producer cache `[tip − K, tip]` builds and serves proofs. |
| ⏳ `TestVerifyLightBlock_AcceptsCanonical` | `heightsync/strong_test.go` | Honest header + commit verifies. |
| ⏳ `TestVerifyLightBlock_RejectsTamperedSig` | `heightsync/strong_test.go` | A flipped signature byte fails ecrecover. |
| ⏳ `TestVerifyLightBlock_RejectsWrongChainID` | `heightsync/strong_test.go` | `chain_id` must match the pinned set. |
| ⏳ `TestVerifyLightBlock_RejectsBlockIDMismatch` | `heightsync/strong_test.go` | `mainnet_block_hash` must match `hdr.BlockID`. |
| ⏳ `TestVerifyLightBlock_RejectsWrongValidatorsHash` | `heightsync/strong_test.go` | `validators_hash` must match the validator-set Merkle root. |
| ⏳ `TestVerifyLightBlock_RejectsBelowTwoThirds` | `heightsync/strong_test.go` | Accumulated power must be strictly `> 2/3` of total. |
| ⏳ `TestClassify_StrongRequiredOutsideD` | `heightsync/inbound_test.go` | `|H − local_aligned| > D` Anchor ⇒ INVALID (`strong_required`). |
| ⏳ `TestClassify_StrongProofInvalid` | `heightsync/inbound_test.go` | Bad `LightBlock` ⇒ INVALID (`strong_proof_invalid`). |
| ⏳ `TestClassify_StrongCarryForwardRejectedOutsideD` | `heightsync/inbound_test.go` | Carry-forward outside `D` is always INVALID. |
| ⏳ `TestDecide_EscalatesToStrong_WhenAboveD` / `_StaysAnchor_WithinD` / `_ForceStrong_OverridesD` | `heightsync/anchor_test.go` | Producer-side escalation table. |
| ⏳ `TestDecide_ForcedTurn_StrongRequired_Emits` | `heightsync/anchor_test.go` | Forced turns with `StrongRequired = true` emit Strong sections. |
| ⏳ `TestPeerTips_MaxAlignedFor_TracksRecipient` | `transport/peer_tips_test.go` | Per-recipient max-aligned tracking drives producer escalation. |
| ⏳ `TestClient_StrongResponse_VerifiedAndCached` / `_InvalidProofDrops` | `transport/client_test.go` | Courier verifies Strong responses; invalid proofs drop. |
| ⏳ `TestServer_StrongAnchorRecorded` / `_StrongRequired_Rejected` / `_StrongProofInvalid_Rejected` | `transport/server_test.go` | Receiver wiring + audit + metrics. |
| ⏳ `TestServer_LightBlockFor_ReturnsCached` / `_AbsentOutsideWindow` | `transport/server_test.go` | Evidence API serves cached Strong proofs. |
| ⏳ `TestServer_ForcedTurn_StrongRequired_RejectsAnchor` | `transport/server_test.go` | Anchor without `LightBlock` is INVALID under a `StrongRequired` forced turn. |
| ⏳ `TestConfirm_RuleStrong_OneVerifiedLightBlock` | `heightsync/confirmation_test.go` | `(C-strong)` confirms on one verified proof. |
| ⏳ `TestConfirm_RuleStrong_BelowQuorumStillConfirms` | `heightsync/confirmation_test.go` | `(C-strong)` does not require host quorum. |
| ⏳ `TestConfirm_RuleHybrid_QuorumClearsFirst` / `_StrongClearsFirst` | `heightsync/confirmation_test.go` | `(C-hybrid)`: either path confirms. |
| ⏳ `TestConfirm_RuleStrong_MonotonicWithValidatorSetLoss` | `heightsync/confirmation_test.go` | Loss of the pinned validator set does **not** flip an already-confirmed height. |
| ⏳ `TestConfirm_RuleHybrid_StaleOracleDoesNotRegress` | `heightsync/confirmation_test.go` | Stale follower does not regress confirmed heights. |
| ⏳ `TestDeferredFail_StrongEvidence_Exonerates` | `heightsync/strong_test.go` | DEFERRED_FAIL evidence with a Strong-verified canonical header exonerates the carrier. |

### In-process e2e (planned)

Files (planned): `heightsync_strong_e2e_test.go`. Suite prefix: `TestHeightSyncStrong_E2E_*`.

| Test | What it will prove |
| ---- | ------------------ |
| ⏳ `TestHeightSyncStrong_E2E_ColdStartEscalation` (S1) | Peer-aligned height far ahead of receiver ⇒ producer emits Strong on the very first request; receiver classifies `VALID_STRONG`. |
| ⏳ `TestHeightSyncStrong_E2E_NoEscalationInsideD` (S2) | Inside `D = 2`, sender emits Anchor; no Strong overhead. |
| ⏳ `TestHeightSyncStrong_E2E_StrongResponseVerifiedByCourier` (S3) | Host emits Strong on a sync-turn response; courier verifies + caches; next request carries originator metadata at full trust. |
| ⏳ `TestHeightSyncStrong_E2E_TamperedProofRejected` (S4) | Server-hook flips a `light_block` byte ⇒ classification `INVALID(strong_proof_invalid)`; metric increments; tip not cached. |
| ⏳ `TestHeightSyncStrong_E2E_CStrongOneProofConfirms` (S5) | A single verified `LightBlock` clears `(C-strong)` `IsStrictlyConfirmed(H)`. |
| ⏳ `TestHeightSyncStrong_E2E_CHybridEitherPathClears` (S6) | `(C-hybrid)`: quorum-first vs strong-first; monotonicity holds in both orders. |
| ⏳ `TestHeightSyncStrong_E2E_CStrongMonotonicAcrossSetLoss` (S7) | Pinned validator set rotates out: confirmed heights remain confirmed; new heights wait for the new set. |
| ⏳ `TestHeightSyncStrong_E2E_CarryForwardOutsideD_Rejected` (S8) | Carry-forward Anchor outside `D` is INVALID even with valid origin signature; `strong_required_rejected_total` increments. |
| ⏳ `TestHeightSyncStrong_E2E_EpochBoundValidatorSet` (S9) | Optional Step 3b: same bytes presented under wrong epoch's set ⇒ INVALID. |
| ⏳ `TestHeightSyncStrong_E2E_StaleFollowerStillVerifiesStrong` (S10) | Receiver's follower is stale; Strong proof verifies against the pinned set without a live follower; height advances. |
| ⏳ `TestHeightSyncStrong_E2E_DeferredFail_StrongEvidence` (S11) | Receiver's follower later confirms canonical hash differs from carrier's claim; evidence packet carries originator signed blob + receiver `LightBlock` ⇒ mock dispute returns DISPUTE_ORIGINATOR. |
| ⏳ `TestHeightSyncStrong_E2E_ForcedTurnStrongRequired` (S12) | Forced turn with `StrongRequired = true`: receivers reject any envelope that lacks `LightBlock` for the duration of the turn. |
| ⏳ `TestHeightSyncStrong_E2E_DisabledStrong_FallsBackToAnchor` | `D = 0` and no `StrongSource` ⇒ behaviour is identical to Anchor mode. |
| ⏳ `TestHeightSyncStrong_E2E_UnavailableLightBlock_FallsBackOmitOutsideSyncTurn` | Host cannot build proof for `h` (outside cache window) ⇒ scheduler Omits outside sync turn; metric `strong_unavailable_total` increments. |

### Container e2e (planned, Phase E)

| Test | What it will prove |
| ---- | ------------------ |
| ⏳ `TestContainerE2E_HeightSync_StrongEscalation` | One `heightsyncd` is reconfigured to advance ahead; courier escalates; Loki + Prom assertions. |
| ⏳ `TestContainerE2E_HeightSync_StrongProofInvalid` | Mutated proof on the wire ⇒ Loki classification line + counter increment. |
| ⏳ `TestContainerE2E_HeightSync_StrictConfirmStrong` | `devshard_heightsync_confirmed_height_strong` gauge increases monotonically as Strong proofs land. |

---

## 7. Coverage matrix per protocol section

| Protocol section | Implemented tests | Planned tests |
| ---------------- | ----------------- | ------------- |
| Sync modes (Omit / Anchor) | `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail`, `TestAnchorScheduler_SyncTurnSweepK10Slots4` | — |
| Sync modes (Strong) | — | S1, S3, `TestVerifyLightBlock_*` |
| Forced sync turn | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits`, `TestServer_Inference_HeightSync_ForcedTurn_*`, `TestAnchorScheduler_EscrowForcedWindow`, `TestAnchorScheduler_CadenceSwallowTail` | S12 |
| `D` band + escalation | — | `TestClassify_StrongRequiredOutsideD`, `TestDecide_EscalatesToStrong_*`, S1, S8 |
| Carry-forward + originator | E1, E2, `TestHeightSyncPeerTips_Carry*`, `TestDecide_OriginatorOmittedInCourierMode` | — |
| Freshness gate `F` | E3, E8, `TestServer_StaleOriginRejected`, `TestClassifyInbound_StaleOriginRejected` | — |
| Lazy classification | E2, `TestServer_LazyAnchor*`, `TestClassifyInbound_LazyOutsideSyncTurn` | — |
| `(C-quorum)` | E4, E5, E6, E11, `TestConfirm_*` | — |
| `(C-strong)` / `(C-hybrid)` | — | S5, S6, S7, `TestConfirm_RuleStrong*`, `TestConfirm_RuleHybrid*` |
| Asymmetric response signatures | E9, E10, `TestClient_ResponseAnchor_*`, `TestServer_ResponseAnchor_SignedByHost`, `TestSignOrigin_*` | — |
| DEFERRED_FAIL attribution | E10 (exculpation API) | S11 (Strong-grade evidence) |
| `ObservedHeightNow` (cPoC C14) | `TestObservedHeightNow_*` | — |
| Optional seed RPC | `TestHTTPClient_SeedHeightSync_RecordsOrigin`, `TestHandleHeightSync_*` | — |
| Audit ring | `TestAuditRing_*`, all e2e tests | — |
| Stale / quiet oracle | Feed **unavailable**: `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`, E6. Feed **quiet** (cached tip): `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn`, `TestDecide_LogStaleSyncTurn`, container cadence | S10 |

---

## 8. Attack-scenario coverage

| Attack | Mitigation in protocol | Test(s) |
| ------ | ---------------------- | ------- |
| Host signs wrong `(H, hash)` | Audit + `(C-quorum)` rejects a single bad vote; deferred-hash check; with Step 8 + Strong, `DISPUTE_ORIGINATOR` evidence | `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`, E5; ⏳ S11 |
| Carrier strips originator metadata | Sync-turn receiver expects originator on cadence; freshness gate forces fresh attestation; carrier becomes the cryptographic signer (DISPUTE_CARRIER) | `TestHeightSyncPeerTips_Carry*`, E1 (originator ≠ user) |
| Carrier replays old originator section | Freshness gate `F` rejects with `stale_origin`; metric + audit dispute_carrier | E3, E8, `TestServer_StaleOriginRejected`, `TestClassifyInbound_StaleOriginRejected` |
| Carrier sends bogus hash with valid framing | Audit ring keeps verbatim bytes; quorum cannot include the cheater; eventually DISPUTE_ORIGINATOR with stored signed blob | E5, E10, `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash` |
| Host returns invalid `sender_signature` on response | Drop tip + `origin_sig_invalid_total`; no cache, no carry, no slash (reputation handles persistent offenders) | E9 variant B (`…ResponseOriginSignatureInvalidDropped`), `TestClient_ResponseAnchor_DropsOnInvalidSig` |
| User claims height well above its true follower (light path only) | Receiver classifies via `InboundTrust`; without Strong, audited as `untrusted_peer`; with Strong, `\|Δ\| > D` ⇒ INVALID(`strong_required`) | `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`; ⏳ S1, S8 |
| Cross-session originator equivocation | Per-`V` audit ring + downstream dispute layer | `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`; full dispute wiring deferred |
| Lost mainnet feed mid-session | `Latest()` fails ⇒ sync-turn **Omit**; recovery resumes; never crashes. Long block time alone ⇒ **degraded Anchor** (`tip_stale_after_ms`), not Omit | `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`, E6; `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn` |
| Validator-set rotation (Strong) | Pinned + epoch-bound check (Step 3b); monotonic `confirmed` does not regress | ⏳ S7, S9 |
| Tampered `LightBlock` on the wire | Step 2/3/5/6 of CometBFT verification | ⏳ S4, `TestVerifyLightBlock_*` |

---

## Maintenance notes

- When a new test lands, add a row to the appropriate section above
  with a one-line "what it proves". A `(✅ added in PR #...)` suffix is
  optional.
- When a planned test (⏳) lands, flip its status to ✅ and keep the
  row in place — the catalog is the historical record.
- When a test is removed, leave a `Removed in PR #...` strikethrough
  for one milestone, then drop it.
- Protocol-level "what does this mean" lives in
  [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md); do
  not duplicate spec text here.
