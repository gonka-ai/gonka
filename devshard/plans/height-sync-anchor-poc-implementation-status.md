# Height Sync Anchor PoC — implementation status

This document tracks progress for section
`10. Step-by-step implementation order` in
`devshard/plans/height-sync-anchor-poc.md`.

## Current status overview

- Implemented steps: **6 / 9**
- In progress steps: **0 / 9**
- Deferred steps: **1 / 9** (Step 5 — operator tooling)
- Not started steps: **2 / 9** (Step 8 docs, Step 9 forced sync turn — see §5.5 redesign)

**Note:** Step 7 (testenv scenarios) covers plan §9.3 points **1–8** on the in-process stack;
Docker/heightsyncd compose parity for item 8 remains an optional follow-up (see `SCENARIOS.md`).

---

## Step-by-step status

### Step 1 — `devshard/heightsync` package

**Status:** Implemented

**Implemented files:**

- `devshard/heightsync/anchor.go`
- `devshard/heightsync/audit.go`
- `devshard/heightsync/anchor_test.go`
- `devshard/heightsync/audit_test.go`

**Implemented behavior:**

- Added light `HeightSyncSection` model for Anchor/Omit payloads:
  - `chain_id`
  - `proof_type` (`height-anchor-v1`)
  - `mainnet_height`
  - `mainnet_block_hash_hex`
  - `timestamp_unix_ms`
  - `direction`
- Added `DecideHints{Nonce, SessionStart, ForceAnchor}` (nonce-driven cadence
  with sync-turn windows).
- Added `AnchorScheduler` with `Decide(ctx, hints)` and the proposal-aligned
  **sync-turn cadence rule**:

  ```text
  inSyncTurn(nonce) ==
    (nonce >= 1 && nonce <= SlotsNum) ||              // initial sync turn
    (nonce >= K && nonce % K < SlotsNum)              // periodic sync turns

  emit Anchor IFF
    ForceAnchor || SessionStart || inSyncTurn(Nonce)
  ```

  - Emits Anchor across **`SlotsNum` consecutive nonces** at the start of
    each sync turn (initial: nonces 1..SlotsNum; periodic: A..A+SlotsNum-1
    for every A = K, 2K, 3K, ...).
  - Emits Omit (`nil`) for all other nonces.
  - Constructor enforces `K >= SlotsNum` (returns `ErrInvalidConfig`
    otherwise) so sync turns never overlap.
  - `K=0` defaults to `K=10`; `SlotsNum=0` defaults to `1`.
  - `K = SlotsNum` makes the schedule wall-to-wall (every nonce Anchor).
  - `SlotsNum = 1` collapses the rule back to single-nonce cadence
    (`nonce % K == 0`).
- Kept `SessionStart` and `ForceAnchor` hints as explicit overrides for
  edge cases (session reset, explicit height-sync RPC, cPoC skip/carry).
- Added oracle error handling semantics:
  - Regular path: oracle errors / nil header degrade to Omit (`nil, nil`).
  - `ForceAnchor=true`: oracle errors are returned (`fmt.Errorf("latest header: %w")`).
  - Sentinel errors for missing oracle (`ErrNoOracle`), nil header
    (`ErrNilOracleHeader`), and bad config (`ErrInvalidConfig`).
- Added `AuditRing` (bounded per-peer in-memory ring):
  - Stores `AnchorAttestation` (`peer_id`, direction, height, block hash,
    observed timestamp, source message, **`Trust`**:
    `trusted_oracle` | `untrusted_peer` | `peer_aligned`).
  - Drops oldest entry when per-peer capacity is exceeded.
  - Returns defensive copies for `MainnetBlockHash`.
  - Supports `List(peerID)` and `ListPeers()`.

**Tests created (`devshard/heightsync/anchor_test.go`):**

- `TestAnchorScheduler_SyncTurnSweepK10Slots4` —
  full sweep over `nonce=1..35` with K=10, slots=4; verifies Anchor at
  exactly `{1..4, 10..13, 20..23, 30..33}` and Omit elsewhere.
- `TestAnchorScheduler_SlotsOneCollapsesToCadence` —
  K=10, slots=1: Anchor only at `{1, 10, 20, 30}` (matches single-nonce
  cadence).
- `TestAnchorScheduler_KEqualsSlotsIsWallToWall` —
  K=4, slots=4: every nonce in 1..12 emits Anchor.
