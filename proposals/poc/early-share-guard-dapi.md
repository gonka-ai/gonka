# Proposal: DAPI-only early PoC share guard

## Summary

Add a DAPI-only validation guard for PoC v2 / CPoC v2 that detects participants who deliver too little of their final PoC output during the first third of the generation window.

The guard does not require chain changes. DAPI captures an early checkpoint by querying current on-chain PoC v2 commitments near the first-third boundary, stores those checkpoints locally, and later compares them with the final commitments used for validation. If DAPI misses the early checkpoint for a `(stage, model_id)`, the guard is skipped for that whole stage and normal PoC v2 validation continues.

## Motivation

Current PoC v2 validation checks the final committed artifact store by sampling dense leaf indices, requesting SMST proofs, and validating the sampled artifacts. This confirms that the final commitment is internally valid, but it does not directly check whether a meaningful share of the final PoC work was already produced early in the PoC window.

The additional guard is intended to make late burst production less attractive. A participant should have a reasonable fraction of its final work committed by the first third of the PoC window, relative to the rest of the network for the same model/stage.

## Current behavior

DAPI validation already queries final commitments in bulk:

- `AllPoCV2StoreCommitsForStage(stage_height)` returns all latest commitments for that PoC/CPoC stage.
- Each commitment is keyed on chain by `(stage_height, participant, model_id)`.
- Later commits overwrite earlier commits in chain state.
- Validators sample dense `leaf_index` values in `[0, final_count)`, request proofs from `POST /v1/poc/proofs`, verify SMST proofs, then run MLNode statistical validation.

Because earlier commitments are overwritten, DAPI cannot recover an earlier checkpoint later unless it captured it locally at the time or uses historical node state. This proposal uses local DAPI capture and explicitly skips the guard for a whole `(stage, model_id)` when its early capture was missed.

## Design

### Early checkpoint capture

For each active PoC/CPoC stage, DAPI calculates a first-third checkpoint height:

- Regular PoC:
  - `first_third_height = poc_start_height + floor(PocStageDuration / 3)`
- CPoC:
  - `first_third_height = confirmation_event.GenerationStartHeight + floor(PocStageDuration / 3)`

At `first_third_height`, DAPI calls:

```text
AllPoCV2StoreCommitsForStage(stage_height)
```

DAPI stores each returned commitment locally as:

```text
stage_height
participant_address
model_id
early_count
early_root_hash
checkpoint_block_height
captured_at_block_height
```

The early capture has no pre-known participant list: a participant appears in the early snapshot only if an early commit exists for them (which implies `early_count >= 1`). "Missing" is therefore a stage-level condition — if DAPI did not capture the early checkpoint for a `(stage, model_id)` at all, the guard is skipped for every participant in that stage and normal PoC v2 validation continues.

### Final comparison

When validation starts, DAPI already queries the final latest commitments. The guard only runs for a `(stage, model_id)` whose early checkpoint was captured. For each participant with a final commitment in such a stage:

```text
early_share = early_count / final_count
```

where `early_count` is taken from the early snapshot, or `0` if the participant has no early commit (see below).

The entire `(stage, model_id)` is excluded from the guard if no early checkpoint was captured for it. Within a captured stage, an individual participant entry is excluded from the early-share distribution if:

- no final commitment exists
- `final_count == 0`
- `early_count > final_count`
- `early_root_hash` is empty or invalid (for participants that do have an early commit)

If the stage early checkpoint was captured but a participant appears in the final list and not in the early snapshot, they had no committed early work and are treated as:

```text
early_count = 0
early_share = 0
```

Such a participant remains in the distribution and affects both the model/stage P50 and their own pass/fail result. Note that this zero comes from the absence of an early commit, not from a captured commit with `early_count == 0` (a commit always implies `early_count >= 1`). Only a stage with no captured early checkpoint is excluded from the guard entirely.

For each `(stage, model_id)`, DAPI computes a weight-weighted median of `early_share` across participants:

```text
p50_share = weighted_median(early_share, weight = established_voting_power)
threshold_share = p50_share * early_share_threshold_ratio
```

`weighted_median` sorts participants by `early_share`, accumulates their weights, and returns the `early_share` at which cumulative weight crosses half of the total weight.

Initial parameter:

```text
early_share_threshold_ratio = 0.5
```

A participant passes the share check if:

```text
participant_early_share >= threshold_share
```

This makes the guard relative to the observed network behavior for the model/stage instead of using a fixed absolute count.

#### Why the median is weighted, and by what

An unweighted median over participants is trivially gameable: spinning up many cheap hosts that commit little early work drags the median down, lowers the threshold, and lets a genuine late-burst producer slip under it. The median must therefore be weighted by something an attacker cannot fabricate on demand.

