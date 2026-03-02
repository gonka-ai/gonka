# GEB-29 / GEB-35 Dispute-Phase Plan

## Goal

Add an objective dispute stage to DKG so dealer validity is not finalized from boolean votes alone. Keep current flow and data model as much as possible, but make `valid_dealers` evidence-based before signing.

## Implementation Status

- [x] Added DKG disputing lifecycle core:
  - `DKG_PHASE_DISPUTING`,
  - `candidate_valid_dealers`,
  - `disputing_phase_deadline_block`,
  - `dispute_phase_duration_blocks`.
- [x] Regenerated protobuf artifacts via `ignite generate proto-go` in `inference-chain`.
- [x] Added automatic phase progression in keeper:
  - `VERIFYING -> DISPUTING` at verifying deadline,
  - `DISPUTING -> COMPLETED/FAILED` at disputing deadline.
- [x] Added `EventDisputePhaseStarted` and emission from keeper.
- [x] Implemented candidate dealer computation and finalization based on candidate set (with backward-compatible fallback recomputation).
- [x] Updated keeper tests for the two-step phase flow.
- [x] Off-chain updates:
  - parse `DKG_PHASE_DISPUTING`/`DKG_PHASE_SIGNED` in epoch JSON parsing,
  - enforce fail-closed signing when consensus-valid dealer shares are incomplete,
  - apply fail-closed checks in threshold signing and group-validation signing paths.
- [x] Existing GEB-35 recomputation path remains active:
  - recompute `AggregatedShares` from consensus `ValidDealers` after final epoch data is known.
- [x] Complaint response tx/message is implemented:
  - `MsgRespondDealerComplaints` (batched responses per dealer tx).
- [x] Complaint evidence is attached to `MsgSubmitVerificationVector`:
  - disputed `(slot_index, ciphertext_index)` pairs are submitted per voted-false dealer during VERIFYING.
- [x] Direct late complaint filing in DISPUTING is removed:
  - complaint set is finalized from verification submissions.
- [x] Basic on-chain complaint storage path is implemented:
  - complaints are persisted in `EpochBLSData.dealer_complaints`,
  - dealer response payloads are stored and indexed by `(dealer_index, complainer_index)`.
- [x] On-chain complaint verification/adjudication is implemented:
  - deterministic ciphertext-binding verification,
  - share-vs-polynomial commitment verification,
  - deterministic dealer/complainer fault assignment.
- [x] Off-chain opening material retention for dispute responses is implemented (current runtime scope: in-memory).
- [ ] Restart-safe persistence for off-chain opening material is pending (survives process/node restart).
- [ ] End-to-end dispute resolution tests are pending (including restart/recovery scenarios).

## Core Design

- Keep `VERIFYING` vote collection.
- Introduce a new `DISPUTING` phase.
- Resolve only disputed dealer-recipient pairs (where recipient voted `false`).
- Finalize `valid_dealers` after dispute resolution.
- Move to `COMPLETED` only if final dealer set is signable.

## Timing and Auto-Transitions (Explicit)

- Dispute phase starts automatically when verifying phase ends:
  - trigger: `current_height >= verifying_phase_deadline_block`.
  - action: compute `candidate_valid_dealers`, set `dkg_phase = DKG_PHASE_DISPUTING`, and set dispute deadline.
- Dispute phase finishes automatically:
  - early finish: if all complaints are resolved before deadline, finalize immediately in the same transition loop,
  - deadline finish: if unresolved items remain at `current_height >= disputing_phase_deadline_block`, finalize deterministically (exclude unresolved dealers or fail epoch based on configured rule).
- Add a module param for duration:
  - `dispute_phase_duration_blocks`.
  - recommended PoC default: `3` blocks (same as current verification window).
- Optional optimization:
  - if there are zero complaints at dispute start, allow immediate `DISPUTING -> COMPLETED` in next transition tick (or same tick if desired by implementation).

## Proto / Types Changes

### `inference-chain/proto/inference/bls/types.proto`

- Extend `DKGPhase`:
  - add `DKG_PHASE_DISPUTING`.