- `TestAnchorScheduler_SessionStartOverridesOmitWindow` —
  forces Anchor at nonce=7 (Omit window for K=10, slots=4).
- `TestAnchorScheduler_ForceAnchorOverridesOmitWindow` —
  forces Anchor at nonce=7 (Omit window).
- `TestAnchorScheduler_NonceZeroEmitsOmit` —
  nonce=0 with no hints is Omit.
- `TestAnchorScheduler_KZeroDefaultsToTen` —
  K=0 defaults to 10 (Omit at 9, Anchor at 10).
- `TestAnchorScheduler_SlotsZeroDefaultsToOne` —
  slots=0 defaults to 1 (Anchor at 1, Omit at 2 with K=10).
- `TestAnchorScheduler_KLessThanSlotsIsRejected` —
  K=2, slots=4 returns `ErrInvalidConfig`.
- `TestAnchorScheduler_OracleErrorOmitUnlessForced` —
  oracle error degrades to Omit on cadence/SessionStart paths, surfaces
  the error under `ForceAnchor=true`.
- `TestAnchorScheduler_NilOracleHeaderHandling` —
  nil header degrades to Omit on cadence path; returns
  `ErrNilOracleHeader` under `ForceAnchor=true`.
- `TestAnchorScheduler_NoOracleHandling` —
  no oracle wired: cadence Omit, forced returns `ErrNoOracle`.
- `TestAnchorScheduler_TimestampSet` —
  emitted `TimestampUnixMs` reflects current time.

**Tests created (`devshard/heightsync/audit_test.go`):**

- `TestAuditRing_AppendsAndListsByPeer` — per-peer append + list order.
- `TestAuditRing_BoundedCapacityDropsOldest` — capacity eviction.
- `TestAuditRing_DefensiveCopy` — caller and ring don't share hash backing arrays.
- `TestAuditRing_ListPeers` — known-peers enumeration.
- `TestInboundTrust` — `InboundTrust` maps peer vs oracle height to
  `untrusted_peer` / `peer_aligned`.

**Tests executed and passed:**

- `go test -v -count=1 ./heightsync/...` (from `devshard/` module) — **all 18 tests pass**:
  13 anchor scheduler tests + 5 audit / trust tests.

**Notes (cadence evolution):**

- Draft 1 (initial PoC plan): height-bucket cadence (`height / K`).
- Draft 2 (proposal updated to "every K envelope nonces"): single-nonce
  rule `nonce % K == 0`. Implementation rewritten.
- Draft 3 (current — round-robin friendly): **sync-turn windows** of
  `SlotsNum` consecutive nonces every K nonces, plus an initial window
  at `1..SlotsNum`. This matches the user's intended round-robin
  behavior across an escrow group: every host in `slots_num` slots
  participates in each sync turn and replies with its own
  `(height, block_hash)`. The constructor now takes both `K` and
  `SlotsNum` and validates `K >= SlotsNum`.

---

### Step 2 — Envelope wire

**Status:** Implemented

**Implemented files:**

- `devshard/proto/devshard/v1/inference_envelope.proto` — protobuf definitions.
- `devshard/types/inference_envelope.pb.go` — generated (`make proto` in `devshard/`).
- `devshard/transport/envelope.go`
- `devshard/transport/envelope_test.go`

**Implemented behavior:**

- **Wrapped bodies use protobuf** (`proto.Marshal` / `proto.Unmarshal`): messages
  `InferenceRequestEnvelope` and `InferenceResponseEnvelope` in package
  `devshard/types`, defined in `proto/devshard/v1/inference_envelope.proto`.
- Optional **`InferenceHeightSyncSection`** (compact): **`InferenceHeightSyncProofType`**
  enum, **`mainnet_height`**, **`mainnet_block_hash_hex`**, **`timestamp_unix_ms`**,
  **`response`** bool (false=request, true=response). No **`chain_id`** on wire;
  decode fills `heightsync.HeightSyncSection.ChainID` as empty. Maps to /
  from `heightsync.HeightSyncSection` in `envelope.go`. Omit mode: `height_sync`
  unset in protobuf.
- Inner inference payloads remain **JSON bytes** (`inference_request_json` /
  `inference_response_json`) matching existing `InferenceRequest` /
  `InferenceResponse` structs; marshaled with **`github.com/goccy/go-json`** for
  parity with the rest of `transport`.
