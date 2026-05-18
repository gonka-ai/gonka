# Height-sync Strong mode — `LightBlock` + `VerifyCommit`, `D` band, (C-strong) confirmation

This plan extends the PoC v2 milestone
([`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) — PoC v2 baseline)
with **Strong mode**: cryptographic mainnet-quorum verification of
`(H, hash)` pairs via a `LightBlock`-equivalent proof, the `|Δ| > D`
escalation rule from the proposal, and the proposal's
**`(C-strong)`** / **`(C-hybrid)`** confirmation rules. After this
milestone, slashable-grade height sync no longer depends on the
host-quorum approximation `(C-quorum)` alone.

Normative spec for everything below lives in
[`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md):

- §"Sync modes" — Anchor / Strong / Omit table
- §"CometBFT `LightBlock` verification" — Steps 1–7, validator-set bind, `>2/3` rule
- §"Trust model" — Strong / Anchor / Omit
- §"Validation pipeline" — step 6 (Strong) + step 7 (same-height / different-hash)
- §"Confirmation API for downstream consumers" — `(C-strong)` and `(C-hybrid)`
- §"Provenance, carry-forward, and malware-host attribution" — `D` band on carry-forward

The text below is the implementation / testing plan; the proposal is
the normative source for any disagreement between this document and
the intended behaviour.

This plan **builds on** PoC v2 / v2.1 (anchor, courier carry-forward,
quorum confirmation, response-leg origin signatures). Nothing here
removes v2 semantics — Strong is **additive** and selected by the
`D` band and policy.

---

## 1. Goals

Ordered from highest to lowest priority.

1. **S1 — `LightBlock`-equivalent proof on the wire** carried inside
   `HeightSyncSection` (`proof_type = cometbft-light-block-v1`,
   `light_block` bytes) with deterministic encoding and forward-compat
   with §"Body envelope (normative)".
2. **S2 — `|Δ| > D` band enforcement** on every inbound Anchor /
   carry-forward Anchor (default `D = 2`). Outside the band, the sender
   MUST escalate to Strong or the receiver classifies **INVALID**.
3. **S3 — Receiver Strong verification path** running the seven steps
   from proposal §"CometBFT `LightBlock` verification" (decode → header
   vs claims → validator set binds header → optional epoch-bound check
   → `BlockID` → commit `>2/3` quorum → recency / staleness).
4. **S4 — Strong producer on hosts** sourcing `light_block` bytes from
   a host-side **header cache** (`[tip − K, tip]`) so Strong does not
   require a synchronous follower round-trip.
5. **S5 — Result class `VALID_STRONG`** with audit-ring + Prom metrics
   parity (`devshard_heightsync_strong_anchors_total`,
   `…_strong_proof_invalid_total`, `…_strong_escalation_total`).
6. **S6 — `(C-strong)` and `(C-hybrid)` confirmation rules** on
   `ConfirmationView`, monotonic, with the same pruning / `W_conf`
   discipline as `(C-quorum)`. Production-class deployments switch to
   `(C-hybrid)` by config.
7. **S7 — DEFERRED_FAIL evidence upgraded to Strong-grade.** When the
   carrier's follower advances past a queued Anchor whose pair does
   **not** match canonical `BlockID.Hash`, evidence carries either the
   originator's signed blob (`DISPUTE_ORIGINATOR`, v2.1 already
   delivers this) **or** a verified `LightBlock` from the receiver
   (Strong-verified canonical pair) so the dispute layer can adjudicate
   without re-fetching from the chain.
8. **In-process e2e first, then container**. Each step lands a unit
   test surface, an in-process e2e scenario, and a docs cross-reference.
   Container parity follows in a separate milestone driven by
   `CONTAINER_E2E_PLAN.md` Phase E.

---

## 2. Scope

### In scope (this milestone)

- `HeightSyncSection.LightBlock` (`bytes`) + `proof_type =
  cometbft-light-block-v1` on the wire (protobuf field 9 + JSON base64).
- Producer-side **Strong source**: an in-process abstraction
  (`StrongSource`) that returns a serialized header-with-commit for a
  given height, backed by the existing **`blockoracle.Header`** +
  **`blockoracle/verifier`** primitives (no CometBFT dependency).
- `D` band enforcement on Anchor + carry-forward Anchor at receivers
  (`D_default = 2`, configurable per deployment).
- Receiver-side **Strong verifier** wrapping `blockoracle/verifier.Verify`,
  bound to a pinned `ValidatorSet` (per chain id), with optional
  epoch-bound Step 3b (pluggable resolver).