- Extend `EpochBLSData` with:
  - `candidate_valid_dealers` (vote-derived set before disputes),
  - `disputing_phase_deadline_block`,
  - dispute summary fields (counts/status) for deterministic finalization.
- Keep `DealerPartStorage` minimal (no on-chain opening-commitment arrays).
- Ciphertext binding is verified via deterministic re-encryption against submitted ciphertexts.

### `inference-chain/proto/inference/bls/params.proto`

- Extend `Params` with:
  - `dispute_phase_duration_blocks` (int64),
  - default value for PoC: `3`.

### `inference-chain/proto/inference/bls/tx.proto`

- Add `MsgRespondDealerComplaints`:
  - one dealer-scoped message (`dealer_index`) with batched per-complainer response entries,
  - each entry contains `complainer_index`, revealed `share_bytes`, and opening material.
- Extend `MsgSubmitVerificationVector` with per-dealer complaint evidence:
  - `dealer_complaints[]` with `dealer_index`,
  - `disputed_slot_index`,
  - `disputed_ciphertext_index`.

### `inference-chain/proto/inference/bls/events.proto`

- Add:
  - `EventDisputePhaseStarted`,
  - `EventDealerComplaintResponded`.

## Keeper Changes

### `inference-chain/x/bls/keeper/phase_transitions.go`

- Update `ProcessDKGPhaseTransitionForEpoch`:
  - add handling for `DKG_PHASE_DISPUTING`,
  - auto-start dispute phase when verifying deadline is reached,
  - auto-finish dispute phase on early-resolution or dispute deadline.
- Update `CompleteDKG` into two-step finalization:
  - at verifying deadline:
    - compute `candidate_valid_dealers` from `DetermineValidDealersWithConsensus`,
    - transition to `DISPUTING` (set `disputing_phase_deadline_block = current_height + dispute_phase_duration_blocks`).
  - at disputing deadline:
    - apply dispute outcomes,
    - compute final `valid_dealers`,
    - run `ComputeGroupPublicKey`,
    - precompute slot public keys,
    - transition to `COMPLETED` or `FAILED`.
- Keep `DetermineValidDealersWithConsensus` as candidate filter, not final authority.

### Candidate Set Definition (Explicit)

- `candidate_valid_dealers` is computed per dealer index `d` at verifying deadline:
  - `has_dealer_part(d)`: dealer submitted a dealer part for this epoch (and it passed basic/message checks at submission),
  - `approval_quorum(d)`: dealer received at least required approvals in verification vectors.
- Proposed v1 rule:
  - `candidate_valid_dealers[d] = has_dealer_part(d) && (yes_votes(d) >= floor(participant_count/2)+1)`.
- Vote counting semantics:
  - denominator is all epoch participants (`participant_count`),
  - missing verification submission or short vector counts as `no`,
  - each participant may submit at most one verification vector.
- Clarification:
  - candidate dealers are not "voters"; they are dealer indices being evaluated,
  - dealer inclusion in candidate set requires dealer part submission and vote quorum; it does not require the dealer to be in a special voter subset beyond normal participant rules.

### `inference-chain/x/bls/keeper/msg_server_dealer.go`

- `SubmitDealerPart`:
  - validate deterministic indexing for warm keys.

### `inference-chain/x/bls/keeper/msg_server_verifier.go`

- `SubmitVerificationVector` remains vote submission.
- `SubmitVerificationVector` additionally stores complaint evidence for voted-false dealers.
- Add complaint and response handlers:
  - enforce one complaint per `(epoch, dealer, complainer)`,
  - enforce response deadlines,
  - enforce complainant voted `false` for that dealer.
  - no direct complaint tx path (prevents end-of-window griefing).

### New dispute verification path (keeper)

- For each disputed item, verify:
  1. ciphertext binding check passes for the referenced ciphertext and recipient key,
  2. revealed `share_bytes` fails/passes commitment polynomial check.
- Determine fault:
  - dealer fault, complainer fault, or unresolved (timeout/no response).

## Decentralized API Changes

### `decentralized-api/internal/bls/dealer.go`

- `generateDealerPart`:
  - generate per-ciphertext opening material,
  - persist local opening material until dispute phase closes.
- `encryptForParticipant`:
  - return encryption metadata needed for later deterministic binding verification.