- **Legacy whole-body JSON** (existing clients): if the body begins with `{`,
  decode as plain `InferenceRequest` / `InferenceResponse` JSON. Height sync is
  treated as **omitted** (`HeightSync == nil`, `SchemaVersion == 0`,
  `WholeBodyJSON == true`).
- **Deprecated JSON envelope** (top-level **`message`**) remains **rejected**.
- **Protobuf envelope:** bodies that do not start as JSON objects decode as
  `InferenceRequestEnvelope` / `InferenceResponseEnvelope` (`WholeBodyJSON ==
  false`).

**Tests created (`devshard/transport/envelope_test.go`):**

- Protobuf round-trip **Anchor** / **Omit** request (Omit asserts `HeightSync`
  nil after `proto.Unmarshal`).
- Wrapped wire does **not** look like a raw JSON object (first byte not `{`).
- Legacy whole-body JSON request/response → **`WholeBodyJSON`**, **`HeightSync`
  nil**.
- Deprecated JSON envelope blob → error.
- Legacy body from **`encoding/json`** unwraps.

**Tests executed and passed:**

- `go test -count=1 ./transport/...` (from `devshard/`) — **all transport tests
  pass**, including the envelope tests above.

**Note:** Step 4 wires the scheduler into **`HTTPClient`**; Step 3 wires the host
**`Server`** (decode + outbound SSE + audit).

---

### Step 3 — Host send / receive wiring

**Status:** Implemented

**Implemented behavior:**

- **`transport.Server`** (`devshard/transport/server.go`):
  - **`WithHeightSync(sched, logOracle)`** already constructs an **`AuditRing`** when
    `sched != nil`.
  - Per-session **`firstInferenceResp`** map + mutex: the **first** successful
    inference response on `POST …/chat/completions` passes
    **`DecideHints{ SessionStart: true }`** together with the monotonic
    **`nextResponseNonce(sessionID)`** nonce; later responses use
    **`SessionStart: false`**.
  - **Inbound**: **`UnwrapInferenceRequestBody`**, classify,
    **`logInboundHeightSync`** (includes **`trust_level`** on Anchor),
    **`recordInboundAnchorIfAnchor`** with **`Trust`** from
    **`heightsync.InboundTrust`** (`untrusted_peer` when peer height is
    strictly greater than local oracle height).
  - **Per-session pending untrusted tip**: when an inbound Anchor is
    ahead-of-oracle, store **`(height, block_hash)`**; on each subsequent
    inference for that session, **`reconcilePendingUntrusted`** compares against
    **`Latest()`** — if oracle reaches the **same** height and **`BlockHash`**
    differs from the pending peer hash, emit **`logging.Warn`** (see plan
    §6.2), then clear pending; if hashes match, clear without warning; if
    oracle skips past the height, drop pending.
  - **Outbound**: after **`HandleRequest`**, first SSE JSON blob may include
    **`height_sync`** on **`devshard_receipt`** when **`AnchorScheduler.Decide`**
    returns a section (hints include **`ForceAnchor: HostRequest.ForceHeightSyncAnchor`**
    from JSON **`force_height_sync_anchor`**); sets **`Direction = "response"`**;
    **`logOutboundHeightSync`** (anchor vs omit); **`recordOutboundAnchorIfAnchor`**
    appends **direction=`response`** attestations (peer id = host signer
    address) with **`Trust=trusted_oracle`**; emit logs include
    **`trust_level=trusted_oracle`**.
  - On **`Decide`** error, logs the error and emits **omit** debug line.

**Tests (`devshard/transport/server_test.go`):**

- **`TestServer_Inference_HeightSync_OutboundAnchor`** — **`WithHeightSync`** +
  fake oracle; asserts **`height_sync`** on first SSE event, outbound audit
  **`Trust=trusted_oracle`**, and an outbound **`response`** row in the audit
  ring for the host address.
- **`TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`** —
  ahead-of-oracle user Anchor, then oracle catches up at same height with
  different hash → one **Warn**.
- **`TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn`** — same
  but matching hash → no Warn.
- **`TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest`** — three
  sequential inferences: cadence Anchor → Omit → **`force_height_sync_anchor`**
  forces receipt Anchor.

**Tests executed and passed:**

- `go test -count=1 ./transport/...` (from `devshard/`).