The weight is the participant's **established per-model voting power** — the delegation-resolved `voting_power` already used by PoC v2 / CPoC v2 validation for sampling and the on-chain 2/3 threshold. DAPI reads it from the `PoCValidationSnapshot` it already queries for sampling:

```text
weight = snapshot.ModelVotingPowers[model_id][participant_address]
```

(with `snapshot.TotalNetworkWeight` as the per-model denominator). Key properties:

- **Not manipulable by the current submission.** It is the effective, previously-settled weight (effective epoch `N-1` while validating upcoming epoch `N`), frozen into the snapshot at the start of PoC validation. It is not the weight being claimed in the round under evaluation, so it cannot be inflated by the very commitment the guard is policing. This is what removes the circularity.
- **Delegation-resolved.** It already includes PoC delegation directed at the participant for that model, matching the weight the rest of the protocol trusts.
- **Sybil-resistant.** Freshly created hosts have zero established voting power and contribute nothing to the median. Such a participant is still *evaluated* against the threshold (its own weight is simply `0`), but it cannot *set* the threshold.

Do not use `MLNodeWeightDistribution` for this weighting: that is the per-node artifact count for the current stage — the value being claimed and validated now — so weighting by it would be circular and manipulable.

### Local miss streak state

DAPI keeps local state per `(participant_address, model_id)`:

```text
consecutive_misses
updated_stage_height
```

**PoC vs CPoC asymmetry.** Regular PoC early-share is cheap to fake, so a passing *regular PoC* round is not trusted to clear the streak. Only a passing *confirmation PoC (CPoC)* resets it. Failures count the same in either phase. The single streak is shared across PoC and CPoC for a `(participant, model)` (the state is not segmented by phase).

State transition:

- If the participant **passes** the early-share check:
  - if this is a **CPoC** stage: `consecutive_misses = 0`
  - if this is a **regular PoC** stage: no change (do not reset `consecutive_misses`); never vote no on a passing stage
- If the participant **fails** the early-share check (either PoC or CPoC):
  - `consecutive_misses += 1`
  - if `consecutive_misses == 1`, allow this stage (grace)
  - if `consecutive_misses >= 2`, vote no for this participant/model

This allows one miss in a row before voting no. A passing regular PoC does not rescue an existing streak, because PoC output is easy to cheat; only a CPoC pass — which is ungameable — clears the streak.

### Early inclusion check

The early checkpoint must be tied to the same artifact stream as the final commitment. Otherwise a participant could submit an early commitment whose root is internally valid but unrelated to the work behind the final commitment.

The SMST commitment has no notion of insertion order: the root commits a set of artifacts keyed by nonce, and a leaf's dense index is the rank of its nonce among all nonces in that snapshot. Dense indices shift as later artifacts arrive, so the same dense index refers to different nonces in the early and final trees, and no "prefix" relation between the two roots can be checked by index. The property that can be checked is inclusion: artifacts committed early must still be present, unchanged, in the final tree.

For each participant/model with an early checkpoint, DAPI verifies inclusion of a sampled subset of early leaves:

1. Deterministically sample `early_inclusion_sample_size` dense indices from `[0, early_count)` using the existing seeded sampling (validator pubkey + fresh block hash), with a distinct early-inclusion seed domain. All indices target one snapshot; the prover serves them through a batch store API so the snapshot tree is resolved once for the whole request.
2. Request proofs for those indices against `(early_root_hash, early_count)` via the existing `POST /v1/poc/proofs` endpoint. Verify each proof; this yields `(nonce, vector)` pairs cryptographically bound to the early root.
3. Request proofs for those nonces against `(final_root_hash, final_count)` via the by-nonce endpoint (below). Verify each proof and check that the returned nonce equals the requested nonce and the returned vector byte-equals the early vector.

The normal final validation sample over `[0, final_count)` is unchanged and independent of this check.

An honest participant can never fail this check: a nonce inserted before the checkpoint is still present in the final tree (same nonce maps to the same slot; duplicate inserts are rejected), and its vector is a deterministic function of the nonce. Any of the following is therefore a hard failure — vote no immediately, not subject to the one-miss grace rule used for low early share:

- an early or final proof fails verification
- the final proof returns a different nonce or vector
- the by-nonce final proof response omits a nonce proven against the participant's own early root

Network errors and timeouts are not hard failures. In enforce mode they return a retryable validation result and use the existing bounded retry queue; after retry exhaustion the participant is reported invalid through the normal validation path. In observe mode they are logged and validation continues.

Retries use a slow-growing backoff: `3s * 1.5^attempt`, capped at `45s`, with 25 total attempts. This spreads retryable network failures over roughly 14 minutes, while still stopping early if the validation phase ends.

### By-nonce proof endpoint