### Restart-safe persistence for openings (Planned, not implemented yet)

- Persist opening records to durable local storage (DB/file), keyed by:
  - `epoch_id`,
  - `recipient_index`,
  - `ciphertext_index`.
- Record payload:
  - `slot_index`,
  - `share_bytes` (32 bytes),
  - `opening_seed` (32 bytes),
  - creation/update metadata.
- Security requirements:
  - encrypt at rest using node-local key material (or equivalent protected keystore integration),
  - avoid plaintext seed/share exposure in logs.
- Lifecycle rules:
  - write-through on dealer-part generation (durable before tx submission),
  - load active records on startup for epochs in `DISPUTING`,
  - delete records after epoch leaves disputing/completion window (`COMPLETED`/`FAILED`/`SIGNED`) with conservative TTL fallback.
- Consistency requirements:
  - idempotent upserts by composite key,
  - crash-safe atomic writes,
  - deterministic replay after restart.
- Fallback behavior:
  - if required record is missing after restart, log explicit error and skip response for that complaint (do not fabricate data).

### `decentralized-api/internal/bls/verifier.go`

- `performVerificationAndReconstruction`:
  - keep local verification,
  - track precise per-dealer/per-slot failures to construct complaint payloads.
- `submitVerificationVectorSimplified`:
  - submit `DealerValidity` together with structured `dealer_complaints` for `false` dealers.

### `decentralized-api/internal/bls/manager.go`

- Extend `VerificationResult` with dispute-related local metadata:
  - pending complaints,
  - complaint references submitted,
  - unresolved dealers for current epoch.

## Post-Dispute Consistency Rules (Mandatory)

- After final `valid_dealers` is determined on chain, every participant node must:
  - reload final epoch data,
  - recompute local `AggregatedShares` using final `valid_dealers` only (not local pre-dispute `DealerValidity`),
  - use those recomputed shares for all subsequent signing operations.
- Fail-closed signing rule:
  - if node is missing any required dealer share for any slot where final `valid_dealers[dealer] == true`, do not submit partial signature for that epoch/request.
- Clarification on participation:
  - being a `valid_dealer` is not required to sign,
  - signer eligibility is based on assigned signing slots and successful reconstruction from final `valid_dealers`,
  - dealer validity controls which dealer contributions are included in share aggregation, not which participants are allowed to submit signatures.

## Dispute Rules

- Complaint accepted only if:
  - complainer is epoch participant,
  - complaint references valid dealer and ciphertext indexes,
  - complainer voted `false` for that dealer.
- Dealer response must satisfy binding + correctness checks for all disputed items.
- Resolution:
  - valid dealer response => complaint false,
  - invalid response or no response by deadline => dealer fault.
- Final `valid_dealers`:
  - `candidate_valid_dealers` minus dealer-fault/unresolved dealers.

## Limits and Anti-Griefing

- Cap complaints per epoch and per complainer.
- Require one complaint object per `(dealer, complainer)` carrying a single `(slot_index, ciphertext_index)` pair.
- Add strict deadline windows and no reopen.
- Optionally require complaint bond/slash-on-false-complaint in later iteration.

## Migration / Rollout

- Add proto fields in backward-compatible way.
- Add a governance flag/param to enable dispute phase at a target height.
- During rollout:
  - old epochs remain old flow,
  - new epochs use dispute-enabled flow.

## Test Plan (High-Level)

- Keeper unit tests:
  - transition `VERIFYING -> DISPUTING -> COMPLETED`,
  - complaint validity checks,
  - dealer fault and complainer fault outcomes,
  - timeout/unresolved paths.
- Integration tests:
  - partial malicious dealer set with disputes resolved,
  - ensure final `valid_dealers` produce signable slot keys.
- Decentralized API tests:
  - complaint generation from local verification failures,
  - response path for local dealer openings,
  - cleanup of local opening material after finalization.

## Notes

- This plan keeps the current boolean `DealerValidity` submission but upgrades final dealer selection to objective dispute resolution.
- Long-term clean design remains PVSS-style verifiable encryption. This plan is intended as practical intermediate hardening with bounded complexity.
