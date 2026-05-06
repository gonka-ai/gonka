# Multi-Model PoC: Aggregation, Delegation, and Voting Power

This document covers the policy layer above the model-aware PoC state in `design-1.md`: how per-model PoC weight becomes chain-wide consensus weight, how non-members of a model group participate in accepting or rejecting that group's PoC results, and where validation voting power is read from. The proposal-level story and shipped summary are in `README.md`.

## Overview

Once PoC becomes per-model, the chain must answer two questions:

1. How does per-model `pocWeight(group_i, p)` become chain-wide `consensusWeight(p)`?
2. How can the chain accept or reject a model-local PoC result when only members of that model group can validate it directly?

These are related but designed independently. Aggregation decides how much consensus value each model contributes. Delegation lets non-members of a model group transfer voting power to a member so the chain can still reach the `>2/3` acceptance threshold.

## Three Weight Terms

`pocWeight(group_i, p)`
- Raw compute proved by host `p` inside model group `group_i`.
- Model-local. Drives inference routing and inference rewards inside the group.

`consensusWeight(p)`
- Aggregated chain-wide weight: `sum(weight_scale_factor_i * pocWeight(group_i, p))` over eligible groups.
- Stored as `ActiveParticipant.Weight`.
- Drives block signing power, governance voting power, PoC validation voting power, and bitcoin-style reward distribution.
- This is the weight that gets delegated.

`votingPower(group_i, p)`
- PoC validation acceptance power held by direct member `p` in model group `group_i`.
- `finalWeight(p) + sum(finalWeight(d) for delegators d -> p in group_i)`.
- `finalWeight` is the post-adjustment weight (after delegation penalties, collateral, and power capping), not raw `consensusWeight`.
- Stored as `ValidationWeight.voting_power` in the model subgroup's `EpochGroupData`.

These three numbers stay separate. A participant can have low `pocWeight` in a group but high `votingPower` from delegators, or high `pocWeight` in a non-eligible group that contributes zero to `consensusWeight`.

## Aggregation

### Why coefficients are needed

Different models measure different kinds of hardware capacity: VRAM requirements differ, tensor parallelism requirements differ, memory bandwidth and interconnect matter differently, throughput per nonce differs. Raw PoC numbers are not directly comparable across models. A per-model coefficient is the simplest conversion rule, and governance can adjust it without redesigning PoC.

### Formula

`consensusWeight(p) = sum over eligible groups i of weight_scale_factor_i * pocWeight(group_i, p)`

The coefficient is called `weight_scale_factor` in proto (`inference-chain/proto/inference/inference/params.proto`) and in `PocParams.models[]`. The Go runtime struct field is named `ConsensusKoeff` and refers to the same value.

### Eligibility

A group is eligible for consensus weight when:

1. Governance-approved with a positive `weight_scale_factor`.
2. Members' consensus weight from the prior epoch's root `EpochGroupData` sums to at least `W_threshold` fraction of total prior-epoch network weight. Mid-epoch member removal is reflected: weight of a participant removed from the SDK group during epoch N-1 does not count toward N's eligibility.
3. At least `V_min` members with positive prior-epoch consensus weight (same source) pass PoC.

### Group cap

An additional layer of protection in case validation within a group is compromised. Consensus weight from any non-initial group is capped at

`cap(group_i) = cap_factor * sum(member's prior-epoch consensus weight from other eligible groups)`

If the raw total exceeds the cap, all member contributions in that group are scaled down proportionally. With `cap_factor = 1`, a group can contribute at most as much weight as its members already proved in other groups, which prevents any single non-initial group from exceeding ~50% of total network weight.

The initial group is exempt from the cap. It is identified by `DelegationParams.initial_model_id`. Delegation affects voting power but not the cap; the cap is PoC-weight-based.

## Delegation State Machine

Three mutable tx-driven records live on-chain until consumed or overwritten.

