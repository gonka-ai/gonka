# Proposal: Multi-Model PoC

POC procedure is short term benchmark to compare how much compute each host has. It happens 1 time per epoch to define weight per each host which then used as consensus weight to produce blocks and for distributing tasks between hosts. Additionally there is Confirmation (random) POC which is used to confirm weight when network is underloaded by inference (to make sure hardware it still there).

POC phases:
- GENERATION (blocks equal to 1-5 min)
- VALIDATION (blocks equal to 2-10 min)
- INFERENCE PHASE (no POC but sometime might be interrupted to Confirmation POC)

> Validation and inference theoretically can be done in parallel.


Current security model required >2/3 of **total network consensus weight** to vote "valid". Without delegation, an attacker needs >2/3 of total network weight to corrupt any (and all) host's validation.

The bitcoin-style part of reward distributed proportionally to this weight. On early phase it's main motivation as inference is much cheaper. 

## Problem

The chain must support multiple models.

Currently the chain can’t support multiple models because we have single-model PoC

Why can’t we support multiple models with single-model PoC?

If we serve multiple models with current single-model PoC, that means that you need to redeploy a model before each cPoC. And if you can do that - you can use this time to deploy models on new nodes. Which essentially opens the network for attack when attacker deploy hardware only for POC phase
Why do we need cPoC at all?

Because a) we want to make sure that if the network load is low - compute is still there b) until the quality of benchmarking hardware by the users’ inference itself is high enough 
Thus the option of redeploying models for PoC vs inference can’t be used and we need to figure out how to support different models during PoC and cPoC.


## Proposal

Let's try to build a system which supports several models simultaneously where POC procedure happens without re-deploy, for every model independently. Such different POCs correspond to quite different compute power (essentially they would measure not raw compute power but how "optimal" the configuration is for specific hardware).
As POC is not only a source of the weight for task distribution across a specific model but also a way to define the consensus weight, we need to define how to aggregate weights from different POCs and how to validate each POC's results.

For aggregation, the chain would have to define how *valuable* each POC's weight is to the chain. Coefficients converting POC weight to consensus weight can be defined as governance parameters by direct voting. They can be defined in a way that bigger, more powerful and more popular models would bring more weight. As the newest hardware is also optimized for serving top-tier models (a lot of VRAM, fast cross-gpu connection, FP4/FP8 support, etc.), it would naturally incentivize hosts to switch newer GPUs to the most powerful models, to get more weight per \$. It's important for the chain's growth to make serving best models (which require most optimized GPUs) most profitable.

This proposal sets the goal to maintain same style of POC validation - every host validates every other host (or its probabilistic analogy for case of slots). One approach to achieve that would be to enforce each host to participate (have hardware) in each model. But such approach is impractical and would raise the hardware requirements too much. To avoid that, the proposal introduces *PoC delegation* from a host to another host it trusts. Such delegation allows to maintain the property of validation by majority of consensus power (but for sure introduces new security assumption, more about it in Appendix A).

To define the process of adding new models to the chain, this proposal allows serving models which are not approved by governance, without inference validation and without gaining consensus power from serving such models. It also defines the process how a model approved by governance becomes eligible for consensus weight.  

> Slot-based validation works the same way: for each model group, only the proportion of total slots that the group's voting power covers is sampled from that group's members. The remaining slots are not reassigned and count as abstention. Acceptance still requires >2/3 of the full global slot count. Slot assignment uses $votingPower$ (delegation-resolved), not raw $consensusWeight$.


### Terms

Let epoch $S$ be current. The following defines weight computation for epoch $S+1$. Pre-eligibility ($PreE_{S+1}$) is determined $N$ blocks before epoch $S+1$ PoC starts. In this section, $*_S$ denotes values from epoch $S$ and used as inputs for epoch $S+1$.
Group membership and delegation are evaluated at the pre-eligibility cutoff and treated as fixed for the epoch.

- $group_i$ — model group for model $i$ (members are hosts with MLNodes serving model $i$). Network supports $M$ models on-chain.

- $pocWeight_S(group_i, p)$ — weight of host $p$ in $group_i$ at epoch $S$. Equals the number of nonces computed by $p$ in PoC procedure for this group and successfully validated. Local weight within the group.

- $consensusKoeff_i$ — coefficient converting $pocWeight$ in $group_i$ to consensus weight. Defined by governance per model. In proto this field is named `weight_scale_factor` (see `PocParams.models[]`); the Go runtime struct field is named `ConsensusKoeff`.

