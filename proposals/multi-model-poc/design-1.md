# Multi-Model PoC: Model-Aware State and Flow

This document describes the model-aware PoC state, storage layout, and runtime flow. Aggregation, delegation, and voting-power resolution are in `design-2.md`. The proposal-level story and shipped summary are in `README.md`.

PoC v2 only. PoC v1 is dead code.

## Pre-Multi-Model State

Before this change, mainnet was forced into a single-model shape. Relevant mainnet params:

- `poc_params.model_id = Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`
- `poc_params.seq_len = 1024`
- `poc_params.validation_sample_size = 200`
- `poc_params.validation_slots = 0` (O(N^2) validation, not slot sampling)
- `poc_params.poc_v2_enabled = true`
- `poc_params.confirmation_poc_v2_enabled = true`
- `poc_params.poc_normalization_enabled = true`

Mainnet was pinned to one model by code and upgrades:

- `v0.2.8` pinned `PocParams.model_id` to `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` and removed obsolete governance models.
- `v0.2.9` removed every remaining governance model except that one.
- `decentralized-api/broker/enforced_model.go` defaulted every node to the same model unless `ENFORCED_MODEL_ID` was explicitly disabled.

### Multi-model primitives that already existed

The repo still carried multi-model plumbing around the edges:

- `Model` registry on-chain
- `HardwareNode.models` for per-node model support declarations
- `ActiveParticipant.models` and `ActiveParticipant.ml_nodes` for per-model node arrays
- `EpochGroupData.model_id` and `sub_group_models` for model subgroups
- `GetRandomExecutor(model)` routing through model-specific subgroups

The system was "multi-model around the edges, single-model in the PoC pipeline".

### Old single-model shape

Old `PocParams` held one global `model_id`, `seq_len`, `stat_test`, `weight_scale_factor`. Old PoC v2 records (commit, distribution, validation) keyed by `(stage, participant[, validator])` with no `model_id` component.

At epoch formation, `ComputeNewWeights` produced one flat `MlNodes[0]` array per participant. `setModelsForParticipants` re-derived per-model assignment from `HardwareNode.models` sorted by governance model order, assigning each node to the first model it supported. `addToModelGroups` copied the participant-global `Weight` unchanged into every model subgroup's `ValidationWeight.weight` field. That was accidentally correct only in single-model: the one subgroup weight equalled the one model's raw PoC weight, so copying the global value introduced no error. With two or more models, the same global value would be written into every subgroup regardless of per-model compute.

## Multi-Model Design

One MLNode hosts one model at a time. A host serving two models runs two distinct MLNodes.

### Per-model PoC parameters

`PocParams.models` is a repeated list of per-model configs and is the active source of truth. Old singular `model_id`, `seq_len`, `stat_test`, `weight_scale_factor` fields remain in the proto marked `[deprecated = true]` and are drained by the upgrade handler. Active logic reads only `PocParams.models`.

```proto
message PoCModelConfig {
  string model_id = 1;
  int64 seq_len = 2;
  PoCStatTestParams stat_test = 3;
  Decimal weight_scale_factor = 4;
  int64 penalty_start_epoch = 5;
}
```

Global fields (`validation_sample_size`, `validation_slots`, `poc_normalization_enabled`) stay on `PocParams`.

### Per-model storage identity

Every PoC v2 record that represents participant work binds to `(stage, model_id)` scope. Adding `model_id` to keeper storage is not enough on its own: the local MMR, proof API, artifact callbacks, commit pipeline, and validation pipeline all follow the same `(stage, model_id)` scoping.

Storage keys:

| Record | Old key | New key |
|---|---|---|
| Commit | (stage, participant) | (stage, participant, model_id) |
| Distribution | (stage, participant) | (stage, participant, model_id) |
| Validation | (stage, participant, validator) | (stage, participant, validator, model_id) |
| Validation snapshot | (stage) | (stage), carries `ModelVotingPowers` internally |

`PoCValidationSnapshot` stays stage-keyed. It now carries per-model voting powers as a `ModelVotingPowers` map inside the single snapshot record.

New KV prefixes 58 (`PoCValidationV2`), 59 (`PoCV2StoreCommit`), 60 (`MLNodeWeightDistribution`). Legacy prefixes 38/39/40 are wiped by the upgrade handler because the codec changed with the new key layout and old entries cannot be decoded with the new one.

Query APIs and internal readers are model-aware. Stage-wide readers return atomic `(participant, model)` records, not participant records with repeated model entries that callers split client-side.

### TX messages

The three top-level PoC v2 messages batch all of a host's per-model state into a single transaction per PoC stage. TX batching and persistence identity are separate concerns: a TX batches multiple model-scoped entries for transport, and each entry persists as one model-scoped record.