**Existing wiring:**

- **`devshard/testenv/cmd/devshardd-testenv/main.go`** already builds
  **`AnchorScheduler`** from **`HEIGHT_SYNC_ANCHOR_PERIOD_NONCES`** (with
  **`K >= slots`**) and passes **`transport.WithHeightSync(anchorSched, md.Oracle)`**.

---

### Step 4 — User send / receive wiring

**Status:** Implemented

**Implemented files:**

- `devshard/transport/client.go`
- `devshard/transport/client_test.go`
- `devshard/user/user.go` (**`InferenceParams.ForceHeightSyncAnchor`** → **`SendOnly`**)

**Implemented behavior:**

- **`ClientConfig`** (`devshard/transport/client.go`):
  - **`HeightSync`** — optional `*heightsync.AnchorScheduler`; **`nil`** keeps
    legacy whole-body JSON inference requests (unchanged).
  - **`HeightSyncLogOracle`** — optional `blockoracle.BlockOracle` for debug
    **`local_aligned` / `delta`** on emit and peer-attestation logs.
- **`HTTPClient`**: when **`HeightSync != nil`**, **`NewHTTPClient`** allocates a
  **`heightsync.AuditRing`** (same default capacity as host); exposed via
  **`HeightSyncAuditRing()`**.
- **`Send`**:
  - **`Decide(ctx, DecideHints{Nonce: req.Nonce, ForceAnchor: req.ForceHeightSyncAnchor})`** — uses existing monotonic
    **`HostRequest.Nonce`** (no separate first-send flag; cadence covers the
    initial sync turn per `height-sync-anchor-poc.md` §5.1).
  - Non-nil section → **`MarshalWrappedInferenceRequest`** (protobuf body),
    **`Content-Type: application/x-protobuf`**, **`direction=request`** on the
    inner JSON-derived section.
  - Omit → legacy JSON body, **`Content-Type: application/json`**.
  - **`logEmitUserHeightSync`** (anchor vs omit); **`recordUserOutboundAnchorIfAnchor`**
    records **`direction=request`** with **`PeerID = user signer address`**.
- **`parseSSEResponse`**: if an SSE JSON object contains **`height_sync`** next to
  **`devshard_receipt`**, **`logPeerHeightSyncFromSSE`** (adds **`trust_level`**
  on Anchor) and **`recordHostInboundAnchorIfAnchor`** with
  **`heightsync.InboundTrust`** vs the user’s log oracle
  (**`direction=response`**, **`PeerID = client.baseURL`**). User **outbound**
  audit rows use **`Trust=trusted_oracle`**; emit logs add
  **`trust_level=trusted_oracle`**.
- Signing unchanged: **`SignRequest`** covers raw POST bytes (protobuf or JSON).
- **`user.Session.SendOnly`** forwards **`InferenceParams.ForceHeightSyncAnchor`** onto **`host.HostRequest`** for **`SendInference`** / manual-force policy hooks.

**Tests (`devshard/transport/client_test.go`):**

- **`setupClientTestEnvWithHeightSync`** — **separate** schedulers for host vs
  client (same **K**, **slots**, oracle); avoids sharing one **`AnchorScheduler`**
  instance between processes.
- **`TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit`** — end-to-end with
  **`WithHeightSync`** server; asserts audit rows for user **request** and host
  **response** anchors at height **42**.
- **`TestHTTPClient_ParseSSE_InboundHeightSyncAudit`** — SSE line with
  **`height_sync`** populates the audit ring for **`baseURL`**.
- **`TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip`** —
  **`InferenceRequest.force_height_sync_anchor`** ↔ **`HostRequest`**.

**Tests executed and passed:**

- `go test -count=1 ./transport/...` (from `devshard/`).

**Not in this step (later):**

- **`devshardctl`** constructing its own oracle + scheduler (still Step 6 /
  tooling when desired).

---

### Step 5 — Explicit height-sync RPC

**Status:** **Deferred (operator tooling, not protocol-critical)**

**Why deferred.** With sync-turn cadence (Step 1), the initial
`nonce <= slots_num` window already gives every reachable host an
Anchor obligation on its very first response in a session. Bootstrap
and lost-first-response recovery are therefore self-healing through
the normal inference path: a lost first response is followed by the
next round-robin request, which still emits Anchor inside the same
window. The endpoint is no longer needed for protocol correctness.

