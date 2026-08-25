# Height-sync test scenarios

Authoritative catalog of automated tests that exercise the height-sync
subsystem. Each row maps a test to **what it proves** and the
corresponding section of the protocol spec
([`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)).

Parameter defaults and constraints: [`height-sync-params.md`](./height-sync-params.md).

This file lives under `devshard/docs/` because tests are part of the
production tree. Development-only design notes may be deleted once
shipped — the canonical record of "what is expected to work" is **this
file** plus the protocol spec.

Legend: ✅ implemented · 🚧 in progress · ⏳ planned · ⏸ deferred to a
later milestone.

---

## Table of contents

1. [Categories and how to run](#1-categories-and-how-to-run)
2. [Unit tests — `devshard/heightsync`](#2-unit-tests--devshardheightsync)
3. [Unit tests — `devshard/transport`](#3-unit-tests--devshardtransport)
4. [In-process e2e — `devshard/testenv/scenarios`](#4-in-process-e2e--devshardtestenvscenarios)
5. [Container e2e — `devshard/testenv/scenarios/container`](#5-container-e2e--devshardtestenvscenarioscontainer)
6. [Planned: block-oracle sourcing and dapi compatibility (D1–D11)](#6-planned-block-oracle-sourcing-and-dapi-compatibility-d1d11)
7. [Planned: log plane — heartbeat, stamps, peer sync, arming, gateway observability (H1–H104)](#7-planned-log-plane--heartbeat-stamps-peer-sync-arming-gateway-observability-h1h104)
8. [Planned: Strong mode (LightBlock + VerifyCommit)](#8-planned-strong-mode-lightblock--verifycommit)
9. [Coverage matrix per protocol section](#9-coverage-matrix-per-protocol-section)
10. [Attack-scenario coverage](#10-attack-scenario-coverage)

Sections 6 and 7 are the planned surface of
[`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md);
its `D*` and `H*` identifiers are carried verbatim so a landing PR can flip
a row from ⏳ to ✅ without re-deriving what it proves. Note that the
plan's implementation phases (A–F) and the container rollout phases in §5
are different numbering schemes.

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

# Log plane (§7): heartbeat / stamps / arming reach into state, user and host
go test -count=1 ./heightsync/... ./state/... ./user/... ./host/...

# Container e2e (separate driver script)
bash testenv/scripts/run-container-heightsync-e2e.sh

# Local dapi backward-compat (mock-dapi stand-ins for api:0.2.15-v5 vs api:0.2.15)
make -C testenv citest-height-sync-dapi-compat
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
| ✅ `TestLocalOracleSource_ParityWithChainOracle` / `TestPeerTipOracleSource_StaleWhenCacheEmpty` / `ReturnsFreshTip` | `source_test.go` | `OracleSource` abstraction wraps both local oracle and peer-tip cache. |
| ✅ `TestDecide_PeerTipOracleSource_ColdStart` / `WarmCachePreservesOriginator` / `TestDecide_LocalOracleSource_OracleError` | `source_test.go` | Courier-mode scheduler integrates with `PeerTipOracleSource`. |
| ✅ `TestClassifyInbound_StaleOriginRejected` | `inbound_test.go` | Originator timestamp older than `F` ⇒ `INVALID(stale_origin)`. |
| ✅ `TestClassifyInbound_LazyOutsideSyncTurn` | `inbound_test.go` | Lazy Anchor outside sync-turn windows is `VALID_LAZY_ANCHOR`. |
| ✅ `TestClassifyInbound_CarryForwardInsideSyncTurnIsCadence` | `inbound_test.go` | Carry-forward inside sync-turn is tagged `cadence`, not `lazy`. |
| ✅ `TestClassifyInbound_SyncTurnOmitInvalid` | `inbound_test.go` | Omit on a sync-turn nonce is INVALID. |
| ✅ `TestNonceInSyncTurn_K8Slots4` | `inbound_test.go` | Nonce → sync-turn-window predicate. |
| ✅ `TestAuditRing_AppendsAndListsByPeer` / `BoundedCapacityDropsOldest` / `DefensiveCopy` / `ListPeers` | `audit_test.go` | Bounded per-peer audit ring stores attestations safely. |
| ✅ `TestInboundTrust` | `audit_test.go` | Peer-aligned vs untrusted-peer trust mapping from oracle delta. |
| ❌ removed | — | Envelope `(C-quorum)` / `ConfirmationIndex` withdrawn (spec §17); use local oracle. |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ✅ `TestQuorumForRoster` | `params_test.go` | Default `Q = ceil(2/3 × N_hosts)` for turn/floor. |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ❌ removed | — | H98 was (C-quorum)-only; freshness still enforced on inbound carry (`inbound.go`). |
| ❌ removed | — | Envelope confirmation index withdrawn (spec §17). |
| ✅ `TestSignOrigin_RoundTrip` | `origin_signing_test.go` | Canonical signing input → secp256k1 signature → verify round-trip. |
| ✅ `TestVerifyOrigin_RejectsTamperedHash` | `origin_signing_test.go` | Any change to fields 1–7 invalidates the signature. |
| ✅ `TestVerifyOrigin_RejectsWrongOriginator` | `origin_signing_test.go` | Recovered address must equal `OriginatorSenderID`. |
| ✅ `TestCanonicalOriginBytes_DomainSeparated` | `origin_signing_test.go` | Domain string `heightsync.origin.v1` is bound into the signing input. |
| ✅ `TestHeartbeatConfig_ValidateRejectsBadOverride` | `params_test.go` | H25: an ack window shorter than the turnover budget, `T_idle ≤ Interval + TurnTimeout`, non-positive durations, and `2·Interval ≥ F` overrides fail `Validate`. The pre-step-4 `D_ack = 2` is now rejected on the shipped schedule and accepted once `block_time` is `8s`. |
| ✅ `TestHeartbeatConfig_Defaults` / `_AckWindowFollowsTheSchedule` | `params_test.go` | H25: `TurnTimeout = 2 · Interval`, and `D_ack` is derived (`⌈(Interval+TurnTimeout)/block_time⌉ + 1`) rather than shipped — a longer interval carries the window with it, slower blocks buy the same wall clock with fewer of them. |
| ✅ `TestHeartbeat_QuietSessionOpensTurn` / `_NoObservedHeightSkips` / `_SpanDispatchAddressesEverySlot` | `heartbeat_test.go` | H1, H3, H4 unit: wall-clock cadence, skip-on-no-height, consecutive span. |
| ✅ `TestHeartbeat_RepeatedSlotClaimsAreNotAQuorum` / `_ExecutorClaimsDischargeCadence` | `heartbeat_test.go` | A turnover needs `Q` **distinct** host claims; executor-stamped responses discharge the cadence just like acks. |
| ✅ `TestHeartbeat_SustainedInferenceFlowNeverHeartbeats` | `user/heightsync_test.go` | H2 over time: four rounds of real inference on an injected clock spanning several `Interval`s emit **zero** heartbeats; stopping the traffic then emits one, so the zeros are load-bearing. |
| ✅ `TestHeartbeat_StalledTurnReopensAfterTurnTimeout` / `_SettledTurnDoesNotWaitOutTurnTimeout` | `heartbeat_test.go` | One unreachable slot cannot silence the cadence forever: the turn is abandoned after `TurnTimeout`, or immediately once the log settles it. |
| ✅ `TestCloseReady_ArmsWithoutAnyTick` | `closeready_test.go` | Arming is lazy on the clock, so a host that stops receiving anything at all — including height — still arms. |
| ✅ `TestRepairBudget_WindowRollsOnElapsedTime` | `repair_budget_test.go` | `R_max` is a per-`Interval` budget measured in elapsed time, not in blocks. |
| ✅ `TestRepairResponderBudget_OnePerTurnSlotAndWindow` | `repair_budget_test.go` | H68 unit: one HEIGHT per `(turn, requester)`, `R_max` per window, already-served pairs survive a refill. |
| ✅ `TestHeartbeat_QuietSessionOpensTurn` / `_NoObservedHeightSkips` / `_SpanDispatchAddressesEverySlot` / `_AckInclusionAndSyncVectorPrevTurn` / `_LiveHostsQuorumCompletes` / `_UnavailableAcksCompleteTurnCarryingTheFloor` | `user/heightsync_test.go` | H1, H3, H4, H6, H7, H24 in-process: `MaybeHeartbeat` span, host mempool acks, quorum complete, blind hosts carrying the floor. |
| ✅ `TestHeartbeat_LoopOpensQuietTurnWithoutCaller` / `_SpanDispatchConcurrentAndContinuesOnError` / `_LoopStopsOnClose` | `user/heightsync_test.go` | H73–H75: cadence loop without the test calling `MaybeHeartbeat`; concurrent non-aborting span; `Close` cancels the ticker. |
| ✅ `TestTurnTracker_OutOfOrderAcksIdenticalRecord` / `_QuorumCompletesTurn` / `_BelowQuorumDegradesNoBlame` / `TestLateAck_DoesNotClearDegraded` / `TestHeightAck_OracleUnavailableCountsTowardQuorum` | `heartbeat_test.go` | H5–H8, H24: turn record, reachability quorum, late acks, `ORACLE_UNAVAILABLE`. |
| ✅ `TestTurnTracker_IngestNextBlockSameStampCompletes` / `_StampPastDeadlineDegrades` | `heartbeat_test.go` | Ingest may tick; lateness is `observed_height > h_req + D_ack` on the ack's own stamp. |
| ✅ `TestTurnTracker_SpanAcrossBlockBoundariesCompletes` | `heartbeat_test.go` | H59: a four-slot span dispatched across three block boundaries completes with no `late` ack and no probe due, and the same span degrades under the pre-step-4 one-block window — so the regression cannot return quietly. |
| ✅ `TestTurnTracker_CompletedTurnIsFinal` | `heartbeat_test.go` | H60: a slot that answered in time re-acks past the deadline; the turn stays `complete` and `completed_at_height` stays where it closed. |
| ✅ `TestTurnTracker_HashlessHeartbeatDoesNotPinTheWindow` | `heartbeat_test.go` | H61: a heartbeat height with no hash is not a stamp (H38), so it cannot pin `h_req` low and make honest acks late. |
| ✅ `TestHeartbeatConfig_FromSnapshotZeroUsesDefaults` / `_InvalidOverlayIsClamped` | `params_test.go` | H62/H63: scheduling knobs overlay, evaluation knobs stay compiled; an invalid overlay clamps to compiled defaults and is counted. |
| ✅ `TestCloseReady_OverlayShortensIdle` / `TestHeartbeat_OverlayShortensCadence` | `host/closeready_test.go`, `user/heightsync_test.go` | H62: a valid overlay changes host arming and session cadence. |
| ✅ `TestTurnTracker_PrunesCompletedTurns` | `heartbeat_test.go` | H65: after many settled turns the turn map stays bounded; `heartbeatAt` and `h_last` survive prune. |
| ✅ `TestTurnTracker_PrunesOpenTurns` | `heartbeat_test.go` | H78: 5 000 unstamped and 5 000 flat-stamped heartbeats leave `TurnCount() ≤ retain+1`. |
| ✅ `TestLogPlane_LateAckAfterTurnPruneAccepted` | `logplane_test.go` | H79: L3 accepts an ack whose heartbeat nonce outlives the pruned turn record. |
| ✅ `TestCheckDiffLogPlane_LongOpenSessionBounded` | `logplane_test.go` | H80: a long open-turn session keeps the turn map at `retain`; log-plane check still succeeds. |
| ✅ `TestRepairProbe_OracleAheadDoesNotDegradeOpenTurn` | `server_repair_test.go` | H81: a host whose oracle is past `D_ack` does not degrade a turn peers still hold open. |
| ✅ `TestHeartbeat_SettleTurnDoesNotFireWhileSMTurnOpen` | `user/heightsync_test.go` | H82: session `SettleTurn` cannot fire while the SM still has the same turn `TurnOpen`. |
| ✅ `TestApplyLocalBestEffort_LogPlaneInvalidFailsBeforeNonce` | `state/heightsync_test.go` | H83: L0 / L1 / L2 invalid height-sync txs fail compose; nonce is not consumed. |
| ✅ `TestApplyLocalBestEffort_LateAckAfterTurnPruneComposesAndApplies` | `state/heightsync_test.go` | H84: a late ack whose heartbeat is in `heartbeatAt` composes and `ApplyDiff`s on a host. |
| ✅ `TestMarkLog_CapacityDropsOldest` | `marks_test.go` | H85: N+1 marks leave at most N retained. |
| ✅ `TestValidateDiff_FailedApplyTxDoesNotLeakMarks` / `_MarksFlushOnlyOnCommit` | `state/heightsync_test.go` | H86: trial apply and applyTx failure leave `HeightSyncMarks()` unchanged; `CommitValidated` flushes. |
| ✅ `TestCheckEnvelopeBinding_RequestLegBlobBounded` | `logplane_test.go` | H87: request-leg L4 blob is the 32-byte canonical digest regardless of HTTP body size. |
| ✅ `TestRecoverSession_HeartbeatContinuesTurnSeq` / `_HeartbeatEmptyStartsAtTurnOne` | `user/recover_test.go` | H88: recovered producer continues `turn_seq`; empty recover still opens turn 1. |
| ✅ `TestLogPlane_L7SameDiffAckSatisfiesVector` | `logplane_test.go` | H66: an ack in the same diff as the next heartbeat satisfies L7 without cloning the tracker. |
| ✅ `TestClassifyInbound_ZeroTimestampCarryForwardIsStale` | `inbound_test.go` | H67: a carry-forward with a zero originator timestamp is `INVALID(stale_origin)`. |
| ✅ `TestLogPlane_AckWithoutVerifierRejected` / `_HeartbeatWithoutVerifierOK` | `logplane_test.go` | H70: L2 fails closed when acks are present and the verifier is nil; heartbeat-only diffs still pass. |
| ✅ `TestLogPlane_OversizedFieldsRejected` | `logplane_test.go` | H71: oversized `peer_seen`, `sync_vector`, and `observed_block_hash` are `INVALID(bad_framing)`. |
| ✅ `TestMissingAcksDue_RequiresWindowClosed` | `repair_budget_test.go` | A repair probe is due only once the whole turnover budget has passed, not one block after `h_req`. |
| ✅ `TestHeartbeat_HashOnlyOracle_TurnCompletes` | `syncstate_test.go` | D9: hash-only oracle (empty Commit) reaches `complete`; `Prove` is not called. |
| ✅ `TestEvaluateSyncStateFromHeader_DoesNotCallLatest` | `syncstate_test.go` | Ack stamp reuses the already-fetched header (same read as the response-leg Anchor). |
| ✅ `TestSignAck_RoundTrip` / `TestCanonicalAckBytes_DomainSeparated` | `ack_signing_test.go` | Domain `heightsync.ack.v1`; field 8 excluded from the signing input. |
| ✅ `TestHost_HeartbeatAck_OwnSlotIntoMempool` / `_WrongSlotSilent` / `_NoHeartbeatNoAck` / `_OracleUnavailableStillRequired` / `_OracleErrorStillRequired` / `_CatchingUp` / `_LagsButClearsSolicitingFloor` / `_OracleStale` / `_AlreadyAppliedDoesNotRereadOracle` / `TestHost_PeerSeenMarksAcksNotHeartbeats` | `host/heightsync_test.go` | E3: ack only for this host's slot; one `Latest()` per exchange; `ORACLE_UNAVAILABLE` still required; the producer's floor basis is `F(ref_nonce + 1)`; `peer_seen` from Diff acks and repair HEIGHT, not sequencer heartbeats. |
| ✅ `TestTurnTracker_AckDeadlineDoesNotWrap` | `heartbeat_test.go` | H100: `HReq + D_ack` saturates; an honest ack at `HReq+1` is not `late`. |
| ✅ `TestLogPlane_TwoHeartbeatsInDiffAckOfFirstAccepted` | `logplane_test.go` | H101: two heartbeats in one Diff; L3 accepts an ack of the first heartbeat's nonce. |
| ✅ `TestHeightSync_RestoreGetDiffsErrorKeepsLastCompletedHeight` | `state/heightsync_snapshot_test.go` | H102: `GetDiffs` error on restore keeps snapshot `h_last`. |

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
| ✅ `TestMaxFresh_SkipsEntriesWithoutBlob` | `peer_tips_test.go` | H76: production `NewHeightSyncPeerTips()` ignores `RecordOrigin` in `MaxFresh` / `Carry`. |
| ✅ `TestMaxFresh_ZeroTimestampIsNotFresh` | `peer_tips_test.go` | H77: a cached origin with both timestamps zero is not fresh. |
| ✅ `TestOriginSignedBlobFor_Lookup` | `peer_tips_test.go` | Evidence API returns the cached blob by (originator, height). |
| ✅ `TestObservedHeightNow_CacheEmpty` / `FreshTip` / `NoHeightSync` | `client_heightsync_test.go` | Courier user's `ObservedHeightNow()` returns `(0, false)` on cold cache and `(H, true)` when a fresh tip exists. |
| ✅ `TestHTTPClient_SeedHeightSync_RecordsOrigin` | `client_heightsync_test.go` | Optional seed RPC populates the peer-tip cache before the first inference. |
| ✅ `TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit` | `client_heightsync_test.go` | Outbound Anchor on a sync-turn nonce hits the wire and the audit ring. |
| ✅ `TestHTTPClient_Send_CourierLazyAnchorMarksPropagated` | `client_heightsync_test.go` | Successful send updates `last_propagated` so the next nonce omits. |
| ✅ `TestHTTPClient_ParseSSE_InboundHeightSyncAudit` | `client_heightsync_test.go` | Inbound SSE response attestations land in the user audit ring. |
| ✅ `TestClient_ResponseAnchor_VerifiesOriginSignature` | `client_heightsync_test.go` | User verifies host's `sender_signature` before caching. |
| ✅ `TestClient_ResponseAnchor_DropsOnInvalidSig` | `client_heightsync_test.go` | Invalid signature ⇒ tip dropped, metric `origin_sig_invalid_total` increments, cache stays cold. |
| ✅ `TestClient_ResponseAnchor_ZeroTimestampNotCached` | `client_heightsync_test.go` | Signed response with both originator timestamps zero is not stored. |
| ✅ `TestClient_RequestLeg_OmitsSenderSignature` | `client_heightsync_test.go` | `Carry` strips `sender_signature` on the request leg (asymmetric verification). |
| ✅ `TestServer_Inference_HeightSync_OutboundAnchor` | `server_heightsync_test.go` | Host emits Anchor on response inside sync turn. |
| ✅ `TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest` | `server_heightsync_test.go` | Server honours per-request force flag. |
| ✅ `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns` / `MatchNoWarn` | `server_heightsync_test.go` | Ahead-of-oracle peer tip is held pending; mismatch on later reconciliation logs warn. |
| ✅ `TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits` | `server_heightsync_test.go` | Host emits Anchor for every response nonce inside an active forced turn. |
| ✅ `TestServer_LazyAnchorAcceptedOutsideSyncTurn` | `server_heightsync_test.go` | `VALID_LAZY_ANCHOR` accepted outside sync-turn windows. |
| ✅ `TestServer_LazyAnchorInsideSyncTurn_IsCadenceAnchor` | `server_heightsync_test.go` | Lazy-shaped Anchor inside sync turn is reclassified as cadence. |
| ✅ `TestServer_StaleOriginRejected` | `server_heightsync_test.go` | Receiver rejects with `reason=stale_origin` past `F`. |
| ❌ removed | — | `ConfirmationView` withdrawn (spec §17). |
| ✅ `TestHandleHeightSync_DisabledReturnsNotFound` / `ForcesAnchor` | `server_heightsync_test.go` | Optional seed RPC (`POST .../height-sync`) is opt-in and returns a forced Anchor on the response. |
| ✅ `TestHandleHeightSync_OmitsSectionOnSignFailure` | `server_heightsync_test.go` | H72: `SignOrigin` failure omits the section rather than shipping an unsigned Anchor-shaped payload. |
| ✅ `TestServer_ResponseAnchor_SignedByHost` | `server_heightsync_test.go` | Host signs the outbound response Anchor (asymmetric verification — response leg). |
| ✅ `TestServer_RequestLeg_DoesNotVerifyOriginSig` | `server_heightsync_test.go` | Hosts accept request-leg carry-forward Anchors without inline signatures. |
| ✅ `TestUser_ForceHeightSyncTurn_AppearsOnlyInTriggerDiff` | `user/heightsync_test.go` | User composer inserts `MsgForceHeightSyncTurn` only on the trigger nonce. |
| ✅ `TestHost_LatestHeight_*` | `host/chainoracle_test.go` | `WithChainOracle` seam: unwired error, nil no-op, live height, error propagation. |

---

## 4. In-process e2e — `devshard/testenv/scenarios`

**Status on this branch:** ✅ Phase B. Files: `heightsync_anchor_e2e_test.go`, `heightsync_anchor_e2e_courier_test.go`, `heightsync_anchor_e2e_confirm_test.go`, `heightsync_anchor_e2e_origin_sig_test.go`. Held-response variants (E2, E3, E8) need `-tags=dev`; E8 skips under `-short`.

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
| ✅ `TestHeightSync_E2E_WideDivergenceNeverBlocksInferences` | Roster at `1000/998/500/5` (`≫ D`) serves twelve nonces over the real HTTP stack: the shipping producers pick stamps the verifier accepts — the slot 995 blocks behind carries `F(m)` rather than its own tip, and nonce 1 confirms *below* its start because the floor is still empty. Every inference reaches `finish`, each host still attests its own tip for the dispute layer, and honest lag draws no attribution. |

### Confirmation

| Test | What it proves |
| ---- | -------------- |
| ❌ removed (was E4) | Envelope `(C-quorum)` withdrawn; local-oracle readiness replaces it for cPoC. |
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

## 5. Container e2e — `citest-height-sync`

On `devshard-testenv` these lived under `testenv/scenarios/container` against
`heightsyncd`. On this branch they are the Makefile target `citest-height-sync`
(`TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run '^TestHeightSync_'`),
against **mock-dapi** `GET /block/latest` (no `heightsyncd`). Default compose
does not set `DEVSHARD_CHAINORACLE_URL`; the citest harness patches only the
generated compose file.

| Phase | Status | Tests | What they prove |
| ----- | ------ | ----- | --------------- |
| Phase A | ✅ | `TestHeightSync_CadenceEmitsAnchor` | First chat is a sync-turn / session-start Anchor (`heightsync: emit` `mode=anchor` in compose logs). `/block/latest` is live (see D6). |
| Phase B | ✅ | `TestHeightSync_LostFirstChunk`; force/session-start covered by A | Lost first SSE chunk still completes with height-sync wired. Cheating-trail mutate hooks remain in-process e2e (catalog §4) — production binaries have no response-mutate hook. |
| Phase C | ✅ | `TestHeightSync_FeedStoppedOmitsThenRecovers` | `docker compose pause mock-dapi` → Omit or degraded Anchor (`tip_stale_after_ms`); unpause recovers a live Anchor. |
| Phase D | ✅ | D1–D11 (unit; D9 is the hash-only heartbeat turn). Container D6 = `TestHeightSync_MockDapiBlockLatest` (0.2.15-v5 / `/block/*`). Container D7 = `TestHeightSync_LegacyDapiChatCompletes` (0.2.15, no `/block/*`). Dapi HTTP mount lives on `ak/height-sync-protocol-dapi`. | Hash-only observer + direct chain + host failover against old dapi (404) and dapi-down (transport). |
| Phase E | ⏳ | Container ports of Strong-mode scenarios (S1..S12, §8) | Real `LightBlock` proofs + `D` band against multi-process oracles. |
| — | ✅ | D7 (§6) | Simulated **old** dapi with no `/block/*` (`TestHeightSync_LegacyDapiChatCompletes`; unit D4/D7). D6 is ✅ `TestHeightSync_MockDapiBlockLatest`. |
| — | ⏳ | H26, H27 (§7.6) | Heartbeat cadence on a quiet compose escrow, and degraded turns with bounded probe traffic when one host is stopped. |
| — | ⏳ | H41, H42 (§7.7) | Gateway `/metrics` and `/v1/debug/heightsync` on a live compose stack: cadence events on a quiet escrow, and `discharged_by_inference` on a busy one. |

---

## 6. Planned: block-oracle sourcing and dapi compatibility (D1–D11)

Plan: [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md)
§7. Height sync consumes `blocks.BlockOracle` and never special-cases
CometBFT in the hot path; these tests prove that a v5 host keeps working
against an **older dapi** (no `/block/*`) and against a **dapi that is
down**, by failing over to the direct-chain adapter in the shape of
`common/chain.NewWithQueryFallback`.

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| D1 | ✅ `TestObserver_ResultBlockToHeader_HashOnly` | `chainoracle/blocks/observer` | Fixture `ResultBlock` maps to `Header` with `BlockHash == Block.Hash()` and an **empty** `Commit.Signatures`. |
| D2 | ✅ `TestDirectChainOracle_PrefersGRPC_FallsBackToRPC` | `chainoracle/blocks/direct` | Adapter over stub gRPC + Comet RPC sets `Latest().BlockHash`, leaves `Commit` empty, prefers gRPC, uses RPC when gRPC is down. |
| D3 | ✅ `TestHostOracle_BlockLatest200_UsesChainOracle` | `chainoracle/blocks/failover`, `heightsync` | With `/block/latest` returning 200 the host uses the chainoracle client and never touches Comet RPC; Anchor still emits. |
| D4 | ✅ `TestHostOracle_BlockLatest404_FallsBackToChain` | `chainoracle/blocks/failover`, `heightsync` | Capability miss (old dapi) falls back to direct chain; the scheduler still emits Anchor. |
| D5 | ✅ `TestHostOracle_DapiAndChainMissing_OmitsAndStale` | `chainoracle/blocks/failover`, `heightsync` | Both sources gone ⇒ Omit; local oracle unavailable for consumers. |
| D6 | ✅ `TestHeightSync_MockDapiBlockLatest` | `testenv/citest` | Current mock-dapi (has `/block/*`, stand-in for `api:0.2.15-v5`) serves advancing heights; v5 stack green without heightsyncd. |
| D7 | ✅ `TestContainerE2E_HeightSync_OldDapiChainOnly` / `TestHeightSync_LegacyDapiChatCompletes` | `chainoracle/blocks/failover`, `testenv/citest` | Simulated old dapi (no `/block/*`, stand-in for `api:0.2.15` from this branch): chat completes; Strong never claimed. |
| D8 | ✅ `TestHostOracle_ProveEndpointAbsent_AnchorUnaffected` | `chainoracle/blocks/failover` | `/block/:height/prove` absent or 501 leaves the Anchor path untouched. |
| D9 | ✅ `TestHeartbeat_HashOnlyOracle_TurnCompletes` | `devshard/heightsync` | A heartbeat turn (§7) over a hash-only direct-chain oracle reaches `complete` without requesting Strong. |
| D10 | ✅ `TestHostOracle_RuntimeFailover_DapiGoesDown` | `chainoracle/blocks/failover` | dapi answered 200, then refuses connections: the next `Latest()` uses direct chain, Anchor still emits, no host restart. |
| D11 | ✅ `TestHostOracle_RuntimeFailback_DapiRecovers` | `chainoracle/blocks/failover` | After the probe interval the next `Latest()` is back on the chainoracle client, again with no restart. |

---

## 7. Planned: log plane — heartbeat, stamps, peer sync, arming, gateway observability (H1–H104)

Spec: [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)
§10–§12, §14 log-plane checks, §17 `(C-turn)`.
Plan: [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md)
§8.

This is the plane that survives replay: `observed_height` inside
`DiffContent`, signed by the user on `MsgHeartbeat` and by a host on
`MsgHeightAck` / stamped inference txs. Nothing here slashes — Phase E
produces **marks**, adjudication lands with Strong.

### 7.1 Cadence, turn record, `(C-turn)`

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H1 | ✅ `TestHeartbeat_QuietSessionOpensTurn` + `TestHeartbeat_QuietSessionWaitsOutIntervalBetweenTurns` | `heightsync` + `user` | Quiet session opens a heartbeat turn within `Interval` of the last turnover, and waits out `Interval` before the next one (unit + `MaybeHeartbeat` in-process). |
| H73 | ✅ `TestHeartbeat_LoopOpensQuietTurnWithoutCaller` | `user` | Production hook: `StartHeartbeatLoop` opens a turn without the test calling `MaybeHeartbeat`. Gateway runtime starts this loop. |
| H74 | ✅ `TestHeartbeat_SpanDispatchConcurrentAndContinuesOnError` | `user` | §10.6: all slot `Send`s start while one host is blocked; a failing send does not drop the remaining heartbeats. |
| H75 | ✅ `TestHeartbeat_LoopStopsOnClose` | `user` | `Close` cancels the ticker; nonce does not keep advancing. |
| H2 | ✅ `TestHeartbeat_BusySessionWithStampsEmitsNone` + `_SustainedInferenceFlowNeverHeartbeats` + `TestHeartbeat_UserOwnStampIsNotATurnover` | `user` | Host-stamped inference traffic discharges the obligation ⇒ **zero** heartbeats, held across several `Interval` crossings on an injected clock, with the traffic-stops tail proving the zero is not vacuous; the user's own stamp is self-signed and discharges nothing. |
| H3 | ✅ `TestHeartbeat_NoObservedHeightSkips` | `heightsync` + `user` | `ObservedHeightNow() == (0,false)` ⇒ no turn claiming a height; skip metric increments. |
| H4 | ✅ `TestHeartbeat_SpanDispatchAddressesEverySlot` | `heightsync` + `user` | `slots_num` consecutive nonces, no ack awaited, every slot addressed exactly once. |
| H5 | ✅ `TestTurnTracker_OutOfOrderAcksIdenticalRecord` | `heightsync` | Acks arriving out of order, several per diff, produce an identical `SyncTurnRecord`. |
| H6 | ✅ `TestTurnTracker_QuorumCompletesTurn` + `TestHeartbeat_LiveHostsQuorumCompletes` | `heightsync` + `user` | `Q` acks ⇒ turn `complete`, `h_last` advances (unit + live hosts). Completion is reachability only; it confirms no height (H53). |
| H7 | ✅ `TestTurnTracker_BelowQuorumDegradesNoBlame` + `TestTurnTracker_StampPastDeadlineDegrades` | `heightsync` | `< Q` counting acks past `D_ack` ⇒ `degraded` with no blame recorded. |
| H8 | ✅ `TestLateAck_DoesNotClearDegraded` | `heightsync` | A late ack counts for height, is tagged `late`, and never clears `degraded`. |
| H59 | ✅ `TestTurnTracker_SpanAcrossBlockBoundariesCompletes` | `heightsync` | The ack window covers the turn it is meant to judge: a four-slot span dispatched across three block boundaries completes, no ack is tagged `late`, and no repair probe is due. The same span under the old one-block window degrades, which is asserted in the same test so the regression cannot come back quietly. |
| H60 | ✅ `TestTurnTracker_CompletedTurnIsFinal` | `heightsync` | The mirror of H8: a slot that answered in time re-acks past the deadline and the turn stays `complete`, with `completed_at_height` frozen where it closed. Without this, attack 22 runs backwards — a late re-ack pulls a settled turn under quorum. |
| H61 | ✅ `TestTurnTracker_HashlessHeartbeatDoesNotPinTheWindow` | `heightsync` | H38's presence rule reaches `h_req`: a height carried with no hash is not a stamp, so it cannot pin the turn's window low and make every honest ack late. |
| H62 | ✅ `TestHeartbeatConfig_FromSnapshotZeroUsesDefaults` + `TestCloseReady_OverlayShortensIdle` + `TestHeartbeat_OverlayShortensCadence` | `heightsync` + `host` + `user` | Scheduling knobs overlay; evaluation knobs stay compiled. A valid overlay shortens host `T_idle` and session `Interval`. |
| H63 | ✅ `TestHeartbeatConfig_InvalidOverlayIsClamped` | `heightsync` | An overlay whose schedule no longer fits the compiled ack window is replaced with compiled defaults; `OverlayClampCount` increments. |
| H64 | ✅ `TestHeightSync_SnapshotRestoreAgreesOnRootAndFloor` | `state` | Snapshot mid forced turn, restore, apply one more diff: root, floor-as-of, and tracker match the never-restarted machine; an L0 reject stays an L0 reject. |
| H65 | ✅ `TestTurnTracker_PrunesCompletedTurns` | `heightsync` | After many turns the turn map stays bounded by `DefaultTurnRetain`; `heartbeatAt` and `h_last` survive prune. L7 and repair still resolve the tail. |
| H78 | ✅ `TestTurnTracker_PrunesOpenTurns` | `heightsync` | 5 000 unstamped and 5 000 flat-stamped heartbeats leave `TurnCount() ≤ retain+1`. |
| H79 | ✅ `TestLogPlane_LateAckAfterTurnPruneAccepted` | `heightsync` | After the turn record is pruned, L3 still accepts an ack whose heartbeat nonce is in `heartbeatAt`. |
| H80 | ✅ `TestCheckDiffLogPlane_LongOpenSessionBounded` | `heightsync` | `AdvanceHeight` over a long open-turn session stays O(`retain`); log-plane check succeeds. |
| H82 | ✅ `TestHeartbeat_SettleTurnDoesNotFireWhileSMTurnOpen` | `user` | A live oracle past `D_ack` does not `SettleTurn` while the SM still holds the same turn `TurnOpen`; `MaxTurnSeq` stays 1. |
| H83 | ✅ `TestApplyLocalBestEffort_LogPlaneInvalidFailsBeforeNonce` | `state` | Compose of an L0-invalid stamp, L1-bad framing, or L2-bad ack fails; nonce is not consumed. A mixed set drops the invalid ack and keeps the heartbeat. |
| H84 | ✅ `TestApplyLocalBestEffort_LateAckAfterTurnPruneComposesAndApplies` | `state` | After turn prune, a late ack whose heartbeat is still in `heartbeatAt` composes and applies on a host that replayed the same log. |
| H85 | ✅ `TestMarkLog_CapacityDropsOldest` | `heightsync` | A mark log of capacity N retains the newest N after N+1 appends. |
| H86 | ✅ `TestValidateDiff_FailedApplyTxDoesNotLeakMarks` + `_MarksFlushOnlyOnCommit` | `state` | `ValidateDiff` of a log-plane-OK diff that then fails `applyTx` leaves marks unchanged; a successful trial flushes only on `CommitValidated`. |
| H87 | ✅ `TestCheckEnvelopeBinding_RequestLegBlobBounded` | `heightsync` | Request-leg L4 stores `CanonicalRequestLegBytes` (32 bytes) regardless of HTTP body size. |
| H88 | ✅ `TestRecoverSession_HeartbeatContinuesTurnSeq` | `user` | After turns 1..3, snapshot, and `RecoverSession`, the next heartbeat is `turn_seq=4` and `sync_vector` describes turn 3. An empty recovered session still opens turn 1. |
| H92 | ✅ `TestHost_BlockedOracleDoesNotHoldMutex` + `TestAnchorScheduler_BlockedOracleDoesNotHoldMutex` | `host` + `heightsync` | A blocked `Latest()` does not hold `host.mu` or the scheduler mutex. |
| H96 | ✅ `TestDecodeMainnetBlockHashHex_OversizedRejected` | `transport` | Hex longer than 64 chars is rejected before `DecodeString`. |
| H97 | ✅ `TestUnwrapInferenceRequestBody_OversizedOriginSigDropped` | `transport` | Field-8 longer than 65 bytes is dropped at unwrap. |
| H100 | ✅ `TestTurnTracker_AckDeadlineDoesNotWrap` | `heightsync` | `HReq = MaxUint64-1`, `D_ack = 10`: honest ack at `HReq+1` is not `late`. |
| H101 | ✅ `TestLogPlane_TwoHeartbeatsInDiffAckOfFirstAccepted` | `heightsync` | Two heartbeats in one Diff; L3 accepts an ack of the first heartbeat's nonce. |
| H102 | ✅ `TestHeightSync_RestoreGetDiffsErrorKeepsLastCompletedHeight` | `state` | `GetDiffs` error on restore keeps snapshot `HeightSyncLastCompletedHeight`. |
| H66 | ✅ `TestLogPlane_L7SameDiffAckSatisfiesVector` | `heightsync` | A heartbeat for turn `S` and an ack for `S-1` in the same diff satisfy L7 without cloning the tracker. |
| H67 | ✅ `TestClassifyInbound_ZeroTimestampCarryForwardIsStale` | `heightsync` | A carry-forward with originator id set and both timestamps zero is `INVALID(stale_origin)`. |
| H76 | ✅ `TestMaxFresh_SkipsEntriesWithoutBlob` | `transport` | Production-shaped `NewHeightSyncPeerTips()` ignores unverified `RecordOrigin` entries in `MaxFresh` and `Carry`. |
| H77 | ✅ `TestMaxFresh_ZeroTimestampIsNotFresh` | `transport` | A verified cache entry with both timestamps zero is not fresh and does not drive `Carry` (cache-side H67). |
| H24 | ✅ `TestHeightAck_OracleUnavailableCountsTowardQuorum` + `TestHost_HeartbeatAck_OracleUnavailableStillRequired` + `TestHeartbeat_UnavailableAcksCompleteTurnCarryingTheFloor` | `heightsync` + `host` + `user` | `ORACLE_UNAVAILABLE` ack is present, required, and **counts** toward `Q` — it carries `F(m)` from the log the host already applies. The self-report is retained, and the slot contributes no envelope Anchor. |
| H25 | ✅ `TestHeartbeatConfig_ValidateRejectsBadOverride` + `_AckWindowFollowsTheSchedule` + `_InvalidOverlayIsClamped` | `heightsync` | `D_ack · block_time ≥ Interval + TurnTimeout`, `T_idle > Interval + TurnTimeout`, positive durations, and `2 · Interval ≤ F` fail fast; the live overlay path calls `Validate` and clamps rather than shipping a config that would fail. |
| H33 | ✅ `TestHeartbeat_StampedBusySessionEmitsNoAcks` | `host` | A busy stamped escrow emits zero heartbeats **and** zero `MsgHeightAck`; acks exist only inside heartbeat turns. |
| H34 | ✅ `TestSeed_SessionOpenStampsNonceOne` | `user` | E9: seed runs before the first outbound diff; nonce 1 `MsgHeartbeat` carries the seeded `(height, hash)`. (`MsgStartInference` stamp is E7.) |
| H35 | ✅ `TestSeed_FanOutSurvivesDeadSlot` | `user` | One slot 404s; the seed still succeeds from another slot and every valid Anchor lands in `HeightSyncPeerTips`. |
| H36 | ✅ `TestSeed_TotalMissDegradesNeverFails` | `user` | All slots unseedable ⇒ session opens normally, `heightsync: seed_missed`, `heartbeat_skipped_no_height` on the first due check; `SendInference` still succeeds. |
| H37 | ✅ `TestSeed_DoesNotAdvanceHLastOrConsumeNonce` | `user` | The seed appends nothing, consumes no nonce, and leaves the heartbeat obligation armed — a seeded session that never works still owes a turn within `Interval`. |
| H38 | ✅ `TestStampPresent_EmptyHashIsAbsent` / `_PresentThenAbsentIsNotRegression` | `heightsync` | Presence keyed on non-empty `observed_block_hash`: a present-then-absent pair is not treated as height 0. L0/L0b skip legs with no claim (wired in E4). |

### 7.2 Verifier checks — L0–L7 and evaluation tiers

The tier split (§14) is itself under test: only pure-`Diff` checks may
invalidate a diff, cross-plane checks record marks, and an oracle check
may defer.

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H9 | ✅ `TestLogPlane_DeterminismAcrossVerifiers` | `state` | Two independently built verifiers on the same diff sequence produce byte-identical `SyncTurnRecord`s and identical L0–L3 / L7 verdicts. |
| H10 | ✅ `TestLogPlane_FabricatedAckRejected` | `heightsync` | User-fabricated ack ⇒ `INVALID(ack_sig_invalid)` (L2). |
| H70 | ✅ `TestLogPlane_AckWithoutVerifierRejected` + `_HeartbeatWithoutVerifierOK` | `heightsync` | Acks with a nil verifier ⇒ `INVALID(ack_sig_invalid)`; heartbeat-only diffs still pass. |
| H71 | ✅ `TestLogPlane_OversizedFieldsRejected` | `heightsync` | Oversized `peer_seen`, `sync_vector`, or `observed_block_hash` ⇒ `INVALID(bad_framing)` (L1 maxima). |
| H11 | ✅ `TestLogPlane_AckCausalityRejected` | `heightsync` | Unknown or mismatched `turn_seq` / `ref_nonce` ⇒ `INVALID(ack_causality)` (L3). |
| H12 | ✅ `TestHeightAck_EnvelopeBindingMismatch` | `transport` | Ack height ≠ `max(own response-leg Anchor, F(m))` ⇒ `DISPUTE_ORIGINATOR` on sight, mark written, no oracle lookup (L4). |
| H12a | ✅ `TestLogPlane_AckLiftDoesNotTripEnvelopeBinding` | `heightsync` | The honest path of L4's asymmetry: an ack at the floor with a lower Anchor beside it draws **no** mark, because that is the producer rule; one block above both the anchor and the floor still marks. Reverting this leg to strict equality would mark every lagging host and leave every other L4 test passing. |
| H13 | ✅ `TestHeartbeat_RequestLegBindingMismatch` | `transport` | Heartbeat height **below** the request-leg section ⇒ `DISPUTE_CARRIER` (L4): the sequencer understates a height its own signed envelope already reports. |
| H103 | ✅ `TestLogPlane_HeartbeatLiftDoesNotTripEnvelopeBinding` | `heightsync` | The request-leg mirror of H12a. A heartbeat lifted to `F(m)` above the sequencer's own section is the producer rule, not a self-contradiction; `F(m)+1` and an understatement both still mark. Strict equality here named every lagging honest sequencer a dispute carrier while catching no attacker — a sequencer inventing a height puts the same number in both fields. |
| H104 | ✅ `TestLogPlane_CarryForwardSectionSkipsHeartbeatBinding` | `heightsync` | A carried peer tip is nobody's first-party read, so the request leg attempts no binding against it and the relayer is not blamed for the originator's number. |
| H13a | ✅ `TestLogPlane_NoEnvelopeSkipsCrossPlaneChecks` | `heightsync` | Catch-up / gossip re-ingest with no envelope skips L4 and L5a; every other verdict is unchanged; an edge mark is not re-derived. |
| H13b | ✅ `TestLogPlane_SectionPresentForOneRecipientOnly` | `transport` | Lazy carry (`last_propagated`) means slot A runs L4 while slot B skips it; both accept the diff. |
| H13c | ✅ `TestLogPlane_HistoricalReplayNoInvalidation` | `heightsync` | Replaying a session whose stamps sit far below the verifier's tip yields **no** `INVALID` — L5a is the only `D`-band check and it is admission-only. |
| H13d | ✅ `TestLogPlane_RefStampBelowFloorRejected` | `heightsync` | A reference height below `F(m)` for its producing nonce ⇒ `INVALID(height_regression)` on every verifier (L0). |
| H13f | ✅ `TestLogPlane_AckBelowFloorRejectedAndLiftAccepted` | `heightsync` | An ack or heartbeat carrying a height below `F(m)` ⇒ `INVALID(height_regression)`: the producer held the log and could have lifted. Lifting to the floor while labelling itself `CATCHING_UP` is accepted, so a lagging host is never forced into an invalid diff. |
| H13g | ✅ `TestLogPlane_ConfirmJudgedAgainstProducingNonce` | `heightsync` | A confirm produced at nonce `m` and landing after another party raised the floor is accepted; the basis is `F(m)`, not the landing floor. Below `F(m)` still fails. |
| H13h | ✅ `TestLogPlane_AckJudgedAgainstRefNonceFloor` | `heightsync` | The ack half of the producing-nonce rule: an ack answering the heartbeat at `r` is judged against `F(r + 1)`, so landing after the floor rose costs an honest host nothing — and a `late` ack (attack 22) is admissible for the same reason. Below its own producing floor still fails. |
| H13i | ✅ `TestFloorIndex_*`, `TestRefStamp_CoversEveryDiffResidentHeight`, `TestRefProducingNonce_PerMessageBasis` | `heightsync` | Floor mechanics: `AsOf` excludes its own nonce, the floor is monotone in nonce, absent stamps are ignored (presence keyed on the hash), a pruned range answers "unknown" rather than a higher floor, and `Clone` isolates trial-apply. Plus the single-semantics rule: every Diff-resident message carries a reference height, and each names its own producing basis. |
| H54 | ✅ `TestFloorIndex_LoneImplausibleClaimDoesNotMoveTheFloor`, `TestFloorIndex_UnaidedRaiseStopsAtWConf`, `TestFloorIndex_QuorumAdmitsTheJumpOneSignerCannot`, `TestFloorIndex_CarriesCannotCorroborate`, `TestFloorIndex_BootstrapSeedsFromCorroborationNotFromTheFirstStamp` | `heightsync` | The raise rule (attack 24b): one signer may lift the floor by at most `W_conf`, a larger jump needs `Q` distinct signers holding the height, and a carried claim — identical to the standing floor, which the producer rule *obliges* a lagging party to write — never corroborates, so an attacker cannot echo itself into a quorum. A bootstrap on mainnet heights still seeds, at the corroborated height rather than at the maximum anyone claimed. |
| H55 | ✅ `TestHeightSyncFloor_ImplausibleClaimIsMarkedAndIgnored` | `state` | The same rule on the consensus path, and the liveness half of it: the poisoned diff still **applies** (L0 asks only `≥ F(m)`), the floor does not follow it, `FLOOR_OUT_OF_BAND` is recorded against the signer at the nonce that carried it, and the next honest heartbeat plus a full start/confirm flow still advance. |
| H56 | ✅ `TestHost_HeartbeatAck_OmitsAStampWhenTheFloorIsOutOfReach` | `host` | The producer escape: a floor more than `W_conf` above the host's own tip is **omitted**, not carried, so one bad height stays under one signature instead of being repeated under every honest one. The ack is still sent, and still reports `CATCHING_UP` — silence would be worse for the roster than a missing height claim. |
| H57 | ✅ `TestHeightSyncFloor_ReorgReturnsToTheLiveBranch`, `TestLogPlane_L6BlamesTheFloorsAuthorForACarriedPair` | `state`, `heightsync` | Reorg recovery (attack 24c) without ever lowering the floor: while the live branch is below `F` every party carries the stale pair and diffs keep applying with **no** marks, and once the live branch passes `F` stamping is first-party again with no new session. L6 is what makes carrying safe — a pair identical to `F(m)` is by construction a carry, so the mark's `Origin` names the floor's author, while a first-party pair has no origin to point at. |
| H58 | ✅ `TestHeightSyncFloor_AdmissionRefusalCannotSplitTheFloor` | `state` | The floor folds applied diffs and nothing else (attack 24d). Two verifiers ingest identical bytes, one of them having refused the exchange at admission (L5a fires for its own follower and for no one else): same `F(m)` at every nonce, same marks, same `INVALID(height_regression)` on the next low stamp. Feeding an admission decision into the floor would split the escrow through a check documented as replay-identical. |
| H13e | ✅ `TestLogPlane_FutureDatedStampDeferredFail` | `heightsync` | `observed_block_hash` belonging to another height ⇒ `DEFERRED_FAIL` once the follower reaches `H`; the pair never confirms (L6). |
| H14 | ✅ `TestHeightAck_FalseSyncedDeferredFail` | `heightsync` | Host claiming `SYNCED` on a stale oracle fails L6 once the follower advances; honest `ORACLE_STALE` carries no penalty. |
| H30 | ✅ `TestLogPlane_PerInferenceHeightOrder` | `heightsync` | `confirm` below `start` is **accepted** (cross-signer: the user carries the roster maximum, the executor its own view); `finish` below `confirm` is `INVALID(height_regression)` (L0b, same executor). |
| H50 | ✅ `TestHeightSyncDivergence_InferenceFlowNeverBlocked` | `state` | A roster spread ~1000 blocks (`≫ D`) runs a full start/confirm/finish flow through `ApplyDiff`: lagging hosts lift to the floor and label themselves `CATCHING_UP`, drawing **no** mark; a raw low tip in `Diff` is refused **without consuming the nonce** so the honest retry lands at the same nonce; a mislabelled `SYNCED` ack still applies, because the log plane has no divergence verdict to reach; and traffic keeps flowing throughout. Divergence must never be a liveness dependency. |
| H51 | ✅ `TestLogPlane_AckJudgedAgainstRefNonceFloor` | `heightsync` | See H13h — a late-landing ack judged against `F(ref_nonce + 1)` rather than the landing floor. |
| H52 | ✅ `TestHeightSyncDivergence_DeadOracleStillCarriesTime` | `state` | The liveness gain from acks carrying reference heights: a host reporting `ORACLE_UNAVAILABLE` echoes `F(m)` from the log it already applies, its ack counts toward turn completion, and it says plainly it is not a height witness. It used to be a hole in the roster's cadence. |
| H53 | ✅ `TestTurnComplete_IsNotAHeightCertificate` | `heightsync` | Turn completion is reachability only; it must not be treated as a height certificate. |

### 7.3 Stamps on existing txs, signature coverage, evidence retention

The trap these pin down: a stamp must sit inside the signature of its own
producer (§10.5.1), which is automatic for `proposer_sig` and **not**
automatic for `executor_sig`.

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H28 | ✅ `TestConfirmStart_TamperedObservedHeightFailsExecutorSig` | `state` | Altering `MsgConfirmStart.observed_height` after signing ⇒ `ErrInvalidExecutorSig`, proving the height really is inside `ExecutorReceiptContent`. |
| H29 | ✅ `TestFinishInference_StampCoveredByProposerSig` | `state` | A stamp on `MsgFinishInference` is covered with no signing-code change; tampering ⇒ `ErrInvalidProposerSig`. |
| H31 | ✅ `TestApply_RecordCarriesStampHeights` | `state` | `started_at_height` / `confirmed_at_height` land on the record from the stamps, and `post_state_root` differs from an unstamped run. |
| H32 | ✅ `TestMarks_RequestLegEvidenceVerifiesOffline` | `heightsync` | A retained request-leg mark (32-byte digest, sig, ts, escrow_id) recovers the user's address and shows section ≠ stamp, long past the ±30 s admission window. |
| H72 | ✅ `TestHandleHeightSync_OmitsSectionOnSignFailure` | `transport` | `SignOrigin` failure omits the response section rather than shipping an unsigned Anchor-shaped payload. |

### 7.4 Peer sync status and repair probe (§11)

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H15 | ✅ `TestSyncVector_AckedContradictsLog` | `heightsync` | `ACKED(j,h,n)` with no ack at `Diff[n]` ⇒ user-attributable mark, still no `INVALID`. |
| H16 | ✅ `TestRepairProbe_HeightNoBlame` | `heightsync` | `MISSING` + no ack + a later probe returning `HEIGHT` ⇒ **no** mark and **no** `USER_CHEATING`; the omission stays unattributed. (E4 lands the negative; probe itself is E5.) |
| H17 | ✅ `TestRepairProbe_UnreachableOrHeight` | `transport` | Missing ack past `D_ack` with a live peer: probe returns `HEIGHT`, height is ingested, `peer_seen` bit set, turn stays `degraded`. Window closed by a log-resident stamp, not the prober's oracle. |
| H81 | ✅ `TestRepairProbe_OracleAheadDoesNotDegradeOpenTurn` | `transport` | Two hosts apply the same diffs; A's oracle is `HReq+D_ack+1`. After `MaybeRepair` on A both trackers stay `TurnOpen` and no probe is sent. |
| H18 | ✅ `TestRepairProbe_DeadPeerBacksOff` | `transport` | Dead peer ⇒ `UNREACHABLE`, local record and backoff only, nothing on the wire toward the user. |
| H19 | ✅ `TestRepairProbe_BudgetAndStagger` | `heightsync` | One probe per `(turn, slot)` per prober, `R_max` cap per `Interval` of elapsed time, deterministic stagger so late probers skip once the ack lands. |
| H68 | ✅ `TestHandleHeightSyncRepair_FloodBoundsOracleReads` + `TestRepairResponderBudget_OnePerTurnSlotAndWindow` | `transport` + `heightsync` | A flood from one peer spends one oracle `Latest()` per `(turn, requester)`; extras are `429`; no marks. |
| H69 | ✅ `TestHandleHeightSyncRepair_UnknownTurnSkipsOracle` | `transport` | A signed probe naming a turn the responder has no record of is `404` with zero oracle reads. |
| H20 | ✅ `TestRepairProbe_ArmedHostStopsProbing` | `heightsync` | An armed host stops probing entirely. |
| H93 | ✅ `TestRepairBudget_PruneBoundsMap` | `heightsync` | After retain+ extra turns, `probed` / `served` stay O(retain × slots). |
| H94 | ✅ `TestRepairDueAll_IncludesDegradedOlderTurn` + `TestRepairProbe_DegradedOlderTurnStillProbed` | `heightsync` + `transport` | A degraded turn `s` missing slot `j` is still probed after turn `s+1` has opened. |
| H95 | ✅ `TestRepairBudget_SleepRespectsCancel` + `TestRepairProbe_CancelInterruptsSleep` | `heightsync` + `transport` | A cancelled context returns from `Sleep` / `MaybeRepair` before the stagger elapses. |

### 7.5 Close-ready arming (§12)

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H21 | ✅ `TestCloseReady_ArmsAfterIdle` | `host` | User silence past `T_idle` arms the host and emits **nothing** on the wire. |
| H22 | ✅ `TestCloseReady_DisarmsOnContact` | `host` | Contact disarms; the `[armed_at, disarmed_at)` interval is retained (level-triggered, not monotone). |
| H23 | ✅ `TestCloseReady_MinorityCannotClose` | `host` | A partitioned armed minority produces no vote and no tx; closing still needs finalization quorum. |

### 7.6 Container (log plane)

| ID | Test (planned name) | What it will prove |
| -- | ------------------- | ------------------ |
| H26 | ⏳ `TestContainerE2E_HeightSync_QuietEscrowHeartbeat` | Quiet compose escrow: heartbeat cadence visible in Loki, `(C-turn)` confirms, **zero** probe traffic on the healthy path. |
| H27 | ⏳ `TestContainerE2E_HeightSync_OneHostStopped` | One host stopped: degraded turns, bounded probe traffic, arming only after `T_idle` of user silence. |

### 7.7 Gateway observability (plan §8.12.1–§8.12.6)

All of these gather at the **gateway** and assert on its Prometheus registry or on
`GET /v1/debug/heightsync` — never on a host scrape and never on a new wire
field. Package `devshard/cmd/devshardctl` unless noted.

| ID | Test (planned name) | What it will prove |
| -- | ------------------- | ------------------ |
| H39 | ⏳ `TestGatewayHeightSync_DivergenceSpreadAndLag` | Two hosts claim `H`, one claims `H − 5` ⇒ `height_spread = 5`, per-slot lag `0/0/5`, leader lag `0`. |
| H40 | ⏳ `TestGatewayHeightSync_StaleClaimDropsFromSpread` | A host that stops acking loses its `host_height` series past `F` and raises `host_claim_age_seconds`; spread does **not** silently shrink to the live pair. |
| H41 | ⏳ `TestGatewayHeightSync_QuietCadenceEventsAndRing` | Quiet escrow: `cadence_events_total{event="heartbeat_opened"}` advances ~once per `Interval`; the ring holds one ordered entry per turn. |
| H42 | ⏳ `TestGatewayHeightSync_InferenceDischargeIsVisible` | Busy stamped escrow: events are `discharged_by_inference`, `heartbeat_opened` stays **zero**, and the ring records the substitution explicitly rather than as an absence. |
| H43 | ⏳ `TestGatewayHeightSync_AbandonedTurnCounted` | One unreachable slot: `turns_abandoned_total` rises, ring entries carry `turn_abandoned`, cadence keeps running. |
| H44 | ⏳ `TestGatewayHeightSync_BucketSealsAfterDAck` | Acks stamped `H` arriving while the tip is `H + 1` land in bucket `H`; the bucket is not published until sealed, so no fake zero for `H`. |
| H45 | ⏳ `TestGatewayHeightSync_BlockWithoutAnchorCounted` | A sealed height with no anchor increments `blocks_without_anchor_total` exactly once. |
| H46 | ⏳ `TestGatewayHeightSync_PeerSeenMatrix` | 4-slot roster with slot 2 invisible to slot 0 only: `peer_seen{observer=0,subject=2}=0`, `unseen_total{subject=2}=1`, `peer_seen_count{observer=0}=3`. |
| H47 | ⏳ `TestGatewayHeightSync_SettleDropsEverySeries` | After settle, **no** series carries that `devshard_id` — asserted by label value, not a per-series list, so a later addition that misses cleanup fails here. |
| H48 | ⏳ `TestGatewayHeightSync_PeerMatrixOptIn` | Matrix off by default ⇒ quadratic series absent while linear `peer_seen_count` / `unseen_total` remain and the matrix is still readable on `/v1/debug/heightsync`. |
| H49 | ⏳ `TestGatewayHeightSync_ArmingPredictionIsInert` | Gateway quiet toward one slot past `T_idle` ⇒ `arming_predicted{slot}=1`, and a limiter/closing test double records **zero** calls — the prediction must never drive a decision. |

`heightsync_close_ready_armed` is deliberately **not** in this group: arming emits
nothing on the wire (§12.2), so the gateway cannot observe it. H21 covers it
host-side. H49's `arming_predicted` is the gateway's view of its **own** silence —
an early warning on a different clock, not a mirror of any host's flag.

---

## 8. Planned: Strong mode (LightBlock + VerifyCommit)

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
| ❌ cancelled | — | Envelope confirmation rules withdrawn; Strong is optional LightBlock verification, not `IsStrictlyConfirmed`. |
| ❌ cancelled | — | Envelope confirmation rules withdrawn; Strong is optional LightBlock verification, not `IsStrictlyConfirmed`. |
| ❌ cancelled | — | Envelope confirmation rules withdrawn; Strong is optional LightBlock verification, not `IsStrictlyConfirmed`. |
| ❌ cancelled | — | Envelope confirmation rules withdrawn; Strong is optional LightBlock verification, not `IsStrictlyConfirmed`. |
| ❌ cancelled | — | Envelope confirmation rules withdrawn; Strong is optional LightBlock verification, not `IsStrictlyConfirmed`. |
| ⏳ `TestDeferredFail_StrongEvidence_Exonerates` | `heightsync/strong_test.go` | DEFERRED_FAIL evidence with a Strong-verified canonical header exonerates the carrier. |

### In-process e2e (planned)

Files (planned): `heightsync_strong_e2e_test.go`. Suite prefix: `TestHeightSyncStrong_E2E_*`.

| Test | What it will prove |
| ---- | ------------------ |
| ⏳ `TestHeightSyncStrong_E2E_ColdStartEscalation` (S1) | Peer-aligned height far ahead of receiver ⇒ producer emits Strong on the very first request; receiver classifies `VALID_STRONG`. |
| ⏳ `TestHeightSyncStrong_E2E_NoEscalationInsideD` (S2) | Inside `D = 2`, sender emits Anchor; no Strong overhead. |
| ⏳ `TestHeightSyncStrong_E2E_StrongResponseVerifiedByCourier` (S3) | Host emits Strong on a sync-turn response; courier verifies + caches; next request carries originator metadata at full trust. |
| ⏳ `TestHeightSyncStrong_E2E_TamperedProofRejected` (S4) | Server-hook flips a `light_block` byte ⇒ classification `INVALID(strong_proof_invalid)`; metric increments; tip not cached. |
| ⏳ `TestHeightSyncStrong_E2E_VerifiedLightBlockAccepted` (S5) | A verified `LightBlock` is accepted as cryptographic height evidence (not envelope quorum). |
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

## 9. Coverage matrix per protocol section

| Protocol section | Implemented tests | Planned tests |
| ---------------- | ----------------- | ------------- |
| Sync modes (Omit / Anchor) | `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail`, `TestAnchorScheduler_SyncTurnSweepK10Slots4` | — |
| Sync modes (Strong) | — | S1, S3, `TestVerifyLightBlock_*` |
| Forced sync turn | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits`, `TestServer_Inference_HeightSync_ForcedTurn_*`, `TestAnchorScheduler_EscrowForcedWindow`, `TestAnchorScheduler_CadenceSwallowTail` | S12 |
| `D` band + escalation | — | `TestClassify_StrongRequiredOutsideD`, `TestDecide_EscalatesToStrong_*`, S1, S8 |
| Carry-forward + originator | E1, E2, `TestHeightSyncPeerTips_Carry*`, `TestDecide_OriginatorOmittedInCourierMode` | — |
| Freshness gate `F` | E3, E8, `TestServer_StaleOriginRejected`, `TestClassifyInbound_StaleOriginRejected` | — |
| Lazy classification | E2, `TestServer_LazyAnchor*`, `TestClassifyInbound_LazyOutsideSyncTurn` | — |
| Envelope `(C-quorum)` | **withdrawn** (§17) | — |
| Strong (`LightBlock`) | — | S5–S7 (optional cryptographic height; not envelope quorum) |
| Asymmetric response signatures | E9, E10, `TestClient_ResponseAnchor_*`, `TestServer_ResponseAnchor_SignedByHost`, `TestHandleHeightSync_OmitsSectionOnSignFailure`, `TestSignOrigin_*` | — |
| DEFERRED_FAIL attribution | E10 (exculpation API), H13e, H14 | S11 (Strong-grade evidence) |
| `ObservedHeightNow` (cPoC C14) | `TestObservedHeightNow_*` | — |
| Optional seed RPC | `TestHTTPClient_SeedHeightSync_RecordsOrigin`, `TestHandleHeightSync_*` | H34–H37 (E9 session-open seed, plan §8.5.1) ✅ |
| Audit ring | `TestAuditRing_*`, all e2e tests | — |
| Stale / quiet oracle | Feed **unavailable**: `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`, E6. Feed **quiet** (cached tip): `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn`, `TestDecide_LogStaleSyncTurn`, container cadence | S10 |
| §7 wire format — envelope is one plane only | `TestEnvelope_*`, `TestUnwrapInferenceRequestBody_*`, H96, H97 | — |
| §10.1–§10.3 heartbeat cadence + obligation | H1, H2, H3, H4, H25, H62, H63, H73, H75 (unit + in-process + loop) | — |
| §10.4 `MsgHeartbeat` / `MsgHeightAck` wire + binding | field-number + ack signing unit + `TestHost_HeartbeatAck_*`, H10, H11, H12, H12a, H13, H33, H88 (recovered `turn_seq` is monotonic), H103–H104 (both L4 legs use the producer rule) | — |
| §10.5 `observed_height` in the log | H6 (turn complete), H9, H13d, H13f–i (reference height vs own tip), H50 (divergence never blocks a leg), H34 (seeded nonce 1 heartbeat), H38 (absent ≠ 0) | — |
| §10.5.1 stamp inside its producer's signature | H28 (`executor_sig` mirror), H29 (`proposer_sig` automatic) | — |
| §10.5.2 derived record heights / logical time | H31, H64 (snapshot restore rebuilds floor and tracker) | switching the consumers is a later milestone |
| §10.6 async fan-out | H4, H5, H74 (in-process span + concurrent dispatch) | — |
| §10.7 turn record + completion | H5, H6, H7, H8, H59, H60, H61, H65, H78, H82 (unit + live host) | — |
| §11.1 `sync_vector` honesty | H4 ack-inclusion / prev-turn vector, H15, H16, H66 | — |
| §11.2 `sync_state` + `peer_seen` | H24 (unit + live host), `TestHost_HeartbeatAck_OwnSlotIntoMempool`, H14, H17 | — |
| §11.3–§11.4 repair probe + budgets | H16 (no-blame negative), H17, H18, H19, H20, H68, H69, H81, H93–H95 | — |
| §12 close-ready arming | H21, H22, H23 | — |
| §14 log-plane checks L0–L7 | H9–H16, H12a, H13a–i, H30, H50–H53, H70, H71, H79, H80, H83, H84 | — |
| §14 floor raise rule, reorg recovery, applied-log-only fold | H54, H55, H56, H57, H58 | — |
| §14 evaluation tiers (what may invalidate) | H13a, H13b, H13c | — |
| §15 signature layers + mark retention | E10 (exculpation), H32, H85, H86, H87 | — |
| §17 turn ≠ height; envelope `(C-quorum)` withdrawn | H53 `TestTurnComplete_IsNotAHeightCertificate`, H6 / H24 | — |
| Block-oracle abstraction + dapi compatibility | D1–D11 | — |

---

## 10. Attack-scenario coverage

| Attack | Mitigation in protocol | Test(s) |
| ------ | ---------------------- | ------- |
| Host signs wrong `(H, hash)` | Audit + deferred-hash check; `DISPUTE_ORIGINATOR` evidence; cPoC slash via verifier vote quorum on local oracles | `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`; ⏳ S11 |
| Carrier strips originator metadata | Sync-turn receiver expects originator on cadence; freshness gate forces fresh attestation; carrier becomes the cryptographic signer (DISPUTE_CARRIER) | `TestHeightSyncPeerTips_Carry*`, E1 (originator ≠ user) |
| Carrier replays old originator section | Freshness gate `F` rejects with `stale_origin`, including a missing timestamp on carry-forward; metric + audit dispute_carrier | E3, E8, `TestServer_StaleOriginRejected`, `TestClassifyInbound_StaleOriginRejected`, H67, H77 |
| Carrier sends bogus hash with valid framing | Audit ring keeps verbatim bytes; quorum cannot include the cheater; eventually DISPUTE_ORIGINATOR with stored signed blob | E5, E10, `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash` |
| Host returns invalid `sender_signature` on response | Drop tip + `origin_sig_invalid_total`; no cache, no carry, no slash (reputation handles persistent offenders) | E9 variant B (`…ResponseOriginSignatureInvalidDropped`), `TestClient_ResponseAnchor_DropsOnInvalidSig` |
| User claims height well above its true follower (light path only) | Receiver classifies via `InboundTrust`; without Strong, audited as `untrusted_peer`; with Strong, `\|Δ\| > D` ⇒ INVALID(`strong_required`) | `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`; ⏳ S1, S8 |
| Cross-session originator equivocation | Per-`V` audit ring + downstream dispute layer | `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`; full dispute wiring deferred |
| Lost mainnet feed mid-session | `Latest()` fails ⇒ sync-turn **Omit**; recovery resumes; never crashes. Long block time alone ⇒ **degraded Anchor** (`tip_stale_after_ms`), not Omit | `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`, E6; `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn` |
| Validator-set rotation (Strong) | Pinned + epoch-bound check (Step 3b); monotonic `confirmed` does not regress | ⏳ S7, S9 |
| Tampered `LightBlock` on the wire | Step 2/3/5/6 of CometBFT verification | ⏳ S4, `TestVerifyLightBlock_*` |
| User never heartbeats on a quiet session (spec attack 14) | Heartbeat is mandatory in *height* cadence; after `T_idle` served-nothing hosts arm close-ready and finalization closes via `USER_TIMEOUT` | ✅ H1, H21 |
| User omits a host's ack, or the host never sent one (15) | `Diff` cannot distinguish the two; the probe fetches a height so alignment continues but attributes nothing; arming keys on user **silence** | ✅ H16, H17, H18 |
| `sync_vector` contradicts the log (16) | The vector is covered by the user's diff signature, so `ACKED` against a log without that ack is self-contradiction; other statuses stay inconclusive | ✅ H15 |
| Host never acks (down / broken oracle / refusing) (17) | Turn goes `degraded` identically for every verifier; probe returns `HEIGHT` or `UNREACHABLE`; omission unattributed either way | ✅ H7 (live unavailable), ✅ H17, H18 |
| Host's ack contradicts its own response Anchor (18) | L4 self-contradiction under one identity ⇒ `DISPUTE_ORIGINATOR` on sight, no oracle lookup, mark persisted verbatim | ✅ H12 |
| Host reports `SYNCED` with a stale oracle (19) | L6 reconciliation ⇒ `DEFERRED_FAIL`; the honest alternatives carry no penalty, so lying is strictly worse | ✅ H14 |
| Repair-probe amplification (20) | One probe per `(turn, slot)` per prober, one HEIGHT per `(turn, requester)` per responder, `R_max` per `Interval` both sides, unknown-turn reject before the oracle, stagger, backoff, zero probes on the healthy path, armed hosts stop | ✅ H19, H20, H68, H69 |
| Partitioned minority tries to close a healthy escrow (21) | Arming emits nothing; closing needs finalization's `2f + 1`; unarmed hosts reject `USER_TIMEOUT` | ✅ H23 |
| Drip-fed late acks to fake a complete turn (22) | Late acks count for height only, never clear `degraded`, and never clear `complete` either — a settled turn is history in both directions. Lateness is judged on the ack's own host-signed stamp, which the sequencer cannot backdate; arming keys on `last_signal_height` toward this host | ✅ H8, ✅ H60 |
| Sequencer rewrites a host's stamp on `MsgConfirmStart` (23) | The pair lives in `ExecutorReceiptContent` and is copied into the rebuilt content before recovery, so any edit fails `executor_sig` | ✅ H28 |
| Stamp regression to widen a band or backdate a duration (24) | L0 against `F(m)` for every Diff-resident height, L0b within one executor; both pure functions of `Diff` | ✅ H13d, ✅ H13f, ✅ H13g, ✅ H13h, ✅ H30 |
| Floor poisoning — an implausibly high reference height no honest producer can clear (24b) | The claim never becomes logical time: past `W_conf` the floor follows only a height `Q` distinct signers hold, and a carry cannot corroborate, so a lone claimant moves it nowhere and is marked `FLOOR_OUT_OF_BAND`. Honest parties no longer repeat the pair either — an out-of-reach floor is omitted rather than carried — so it stays under one signature, where L6 and L4 already reach it. The diff itself still applies: L0 asks only `≥ F(m)` | ✅ H54, ✅ H55, ✅ H56, ✅ H13g (basis), ✅ H12 (edge attribution) |
| Divergence itself used as a liveness weapon — a lagging host makes every diff carrying its stamp INVALID (24c) | The producer rule is always satisfiable (`F(m)` is already in the log), so a diverged host can serve without lying; `sync_state` and the envelope anchors record the gap, and no log-plane verdict rests on it. A verdict that a lagging host cannot avoid would be a DoS against the escrow | ✅ H50, ✅ H52, ✅ `TestHeightSync_E2E_WideDivergenceNeverBlocksInferences` |
| Reorg wedge — the chain reorgs below the floor, so nobody holds a first-party height that clears it (24d) | The floor never falls, and does not need to: carriers keep the escrow applying diffs while the live branch is below it, L6 attributes the stale pair to the floor's author rather than to the carriers, and stamping is first-party again once the live branch passes it. Deeper than `W_conf`, producers omit instead | ✅ H57 |
| Splitting the floor by refusing a diff at admission (24e) | The floor folds applied diffs and nothing else, so an L5a refusal — which the same diff would not even face when it arrives by catch-up — cannot make two verifiers disagree about `F(m)` or about any later L0 verdict | ✅ H58 |
| Pre-signing a future height (25) | `observed_block_hash` cannot be produced for an unmined block; L6 never confirms the pair | ✅ H13e |
| Replay-time invalidation of an honest session (26) | Only pure-`Diff` checks may invalidate; L5a is admission-only and L4 is skipped without an envelope | ✅ H13a, H13c |
| dapi unavailable or too old to serve `/block/*` | Failover to the direct-chain adapter (hash-only); a missing or down dapi degrades capability and never fails a session | ⏳ D4, D5, D7, D10, D11 |

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
- Planned rows in §6 and §7 keep the plan's `D*` / `H*` identifiers. When
  one lands, flip ⏳ to ✅ and replace the planned name with the real test
  name; keep the identifier so the plan and the catalog stay joinable.
- A new log-plane check needs a row in §7.2 **and** a statement of its
  tier (pure `Diff`, same-exchange edge, or deferrable oracle). A check
  that can invalidate a diff without frozen inputs is a bug, and H13a /
  H13c are the regression guards for it.