```proto
message PoCDelegation {
  string model_id    = 1;
  string delegator   = 2;
  string delegate_to = 3;
}
// key: (model_id, delegator)
// persistent until overwritten or cleared

message PoCRefusal {
  string model_id    = 1;
  string participant = 2;
}
// key: (model_id, participant)
// consumed after epoch formation

message PoCDirectIntent {
  string model_id    = 1;
  string participant = 2;
}
// key: (model_id, participant)
// bootstrap-only; consumed after epoch formation
```

At most one of `{delegation, refusal, intent}` exists per `(model_id, participant)` at any time. Sending any one of them clears the other two.

Delegation is per-group, one target per `(model_id, delegator)`. Split delegation is not supported. A group member's own delegation record for that same group is ignored: DIRECT membership takes precedence.

### TX handler rules

`MsgSetPoCDelegation`:
- `model_id` must be governance-approved.
- `delegate_to` (when non-empty) must be valid bech32. Membership is checked at resolution time, not tx time.
- Self-delegation rejected.
- Empty `delegate_to` clears the entry.
- Clears refusal and intent for the same `(model_id, sender)`.

`MsgRefusePoCDelegation`:
- `model_id` must be governance-approved.
- Clears delegation and intent for the same `(model_id, sender)`.

`MsgDeclarePoCIntent`:
- `model_id` must be governance-approved.
- Clears delegation and refusal for the same `(model_id, sender)`.
- If sender submits PoC for this model, they resolve as DIRECT and the intent record is ignored.

Changes take effect from the next epoch.

## Snapshots

Two separate snapshots, captured at different times, for different purposes.

### BootstrapDelegationSnapshot

Captured at `start_poc - deploy_window`. Stores delegations and intents for approved models that are not yet active in `AP(N)`.

```proto
message BootstrapDelegationSnapshot {
  int64 snapshot_height              = 1;
  repeated PoCDelegation delegations = 2;
  repeated PoCDirectIntent intents   = 3;
}
```

The chain uses it to evaluate advisory pre-eligibility for bootstrap candidates (governance approval, `W_threshold`, `V_min`, `>2/3` reachability) and to emit events so operators can see if a bootstrap model looks viable before committing hardware. Later, at validation start, the same snapshot builds voting powers for bootstrap-model direct members.

`deploy_window` is the runway between the snapshot and PoC start for hosts that declared INTENT to provision MLNodes for the new model in time.

A late delegation submitted after this snapshot does not affect the current bootstrap-model validation path. The frozen snapshot still controls current-cycle bootstrap validation.

### DelegationSnapshot

Captured at `poc_validation_start`. Stores delegations and refusals, filtered to active participants and approved models. Intents are intentionally excluded.

```proto
message DelegationSnapshot {
  int64 snapshot_height              = 1;
  repeated PoCDelegation delegations = 2;
  repeated PoCRefusal refusals       = 3;
}
```

The chain uses it at epoch formation to resolve participation modes (DIRECT, DELEGATE, REFUSE, NONE) and compute next-epoch voting powers.

## Participation Modes

For each governance-approved group, every host with consensus weight resolves to one of five modes at epoch formation.

1. DIRECT. The host submitted a PoC store commit for the model this epoch. DIRECT is not a transaction; the chain derives it from the commit's presence.
2. DELEGATE. The host stored a `MsgSetPoCDelegation` record targeting a group member. `delegation_share` of its weight transfers to the target.
3. REFUSE. The host stored a `MsgRefusePoCDelegation` record and pays `refusal_penalty`.
4. INTENT. The host stored a `MsgDeclarePoCIntent` record. Effective only for models not yet active; ignored for models that already have voting power in the current epoch. Its purpose is to surface bootstrap pre-eligibility before hosts commit hardware.
5. NONE. The host did none of the above and pays `no_participation_penalty`.

DIRECT is not exclusive with the tx-driven records. If a host stores a delegation record for group `i` and also submits a PoC store commit for `i`, it resolves as DIRECT and the delegation record is ignored. The three tx-driven records (DELEGATE, REFUSE, INTENT) are mutually exclusive per `(model_id, participant)` with last-write-wins.