**Skipped behavior (from plan, kept for future):**

- Host route `POST .../height-sync` + `HandleHeightSync`.
- User client `HostClient.FetchHeightSync`.
- `devshardctl height-sync` subcommand and `/v1/debug/height-sync`
  debug proxy endpoint.

**Scenarios that would justify reviving Step 5 (operator tooling):**

1. **Stand-alone height fetch from `devshardctl`.** Smoke / CI
   scripts and operators want "what does host X think the mainnet
   tip is right now?" in one HTTP call, without crafting a real
   inference, signing an application payload, or consuming an
   escrow nonce. Useful for verifying a freshly deployed host has
   its oracle wired correctly before any traffic flows.
2. **Forensics / dispute snapshot.** Capture what host X claims
   **right now**, regardless of session activity, so a dispute
   reviewer can compare the snapshot against canonical mainnet and
   against archived audit-ring entries. Same shape as (1) with a
   different consumer.
3. **Liveness probe with fresh oracle data.** Heartbeat /
   monitoring path that confirms two things at once: the host
   responds, and its oracle is producing fresh `(H, hash)`. A
   plain TCP / `/healthz` check confirms only the former; this RPC
   adds oracle freshness without the cost of a real inference.

**Coverage of the e2e scenario change.** The "Recovery via explicit
fetch" and "Stand-alone fetch" cases originally listed in
`height-sync-anchor-poc.md` § 9.3 have been replaced with a single
**lost-first-response self-healing** case that proves bootstrap
completes through the normal inference path even when one host is
killed mid-request — no explicit fetch RPC required.

---

### Step 6 — Config plumbing

**Status:** Implemented

**Implemented behavior:**

- **`devshard/testenv/config/config.go`**
  - `height_sync.anchor_period_nonces` (`AnchorPeriodNonces`) — default **10**
    after `ApplyDefaults` when unset or zero.
  - `height_sync.sync_turn_slots` (`SyncTurnSlots`) — default **`devshard.group_size`**
    when unset or zero; minimum **1**.
  - **`Validate`:** rejects negative fields; rejects
    `anchor_period_nonces < sync_turn_slots`.
  - **`DefaultAnchorPeriodNonces`** constant exported for tests.
- **`devshard/testenv/cmd/gencompose/main.go`**
  - Injects into every **`devshardd-testenv-*`** service:
    `HEIGHT_SYNC_ANCHOR_PERIOD_NONCES`, `HEIGHT_SYNC_SYNC_TURN_SLOTS`
    (from resolved config).
  - Injects into **`devshardctl`**:
    `CHAIN_ID`, `HEIGHT_SYNC_URL`, `HEIGHT_SYNC_ANCHOR_PERIOD_NONCES`,
    `HEIGHT_SYNC_SYNC_TURN_SLOTS` so the proxy matches host cadence.
  - Regenerated **`docker-compose.yml`** and **`config.yaml`** via
    `go run ./cmd/gencompose` (committed snapshot includes
    `anchor_period_nonces: 10`, `sync_turn_slots: 4`).
- **`devshard/testenv/cmd/devshardd-testenv/main.go`**
  - Reads **`HEIGHT_SYNC_SYNC_TURN_SLOTS`** (optional); default remains
    **`len(escrow group)`**. Removed silent **`anchorK = max(anchorK, slots)`**
    bump — invalid **`K < slots`** surfaces from
    **`heightsync.NewAnchorScheduler`** at startup.
- **`devshard/cmd/devshardctl/main.go`** + **`devshard/user/httpsession.go`**
  - When **`MOCK_CHAIN_URL`**, **`HEIGHT_SYNC_URL`**, and **`CHAIN_ID`**
    are all set (compose provides them), builds **`mockdapi`** +
    **`AnchorScheduler`** with the same env precedence as the host and
    passes **`transport.ClientConfig`** through new
    **`HTTPSessionConfig.ExtraClientConfig`** so user outbound requests
    use protobuf envelopes + audit ring (Step 4).
  - **`defer`:** closes session then **`mockdapi`** oracle.

**Tests:**

- `go test ./testenv/config/...` — **`TestRepoConfigLoads`** anchor assertions +
  **`TestConfig_ValidateRejectsAnchorPeriodLessThanSyncTurnSlots`**.