The existing proofs endpoint is dense-index keyed, and the validator does not know a nonce's dense index in the final snapshot. Add:

```text
POST /v1/poc/proofs/by-nonce
```

Same auth scheme, snapshot binding (`root_hash` + `count` validated via `GetRootAt`), and batch limit as `POST /v1/poc/proofs`, but with `nonces` instead of `leaf_indices`. It gets its own request signing payload with a `poc-proofs-by-nonce-v1` domain tag; without the tag, a signature over a leaf-index request could replay as a nonce request because both carry lists of 32-bit integers. For each found nonce the response carries `dense_index`, `nonce`, `vector`, and `proof`. Missing nonces are omitted; the client treats any omitted requested nonce as `ErrNonceAbsent`, a permanent inclusion failure.

The prover maps nonce to position: walking the nonce's bit path collects the proof siblings, and summing left-sibling counts on right turns yields the dense index. This is O(depth) and needs one new store method, `GetArtifactAndProofByNonce(nonce, snapshotCount)`.

The prover-supplied mapping is trust-free. The proof path is derived from the nonce bits and sibling counts are committed by the root hash, so `VerifySMSTProofWithDenseIndex` binds `(nonce, vector, dense_index)` to the root; a lying prover cannot substitute a leaf or index without failing verification.

SMST also supports non-membership proofs (empty-hash siblings along the nonce path), so "not found" could be made provable. This is deliberately omitted: a malicious prover preferring deniability would time out rather than omit the nonce, and timeouts already funnel into enforce-mode retry handling.

### Snapshot cache and warm-up

SMST snapshots are expensive to rebuild because later inserts mutate the tree. A count equal to the live tree is served directly from it (no rebuild, no cache entry). Historical counts go through a process-wide snapshot tree cache:

- at most 4 snapshot trees in memory across the process
- 20-minute idle eviction for unpinned entries
- single-flight builds, with at most 2 concurrent rebuilds
- final-count prebuilds and warmed early snapshots are pinned, so garbage-count requests cannot evict the hot trees

Both `POST /v1/poc/proofs` and `POST /v1/poc/proofs/by-nonce` use batch store APIs, so a request resolves the snapshot tree once and serves all requested proofs from that tree.

The prover also warms the likely early snapshot on the exact first-fraction block event. It does not infer the count from local flush state. Instead it queries its own on-chain `PoCV2StoreCommit` for each local model and warms that recorded count in the background. If the warm-up is missed or late, the demand-driven cache still builds the snapshot on the first proof request and later retries hit the finished cache entry.

## Local storage

Add a small DAPI-local persistent store, backed by the existing local DB mechanism or a dedicated SQLite file.

Proposed tables:

```sql
CREATE TABLE IF NOT EXISTS poc_early_checkpoints (
  stage_height INTEGER NOT NULL,
  participant_address TEXT NOT NULL,
  model_id TEXT NOT NULL,
  early_count INTEGER NOT NULL,
  early_root_hash BLOB NOT NULL,
  checkpoint_block_height INTEGER NOT NULL,
  captured_at_block_height INTEGER NOT NULL,
  PRIMARY KEY (stage_height, participant_address, model_id)
);

CREATE TABLE IF NOT EXISTS poc_early_guard_state (
  participant_address TEXT NOT NULL,
  model_id TEXT NOT NULL,
  consecutive_misses INTEGER NOT NULL DEFAULT 0,
  updated_stage_height INTEGER NOT NULL,
  PRIMARY KEY (participant_address, model_id)
);
```

Optional operational metadata:

```sql
CREATE TABLE IF NOT EXISTS poc_early_capture_runs (
  stage_height INTEGER NOT NULL,
  model_id TEXT NOT NULL,
  target_block_height INTEGER NOT NULL,
  captured_at_block_height INTEGER NOT NULL,
  captured_commit_count INTEGER NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY (stage_height, model_id)
);
```

### Retention

DAPI already prunes local PoC/CPoC data on a stage/epoch basis: the per-`(stage, model)` artifact store (`ManagedArtifactStore`) keeps only the most recent stages via its background cleanup loop (currently `retainCount = 10`), and `ManagedStorage` retains the last few epochs. The new tables follow the same model:

- `poc_early_checkpoints` and `poc_early_capture_runs` are stage-keyed and ride the existing stage-based pruning. Their retention must be `<=` the artifact store's `retainCount`, since a checkpoint is useless once its stage's artifacts are gone. Simplest is to prune them in the same cleanup loop using the same `retainCount`.
- `poc_early_guard_state` is intentionally longer-lived: it is keyed per `(participant_address, model_id)`, not per stage, and carries the cross-epoch miss streak. It is small and persists across epochs, with at most occasional GC for participants/models that no longer exist.