Participation is not enforced at the tx layer. Nothing forces a host to choose; hosts that submit no commit and store no tx record resolve as NONE. Each model has a `penalty_start_epoch`, and before that epoch all penalties for that model are skipped, giving hosts time to prepare.

### Resolution implementation

At epoch formation, `ResolveGroupParticipation` reads `DelegationSnapshot` and produces one of four values from the Go `ParticipationMode` enum:

```
ModeDirect
ModeDelegate
ModeRefuse
ModeNone
```

INTENT is not a value in this enum. It is a bootstrap-only signal that feeds `BootstrapDelegationSnapshot` and advisory pre-eligibility events before PoC starts. By the time `ResolveGroupParticipation` runs, `DelegationSnapshot` already excludes intents, and a host that declared INTENT either submitted a store commit (resolves DIRECT) or did not (resolves NONE by default).

Resolution logic (pseudocode):

```
for each participant with ConsensusWeights[p] > 0:
    if p in group.Members         -> DIRECT
    else if Refusals[p]           -> REFUSE
    else if Delegations[p] targets a valid DIRECT member
            with positive weight  -> DELEGATE
    else                          -> NONE
```

An invalid delegate target (not a member, or member with zero weight) resolves to NONE. An event is emitted for operator visibility. Resolution never panics or aborts epoch creation.

A group that fails pre-eligibility can still become eligible if enough hosts independently participate in PoC for it. Pre-eligibility is advisory, not a hard block.

## Validation Voting Power Source

`ActiveParticipants` is immutable once set at epoch formation. If a member is invalidated or deactivated mid-epoch, `AP(N).VotingPowers` would still include them. To exclude removed members from PoC validation, the chain reads validation voting powers from `EpochGroupData` subgroups, where `RemoveMember` (via the SDK group store) physically deletes the member.

### EpochGroupData layout

Stored per `(epochIndex, modelId)`. Each `EpochGroupData` wraps an SDK group.

Root group (`model_id = ""`):
- `validation_weights`: all members.
- `validation_weights[i].weight` = `AP.Weight` (consensus weight).
- `validation_weights[i].ml_nodes` = nil.
- `validation_weights[i].voting_power` = 0.
- `sub_group_models`: list of model IDs.

Model subgroup (`model_id = "llama3"`):
- Separate SDK group with its own `GroupId`.
- `validation_weights`: only members serving that model.
- `validation_weights[i].weight` = sum of raw `PocWeight` for that model's nodes (no coefficient).
- `validation_weights[i].ml_nodes` = model-specific node info.
- `validation_weights[i].voting_power` = delegation-resolved voting power for that model.

A host serving two models has three independent member records across three SDK groups. Root stores the consensus weight; each subgroup stores the model-local PoC weight and the model-local voting power.

### EpochMember bridge

`EpochMember` is an in-memory Go struct that carries data from `ActiveParticipant` into `EpochGroupData`:

```
ActiveParticipant -> NewEpochMemberFromActiveParticipant -> EpochMember
EpochMember       -> AddMember -> updateEpochGroupWithNewMember -> ValidationWeight
```

`AddMember` on the root group writes to the root `EpochGroupData`, then `addToModelGroups` creates a shallow copy (`subMember := member`), overrides its `Weight` with model-local PocWeight, and calls `AddMember` on each subgroup. Each subgroup's `updateEpochGroupWithNewMember` extracts the right model's `voting_power` and `ml_nodes` using `eg.GroupData.ModelId`.

Shallow copying the `VotingPowers` slice is safe because `VotingPowers` is read-only at that point.

| `AP` field    | `EpochMember` field | `ValidationWeight` field | Extraction |
|---------------|---------------------|--------------------------|------------|
| `MlNodes`     | `MlNodes`           | `ml_nodes`               | `getMLNodeInfo(member, eg.GroupData.ModelId)` |
| `VotingPowers`| `VotingPowers`      | `voting_power`           | lookup by `eg.GroupData.ModelId` |

### Read paths by flow

Regular PoC validation, existing models: read from subgroup `EpochGroupData.ValidationWeights.voting_power` via `getEffectiveValidationBaseState` (in `inference-chain/x/inference/module/module.go`). Callers receive the consensus weights and per-model voting powers filtered by live SDK group membership.