- `go test ./testenv/cmd/gencompose/...` — compose contains new env keys.
- `go test ./user/...` `./cmd/devshardctl/...` `./transport/...`
  `./testenv/cmd/devshardd-testenv/...`.

---

### Step 7 — Testenv scenario

**Status:** Partially implemented (§9.3 points 1–5 + 4.1 carry-forward + trust/reconcile unit tests; §9.3 point 6 — single-message variant only, **superseded by §5.5 forced sync turn (planned)**; §9.3 points **7–8** implemented on in-process HTTP stack; Docker/`heightsyncd` literal stop test optional)

**What landed:**

- In-process e2e (four `httptest` hosts, static oracle, `K=8`, `slots=4`): `devshard/testenv/scenarios/heightsync_anchor_e2e_test.go` — `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail`, `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts`, `TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing`, **`TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`** (§9.3 **item 6** manual-force Anchor via `InferenceParams.ForceHeightSyncAnchor`).
- Hosts use `types.SessionConfigWithPrice(4, 1)` to match `NewHTTPSession` fee/threshold semantics; after each `SendInference`, `ApplyCatchUpDiffs` keeps round-robin hosts aligned without gossip.
- Added log-scrape assertions using a capture logger over `logging.SetLogger(...)`: user request mode pattern, host inbound mode pattern by `host_id`, and anchored prefix equality (`block_hash_prefix` == `peer_block_hash_prefix` by nonce index).
- Added client-side higher-tip carry-forward in `transport.HTTPClient`: when SSE host attestation reports a higher `mainnet_height`, subsequent outbound Anchor sections carry that higher `(height, hash)` during sync turns.
- Added mixed-height e2e: host2 starts at `X+1` while others/client start at `X`; after receiving host2 attestation, user sends `X+1` to later hosts in the same sync turn and those hosts store `X+1` in inbound audit rings.
- Added lost-first-response self-healing case: nonce 1 serving host is killed mid-flight, nonce 2 still anchors on request+response path, and user audit ring has host attestation by nonce 4 without explicit height-sync RPC.
- **§9.3 item 6 — Manual-force path (single-message PoC, SUPERSEDED).** `host.HostRequest.ForceHeightSyncAnchor` / `InferenceRequest.force_height_sync_anchor` / `InferenceParams.ForceHeightSyncAnchor` plumbed to `DecideHints.ForceAnchor` on user (`transport/client.go`) and host (`transport/server.go`). Transport tests: `TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest`, `TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip`. **Limitation:** the flag only forces Anchor on the single envelope it rides on, not on the whole `slots_num`-wide sync turn. Per the redesigned §5.5 plan ("Forced sync turn — diff-anchored manual-force"), this needs to be replaced/extended by a stateful `MsgForceHeightSyncTurn` driven from escrow state. See "Step 9 — Forced sync turn (diff-anchored)" below.
- Docker `heightsyncd` + compose smoke from the plan is still optional follow-up.
- **§9.3 point 7 — Cheating-trail (no live verification).** `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`: after scheduler chooses an Anchor at nonce `1`, `transport.ClientConfig.HeightSyncRequestMutateHook` replaces only `mainnet_block_hash_hex` with a bogus digest while keeping the oracle’s `mainnet_height`; the serving host’s audit ring stores **those bytes verbatim** on the inbound `direction=request` row (`Trust=peer_aligned` at same height per PoC rules). Documents the offline-verifier story from `height-sync-anchor-poc.md` §9.3 item 7. Scenario narrative: `devshard/testenv/scenarios/SCENARIOS.md` (“Cheating-trail — bogus user block hash”).
- **§9.3 point 8 — Height-sync feed stopped.** `sharedStoppingOracle` + `setupFourHostHTTPHeightSyncStoppingOracle`: one oracle instance shared by all schedulers; `SetStopped(true)` makes `Latest` fail like a dead **heightsyncd** for every consumer. Tests: `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors` (nonce `2` still in initial sync turn → user/host logs `mode=omit`, inference OK), `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns` (nonce `8` Anchor after feed resumes). Refactor: `setupFourHostHTTPHeightSyncFromBlockOracles` centralizes wiring. Compose-level `docker stop height-sync` proposed in `SCENARIOS.md`, not automated here.

**Tests:**

