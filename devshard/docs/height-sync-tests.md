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
7. [Planned: log plane — heartbeat, stamps, peer sync, arming (H1–H38)](#7-planned-log-plane--heartbeat-stamps-peer-sync-arming-h1h38)
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
| ✅ `TestHeartbeatConfig_ValidateRejectsBadOverride` | `params_test.go` | H25: bad `T_idle` / `MinRoundsPerBlock` / `K_hb·block_time` overrides fail `Validate`. |
| ✅ `TestHeartbeat_QuietSessionOpensTurn` / `_NoObservedHeightSkips` / `_SpanDispatchAddressesEverySlot` | `heartbeat_test.go` | H1, H3, H4 unit: obligation, skip-on-no-height, consecutive span. |
| ✅ `TestHeartbeat_QuietSessionOpensTurn` / `_NoObservedHeightSkips` / `_SpanDispatchAddressesEverySlot` / `_AckInclusionAndSyncVectorPrevTurn` / `_LiveHostsQuorumCompletes` / `_UnavailableAcksDoNotCountAndDegrade` | `user/heightsync_test.go` | H1, H3, H4, H6, H7, H24 in-process: `MaybeHeartbeat` span, host mempool acks, quorum complete, unavailable does not count and degrades past `D_ack`. |
| ✅ `TestTurnTracker_OutOfOrderAcksIdenticalRecord` / `_QuorumCompletesAndConfirms` / `_BelowQuorumDegradesNoBlame` / `TestLateAck_DoesNotClearDegraded` / `TestHeightAck_OracleUnavailableStillRequired` | `heartbeat_test.go` | H5–H8, H24: turn record, `(C-turn)`, late acks, `ORACLE_UNAVAILABLE`. |
| ✅ `TestTurnTracker_IngestNextBlockSameStampCompletes` / `_StampPastDeadlineDegrades` | `heartbeat_test.go` | `D_ack=1`: ingest may tick; lateness is `observed_height > h_req + D_ack`. |
| ✅ `TestHeartbeat_HashOnlyOracle_TurnCompletes` | `syncstate_test.go` | D9: hash-only oracle (empty Commit) reaches `complete`; `Prove` is not called. |
| ✅ `TestEvaluateSyncStateFromHeader_DoesNotCallLatest` | `syncstate_test.go` | Ack stamp reuses the already-fetched header (same read as the response-leg Anchor). |
| ✅ `TestSignAck_RoundTrip` / `TestCanonicalAckBytes_DomainSeparated` | `ack_signing_test.go` | Domain `heightsync.ack.v1`; field 8 excluded from the signing input. |
| ✅ `TestHost_HeartbeatAck_OwnSlotIntoMempool` / `_WrongSlotSilent` / `_NoHeartbeatNoAck` / `_OracleUnavailableStillRequired` / `_OracleErrorStillRequired` / `_CatchingUp` / `_OracleStale` / `_AlreadyAppliedDoesNotRereadOracle` | `host/heightsync_test.go` | E3: ack only for this host's slot; one `Latest()` per exchange; `ORACLE_UNAVAILABLE` still required; `peer_seen` from Diff. |

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
| ✅ `TestObservedHeightNow_CacheEmpty` / `FreshTip` / `NoHeightSync` | `client_heightsync_test.go` | Courier user's `ObservedHeightNow()` returns `(0, false)` on cold cache and `(H, true)` when a fresh tip exists. |
| ✅ `TestHTTPClient_SeedHeightSync_RecordsOrigin` | `client_heightsync_test.go` | Optional seed RPC populates the peer-tip cache before the first inference. |
| ✅ `TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit` | `client_heightsync_test.go` | Outbound Anchor on a sync-turn nonce hits the wire and the audit ring. |
| ✅ `TestHTTPClient_Send_CourierLazyAnchorMarksPropagated` | `client_heightsync_test.go` | Successful send updates `last_propagated` so the next nonce omits. |
| ✅ `TestHTTPClient_ParseSSE_InboundHeightSyncAudit` | `client_heightsync_test.go` | Inbound SSE response attestations land in the user audit ring. |
| ✅ `TestClient_ResponseAnchor_VerifiesOriginSignature` | `client_heightsync_test.go` | User verifies host's `sender_signature` before caching. |
| ✅ `TestClient_ResponseAnchor_DropsOnInvalidSig` | `client_heightsync_test.go` | Invalid signature ⇒ tip dropped, metric `origin_sig_invalid_total` increments, cache stays cold. |
| ✅ `TestClient_RequestLeg_OmitsSenderSignature` | `client_heightsync_test.go` | `Carry` strips `sender_signature` on the request leg (asymmetric verification). |
| ✅ `TestServer_Inference_HeightSync_OutboundAnchor` | `server_heightsync_test.go` | Host emits Anchor on response inside sync turn. |
| ✅ `TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest` | `server_heightsync_test.go` | Server honours per-request force flag. |
| ✅ `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns` / `MatchNoWarn` | `server_heightsync_test.go` | Ahead-of-oracle peer tip is held pending; mismatch on later reconciliation logs warn. |
| ✅ `TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits` | `server_heightsync_test.go` | Host emits Anchor for every response nonce inside an active forced turn. |
| ✅ `TestServer_LazyAnchorAcceptedOutsideSyncTurn` | `server_heightsync_test.go` | `VALID_LAZY_ANCHOR` accepted outside sync-turn windows. |
| ✅ `TestServer_LazyAnchorInsideSyncTurn_IsCadenceAnchor` | `server_heightsync_test.go` | Lazy-shaped Anchor inside sync turn is reclassified as cadence. |
| ✅ `TestServer_StaleOriginRejected` | `server_heightsync_test.go` | Receiver rejects with `reason=stale_origin` past `F`. |
| ✅ `TestServer_ConfirmationView_AfterLazyInbound` | `server_heightsync_test.go` | Server-side `IsStrictlyConfirmed` reaches `confirmed` via carried originator metadata. |
| ✅ `TestHandleHeightSync_DisabledReturnsNotFound` / `ForcesAnchor` | `server_heightsync_test.go` | Optional seed RPC (`POST .../height-sync`) is opt-in and returns a forced Anchor on the response. |
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
| D5 | ✅ `TestHostOracle_DapiAndChainMissing_OmitsAndStale` | `chainoracle/blocks/failover`, `heightsync` | Both sources gone ⇒ Omit + `ConfirmStale`, no errors reach inference. |
| D6 | ✅ `TestHeightSync_MockDapiBlockLatest` | `testenv/citest` | Current mock-dapi (has `/block/*`, stand-in for `api:0.2.15-v5`) serves advancing heights; v5 stack green without heightsyncd. |
| D7 | ✅ `TestContainerE2E_HeightSync_OldDapiChainOnly` / `TestHeightSync_LegacyDapiChatCompletes` | `chainoracle/blocks/failover`, `testenv/citest` | Simulated old dapi (no `/block/*`, stand-in for `api:0.2.15` from this branch): chat completes; Strong never claimed. |
| D8 | ✅ `TestHostOracle_ProveEndpointAbsent_AnchorUnaffected` | `chainoracle/blocks/failover` | `/block/:height/prove` absent or 501 leaves the Anchor path untouched. |
| D9 | ✅ `TestHeartbeat_HashOnlyOracle_TurnCompletes` | `devshard/heightsync` | A heartbeat turn (§7) over a hash-only direct-chain oracle reaches `complete` without requesting Strong. |
| D10 | ✅ `TestHostOracle_RuntimeFailover_DapiGoesDown` | `chainoracle/blocks/failover` | dapi answered 200, then refuses connections: the next `Latest()` uses direct chain, Anchor still emits, no host restart. |
| D11 | ✅ `TestHostOracle_RuntimeFailback_DapiRecovers` | `chainoracle/blocks/failover` | After the probe interval the next `Latest()` is back on the chainoracle client, again with no restart. |

---

## 7. Planned: log plane — heartbeat, stamps, peer sync, arming (H1–H38)

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
| H1 | ✅ `TestHeartbeat_QuietSessionOpensTurn` | `heightsync` + `user` | Quiet session opens a heartbeat turn before `h_last + K_hb + D_ack` (unit + `MaybeHeartbeat` in-process). |
| H2 | ⏳ `TestHeartbeat_BusySessionWithStampsEmitsNone` | `user` | Stamped inference traffic discharges the obligation ⇒ **zero** heartbeats. |
| H3 | ✅ `TestHeartbeat_NoObservedHeightSkips` | `heightsync` + `user` | `ObservedHeightNow() == (0,false)` ⇒ no turn claiming a height; skip metric increments. |
| H4 | ✅ `TestHeartbeat_SpanDispatchAddressesEverySlot` | `heightsync` + `user` | `slots_num` consecutive nonces, no ack awaited, every slot addressed exactly once. |
| H5 | ✅ `TestTurnTracker_OutOfOrderAcksIdenticalRecord` | `heightsync` | Acks arriving out of order, several per diff, produce an identical `SyncTurnRecord`. |
| H6 | ✅ `TestTurnTracker_QuorumCompletesAndConfirms` + `TestHeartbeat_LiveHostsQuorumCompletes` | `heightsync` + `user` | `Q` acks ⇒ turn `complete`, `h_last` advances, `(C-turn)` confirms (unit + live hosts). |
| H7 | ✅ `TestTurnTracker_BelowQuorumDegradesNoBlame` + `TestHeartbeat_UnavailableAcksDoNotCountAndDegrade` | `heightsync` + `user` | `< Q` counting acks past `D_ack` ⇒ `degraded` with no blame recorded (unit + live host). |
| H8 | ✅ `TestLateAck_DoesNotClearDegraded` | `heightsync` | A late ack counts for height, is tagged `late`, and never clears `degraded`. |
| H24 | ✅ `TestHeightAck_OracleUnavailableStillRequired` + `TestHost_HeartbeatAck_OracleUnavailableStillRequired` + `TestHeartbeat_UnavailableAcksDoNotCountAndDegrade` | `heightsync` + `host` + `user` | `ORACLE_UNAVAILABLE` ack is present and required, does not count toward `Q`, leaves `(C-turn)` unaffected. |
| H25 | ✅ `TestHeartbeatConfig_ValidateRejectsBadOverride` | `heightsync` | `MinRoundsPerBlock ≥ 2`, `T_idle > K_hb + D_ack`, and `K_hb · block_time ≤ F/2` fail fast at startup. |
| H33 | ⏳ `TestHeartbeat_StampedBusySessionEmitsNoAcks` | `host` | A busy stamped escrow emits zero heartbeats **and** zero `MsgHeightAck`; acks exist only inside heartbeat turns. |
| H34 | ✅ `TestSeed_SessionOpenStampsNonceOne` | `user` | E9: seed runs before the first outbound diff; nonce 1 `MsgHeartbeat` carries the seeded `(height, hash)`. (`MsgStartInference` stamp is E7.) |
| H35 | ✅ `TestSeed_FanOutSurvivesDeadSlot` | `user` | One slot 404s; the seed still succeeds from another slot and every valid Anchor lands in `HeightSyncPeerTips`. |
| H36 | ✅ `TestSeed_TotalMissDegradesNeverFails` | `user` | All slots unseedable ⇒ session opens normally, `heightsync: seed_missed`, `heartbeat_skipped_no_height` on the first due check; `SendInference` still succeeds. |
| H37 | ✅ `TestSeed_DoesNotAdvanceHLastOrConsumeNonce` | `user` | The seed appends nothing, consumes no nonce, and leaves the heartbeat obligation armed — a seeded session that never works still owes a turn within `K_hb`. |
| H38 | ✅ `TestStampPresent_EmptyHashIsAbsent` / `_PresentThenAbsentIsNotRegression` | `heightsync` | Presence keyed on non-empty `observed_block_hash`: a present-then-absent pair is not treated as height 0. L0/L0b skip legs with no claim (wired in E4). |

### 7.2 Verifier checks — L0–L7 and evaluation tiers

The tier split (§14) is itself under test: only pure-`Diff` checks may
invalidate a diff, cross-plane checks record marks, and an oracle check
may defer.

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H9 | ✅ `TestLogPlane_DeterminismAcrossVerifiers` | `state` | Two independently built verifiers on the same diff sequence produce byte-identical `SyncTurnRecord`s and identical L0–L3 / L5b / L7 verdicts. |
| H10 | ✅ `TestLogPlane_FabricatedAckRejected` | `heightsync` | User-fabricated ack ⇒ `INVALID(ack_sig_invalid)` (L2). |
| H11 | ✅ `TestLogPlane_AckCausalityRejected` | `heightsync` | Unknown or mismatched `turn_seq` / `ref_nonce` ⇒ `INVALID(ack_causality)` (L3). |
| H12 | ✅ `TestHeightAck_EnvelopeBindingMismatch` | `transport` | Ack height ≠ its own response-leg Anchor ⇒ `DISPUTE_ORIGINATOR` on sight, mark written, no oracle lookup (L4). |
| H13 | ✅ `TestHeartbeat_RequestLegBindingMismatch` | `transport` | Heartbeat height ≠ request-leg section ⇒ `DISPUTE_CARRIER` (L4). |
| H13a | ✅ `TestLogPlane_NoEnvelopeSkipsCrossPlaneChecks` | `heightsync` | Catch-up / gossip re-ingest with no envelope skips L4 and L5a; every other verdict is unchanged; an edge mark is not re-derived. |
| H13b | ✅ `TestLogPlane_SectionPresentForOneRecipientOnly` | `transport` | Lazy carry (`last_propagated`) means slot A runs L4 while slot B skips it; both accept the diff. |
| H13c | ✅ `TestLogPlane_HistoricalReplayNoInvalidation` | `heightsync` | Replaying a session whose stamps sit far below the verifier's tip yields **no** `INVALID` — L5a is admission-only, L5b compares in-turn acks. |
| H13d | ✅ `TestLogPlane_HeightRegressionAcrossNonces` | `heightsync` | A stamp below an earlier nonce's stamp ⇒ `INVALID(height_regression)` on every verifier (L0). |
| H13e | ✅ `TestLogPlane_FutureDatedStampDeferredFail` | `heightsync` | `observed_block_hash` belonging to another height ⇒ `DEFERRED_FAIL` once the follower reaches `H`; the pair never confirms (L6). |
| H14 | ✅ `TestHeightAck_FalseSyncedDeferredFail` | `heightsync` | Host claiming `SYNCED` on a stale oracle fails L6 once the follower advances; honest `ORACLE_STALE` carries no penalty. |
| H30 | ⏳ `TestLogPlane_PerInferenceHeightOrder` | `heightsync` | `confirm` below `start` for one `inference_id` ⇒ `INVALID(height_regression)` (L0b). Waits on E7 stamps. |

### 7.3 Stamps on existing txs, signature coverage, evidence retention

The trap these pin down: a stamp must sit inside the signature of its own
producer (§10.5.1), which is automatic for `proposer_sig` and **not**
automatic for `executor_sig`.

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H28 | ⏳ `TestConfirmStart_TamperedObservedHeightFailsExecutorSig` | `state` | Altering `MsgConfirmStart.observed_height` after signing ⇒ `ErrInvalidExecutorSig`, proving the height really is inside `ExecutorReceiptContent`. |
| H29 | ⏳ `TestFinishInference_StampCoveredByProposerSig` | `state` | A stamp on `MsgFinishInference` is covered with no signing-code change; tampering ⇒ `ErrInvalidProposerSig`. |
| H31 | ⏳ `TestApply_RecordCarriesStampHeights` | `state` | `started_at_height` / `confirmed_at_height` land on the record from the stamps, and `post_state_root` differs from an unstamped run. |
| H32 | ✅ `TestMarks_RequestLegEvidenceVerifiesOffline` | `heightsync` | A retained request-leg mark `(body, sig, ts, escrow_id)` recovers the user's address and shows section ≠ stamp, long past the ±30 s admission window. |

### 7.4 Peer sync status and repair probe (§11)

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H15 | ✅ `TestSyncVector_AckedContradictsLog` | `heightsync` | `ACKED(j,h,n)` with no ack at `Diff[n]` ⇒ user-attributable mark, still no `INVALID`. |
| H16 | ✅ `TestRepairProbe_HeightNoBlame` | `heightsync` | `MISSING` + no ack + a later probe returning `HEIGHT` ⇒ **no** mark and **no** `USER_CHEATING`; the omission stays unattributed. (E4 lands the negative; probe itself is E5.) |
| H17 | ⏳ `TestRepairProbe_UnreachableOrHeight` | `transport` | Missing ack past `D_ack` with a live peer: probe returns `HEIGHT`, height is ingested, `peer_seen` bit set, turn stays `degraded`. |
| H18 | ⏳ `TestRepairProbe_DeadPeerBacksOff` | `transport` | Dead peer ⇒ `UNREACHABLE`, local record and backoff only, nothing on the wire toward the user. |
| H19 | ⏳ `TestRepairProbe_BudgetAndStagger` | `heightsync` | One probe per `(turn, slot)` per prober, `R_max` cap per `K_hb`, deterministic stagger so late probers skip once the ack lands. |
| H20 | ⏳ `TestRepairProbe_ArmedHostStopsProbing` | `heightsync` | An armed host stops probing entirely. |

### 7.5 Close-ready arming (§12)

| ID | Test (planned name) | Package | What it will prove |
| -- | ------------------- | ------- | ------------------ |
| H21 | ⏳ `TestCloseReady_ArmsAfterIdle` | `host` | User silence past `T_idle` arms the host and emits **nothing** on the wire. |
| H22 | ⏳ `TestCloseReady_DisarmsOnContact` | `host` | Contact disarms; the `[armed_at, disarmed_at)` interval is retained (level-triggered, not monotone). |
| H23 | ⏳ `TestCloseReady_MinorityCannotClose` | `host` | A partitioned armed minority produces no vote and no tx; closing still needs finalization quorum. |

### 7.6 Container (log plane)

| ID | Test (planned name) | What it will prove |
| -- | ------------------- | ------------------ |
| H26 | ⏳ `TestContainerE2E_HeightSync_QuietEscrowHeartbeat` | Quiet compose escrow: heartbeat cadence visible in Loki, `(C-turn)` confirms, **zero** probe traffic on the healthy path. |
| H27 | ⏳ `TestContainerE2E_HeightSync_OneHostStopped` | One host stopped: degraded turns, bounded probe traffic, arming only after `T_idle` of user silence. |

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
| `(C-quorum)` | E4, E5, E6, E11, `TestConfirm_*` | — |
| `(C-strong)` / `(C-hybrid)` | — | S5, S6, S7, `TestConfirm_RuleStrong*`, `TestConfirm_RuleHybrid*` |
| Asymmetric response signatures | E9, E10, `TestClient_ResponseAnchor_*`, `TestServer_ResponseAnchor_SignedByHost`, `TestSignOrigin_*` | — |
| DEFERRED_FAIL attribution | E10 (exculpation API), H13e, H14 | S11 (Strong-grade evidence) |
| `ObservedHeightNow` (cPoC C14) | `TestObservedHeightNow_*` | — |
| Optional seed RPC | `TestHTTPClient_SeedHeightSync_RecordsOrigin`, `TestHandleHeightSync_*` | H34–H37 (E9 session-open seed, plan §8.5.1) ✅ |
| Audit ring | `TestAuditRing_*`, all e2e tests | — |
| Stale / quiet oracle | Feed **unavailable**: `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_*`, E6. Feed **quiet** (cached tip): `TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn`, `TestDecide_LogStaleSyncTurn`, container cadence | S10 |
| §7 wire format — envelope is one plane only | `TestEnvelope_*`, `TestUnwrapInferenceRequestBody_*` | — |
| §10.1–§10.3 heartbeat cadence + obligation | H1, H3, H4, H25 (unit + in-process) | H2 |
| §10.4 `MsgHeartbeat` / `MsgHeightAck` wire + binding | field-number + ack signing unit + `TestHost_HeartbeatAck_*`, H10, H11, H12, H13 | H33 |
| §10.5 `observed_height` in the log | H6 (turn complete), H9, H13d, H34 (seeded nonce 1 heartbeat), H38 (absent ≠ 0) | — |
| §10.5.1 stamp inside its producer's signature | — | H28 (`executor_sig` mirror), H29 (`proposer_sig` automatic) |
| §10.5.2 derived record heights / logical time | — | H31; switching the consumers is a later milestone |
| §10.6 async fan-out | H4, H5 (in-process span + unit) | — |
| §10.7 turn record + completion | H5, H6, H7, H8 (unit + live host) | — |
| §11.1 `sync_vector` honesty | H4 ack-inclusion / prev-turn vector, H15, H16 | — |
| §11.2 `sync_state` + `peer_seen` | H24 (unit + live host), `TestHost_HeartbeatAck_OwnSlotIntoMempool`, H14 | H17 |
| §11.3–§11.4 repair probe + budgets | H16 (no-blame negative) | H17, H18, H19, H20 |
| §12 close-ready arming | — | H21, H22, H23 |
| §14 log-plane checks L0–L7 | H9–H16, H13a–e | H30 |
| §14 evaluation tiers (what may invalidate) | H13a, H13b, H13c | — |
| §15 signature layers + mark retention | E10 (exculpation), H32 | — |
| §17 `(C-turn)` | H6, H24 (unit + live host), `TestConfirm_TurnRule` | — |
| Block-oracle abstraction + dapi compatibility | D1–D11 | — |

---

## 10. Attack-scenario coverage

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
| User never heartbeats on a quiet session (spec attack 14) | Heartbeat is mandatory in *height* cadence; after `T_idle` served-nothing hosts arm close-ready and finalization closes via `USER_TIMEOUT` | ⏳ H1, H21 |
| User omits a host's ack, or the host never sent one (15) | `Diff` cannot distinguish the two; the probe fetches a height so alignment continues but attributes nothing; arming keys on user **silence** | ⏳ H16, H17, H18 |
| `sync_vector` contradicts the log (16) | The vector is covered by the user's diff signature, so `ACKED` against a log without that ack is self-contradiction; other statuses stay inconclusive | ✅ H15 |
| Host never acks (down / broken oracle / refusing) (17) | Turn goes `degraded` identically for every verifier; probe returns `HEIGHT` or `UNREACHABLE`; omission unattributed either way | ✅ H7 (live unavailable), ⏳ H17, H18 |
| Host's ack contradicts its own response Anchor (18) | L4 self-contradiction under one identity ⇒ `DISPUTE_ORIGINATOR` on sight, no oracle lookup, mark persisted verbatim | ✅ H12 |
| Host reports `SYNCED` with a stale oracle (19) | L6 reconciliation ⇒ `DEFERRED_FAIL`; the honest alternatives carry no penalty, so lying is strictly worse | ✅ H14 |
| Repair-probe amplification (20) | One probe per `(turn, slot)`, `R_max` per `K_hb`, deterministic stagger, backoff, zero probes on the healthy path, armed hosts stop | ⏳ H19, H20 |
| Partitioned minority tries to close a healthy escrow (21) | Arming emits nothing; closing needs finalization's `2f + 1`; unarmed hosts reject `USER_TIMEOUT` | ⏳ H23 |
| Drip-fed late acks to fake a complete turn (22) | Late acks count for height only, never clear `degraded`; arming keys on `last_signal_height` toward this host | ⏳ H8 |
| Sequencer rewrites a host's stamp on `MsgConfirmStart` (23) | The pair lives in `ExecutorReceiptContent` and is copied into the rebuilt content before recovery, so any edit fails `executor_sig` | ⏳ H28 |
| Stamp regression to widen a band or backdate a duration (24) | L0 across nonces, L0b within one inference; both pure functions of `Diff` | ✅ H13d, ⏳ H30 |
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