Regular PoC validation, bootstrap models: compute fresh from `AP(N).Weight` + `BootstrapDelegationSnapshot` + current stage store commits. No EpochGroupData subgroup exists for bootstrap models yet.

Confirmation PoC: reads voting powers from the effective epoch's subgroup `EpochGroupData.ValidationWeights.voting_power` via the same `getEffectiveValidationBaseState` path.

Inference validation: reads `ValidationWeight.weight` and `ValidationWeight.reputation` via the transient cache. Unchanged by the delegation layer.

### Mid-epoch member removal

`RemoveMember` calls `updateMember(ctx, address, 0, "")`. The Cosmos SDK `UpdateGroupMembers` handler deletes members when weight is set to `"0"` and physically removes them from the group store. `GetGroupMembers` returns only non-deleted members. Removed participants are automatically excluded from both consensus weights and per-model voting powers for the next PoC validation snapshot capture, and from inference validation via the transient cache.

`ActiveParticipant.VotingPowers` is still populated at epoch formation for epoch-transition verification and visibility, but no runtime PoC path reads it as a source.

## Acceptance Rule

Host `p`'s PoC result in group `i` is accepted when

`sum(votingPower of approvers) / totalNetworkWeight > 2/3`

If neither valid nor invalid votes reach 2/3, the guardian tiebreak rule applies: the decision passes only if every voting guardian agrees unanimously. Hosts not in the group and not delegating effectively abstain, which counts against acceptance.

Slot sampling uses the same `votingPower` values. For each model, the slot count is `floor(modelVotingPower / totalNetworkWeight * validation_slots)`. Remaining global slots stay empty and behave as abstention. Approval still requires `>2/3` of the full global slot count.

## DelegationWeightCalculator

The cross-group calculator operates on:

- `Groups`
- `ConsensusWeights` from `AP(N).weight`
- `Delegations`
- `Refusals`
- Governance params `WThreshold`, `VMin`, `CapFactor`

It provides:

- `IsGroupPreEligible`
- `ProjectedReachableVotingPower`
- `MeetsReachabilityThreshold`
- `IsGroupEligible`
- `ResolveGroupParticipation`
- `ComputeGroupCap`
- `ComputeConsensusWeights`
- `ComputeGroupVotingPowers`

Two calculators handle different concerns. `PoCWeightCalculator` validates individual PoC results (`>2/3` acceptance threshold, slot sampling, guardian tiebreak) and produces raw per-model `pocWeight`. It has no knowledge of cross-model aggregation, delegation economics, or eligibility. `DelegationWeightCalculator` operates after model assignment: it determines eligible groups, applies group caps, computes aggregated `consensusWeight`, resolves participation modes, and computes per-model voting powers. This separation keeps PoC validation independent from the multi-model policy layer.

Bootstrap pre-eligibility currently checks governance approval, weight threshold, `V_min`, and explicit `>2/3` reachability. Post-PoC eligibility currently checks governance approval, weight threshold, and at least `V_min` members with positive `pocWeight`; an explicit reachability check is not yet enforced in `IsGroupEligible` (see open questions).

## Epoch Timeline

1. Before `start_poc - deploy_window`
   Regular epoch `N` is running. Delegation, refusal, and direct-intent preferences remain mutable.

2. At `start_poc - deploy_window`
   The chain captures `BootstrapDelegationSnapshot` for bootstrap candidates. It evaluates advisory pre-eligibility for each candidate (governance approval, `W_threshold`, `V_min`, `>2/3` reachability from direct intent + delegations) and emits events. Hosts with INTENT use the deploy window to provision hardware.

3. At `start_poc`
   PoC generation starts for epoch `N+1`. DIRECT membership for bootstrap models is determined by who actually submits a store commit, not by who declared intent.

4. At `poc_validation_start`
   The chain captures `DelegationSnapshot` for all approved models. It then builds `PoCValidationSnapshot.ModelVotingPowers` from two branches: existing active models read voting powers from their subgroup `EpochGroupData.ValidationWeights.voting_power`; bootstrap models compute voting powers fresh from `AP(N).Weight` + `BootstrapDelegationSnapshot` + the just-submitted store commits.