- $consensusWeight_S(p) = \sum_{i: group_i \in E_S} consensusKoeff_i \times pocWeight_S(group_i, p)$ — (see Appendix A for cap protection)

- $members(group_i) = \lbrace p : p \text{ has MLNode deployed for model } i \rbrace$ — hosts with MLNode deployed for the model

- $hosts_S(group_i) = \lbrace p : consensusWeight_S(p) > 0 \text{ and } p \in members(group_i) \rbrace$

  Members with non-zero consensus weight. The weight may come from any eligible group, not necessarily $group_i$.

- $PreE_{S+1}$ — set of pre-eligible groups for epoch $S+1$. A group $group_i \in PreE_{S+1}$ if conditions 1-3 hold:
  1. Model $i$ is approved by governance with defined $consensusKoeff_i$
  2. $\sum_{p \in members(group_i)} consensusWeight_S(p) \geq W_{threshold} \times \sum_{p} consensusWeight_S(p)$
  3. $|hosts_S(group_i)| \geq V_{min}$

- $E_{S+1}$ — set of consensus-eligible groups for epoch $S+1$. A group $group_i \in E_{S+1}$ if:
  - $group_i \in PreE_{S+1}$
  - At least $V_{min}$ hosts in the group pass PoC validation at epoch $S+1$ (see validation rule below)

- $W_{threshold}$ — minimum fraction of total network consensus weight required for group eligibility (governance parameter)

- $V_{min}$ — minimum number of hosts with non-zero consensus weight required in a group (governance parameter)

- Currently $group_{Qwen3-235B-FP8}$ is the only eligible group (single-model PoC). This proposal extends to multiple groups.

- The initial group ($group_{Qwen3-235B-FP8}$) is exempt from the weight cap (Appendix A) and provides base consensus weight for validating new groups.

- A host participating in multiple eligible groups requires separate hardware per group. PoC runs concurrently across all eligible groups within the same epoch.

- $delegation_S(group_i, p_{from}, p_{to})$ — consensus weight delegated from host $p_{from}$ to host $p_{to}$ for validation in $group_i$ at epoch $S$. Host $p_{from} \notin members(group_i)$; host $p_{to} \in members(group_i)$. Delegation is set before epoch start; changes during an epoch take effect from the next epoch.

- $delegationShare$ — fraction of value a delegator shares with the delegate for a group (governance parameter, e.g., 1%)

- $refusalPenalty$ — penalty applied when a host explicitly refuses to participate in a group; should be > $delegationShare$ (governance parameter, e.g., 5%)

- $noParticipationPenalty$ — penalty applied when a host fails to make a participation choice for a governance-approved group (governance parameter, e.g., 0.01)

- $penaltyStartEpoch(group_i)$ — first epoch when participation penalties and delegation share apply for `group_i`

- $votingPower_S(group_i, p) = consensusWeight_S(p) + \sum_{p_{from}} delegation_S(group_i, p_{from}, p)$ — total validation voting power of host $p$ in $group_i$

  Delegation constraints: $delegation_S(group_i, p_{from}, p_{to}) \ge 0$ and, for each $(group_i, p_{from})$, $\sum_{p_{to}} delegation_S(group_i, p_{from}, p_{to}) \le consensusWeight_S(p_{from})$.

### Eligible Groups

Weight computed in PoC procedure for eligible model groups contributes to total consensus weight via governance-defined coefficient. Consensus weight determines:
- Block signing power
- Governance voting power
- PoC validation voting power
- **Bitcoin-style reward distribution** (proportional to consensus weight)

Within a group, inference requests are distributed according to $pocWeight_S(group_i, p)$. Inference rewards follow the same distribution.

### PoC Validation

**Delegation**: Hosts not in a group can delegate their consensus weight to a host who is. The delegate votes on their behalf. Delegation is per-group and set before epoch start.

**Validation rule**: Host $p$'s PoC result in eligible $group_i$ is accepted if:

$$\frac{\sum_{v \text{ votes valid for } p} votingPower_S(group_i, v)}{\sum_{q} consensusWeight_S(q)} > \frac{2}{3}$$

- Numerator: sum of $votingPower_S(group_i, v)$ from all validators $v$ who approved $p$
- Denominator: total network consensus weight (all hosts, all groups)

If valid votes do not exceed `2/3`, and invalid votes also do not exceed `2/3`, the existing guardian tiebreak rule applies.

Hosts not in the group and not delegating effectively vote against approval. Delegation is therefore essential for any group whose direct members hold less than 2/3 of total network weight.