- `go test ./testenv/scenarios/... -run '^TestHeightSyncAnchor_E2E_'`
- `go test ./transport/... -run 'UntrustedReconcile|OutboundAnchor'`
- `go test ./heightsync/... -run InboundTrust`

---

### Step 9 — Forced sync turn (diff-anchored manual-force) — PLANNED

**Status:** Not started. **Supersedes** the per-request `ForceAnchor`
plumbing tracked under Step 7 / §9.3 item 6.

**Why:** the current PoC implementation forces Anchor on a single
envelope only. The cPoC manual-force semantics require Anchor on
**every** host slot in the round-robin (one full `slots_num`-wide
sync turn) so all hosts in the group converge on the same
`(mainnet_height, mainnet_block_hash)` before/around a sensitive
transition (e.g. cPoC carry of skip evidence).

**Scope (mirror of `height-sync-anchor-poc.md` §5.5):**

1. **New tx `MsgForceHeightSyncTurn`** in `devshard/types` (proto
   field on `DevshardTx.oneof`):
   - `trigger_nonce` — the nonce of the diff that opened the turn.
   - `end_nonce` — `trigger_nonce + slots_num - 1` (inclusive).
   - `reason` — string tag for audit (e.g. `cpoc_skip`,
     `cpoc_carry`, `operator`).
2. **State-machine field `ForcedHeightSyncTurn{Active, StartNonce,
   EndNonce, Reason}`** on the escrow / session state, set by
   `applyTx(MsgForceHeightSyncTurn)` and auto-cleared in
   `applyCore` once `LatestNonce > EndNonce`.
3. **State-machine validation:**
   - At most one `MsgForceHeightSyncTurn` per diff
     (`ErrMultipleForceHeightSyncTurnMsgs`, mirror of
     `ErrMultipleStartMsgs`).
   - A second `MsgForceHeightSyncTurn` arriving while
     `ForcedHeightSyncTurn.Active && trigger_nonce <= EndNonce`
     applies as a no-op (silently dropped from `applied`).
4. **`DecideHints` extended** with `ForcedTurn *ForcedHeightSyncTurn`.
   `AnchorScheduler.shouldEmit` rules:
   - Inside `[StartNonce, EndNonce]` → Anchor.
   - Cadence rule continues to apply, but cadence windows that
     **touch or overlap** the active forced window are **swallowed**
     (no double-Anchor at the boundary nonce); `i*K` cadence
     resumes unaffected after the forced window closes.
5. **Trigger plumbing (kept, advisory only):**
   - `InferenceParams.ForceHeightSyncAnchor` and
     `InferenceRequest.force_height_sync_anchor` retain their
     current shapes. Their **only** effect becomes "propose a
     `MsgForceHeightSyncTurn` in the next diff if no forced turn is
     active". When a forced turn is active they are silently dropped
     at the diff-composer (request still goes through; nothing else
     changes).
   - **Not authoritative.** These HTTP-level flags are honest-client
     ergonomics; the network learns about the forced window
     **exclusively** from the `MsgForceHeightSyncTurn` tx in the
     diff. A malicious user that omits the flag never opens a turn;
     a malicious user that omits `height_sync` from in-window
     requests after opening a turn does **not** invalidate those
     requests — hosts log a
     `height_sync_force_request_anchor_missing` warn entry as
     dispute evidence and serve normally.
6. **Authoritative server-side enforcement.** Each host reads its
   **own** `ForcedHeightSyncTurn` snapshot from escrow state after
   applying the trigger diff and drives `AnchorScheduler.Decide`
   for **outbound responses** off that snapshot. A host whose
   response inside `[StartNonce, EndNonce]` does not Anchor has
   provably misbehaved (signed receipt + audit ring). User
   requests in the window are best-effort; nothing rejects them.
7. **Audit / log marking:**
   - Anchors emitted under a forced turn carry an extra debug-log
     key `forced_turn_reason=<reason>`.
   - Audit ring entries gain a local-only `Trigger=forced` marker
     (not on the wire) for evidence ordering.

**Files expected to change:**

- `devshard/proto/devshard/v1/diff.proto` (or wherever `DevshardTx`
  is defined) — add `MsgForceHeightSyncTurn` to the `oneof`; regen
  `devshard/types/diff.pb.go`.
- `devshard/types/errors.go` — `ErrMultipleForceHeightSyncTurnMsgs`.
- `devshard/state/...` (or current owner of escrow state) —
  `ForcedHeightSyncTurn` field + apply rules + tests.