5. At end of PoC validation
   The chain computes `AP(N+1)` via the epoch formation pipeline below.

## Epoch Formation Pipeline

The epoch formation pipeline in `onEndOfPoCValidationStage` (verified against `inference-chain/x/inference/module/module.go:641-768`):

```
1. ComputeNewWeights
   PoCWeightCalculator validates PoC results per (participant, model).
   Preserved nodes keep their previous model bucket.
   Merge preserved + fresh PoC nodes per model.
   Output: activeParticipants with per-model MlNodes and raw pocWeight.

2. setModelsForParticipants
   Assigns each ML node to exactly one governance model.

3. DelegationWeightCalculator
   - EligibleGroups: filter by governance approval + W_threshold + V_min
   - ResolveGroupParticipation: DIRECT/DELEGATE/REFUSE/NONE per model
   - ComputeConsensusWeights: aggregate with weight_scale_factor, apply group caps

4. Delegation + bootstrap penalty accumulation
   Penalties from REFUSE, NONE, and bootstrap modes sum across models,
   capped at 1.0. DELEGATE transfers delegation_share to the target.
   Applied directly to participant weight.

5. AdjustWeightsByCollateral

6. ApplyEpochPowerCapping

7. computeAndSetVotingPowers
   Uses final post-adjustment weights + DelegationSnapshot + DIRECT membership.
   Writes to ActiveParticipant.VotingPowers.

8. AllocateMLNodesForPoC
   Per model, selects a fraction of nodes and sets POC_SLOT=true.
   These nodes continue serving inference during PoC.

9. SetActiveParticipants (immutable from this point)

10. addEpochMembers
    Propagates to EpochGroupData root group and model subgroups.
    addToModelGroups writes subgroup weight = sum of raw PocWeight (no coefficient).
    Subgroup ValidationWeight.voting_power = model-local delegation-resolved power.

11. Cleanup: delete consumed PoCRefusal and PoCDirectIntent entries.
```

## Confirmation PoC

Confirmation PoC reuses the same model-aware validation snapshot path. At the confirmation `GENERATION -> VALIDATION` transition, the chain captures a `PoCValidationSnapshot` whose `ModelVotingPowers` come from the effective epoch's subgroup `EpochGroupData.ValidationWeights.voting_power`, filtered by live SDK group membership. DAPI uses the same snapshot for slot sampling.

## Delegation Weight Adjustment

Design intent:

- REFUSE reduces weight (or reward) by `refusal_penalty`.
- NONE reduces weight (or reward) by `no_participation_penalty`.
- DELEGATE shares `delegation_share` from delegator to delegate.
- Each model may defer all three adjustments until its `penalty_start_epoch`.

Current implementation applies these adjustments directly to participant consensus weight. Whether they should instead affect only bitcoin-style rewards is an open question (see below).

## Upgrade Backfill

The `v0.2.12` upgrade handler backfills `ActiveParticipant.VotingPowers` and subgroup `ValidationWeight.voting_power` for the current epoch. Without this, the first post-upgrade epoch would see no existing-model voting powers and would treat every approved model as a bootstrap candidate. Implemented in `inference-chain/app/upgrades/v0_2_12/upgrades.go` (`backfillVotingPower`, invoked at step 4 of 5 in the upgrade handler).

The upgrade handler also initializes `DelegationParams` with zero-valued defaults (`w_threshold`, `v_min`, `cap_factor`, and all three penalty fractions are 0) and sets `initial_model_id` to the founding model. Concrete values for these fields and per-model `penalty_start_epoch` must be set via governance before penalty enforcement goes live.

## Open Questions

- Should delegation penalties and `delegation_share` affect only bitcoin-style rewards instead of consensus weight? Current implementation applies both directly to participant consensus weight.

## See Also

- `README.md` — proposal-level story and shipped summary.
- `design-1.md` — model-aware PoC state, storage layout, runtime flow.