```proto
message PoCV2CommitEntry {
  string model_id = 1;
  uint32 count = 2;
  bytes root_hash = 3;
}

message MLNodeDistributionEntry {
  string model_id = 1;
  repeated MLNodeWeight weights = 2;
}

message PoCValidationEntryV2 {
  string participant_address = 1;
  string model_id = 2;
  int64 validated_weight = 3;
}
```

`MsgPoCV2StoreCommit.entries[]` carries one `PoCV2CommitEntry` per model. `MsgMLNodeWeightDistribution.entries[]` carries one `MLNodeDistributionEntry` per model. `MsgSubmitPocValidationsV2.validations[]` carries one `PoCValidationEntryV2` per `(validated participant, model)`.

Commit cadence is unchanged: during generation, the participant submits periodic commits as artifacts accumulate. Each commit/distribution TX batches the current per-model entries for transport.

### PoC generation

At upcoming epoch creation time, the chain creates one empty subgroup per entry in `PocParams.models`. `SubGroupModels` is explicit before PoC starts so there is no bootstrap case where the upcoming epoch exists but its parent model list is still empty.

Broker resolves a node's PoC model via `resolvePoCModelForNode`:

1. If `EpochMLNodes` has exactly one entry, use that model.
2. If `EpochMLNodes` has multiple entries, skip (ambiguous).
3. If `EpochMLNodes` is empty (fresh node, no epoch assignment yet), pick the first governance-approved model from the node's configured models, alphabetically sorted. Sort is only for determinism; the rest of the pipeline assumes one configured model per node, so this path is meaningful only in the single-model case.

For a node with a resolved assignment, broker dispatches generation only to the MLNode serving that assigned model in the current epoch. Each model's artifacts go to a separate local store.

### DAPI artifact storage

DAPI stores PoC artifacts in local MMR-backed stores, one per `(stage, model_id)`. The directory layout is `<base>/<stage>/<url-encoded model_id>/`. Multiple model stores under the same stage are accessed concurrently during generation. Proof requests, proof signatures, callback routes (`/v2/poc-batches/:model_id/generated`, `/v2/poc-batches/:model_id/validated`), and artifact-state queries all include `model_id`.

### PoC validation

Direct validation for model X is performed only by MLNodes serving model X. Cross-model validation happens via delegation (see `design-2.md`), which transfers voting power without transferring execution.

Validation work items are keyed by `(participant, model)`. One validation result per `(participant, model, validator)`.

Slot sampling seed: `SHA256(validatorPubKey:blockHash:blockHeight:modelId)`. Different models get independent sampling.

Proof requests include `model_id` to route to the correct artifact store. Validation callback routing uses the model-scoped path `/v2/poc-batches/:model_id/validated`.

Vote power is per-model, delegation-resolved, read from `ValidationWeight.voting_power` on the subgroup. The acceptance rule is `sum(votingPower of approvers) / totalNetworkWeight > 2/3`. If neither valid nor invalid reaches 2/3, the guardian tiebreak rule applies: the decision passes only if every voting guardian agrees unanimously.

Confirmation PoC follows the same model-aware paths: per-model stores, per-model proofs, per-model validation records.

### Preserved nodes stay in model bucket

Previously `GetPreviousEpochMLNodesWithInferenceAllocation` flattened preserved nodes into `MlNodes[0]`, and `setModelsForParticipants` later re-derived model membership from `HardwareNode.models`. A preserved node could lose its proven model bucket and be reassigned to a different model.

Now the previous per-model bucket structure is preserved. Each preserved node keeps its previous `model_id`. Preserved and fresh PoC nodes merge per model, not as a flat list.

### Per-model weight and subgroup state

Subgroup weight per `(participant, model)` is computed first, from the sum of that model's node `PocWeight` values. `addToModelGroups` writes this per-model weight into the subgroup's `ValidationWeight.weight`. Participant-global weight is the aggregated sum of scaled per-model weights (see `design-2.md` for the aggregation formula), then adjusted by collateral and power capping.

Collateral and power capping apply to the aggregated participant weight only. They are participant-level concepts (collateral is staked per participant, not per model). Subgroup weights reflect proven compute; participant weight reflects consensus power.

### Single-model enforcement removed

`enforced_model.go` defaulted all nodes to one model and is removed. Single-model deployments are now expressed by configuration (`PocParams.models` with one entry, or node config with one model) rather than by silently rewriting node state.

## See Also

- `README.md` — proposal-level story and shipped summary.
- `design-2.md` — aggregation, delegation, participation modes, voting-power resolution, epoch formation pipeline.