- Result class **VALID_STRONG** in `heightsync/inbound.go`; audit ring
  tag `strong`; Prom counters; logs.
- `IsStrictlyConfirmed` selector: `(C-quorum)` (existing),
  `(C-strong)` (new), `(C-hybrid)` (new). Per-deployment config.
- `MsgForceHeightSyncTurn` interaction: forced sync turn signalling
  unchanged; cadence still emits Anchor; Strong is opt-in per envelope
  when `|Δ| > D` or policy requires (`StrongRequired = true`).
- Deferred queue + DEFERRED_FAIL evidence carries either origin signed
  blob (v2.1) or, when receiver has a verified `LightBlock`, the
  canonical header (Strong-grade exoneration / blame).
- In-process e2e for each step (§5).
- Documentation updates: this plan, the implementation-status
  companion, README cross-references, proposal "Status" line bump.

### Out of scope (deferred to follow-on milestones)

- **Real CometBFT `tendermint.types.LightBlock` integration.** The
  proposal's encoding is `tendermint.types.LightBlock`; for the
  milestone we reuse `blockoracle.Header` (already in tree) as a
  binary-compatible carrier. Wire field stays `bytes` so a later
  switch is a content-only migration.
- **Full validator-set rotation crypto**. Step 3b uses a static pinned
  set per chain id; epoch-bound resolution is a pluggable interface
  with a stub implementation (deferred to FINALIZATION_COLLECTOR work).
- **Slashing-tx dispatch.** Evidence APIs are exposed; on-chain
  `MsgHeightSyncEvidence` wiring is owned by the cPoC / dispute plan.
- **Container parity** for Strong mode. Lives in a new Phase E in
  `CONTAINER_E2E_PLAN.md`; in-process tests must stabilise first.
- **Cross-chain Strong** (different mainnet `chain_id`s in the same
  session). Out of scope.