- `devshard/heightsync/anchor.go` — extend `DecideHints` with
  `ForcedTurn`; update `shouldEmit`.
- `devshard/transport/server.go` / `client.go` — read
  `ForcedHeightSyncTurn` from current session/escrow snapshot, pass
  via `DecideHints`. Treat the existing per-request
  `ForceHeightSyncAnchor` flag as the **trigger** (compose
  `MsgForceHeightSyncTurn` if no turn active and we are the diff
  origin; ignore otherwise).
- `devshard/user/user.go` — diff composer prepends
  `MsgForceHeightSyncTurn` when `InferenceParams.ForceHeightSyncAnchor`
  is set and no active forced turn.

**Test plan (cross-references `height-sync-anchor-poc.md` §5.5
"Test coverage"):**

- `devshard/heightsync/anchor_test.go`:
  - `TestAnchorScheduler_ForcedTurn_EmitsForFullWindow`
  - `TestAnchorScheduler_ForcedTurn_CoalescesWithCadenceWindow`
  - `TestAnchorScheduler_ForcedTurn_NonOverlappingDoesNotExtend`
- `devshard/state/...` (or escrow state package):
  - `TestState_MsgForceHeightSyncTurn_OpensAndCloses`
  - `TestState_MsgForceHeightSyncTurn_IgnoredWhileActive`
  - `TestState_MsgForceHeightSyncTurn_AtMostOnePerDiff`
  - `TestState_MsgForceHeightSyncTurn_AppearsOnlyInTriggerDiff` —
    after the user pipeline opens a turn at nonce `N`, diffs `N+1
    .. EndNonce` MUST NOT contain another `MsgForceHeightSyncTurn`.
    Locks "single message in the chain" and prevents accidental
    per-host re-issuance.
- `devshard/transport/server_test.go`:
  - `TestServer_Inference_HeightSync_ForcedTurn_AnchorsAcrossSlots`
  - `TestServer_Inference_HeightSync_ForcedTurn_NoReopenAfterClose`
  - `TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits`
    — host applies a trigger diff that opens
    `ForcedHeightSyncTurn`, then receives an inbound request inside
    `[StartNonce, EndNonce]` whose envelope is plain JSON (no
    `height_sync`). Asserts: response receipt still carries Anchor,
    request still processes (no INVALID), audit ring records a
    `height_sync_force_request_anchor_missing` warn entry.
- `devshard/transport/client_test.go`:
  - `TestHTTPClient_Send_HeightSync_ForcedTurn_AnchorsAcrossSlots`
- `devshard/testenv/scenarios/heightsync_anchor_e2e_test.go` — new
  family `TestHeightSyncAnchor_E2E_ForcedSyncTurn_*` (described in
  `SCENARIOS.md` "Manual-force forced sync turn"):
  - `TestHeightSyncAnchor_E2E_ForcedSyncTurn_AnchorsEntireSlotWindow`
    (honest user, Scenario A)
  - `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits`
    (malicious user, Scenario E — response side normative,
    request side best-effort + warn evidence)
  - `TestHeightSyncAnchor_E2E_ForcedSyncTurn_IgnoresReentryWhileActive`
  - `TestHeightSyncAnchor_E2E_ForcedSyncTurn_CoalescesWithPlannedCadence`

**Migration / compatibility:**

- Single-message tests
  (`TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest`,
  `TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip`,
  `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`) **stay** —
  they lock the trigger plumbing.
- The PoC E2E `…_ForceAnchorOutsideSyncTurn` is then **complemented**
  (not deleted) by `TestHeightSyncAnchor_E2E_ForcedSyncTurn_*`.

---

### Step 8 — Docs update

**Status:** Partially complete (`height-sync-anchor-poc.md` §6 trust/reconcile and §9 tests; **`testenv-blockoracle-integration.md`** still TODO below)

**Remaining planned behavior:**

- Update `devshard/docs/testenv-blockoracle-integration.md` with:
  - new debug logs,
  - explicit `height-sync` RPC,
  - recovery workflow for lost first response,
  - manual verification commands.

---

## Notes

- This status file is intentionally operational (what is implemented now),
  while `height-sync-anchor-poc.md` remains the design/implementation plan.
- Update this file at the end of each completed step so reviewers can see
  deltas and test evidence quickly.