## DAPI flow

### Block listener / checkpoint worker

Add a DAPI worker that observes chain phase state and schedules one capture per PoC/CPoC stage:

1. Detect current stage and first-third target height.
2. Wait until `target_height`.
3. Query `AllPoCV2StoreCommitsForStage(stage_height)`.
4. Store all returned commitments as local early checkpoints.
5. Mark the capture run as completed.

The worker should be idempotent. If DAPI restarts after capture, it should not overwrite an existing checkpoint set unless explicitly configured to do so.

### Validation path

Extend `OffChainValidator.ValidateAll` / `validateParticipant`:

1. Load final commits as today.
2. Load early checkpoints for the same stage.
3. Compute the per-model weight-weighted P50 over `early_count / final_count`, weighting each participant by its established per-model voting power from the validation snapshot (`snapshot.ModelVotingPowers[model_id]`).
4. Before MLNode statistical validation, run the early guard for participants with available checkpoint data:
   - threshold check
   - miss streak check
   - early inclusion check
5. If the guard decides to vote no, return permanent validation failure and submit `ValidatedWeight = -1`. If the inclusion proof is transiently unavailable in enforce mode, return retryable validation failure and let the existing retry queue handle it.
6. If the guard is unavailable, continue with normal validation.

## Configuration

Suggested DAPI config:

```yaml
early_share_guard:
  mode: disabled
  first_fraction: 0.3333333333
  threshold_ratio: 0.5
  require_inclusion_proof: true
  inclusion_sample_size: 5
```

The guard should default to disabled until tested on testnet/mainnet in observe-only mode.

Recommended modes:

- `disabled`: no capture, no validation effect
- `observe`: capture checkpoints and log pass/fail decisions, but never vote no
- `enforce`: vote no after the miss-streak rule triggers or the early inclusion check fails

## Failure behavior

The guard must fail open for missing local data:

- DAPI missed the first-third capture for a `(stage, model_id)`: skip guard for that whole stage.
- DAPI restarted before capture and no checkpoint exists: skip guard.
- Chain query failed at capture time: retry until a configured deadline, then skip.
- Established per-model voting power is unavailable in the validation snapshot (model absent, or its total voting power is `0`): skip guard for that `(stage, model_id)`. There is no unweighted fallback — if the weighting data cannot be obtained, the guard does not run.

Note that a participant present in the final list but absent from the captured early snapshot is *not* a skip case: with the stage captured, it is treated as `early_share = 0` and evaluated normally (see Final comparison).

The guard should fail closed for invalid captured data:

- `early_count > final_count`: vote no if the miss streak triggers; also skip the inclusion check.
- an early or final inclusion proof fails verification: vote no.
- the final tree does not contain a sampled early nonce with the same vector (mismatch or omitted by-nonce response): vote no.

## Limitations

This is intentionally DAPI-local and does not create a consensus-level rule. Different validators can make different decisions if they miss different checkpoint data. To reduce false negatives, missing checkpoint data must skip the guard rather than penalize.

The robust consensus version would store early checkpoint history or first-third checkpoints on chain. This proposal avoids chain changes and accepts the weaker availability guarantees.

## Implementation touchpoints

- `decentralized-api/poc/phase.go`: helper for first-third target height.
- `decentralized-api/internal/event_listener/new_block_dispatcher.go`: stage detection and checkpoint worker scheduling.
- `decentralized-api/poc/validator.go`: load early guard data, run threshold/miss/inclusion checks. Reuse the `PoCValidationSnapshot.ModelVotingPowers` it already queries for sampling as the per-model weights for the weighted median. Reuse the seeded sampling helper for the early sample; the final validation sample is unchanged. Use slow-growing retry backoff for transient proof failures.
- `decentralized-api/poc/proof_client.go`: reuse `FetchAndVerifyProofs` for the early proofs; add a by-nonce fetch/verify for the final-tree inclusion proofs, treating omitted requested nonces as `ErrNonceAbsent`.
- `decentralized-api/internal/server/public/poc_handler.go`: add `POST /v1/poc/proofs/by-nonce` with a domain-separated request signing payload; the existing `POST /v1/poc/proofs` already serves arbitrary `(root_hash, count, leaf_indices)` snapshots, including the early one.
- `decentralized-api/poc/artifacts`: add batch proof APIs plus `GetArtifactAndProofByNonce(nonce, snapshotCount)` to the `ArtifactStore` interface and SMST store. Add the bounded process-wide snapshot cache.
- `decentralized-api/poc/validator.go` block hook: warm the prover-side early snapshot by querying the chain for the local participant's recorded commit at the first-fraction block.
- DAPI local DB package: add checkpoint and guard-state persistence.

## Open questions

- None currently open.
