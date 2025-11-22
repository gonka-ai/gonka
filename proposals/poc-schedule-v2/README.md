# [IMPLEMENTED]: Schedule for MLNodes to serve inference during PoC

Chain automatically assigns a portion of MLNodes to serve inference during the next PoC slot to keep inference live.
The initial version [model_assignment.go](https://github.com/gonka-ai/gonka/blob/e9dbf137b0fbb050c724877b4b607da88ab1dc64/inference-chain/x/inference/module/model_assignment.go#L139) assigned 50% of weight per participant per model.

## Problem 

After significant chain growth, there is not enough inference at the moment to utilize 50% of compute during the inference phase, so many MLNodes stay unverified.
To raise security (together with confirmation PoC from `proposals/random-poc`), this branch allocates `POC_SLOT=true` by model weight percentages instead of per-participant halves, and samples participants who served in the previous epoch.

New `POC_SLOT=true` allocation (10% target, configured by government):

Main approach: filter eligible nodes per participant -> sample N/2+1 participants with history per model -> allocate smallest eligible nodes round-robin until target weight is reached.

1. Eligibility filtering per participant: top participants whose cumulative weight makes up the top 75% of total capped weight must keep the heaviest 25% of their nodes (by weight) ineligible to preserve voting capacity (`POC_SLOT=false`). Filter out outlier nodes using the IQR method (`Q3 + 1.5*IQR`) and skip the cut when the distribution is flat.
2. Participant rotation per model: sample N/2+1 participants (N = eligible participants for that model) that served in the previous epoch using the deterministic seed `filter_{epoch}_{participants_hash}_{model}`. Only sampled participants contribute nodes to the eligible pool.
3. While building the eligible pool, explicitly keep participants whose all nodes become eligible for POC_SLOT=true (non-voting) under 34% of total capped weight. Phases 1-2 use raw node weights for filtering; Phase 3 uses final capped weights to ensure at least two thirds of validation power keep `PRE_POC` voting capacity.
4. Weight-based allocation per model: compute `targetPoCWeight = PocSlotAllocation * totalModelWeight` (set to 0.1 in upgrade) and iterate round-robin across sampled participants, always flipping the smallest `POC_SLOT=false` node first. Each full cycle must allocate at least once; otherwise we exit early and keep logs for validation.

## Implementation

- Flow is split: `setModelsForParticipants` deterministically assigns governance models to each participant's nodes, then `AllocateMLNodesForPoC` runs a second pass to flag PoC allocation.
- `AllocateMLNodesForPoC` drives per-model allocations instead of per-participant halves. Keeper exposes `GetEpochGroupData` and `GetParams`; `EpochParams.PocSlotAllocation` controls the target fraction (defaults to 0.5 when unset for backward compatibility, but v0.2.5 upgrade explicitly sets it to 0.1).
- `EpochMLNodeData` caches `<model, participant>` node lists, exposes sorted accessors, aggregates node and participant weights, and produces the hashed participant set that seeds deterministic sampling. All subsequent phases reuse this structure so ordering stays deterministic.
- Filtering pipeline:
  - `calculateParticipantWeightThreshold75Percent` + `calculatePerParticipantThreshold` enforce the 75/25 rule: participants representing 75% of capped weight keep at least 25% of their nodes ready for voting. Uniform-weight edge cases downgrade to explicit count caps.
  - `calculateNodeWeightThresholdIQR` trims global outliers at `Q3 + 1.5*IQR`; when the distribution is flat (`IQR=0`) we skip the cut to avoid accidental starvation.
  - `canAllocateParticipantNode` enforces the <34% non-voting ceiling while filling the eligible pool, so at least two thirds of capped weight keep `PRE_POC` voting capacity.
- Sampling and rotation: `sampleEligibleParticipantsWithHistory` shuffles only the addresses that served the model in the previous epoch. From that list we take `len(participants)/2 + 1`; everyone else waits their turn.
- Allocation loop: compute `targetPoCWeight = PocSlotAllocation * totalModelWeight`, iterate participants round-robin, always flipping the smallest `POC_SLOT=false` node first. Each full cycle must allocate at least once; otherwise we exit early, preventing infinite loops when weight is exhausted. Post-pass logs include node IDs, counts, and weights for quick validation.

Net effect: the branch coordinates weight at model scope, keeps rotation history-aware, and preserves deterministic auditing.