- **Strong on response leg via Sender Signature v2** (signing the
  Strong header with the host's session key). v2.1 response signing
  remains; Strong proof is `LightBlock` itself, no extra signature
  required for the proposal semantics.

---

## 3. Architecture changes

### 3.1 `HeightSyncSection` — Strong fields

Add two optional fields to `HeightSyncSection` (Go struct + JSON
envelope + protobuf field 9; field 8 is `sender_signature` from Step
8):

```go
type HeightSyncSection struct {
    // ...existing fields (1–8)...
    // ProofType already exists; set to StrongProofType for Strong.
    LightBlock []byte `json:"light_block,omitempty"`
}

const (
    AnchorProofType = "height-anchor-v1"             // existing
    StrongProofType = "cometbft-light-block-v1"      // new
)
```

Rules:

- `LightBlock` is **required** when `ProofType == StrongProofType` and
  **MUST be empty** otherwise.
- `MainnetHeight` / `MainnetBlockHashHex` are required for both Anchor
  and Strong; the `LightBlock` payload MUST match both when present.
- `OriginatorSenderID` / `OriginatorTimestampMs` semantics from v2
  apply unchanged. Strong attestations from the host's own oracle MUST
  set the originator triple to the host itself.
- Wire-level: protobuf field 9 (`bytes light_block`); JSON: standard
  base64 of the raw bytes. JSON field name `light_block`.

### 3.2 `StrongSource` — host-side `LightBlock` producer

```go
package heightsync

type StrongSource interface {
    // BuildLightBlock returns the serialized LightBlock bytes for height h.
    // Returns ErrLightBlockUnavailable when h is outside the cache window.
    BuildLightBlock(ctx context.Context, h int64) ([]byte, error)
}
```

Default implementation `BlockOracleStrongSource` wraps the existing
`blockoracle.BlockOracle` history (which already carries
`Commit.Signatures` per `devshard/blockoracle/types.go`):

- Maintains a rolling cache `[tip − K, tip]` (K configurable, default
  `K = 64`).
- `BuildLightBlock(h)` returns `Marshal(blockoracle.Header)` for the
  cached header. The receiver-side verifier (§3.3) decodes the same
  type.
- `ErrLightBlockUnavailable` when `h` is outside the cache window —
  receivers fall back to Anchor (Anchor will then trip the `D` band
  check on the receiver and the sender will be asked to re-emit).

Hosts opt in via `WithStrongSource(src StrongSource)` on
`transport.Server` / `transport.HTTPClient`. Without an opt-in, the
existing Anchor / Omit cadence is unchanged.

### 3.3 Receiver-side `StrongVerifier`

```go
package heightsync

type StrongVerifier interface {
    VerifyLightBlock(
        chainID string,
        h int64,
        blockHash []byte,
        proof []byte,
    ) (VerifiedLightBlock, error)
}

type VerifiedLightBlock struct {
    ChainID        string
    Height         int64
    BlockHash      []byte
    ValidatorsHash []byte
    TimestampMs    int64
}
```

Default implementation `PinnedSetStrongVerifier` wraps
`devshard/blockoracle/verifier.Verifier`:

- Bound to a `verifier.ValidatorSet` per chain id at construction time.
- `VerifyLightBlock` decodes the bytes as `blockoracle.Header`, runs
  the seven steps from proposal §"CometBFT `LightBlock` verification":
  1. **Decode** the bytes.
  2. **Header vs claims** — `hdr.ChainID == chainID`, `hdr.Height ==
     h`, `len(blockHash) == 32`.
  3. **Validator-set binds header** — `hdr.ValidatorsHash == validators
     Merkle root` (already enforced by the existing `Verifier.Verify`
     ecrecover path).
  4. **(Optional) Step 3b — epoch-bound** — when an `EpochParticipants`
     resolver is configured: reject unless `hdr.ValidatorsHash`
     matches the cached per-epoch root. Stubbed in this milestone with
     `NoopEpochResolver` returning *match*; full resolver lives in
     FINALIZATION_COLLECTOR work.
  5. **`BlockID`** — `hdr.BlockHash == blockHash`.
  6. **Commit quorum** — `verifier.Verify(hdr, lastHeight=0)` (`>2/3`
     accumulated power, no duplicates, ecrecover-bound).
  7. **(Optional) Recency / hardening** — `h >= local_tip −
     max_lag_blocks` ⇒ otherwise `VALID_STALE`.

Errors: `ErrStrongChainID`, `ErrStrongHeight`, `ErrStrongHash`,
`ErrStrongValidators`, `ErrStrongCommit`, `ErrStrongStale`.

### 3.4 `D` band on receivers

Both `transport.Server` and `transport.HTTPClient` get:

```go
type StrongPolicy struct {
    D            int64 // proposal default 2; 0 ⇒ DefaultStrongD
    Required     bool  // when true, every Anchor MUST be Strong
    EscalateOnD  bool  // when true, classify INVALID outside D unless Strong
}
```

Inbound classification (`heightsync.ClassifyInboundRequestAnchor`,
`ClassifyInboundResponseAnchor`) gains a `LocalAligned int64` input
(receiver's `blockoracle` height at observation time) and:

- If `|hs.MainnetHeight − LocalAligned| > D` and `hs.ProofType !=
  StrongProofType`: classify **INVALID** with `reason = strong_required`
  (new `ResultInvalidStrongRequired`).
- If `hs.ProofType == StrongProofType`: run `StrongVerifier.VerifyLightBlock`;
  on failure, **INVALID** with `reason = strong_proof_invalid` (new
  `ResultInvalidStrongProof`).
- On success: classify `VALID_STRONG` (new `ResultValidStrong`, audit
  tag `strong`).

Carry-forward Anchors (non-empty `OriginatorSenderID`) outside `D` are
**always** INVALID, regardless of originator freshness — see proposal
§"Provenance, carry-forward, and malware-host attribution" /
§"Validation pipeline" step 5.

### 3.5 Producer-side escalation

`AnchorScheduler.Decide` learns a new `DecideHints.StrongHint`:

```go
type StrongHint struct {
    // Force returns true to require Strong on this nonce regardless of D.
    Force bool
    // PeerAlignedHeight is the latest height we believe the peer has
    // (from cached receiver tips). When 0, sender defaults to its own
    // tip and Strong is opt-in via Force.
    PeerAlignedHeight int64
    // D is the band width; 0 ⇒ DefaultStrongD.
    D int64
}
```

When `|own.MainnetHeight − PeerAlignedHeight| > D` or `Force = true`
and `StrongSource` is configured, the scheduler emits a Strong section
(`ProofType = StrongProofType`, `LightBlock = bytes`). Otherwise
behaviour is unchanged.

The `transport.HTTPClient` populates `StrongHint.PeerAlignedHeight`
from `HeightSyncPeerTips.MaxAlignedFor(recipient)` (new helper) so
the client decides escalation locally. Servers do the same from
`pendingUntrustedTip` plus the inbound audit ring.

### 3.6 Confirmation rules — `(C-strong)` and `(C-hybrid)`

`heightsync.ConfirmationConfig` gains:

```go
type ConfirmationRule int
const (
    ConfirmRuleQuorum ConfirmationRule = iota // (C-quorum) — existing
    ConfirmRuleStrong                         // (C-strong) — new
    ConfirmRuleHybrid                         // (C-hybrid) — either clears
)

type ConfirmationConfig struct {
    Rule ConfirmationRule
    // ...existing Quorum, Freshness, WConf, Roster, Oracle...
    StrongOracle interface { LightBlockSeen(h int64) bool } // new
}
```

`Confirm(h)` returns `confirmed` iff:

- `ConfirmRuleQuorum`: existing logic (`≥ Q` distinct fresh originators).
- `ConfirmRuleStrong`: receiver has at least one **verified**
  `LightBlock` at height ≥ `h` (sourced from `StrongOracle`).
- `ConfirmRuleHybrid`: **either** of the above clears (default for
  production-class deployments).

A monotonic `confirmed_heights` set still guards `confirmed → stale`.
Stale oracle still wins: when the host's `Stale()` is true,
`IsStrictlyConfirmed(_) = stale` for new transitions (`(C-strong)`
without a fresh local follower cannot escalate to confirmed; existing
confirmations are retained).

### 3.7 Audit + metrics

- `heightsync.AnchorCadenceTag` gains `TagStrong`.
- New Prom counters (`heightsync/prom_anchor.go`):
  - `devshard_heightsync_strong_anchors_total{direction}` — Strong
    proofs verified.
  - `devshard_heightsync_strong_proof_invalid_total{reason}` — verify
    failures, labeled by step (`decode`, `header_claims`,
    `validators_hash`, `block_id`, `commit_quorum`, `stale`).
  - `devshard_heightsync_strong_escalation_total{direction}` — sender
    escalated to Strong because `|Δ| > D`.
  - `devshard_heightsync_strong_required_rejected_total` — receiver
    rejected an Anchor outside `D`.

`heightsync.AuditRing` entries gain `LightBlockVerified bool` so the
dispute layer can tell Strong-verified entries from Anchor entries
when assembling evidence.

### 3.8 Forced turns + Strong

Forced sync turns (`MsgForceHeightSyncTurn`) keep their existing
`slots_num`-wide Anchor obligation. When a host's `StrongPolicy.Required
== true` (e.g. for `dispute_open` triggers), the forced turn upgrades
to **Strong** for those nonces; receivers MUST classify Anchor without
`LightBlock` as INVALID for that span. The trigger reason
(`dispute_open`, `operator_force`, …) drives this through a new
`ForceDirective.StrongRequired bool` field (forward-compat: defaults to
false).

### 3.9 Deferred queue + Strong-grade DEFERRED_FAIL

The deferred Anchor queue (existing in v2 for Anchor) keeps its v2.1
attribution behaviour. When the receiver's own follower advances past
`H`, the receiver MAY upgrade its `DEFERRED_FAIL` evidence by attaching
**its own** `LightBlock` for `H` (Strong-verified canonical pair):

- v2.1 evidence: `signed_blob` (origin) + `(H, hash_peer)` + receiver
  oracle's `(H, hash_local)`.
- Strong evidence: same + `LightBlock` for `(H, hash_local)` so the
  dispute layer does not need to retrieve and verify the header
  itself.

This is exposed via `HTTPClient.HeightSyncEvidenceFor(originator, h)`
which returns the cached blob; the receiver-side `Server.LightBlockFor(h)`
returns the Strong proof from its rolling cache (when available).
On-chain `MsgHeightSyncEvidence` wiring remains out of scope.

---

## 4. Step-by-step

Each step is sized so it can land as an independent PR with its own
test surface.

### Step 1 — `HeightSyncSection.LightBlock` + `StrongProofType` on the wire

Status target: ⏳ → ✅

- Add `LightBlock []byte` on `heightsync.HeightSyncSection`.
- Add `StrongProofType = "cometbft-light-block-v1"`.
- Extend `devshard/proto/devshard/v1/inference_envelope.proto`:
  `bytes light_block = 9;`. Regenerate with `make proto`.
- `transport/envelope.go`: map field 9 in `heightSyncToProto` /
  `heightSyncFromProto`. JSON base64 round-trip.
- Update `heightsync/inbound.go::IsAnchorSection` to treat both
  `AnchorProofType` and `StrongProofType` as "anchor sections" for the
  purposes of cadence emission; Strong is a stricter Anchor.

**Tests:**

- `transport/envelope_test.go::TestEnvelope_LightBlock_RoundTrip`
- `transport/envelope_test.go::TestEnvelope_StrongProofType_RoundTrip`
- `transport/envelope_test.go::TestEnvelope_LightBlock_RejectedForAnchorProofType`
- `heightsync/anchor_test.go::TestIsAnchorSection_AcceptsStrong`

### Step 2 — `StrongSource` + producer header cache

- New `heightsync/strong_source.go` with `StrongSource` interface,
  `BlockOracleStrongSource` (rolling cache `[tip − K, tip]`),
  `ErrLightBlockUnavailable`.
- `transport.ServerOption.WithStrongSource(src StrongSource)`.
- `transport.ClientConfig.StrongSource` (optional).
- Hosts populate `HeightSyncSection.LightBlock` when scheduler emits
  `StrongProofType` (via `attachStrongProof(sec, h, src)` mirroring
  `attachResponseOriginSignature`).

**Tests:**

- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_RoundTrip`
- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_OutsideWindow`
- `heightsync/strong_source_test.go::TestBlockOracleStrongSource_CacheRetention`
- `transport/server_test.go::TestServer_AttachesStrongProof_WhenForce`
- `transport/client_test.go::TestClient_AttachesStrongProof_WhenForce`

### Step 3 — `StrongVerifier` + receiver `D` band

- New `heightsync/strong.go`: `StrongVerifier`, `PinnedSetStrongVerifier`
  wrapping `blockoracle/verifier.Verifier`. Optional `EpochResolver`
  with `NoopEpochResolver`.
- New result classes in `heightsync/inbound.go`: `ResultValidStrong`,
  `ResultInvalidStrongRequired`, `ResultInvalidStrongProof`.
- Extend `ClassifyInboundRequestAnchor` / `ClassifyInboundResponseAnchor`
  with `LocalAligned`, `D`, and `StrongVerifier` inputs.
- Carry-forward Anchor outside `D` → INVALID (no Strong path on
  carry-forward in this milestone).

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

- `transport/server.go::recordInboundAnchorIfAnchor` accepts new
  result classes; sets `LightBlockVerified` on the audit entry.
- `heightsync/audit.go::AnchorAttestation.LightBlockVerified bool`,
  `TagStrong AnchorCadenceTag`.
- `heightsync/prom_anchor.go`: new metrics from §3.7.
- `transport/client.go::ingestResponseHeightSync` runs Strong verify
  when the response is Strong; on failure, drop the tip + bump
  `strong_proof_invalid_total` (does not crash other classification).

**Tests:**

- `transport/server_test.go::TestServer_StrongAnchorRecorded`
- `transport/server_test.go::TestServer_StrongRequired_Rejected`
- `transport/server_test.go::TestServer_StrongProofInvalid_Rejected`
- `transport/client_test.go::TestClient_StrongResponse_VerifiedAndCached`
- `transport/client_test.go::TestClient_StrongResponse_InvalidProofDrops`

### Step 5 — Producer escalation (`StrongHint`, `PeerAlignedHeight`)

- `heightsync.DecideHints.StrongHint *StrongHint`.
- `AnchorScheduler.Decide` consults `StrongHint` + `StrongSource`;
  emits Strong section when `|Δ| > D` or `Force = true`.
- `HeightSyncPeerTips.MaxAlignedFor(recipient string) int64` returns
  the recipient's last known aligned height; clients use it as
  `PeerAlignedHeight`.
- Metrics: `strong_escalation_total{direction}`.

**Tests:**

- `heightsync/anchor_test.go::TestDecide_EscalatesToStrong_WhenAboveD`
- `heightsync/anchor_test.go::TestDecide_StaysAnchor_WithinD`
- `heightsync/anchor_test.go::TestDecide_ForceStrong_OverridesD`
- `transport/peer_tips_test.go::TestPeerTips_MaxAlignedFor_TracksRecipient`
- `transport/client_test.go::TestClient_EscalatesToStrong_WhenPeerFarAhead`

### Step 6 — `(C-strong)` and `(C-hybrid)` in `ConfirmationView`

- `heightsync.ConfirmationConfig.Rule` selector.
- `ConfirmationIndex.RecordStrong(h, originator, observedAt)` upserts
  a per-originator Strong-verified height; same `W_conf` pruning.
- `IsStrictlyConfirmed` implements the three rules from §3.6.
- New monotonic `confirmed_heights_strong` set so dropping the
  validator set does not flip a confirmed height back to pending.

**Tests:**

- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_OneVerifiedLightBlock`
- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_BelowQuorumStillConfirms`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_QuorumClearsFirst`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_StrongClearsFirst`
- `heightsync/confirmation_test.go::TestConfirm_RuleStrong_MonotonicWithValidatorSetLoss`
- `heightsync/confirmation_test.go::TestConfirm_RuleHybrid_StaleOracleDoesNotRegress`

### Step 7 — DEFERRED_FAIL + Strong-grade evidence

- `heightsync.AnchorEvidence` (existing v2.1 carrier blob) gains
  optional `ReceiverLightBlock []byte` so the dispute layer can read a
  canonical proof for `(H, hash_local)` directly from the evidence
  blob.
- `HTTPClient.HeightSyncEvidenceFor` (existing) unchanged in shape;
  new sister `Server.LightBlockFor(h int64) ([]byte, bool)` returns
  Strong proof when cached.
- Mock dispute verifier in tests checks both halves: origin signed
  blob (v2.1) for blame and receiver `LightBlock` (Strong) for
  canonical pair.

**Tests:**

- `heightsync/strong_test.go::TestDeferredFail_StrongEvidence_Exonerates`
- `transport/server_test.go::TestServer_LightBlockFor_ReturnsCached`
- `transport/server_test.go::TestServer_LightBlockFor_AbsentOutsideWindow`

### Step 8 — Forced turn `StrongRequired`

- `EscrowHeightSyncHints.StrongRequired bool` (forward-compat default
  false); when true, every envelope under the forced turn MUST be
  Strong (Anchor without `LightBlock` is INVALID).
- Schedulers honour `StrongRequired` from
  `DecideHints.Escrow.StrongRequired`.

**Tests:**

- `heightsync/anchor_test.go::TestDecide_ForcedTurn_StrongRequired_Emits`
- `transport/server_test.go::TestServer_ForcedTurn_StrongRequired_RejectsAnchor`

---

## 5. In-process e2e plan

All scenarios run under `devshard/testenv/scenarios/` using the
existing `httptest.Server` harness — no docker required. Run via:

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncStrong_E2E_'
```

Several scenarios use the existing held-response sync turn harness
(`-tags=dev`).

### S1 — Cold-start Strong escalation when peer far ahead

Four hosts, K=8, slots_num=4, `D=2`. Host A's local oracle is at
`tip=100`; courier user has cached host B's response at `H=200` from a
previous session.

- Nonce 1 outbound to host A: cache says peer-aligned 200, local
  Anchor would say 100 → `|Δ| = 100 > D`.
- Assert: client emits **Strong** section
  (`ProofType=StrongProofType`, `LightBlock != nil`) for nonce 1.
- Host A verifies the proof; classification = **VALID_STRONG**; audit
  tag = `strong`.

Test: `TestHeightSyncStrong_E2E_ColdStartEscalation`.

### S2 — Strong inside `D` band stays Anchor

Hosts B, C, D within `D=2` of receiver's aligned height. Sender emits
Anchor; receiver classifies VALID_ANCHOR; no Strong proof requested.

Test: `TestHeightSyncStrong_E2E_NoEscalationInsideD`.

### S3 — Valid Strong proof accepted on response leg

Host emits Strong on a sync-turn response (e.g. forced trigger).
Courier user verifies the `LightBlock`, caches the section, and
includes the originator triple on the next request. Receiver
classifies the next inbound as VALID_LAZY_ANCHOR (v2 path) with the
origin host attributed.

Test: `TestHeightSyncStrong_E2E_StrongResponseVerifiedByCourier`.

### S4 — Tampered Strong proof rejected with `strong_proof_invalid`

Use a test hook (`SetHeightSyncResponseAfterSignHook` analogue:
`SetHeightSyncStrongAfterAttachHook`) to flip a byte in
`HeightSyncSection.LightBlock`. Assert receiver classifies
**INVALID** with `reason=strong_proof_invalid`; metric
`devshard_heightsync_strong_proof_invalid_total{reason="commit_quorum"}`
increments; tip not cached; audit entry has `LightBlockVerified=false`.

Test: `TestHeightSyncStrong_E2E_TamperedProofRejected`.

### S5 — `(C-strong)`: one verified `LightBlock` confirms

Configure `ConfirmRuleStrong`. Run a single Strong-verified ingest at
`H`. Assert `IsStrictlyConfirmed(H) == confirmed`; previous heights
remain `pending` (no chain back-fill).

Test: `TestHeightSyncStrong_E2E_CStrongOneProofConfirms`.

### S6 — `(C-hybrid)`: either path clears

Variant A: 3 hosts attest at `H` via Anchor → quorum reaches first;
later a Strong proof arrives for the same `H`. Assert monotonicity:
`confirmed` does not flip.

Variant B: 1 host emits Strong → `(C-strong)` clears first; later
quorum builds. Same assertion.

Test: `TestHeightSyncStrong_E2E_CHybridEitherPathClears`.

### S7 — `(C-strong)` monotonic across validator-set loss

Confirmed at `H` under one validator set. Receiver loses the set
(e.g. epoch rotates and resolver not yet caught up). Assert `H` stays
`confirmed` (monotonicity guaranteed by `confirmed_heights_strong`);
new confirmations at higher heights return `pending` until the new
set lands.

Test: `TestHeightSyncStrong_E2E_CStrongMonotonicAcrossSetLoss`.

### S8 — Carry-forward Anchor outside `D` is INVALID

Courier user holds host B's response at `H=200`; sends to host A
whose local aligned is `100`. Even though originator metadata is
valid and fresh, host A classifies the carry-forward as
**INVALID(`strong_required`)** because Strong was not used. Metric
`devshard_heightsync_strong_required_rejected_total` increments.

Test: `TestHeightSyncStrong_E2E_CarryForwardOutsideD_Rejected`.

### S9 — Validator-set rotation (epoch-bound Step 3b)

Use the test `EpochResolver` to gate the validator set per epoch.
Strong proof for `H` in epoch `e` MUST decode against epoch `e`'s
set; the same bytes presented "next epoch" with a swapped set are
INVALID.

Test: `TestHeightSyncStrong_E2E_EpochBoundValidatorSet`.

### S10 — Stale follower + Strong escalation succeeds

Receiver's local follower is stale (`Stale()==true`); sender escalates
to Strong; receiver verifies the proof against its **pinned** set
(does **not** need its follower to be live). Assert Strong path
succeeds even when `(C-quorum)` returns `stale`.

Test: `TestHeightSyncStrong_E2E_StaleFollowerStillVerifiesStrong`.

### S11 — DEFERRED_FAIL upgraded to Strong evidence

Courier user receives host A's Anchor `(H, hash_A)` with a valid
origin signature (v2.1). Host A's claim is wrong: host B's follower
later confirms canonical `hash_B != hash_A`. Host B raises
DEFERRED_FAIL. Evidence carries both halves: host A's signed
`signed_blob_A` (blame) and host B's verified `LightBlock` for
`(H, hash_B)` (canonical). Mock dispute verifier returns
**DISPUTE_ORIGINATOR** + canonical proof attached.

Test: `TestHeightSyncStrong_E2E_DeferredFail_StrongEvidence`.

### S12 — Forced turn with `StrongRequired = true`

`MsgForceHeightSyncTurn` with `StrongRequired = true` (e.g. for a
`dispute_open` trigger). Hosts emit Strong for the next
`slots_num` nonces; receivers MUST reject Anchor (no `LightBlock`) in
the window with `reason = strong_required`.

Test: `TestHeightSyncStrong_E2E_ForcedTurnStrongRequired`.

### Negative variants (sibling tests)

- `…WithDisabledStrong_FallsBackToAnchor` — `StrongPolicy.D = 0` and
  no `StrongSource`: behaviour is identical to v2.
- `…WithUnavailableLightBlock_FallsBackOmitOutsideSyncTurn` — host
  cannot build proof for `h`: scheduler omits and bumps
  `strong_unavailable_total` (no panic, no INVALID on the wire).

---

## 6. Container e2e plan (follow-on milestone, separate document)

Once §5 stabilises in-process, the same scenarios are ported to
`devshard/testenv/scenarios/container/` under `-tags=testenvci`.
Tracking lives in
[`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md)
§7 (new Phase E — "Strong mode"). Sketch:

- `TestContainerE2E_HeightSync_StrongEscalation` — devshardctl + four
  hosts; one host's `heightsyncd` is reconfigured to advance ahead of
  the rest; courier escalates to Strong; Loki assertions match S1.
- `TestContainerE2E_HeightSync_StrongProofInvalid` — Loki line for
  `strong_proof_invalid`; Prom counter increment.
- `TestContainerE2E_HeightSync_StrictConfirmStrong` — Prom gauge
  `devshard_heightsync_confirmed_height_strong` increases as Strong
  proofs land.

---

## 7. Backwards compatibility

- Wire envelope: `light_block` field is optional; old receivers ignore
  it (proto3 / JSON keys). Old senders never set it.
- Receivers without `StrongVerifier` configured behave **exactly** as
  v2 / v2.1: Strong sections fall back to Anchor classification rules.
- Producers without `StrongSource` never emit Strong — `D` band check
  becomes informational warn rather than rejection (`StrongPolicy.EscalateOnD
  = false`).
- `ConfirmationRule` default remains `ConfirmRuleQuorum` for backwards
  compatibility with v2 status; deployments opt into `Hybrid` via
  config.
- Soft-rollout: log warn when `|Δ| > D` and Strong absent for one
  release before flipping `EscalateOnD = true` by default.

---

## 8. Risks

- **Validator-set rotation timing.** Without epoch-bound Step 3b,
  Strong verification will reject proofs from a rotated set. Mitigation:
  ship `EpochResolver` interface even if the default is `Noop`; users
  with a real resolver opt in.
- **Header cache size.** `[tip − K, tip]` cache grows with `K`; default
  `K = 64` heights × ~1.5 KB ≈ 100 KB per host. Mitigation: configurable.
- **`D` tuning.** `D = 2` is the proposal default but small clusters
  with high reorg rates may need `D = 3 .. 5`. Mitigation: per-deployment
  config; the band is documented in proposal §"Sync modes".
- **Conflation with `(C-quorum)`.** Production-class deployments must
  pick `Hybrid` deliberately; PoC stays on `Quorum`. Mitigation: doc
  + lint rule for deployments that set Strong without `Hybrid`.
- **Real CometBFT integration deferred.** Wire format is `bytes`; a
  later switch from `blockoracle.Header` to `tendermint.types.LightBlock`
  is a content-only migration but requires coordinated upgrade.
  Mitigation: keep the verifier behind a single `StrongVerifier`
  interface so the switch is local.
- **Cross-chain proofs.** Receivers reject proofs whose `chain_id`
  does not match the deployment's chain id. Strong cannot bridge
  chains in this milestone.

---

## 9. Test strategy summary

| Layer | Mechanism | Examples |
| --- | --- | --- |
| Unit (Go) | `go test ./heightsync/... ./transport/... ./blockoracle/...` | `StrongVerifier`, `StrongSource`, `Decide` escalation, confirmation rules, Step 3b resolver |
| In-process e2e | `httptest` harness in `testenv/scenarios/...` | S1–S12 above |
| Mock cPoC consumer | Mock verifier calling `IsStrictlyConfirmed(Hybrid)` | S5, S6, S7 |
| Mock dispute consumer | Mock dispute layer verifying canonical `LightBlock` | S11 |
| Negative tests | Hook-based mutation (`SetHeightSyncStrongAfterAttachHook`, etc.) | S4, S9 |
| Container e2e | Separate milestone; `-tags=testenvci` | see §6 |

Coverage targets:

- New code (`StrongVerifier`, `StrongSource`, `ConfirmationRule`
  selector, `D` band classification): **≥ 80 % line**.
- Every result class — `VALID_STRONG`, `INVALID(strong_required)`,
  `INVALID(strong_proof_invalid)` — exercised at least once in
  in-process e2e.
- `(C-strong)` and `(C-hybrid)` each have one happy-path + one
  monotonicity test.

---

## 10. Where this fits with other documents

- **Normative spec:**
  [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)
  §6–§12 (Strong mode, receiver pipeline, confirmation API).
- **PoC v2 baseline:**
  [`height-sync-tests.md`](../docs/height-sync-tests.md) §2–§4.
  Strong is **additive** to v2.
- **Status:**
  [`height-sync-strong-mode-implementation-status.md`](./height-sync-strong-mode-implementation-status.md)
  tracks step-by-step progress against §4 above.
- **Container parity:**
  [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md)
  §7 — Phase E will be opened when this in-process plan completes.
- **Downstream consumers:**
  [`CPOC_PROTOCOL.md`](../docs/proposals/CPOC_PROTOCOL.md) verdict
  predicate selects `(C-hybrid)` once available;
  [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](../docs/proposals/FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)
  consumes Strong-verified `(H, hash)` for finalization timeout
  evidence.

---

## 11. Done definition

This milestone is complete when:

1. Steps 1–8 of §4 are merged with their unit tests.
2. In-process scenarios S1–S12 of §5 are committed under
   `testenv/scenarios/` and pass on a clean checkout.
3. The implementation-status document
   ([`height-sync-strong-mode-implementation-status.md`](./height-sync-strong-mode-implementation-status.md))
   shows every step as **✅ Implemented** with file pointers.
4. The proposal's `Status` line is bumped to "PoC v2 + Strong mode
   implemented; deferred items: real CometBFT `LightBlock` decoder
   migration, on-chain `MsgHeightSyncEvidence` tx".
5. A `Phase E` row exists in
   [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md)
   §7 (status ⏳, no work started) so the container milestone has a
   tracking row.
6. `(C-hybrid)` is selectable by `ConfirmationConfig.Rule` at runtime
   and documented in the README.
