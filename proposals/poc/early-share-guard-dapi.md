# Proposal: DAPI-only early PoC share guard

## Summary

Add a DAPI-only validation guard for PoC v2 / CPoC v2 that detects participants who deliver too little of their final PoC output during the first third of the generation window.

The guard does not require chain changes. DAPI captures an early checkpoint by querying current on-chain PoC v2 commitments near the first-third boundary, stores those checkpoints locally, and later compares them with the final commitments used for validation. If DAPI misses the early checkpoint for a `(stage, model_id)`, the guard is skipped for that whole stage and normal PoC v2 validation continues.

## Motivation

Current PoC v2 validation checks the final committed artifact store by sampling leaf indices, requesting MMR proofs, and validating the sampled artifacts. This confirms that the final commitment is internally valid, but it does not directly check whether a meaningful share of the final PoC work was already produced early in the PoC window.

The additional guard is intended to make late burst production less attractive. A participant should have a reasonable fraction of its final work committed by the first third of the PoC window, relative to the rest of the network for the same model/stage.

## Current behavior

DAPI validation already queries final commitments in bulk:

- `AllPoCV2StoreCommitsForStage(stage_height)` returns all latest commitments for that PoC/CPoC stage.
- Each commitment is keyed on chain by `(stage_height, participant, model_id)`.
- Later commits overwrite earlier commits in chain state.
- Validators sample `leaf_index` values in `[0, final_count)`, request proofs from `POST /v1/poc/proofs`, verify MMR proofs, then run MLNode statistical validation.

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

### Prefix proof check

The early checkpoint must be tied to the same artifact prefix as the final commitment. Otherwise a participant could submit an early commitment with a root that is valid but unrelated to the final artifact stream.

For each participant/model with an early checkpoint, DAPI ensures that the final validation sample includes at least one shared leaf index:

```text
shared_leaf_index < early_count
```

Recommended sampling approach:

1. Pick one deterministic `shared_leaf_index` from `[0, early_count)`.
2. Pick the remaining `sample_size - 1` leaf indices from `[0, final_count)` using the existing deterministic sampling logic.
3. Deduplicate while preserving the target sample size where possible.

During validation:

1. Request the normal proof set against `(final_root_hash, final_count)`.
2. Request the shared leaf proof against `(early_root_hash, early_count)`.
3. Compare the shared artifact returned by both proof requests:
   - `leaf_index` must match
   - `nonce` must match
   - `vector` must match
4. If the shared artifact differs, treat the early checkpoint as invalid and vote no immediately. This is not subject to the one-miss grace rule used for low early share: an honest participant whose early commitment is a genuine prefix of its final stream can never produce a mismatching shared leaf, so any mismatch is treated as a hard failure.

The comparison uses `leaf_index`, not nonce value. The commitment count is the MMR leaf count, while nonce values are generated values and are not guaranteed to be ordered or bounded by the count.

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
   - shared leaf proof comparison
5. If the guard decides to vote no, return permanent validation failure and submit `ValidatedWeight = -1`.
6. If the guard is unavailable, continue with normal validation.

## Configuration

Suggested DAPI config:

```text
poc_early_share_guard_enabled = false
poc_early_share_first_fraction = 0.3333333333
poc_early_share_threshold_ratio = 0.5
poc_early_share_require_prefix_proof = true
```

The guard should default to disabled until tested on testnet/mainnet in observe-only mode.

Recommended modes:

- `disabled`: no capture, no validation effect
- `observe`: capture checkpoints and log pass/fail decisions, but never vote no
- `enforce`: vote no after the miss-streak rule triggers or prefix proof comparison fails

## Failure behavior

The guard must fail open for missing local data:

- DAPI missed the first-third capture for a `(stage, model_id)`: skip guard for that whole stage.
- DAPI restarted before capture and no checkpoint exists: skip guard.
- Chain query failed at capture time: retry until a configured deadline, then skip.
- Established per-model voting power is unavailable in the validation snapshot (model absent, or its total voting power is `0`): skip guard for that `(stage, model_id)`. There is no unweighted fallback — if the weighting data cannot be obtained, the guard does not run.

Note that a participant present in the final list but absent from the captured early snapshot is *not* a skip case: with the stage captured, it is treated as `early_share = 0` and evaluated normally (see Final comparison).

The guard should fail closed for invalid captured data:

- `early_count > final_count`: vote no if the miss streak triggers; also treat prefix proof as unavailable.
- early root cannot prove the shared leaf: vote no.
- shared leaf proof differs between early and final commitments: vote no.

## Limitations

This is intentionally DAPI-local and does not create a consensus-level rule. Different validators can make different decisions if they miss different checkpoint data. To reduce false negatives, missing checkpoint data must skip the guard rather than penalize.

The robust consensus version would store early checkpoint history or first-third checkpoints on chain. This proposal avoids chain changes and accepts the weaker availability guarantees.

## Implementation touchpoints

- `decentralized-api/poc/phase.go`: helper for first-third target height.
- `decentralized-api/internal/event_listener/new_block_dispatcher.go`: stage detection and checkpoint worker scheduling.
- `decentralized-api/poc/validator.go`: load early guard data, adjust sampling, run threshold/miss/prefix checks. Reuse the `PoCValidationSnapshot.ModelVotingPowers` it already queries for sampling as the per-model weights for the weighted median.
- `decentralized-api/poc/proof_client.go`: reuse `FetchAndVerifyProofs` for the shared early proof request.
- `decentralized-api/internal/server/public/poc_handler.go`: no protocol change required; it already supports requesting proofs for arbitrary `(root_hash, count, leaf_indices)` snapshots present in the local artifact store.
- DAPI local DB package: add checkpoint and guard-state persistence.

## Open questions

- None currently open.