**Voting power details**:
- Number of MLNodes does not matter -- 1 MLNode or 100 MLNodes yields the same vote power
- Delegation changes take effect from next epoch

**Trust model**: Delegator trusts the delegate to vote correctly.

### Participation Modes

Every host with consensus weight resolves to one of five participation modes per governance-approved group at epoch formation:

1. DIRECT — host deploys hardware and submits a PoC store commit for the group. DIRECT is derived from commit presence, not from a separate transaction.
2. DELEGATE — host stores a `MsgSetPoCDelegation` record targeting a group member. `delegationShare` of the delegator's weight transfers to the delegate.
3. REFUSE — host stores a `MsgRefusePoCDelegation` record and pays `refusalPenalty`.
4. INTENT — host stores a `MsgDeclarePoCIntent` record. Effective only for bootstrap models not yet active in the current epoch; ignored for active models. Its purpose is to surface bootstrap pre-eligibility before hosts commit hardware.
5. NONE — host did none of the above. Pays `noParticipationPenalty`.

The three tx-driven records (DELEGATE, REFUSE, INTENT) are mutually exclusive per `(model_id, participant)` with last-write-wins: sending any one of them clears the other two. DIRECT takes precedence over any stored tx record — if a host delegates and also submits PoC, it resolves as DIRECT.

Participation is not enforced at the tx layer. Hosts that submit no commit and store no tx record resolve as NONE. Before $penaltyStartEpoch(group_i)$, all penalties for that group are skipped, giving hosts time to prepare. Starting at $penaltyStartEpoch(group_i)$, REFUSE, NONE, and bootstrap penalties apply.

This incentivizes >2/3 of total consensus weight to participate in PoC validation for every governance-approved group.

### Unregistered Models

Any host can add a model to the chain and serve inference without governance approval (with additional fees).

Properties:
- No inference validation by other hosts
- Price set directly by host
- Requests sent directly to host
- Host stores payload locally but no cross-validation
- Each GNK payment has fee sent to governance
- No bitcoin-style rewards

Purpose: build demo-case for governance proposal to show demand for the model.

### Model Lifecycle

1. Unregistered phase — host adds model, serves inference directly to users, builds demo-case for governance proposal. Not yet implemented (see TODO).
2. Governance proposal — model approved with defined $consensusKoeff_i$, group created.
3. Bootstrap period — for models not yet active, the chain runs a bootstrap flow before PoC starts. At `start_poc - deploy_window`, the chain snapshots INTENT and DELEGATE state for bootstrap candidates, evaluates advisory pre-eligibility (governance approval, $W_{threshold}$, $V_{min}$, $>2/3$ reachability from direct intent + delegations), and emits events so operators see viability before committing hardware. Hosts with INTENT deploy their hardware during the `deploy_window`. At validation start, DIRECT membership is determined by who actually submitted a store commit, not by who declared intent.
4. Pre-penalty phase — PoC runs for the group but participation penalties stay disabled until `penaltyStartEpoch`.
5. Penalty phase — participation rules apply with `noParticipationPenalty`, `delegationShare`, and `refusalPenalty`; eligibility still depends on meeting conditions ($W_{threshold}$, $V_{min}$, passing PoC validation).

A governance-approved group may or may not be eligible in any given epoch depending on whether it meets eligibility conditions. Pre-eligibility is advisory; a group that fails pre-eligibility can still become eligible if enough hosts independently participate in PoC for it.

## Implementation

### Two separate weights

`pocWeight(group_i, p)` is model-local: the number of nonces host `p` computed and got validated in group `i`. It drives inference routing inside the group.

`consensusWeight(p) = sum(weight_scale_factor_i * pocWeight(group_i, p))` is the aggregated chain-wide weight that determines block signing power, governance voting power, PoC validation power, and bitcoin-style reward distribution. Stored as `ActiveParticipant.Weight`.

### Eligibility

A group is eligible for consensus weight when:

1. Governance-approved with a positive coefficient.
2. Members' consensus weight from the prior epoch's root `EpochGroupData` sums to at least `W_threshold` fraction of total prior-epoch network weight. Mid-epoch member removal is reflected: weight of a participant removed from the SDK group during epoch N-1 does not count toward N's eligibility.
3. At least `V_min` members with positive prior-epoch consensus weight (same source) pass PoC.

### Group cap

An additional layer of protection in case validation within a group is compromised. Consensus weight from any non-initial group is capped at `cap_factor * sum(members' prior-epoch consensus weight from other eligible groups)`. If the raw total exceeds the cap, all member contributions in that group are scaled down proportionally. With `cap_factor = 1`, a group can contribute at most as much weight as its members already proved in other groups, which prevents any single non-initial group from exceeding ~50% of total network weight. The initial group is exempt via `DelegationParams.initial_model_id`. Delegation affects voting power but not the cap.

### Validation rule

Host `p`'s PoC result in group `i` is accepted when `sum(votingPower of approvers) / totalNetworkWeight > 2/3`. If neither valid nor invalid votes reach 2/3, the guardian tiebreak rule from pre-multi-model PoC applies: the decision passes only if every voting guardian agrees unanimously. Hosts not in the group and not delegating effectively abstain, which counts against acceptance.

### ActiveParticipant and EpochGroupData

`ActiveParticipant` stores per-model delegation-resolved voting powers in a `repeated ModelVotingPower voting_powers` field. `ActiveParticipants` is written once at epoch formation and stays immutable for the duration of that epoch. Because it cannot reflect mid-epoch changes, the same data is also propagated to `EpochGroupData`, which does reflect mid-epoch changes like member removal.

`EpochGroupData` is the mutable per-epoch state. A root group (`model_id = ""`) stores all members with their consensus weight. Each model subgroup (`model_id = "llama3"`) stores only members serving that model, with model-local fields:

- `ValidationWeight.weight` = sum of raw `PocWeight` for that model (no coefficient).
- `ValidationWeight.ml_nodes` = that model's node info.
- `ValidationWeight.voting_power` = delegation-resolved voting power for that model.

Root group entries have `voting_power = 0` and `ml_nodes = nil`.

Both stores exist because they serve different purposes. `ActiveParticipants` is the permanent historical record of each epoch's active set. `EpochGroupData` reflects the live state: when a host is invalidated mid-epoch, it is removed from the group, and `GetGroupMembers` excludes it from voting power snapshots and consensus weight calculations.

### New on-chain collections and upgrade

PoC commits, weight distributions, and validations gained `model_id` in their storage key. Adding a key component changes the codec, so the old KV prefixes (38, 39, 40) cannot be decoded with the new layout. New collections live under prefixes 58, 59, 60; no post-upgrade reader touches 38/39/40.

The three top-level PoC v2 messages batch all of a host's per-model state into a single transaction per PoC stage. `MsgPoCV2StoreCommit.entries[]` carries one `PoCV2CommitEntry{model_id, count, root_hash}` per model. `MsgMLNodeWeightDistribution.entries[]` carries one `MLNodeDistributionEntry{model_id, weights[]}` per model. `MsgSubmitPocValidationsV2.validations[]` carries one `PoCValidationEntryV2{participant, weight, model_id}` per (validated participant, model).

The `v0.2.12` upgrade handler:

1. Clears all entries under legacy prefixes 38/39/40 with raw store iteration.
2. Migrates singular `PocParams` fields (`model_id`, `seq_len`, `stat_test`, `weight_scale_factor`) into `PocParams.models[]` and sets `penalty_start_epoch = 0` on the resulting entry.
3. Initializes `DelegationParams` with zero-valued defaults (`w_threshold`, `v_min`, `cap_factor`, and all three penalty fractions are 0) and sets `initial_model_id` to the founding model.
4. Backfills `ActiveParticipant.VotingPowers` and subgroup `ValidationWeight.voting_power` for the current epoch (required because pre-upgrade data has these fields at zero).
5. Seeds new pruning state markers so the pruner does not walk empty historical ranges.

### DAPI artifact storage

DAPI stores PoC artifacts in local MMR-backed stores, one per `(stage, model_id)`. The directory layout is `<base>/<stage>/<url-encoded model_id>/`. Multiple model stores under the same stage are accessed concurrently during generation. Proof requests, proof signatures, callback routes (`/v2/poc-batches/:model_id/generated`, `/v2/poc-batches/:model_id/validated`), and artifact-state queries all include `model_id`.

### Delegation snapshots

Two separate snapshots, captured at different times, for different purposes.

`BootstrapDelegationSnapshot` is captured at `start_poc - deploy_window`. It stores delegations and intents for approved models that are not yet active. The chain uses it to evaluate advisory pre-eligibility and, later at validation start, to build voting powers for bootstrap models. It is captured early so operators see pre-eligibility events before committing hardware. The `deploy_window` is the runway for hosts that declared INTENT to provision MLNodes for the new model (on new hardware or on existing hardware) in time for PoC start.

`DelegationSnapshot` is captured at `poc_validation_start`. It stores delegations and refusals (no intents) for all approved models. The chain uses it at epoch formation to resolve participation modes (DIRECT/DELEGATE/REFUSE/NONE) and compute next-epoch voting powers.

For existing active models, validation-time voting powers come from `EpochGroupData` subgroups (already delegation-resolved at previous epoch formation). For bootstrap models, voting powers are computed fresh from consensus weights + bootstrap snapshot + actual store commits.

### Model resolution

Broker resolves which model a node generates PoC for via `resolvePoCModelForNode`:

1. If `EpochMLNodes` has exactly one entry, use that model.
2. If `EpochMLNodes` has multiple entries, skip (ambiguous).
3. If `EpochMLNodes` is empty (fresh node, no epoch assignment yet), pick the first governance-approved model from the node's configured models, alphabetically sorted. Sort is only for determinism; the rest of the pipeline still assumes one configured model per node, so this path is meaningful only in the single-model case.

### Slot sampling

In slot mode, the system computes how many of the global `validation_slots` belong to a model: `floor(modelVotingPower / totalNetworkWeight * validation_slots)`. The remaining global slots stay empty and behave as abstention. Approval still requires `>2/3` of the full global slot count. Slot assignment uses `votingPower` (delegation-resolved). When slot mode is disabled, approval uses model-local delegated voting power against total network weight.

### Weight formation

Two calculators handle different concerns. `PoCWeightCalculator` validates individual PoC results (>2/3 acceptance threshold, slot sampling, guardian tiebreak) and produces raw per-model `pocWeight`. It has no knowledge of cross-model aggregation, delegation economics, or eligibility. `DelegationWeightCalculator` operates after model assignment: it determines eligible groups, applies group caps, computes aggregated `consensusWeight`, resolves participation modes, and computes per-model voting powers. This separation keeps PoC validation independent from the multi-model policy layer.

The epoch formation pipeline in `onEndOfPoCValidationStage`:

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
   - ComputeConsensusWeights: aggregate with coefficients, apply group caps

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

The initial model is exempt from the group cap through `initial_model_id`. Confirmation PoC reuses the same model-aware validation snapshot and weight-calculation path.

`design-1.md` covers the model-aware PoC state, storage keys, and runtime flow in more depth. `design-2.md` covers delegation, voting-power resolution, snapshots, and the `DelegationWeightCalculator` in more depth.

## Open Questions

- **Q1**: Can a host split delegation across multiple hosts in the same group? Current design: no, one target per `(model_id, delegator)`.
- **Q5**: What should the group-cap factor $f$ be? Governance parameter `cap_factor`; current default is zero (cap disabled), concrete value must be set before release.
- Should delegation penalties and `delegation_share` affect only bitcoin-style rewards instead of consensus weight? Current implementation applies both directly to participant consensus weight.
- Should `IsGroupEligible` add the same explicit `>2/3` reachability check currently used only by bootstrap pre-eligibility?
- Mechanism to revoke delegation mid-epoch if delegate votes maliciously.

## TODO

- The unregistered-model phase from the Model Lifecycle section is not implemented. All PoC paths require the model to be governance-approved. If the unregistered-model flow is still required, it needs a separate implementation.
- The upgrade handler leaves `DelegationParams` and every `penalty_start_epoch` at zero. Before release, set concrete values for `w_threshold`, `v_min`, `cap_factor`, the three penalty fractions, and per-model `penalty_start_epoch`.
- When a node declares support for multiple models, the fresh-node fallback in `resolvePoCModelForNode` needs a cleaner resolution path. Current code assumes one configured model per node.

## Appendix A: Delegation-based Attack and Protection

**Attack:** Host accumulates >2/3 $votingPower$ via delegation, validates fake participant claiming large weight, gains consensus control.

**Protection option:** Cap weight from each group by members' proven weight elsewhere.

$$\text{consensus weight from } group_i \leq f \times \sum_{p \in members(group_i)} \text{(}p\text{'s consensus weight from other eligible groups)}$$

If a group's raw PoC weight exceeds the cap, scale all members proportionally to fit.

For clarity: "other eligible groups" refers to consensus weight already earned from eligible groups excluding $group_i$ itself (i.e., using $consensusWeight_S$ contributions from $E_S \setminus \lbrace group_i \rbrace$), to avoid circular dependence.

- Initial group exempt (no cap)
- $f$ is a governance parameter
- Delegation affects $votingPower$ but not the cap (cap is PoC-weight-based)

This bounds the damage from fake participants: even if they pass validation, their weight contribution is limited by real members' stake in other groups. The cap is a secondary defense; validation (>2/3 of network weight) remains the primary one.
