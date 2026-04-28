# PoC Validation Sampling Optimization

## Overview

**Status**: Implemented

This optimization reduces PoC validation complexity from O(N^2) to O(N × N_SLOTS) by assigning each participant a fixed set of validators through weighted random sampling. Only assigned validators validate each participant, and only their votes count for consensus.

**Key Files**:
- Algorithm: `inference-chain/x/inference/calculations/slots.go`
- Chain validation: `inference-chain/x/inference/module/chainvalidation.go`
- DAPI filtering: `decentralized-api/poc/validator.go`
- Snapshot storage: `inference-chain/x/inference/keeper/poc_validation_snapshot.go`
- Proto definitions: `inference-chain/proto/inference/inference/poc_validation_snapshot.proto`
- Parameter: `PocParams.ValidationSlots` in `inference-chain/proto/inference/inference/params.proto`

## Problem

Current PoC validation has O(N^2) complexity where N is the number of active participants:

- Each validator validates ALL participants with commits (`validator.go:ValidateAll` iterates `AllPoCV2StoreCommitsForStage`)
- Chain checks votes from ALL validators for each participant (`chainvalidation.go:pocValidated` iterates `CurrentValidatorWeights`)
- Total validations per epoch: N validators × N participants = N^2

This is not scalable. With 100 participants, 10,000 validations occur per epoch. With 1,000 participants, 1,000,000 validations.

## Solution

Reduce complexity to O(N × N_SLOTS) by assigning each participant a fixed set of validation slots through weighted random sampling. For a model-local validator set, only the model-local share of slots is sampled; the remaining slots behave as implicit abstentions against the full network-wide threshold.

Note: the synthetic-abstention rule in this section was added to support the multi-model delegation design. The original single-model slot approach sampled all `N_SLOTS` from one validator population. In the multi-model case, each model group may hold only a fraction of total network voting power, so the unsampled remainder must count as abstention to preserve the full-network acceptance threshold.

### Core Mechanism

1. Each participant gets `N_SLOTS` total validation slots.
2. For model `group_i`, let:
   - `T = totalNetworkWeight`
   - `G_i = sum(votingPower(group_i, *))`
   - `N_group = floor(G_i / T * N_SLOTS)`
3. Sample only `N_group` slots from the model-local voting-power distribution. The remaining `N_SLOTS - N_group` slots are implicit abstentions.
4. Sampling uses `app_hash` (captured at validation phase start) as randomness source, so both DAPI and chain produce identical assignments:
   ```
   sortedEntries, totalWeight := PrepareSortedEntries(weights)
   assignedValidators := GetSlotsFromSorted(appHash, P.address, sortedEntries, totalWeight, N_group)
   ```
5. Participant passes if >66.7% of the full `N_SLOTS` vote valid (strictly greater than `N_SLOTS * 2 / 3`).

### Weight Synchronization

The validation voting-power inputs must be identical in DAPI and chain at validation time. This is achieved via on-chain `PoCValidationSnapshot` captured at validation phase start (see Appendix: Implementation Details).

### Decision Logic

When slots are enabled, each sampled slot counts as 1 vote. The same validator can appear in multiple sampled slots — this is how model-local weight is encoded. The sampled subset has size `N_group`; the unsampled `N_SLOTS - N_group` slots are implicit abstentions. The threshold is still checked against the full `N_SLOTS`. When `ValidationSlots == 0`, validation uses weight-based counting. When slot mode fails to reach 2/3, runtime fallback is guardian protection. O(N^2) fallback is not implemented.

```go
func (wc *WeightCalculator) pocValidated(vals []types.PoCValidationV2, participantAddress string) bool {
    assignedValidators := wc.getAssignedValidators(participantAddress)
    outcome := wc.calculateAssignedOutcome(vals, assignedValidators)

    // Sample only the model-local share of slots.
    sampledSlots := ComputeSampledSlotCount(modelWeight, totalNetworkWeight, N_SLOTS)
    assignedValidators := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, sampledSlots)

    // 66.7% threshold: need >2/3 of the full slot count.
    // Unsampled slots are implicit abstentions.
    twoThirdsWeight := N_SLOTS * 2 / 3

    if outcome.ValidWeight > twoThirdsWeight {
        return true  // >66.7% voted valid
    }
    if outcome.InvalidWeight > twoThirdsWeight {
        return false // >66.7% voted invalid
    }

    // No supermajority — fall back to guardian protection
    return wc.guardianProtection(vals, participantAddress, outcome)
}
```

## Security Analysis

### Security Model

**Previous model (O(N^2))**: Required >50% of **ALL validator weight** to vote "valid". An attacker needed >50% of total network weight to corrupt any participant's validation.

**Current model (sampled)**: Requires >66.7% of the full slot budget to vote "valid", while only the model-local share of slots is sampled and the remainder are implicit abstentions. Sampling means each participant has a small independent probability of getting an unfavorable slot assignment inside the sampled share, but the missing network weight still counts against approval.

### Binomial Attack Model

With sampling, an attacker controlling fraction `f` of the model-local sampled weight could be over-represented in a specific participant's assigned validators by chance. This follows a binomial distribution inside the sampled share.

**Computation Method**:

Attack probability is calculated using the binomial probability mass function:

```
P(X = k) = C(n, k) * p^k * (1-p)^(n-k)
```

where:
- `n = N_group` (number of sampled validation slots for the model)
- `k =` number of malicious slots
- `p = f` (attacker weight fraction, probability each slot selects attacker validator)
- `C(n, k) = n! / (k! * (n-k)!)` is the binomial coefficient

Attack succeeds only if the attacker controls enough sampled slots to overcome the full-slot threshold after implicit abstentions. In practice this means the model-local group must already represent enough network weight; otherwise no sampled majority can reach the full 2/3 threshold.

```
P(attack) = sum_{k=floor(n*2/3)+1}^{n} P(X = k)
```

To avoid numerical overflow with large factorials, computation uses logarithms:

```
log P(X = k) = log C(n, k) + k*log(p) + (n-k)*log(1-p)
log C(n, k) = sum_{i=0}^{k-1} [log(n-i) - log(i+1)]
```

### Attack Probability Tables (2/3 Threshold)

Per-participant attack probability:

| Attacker Weight (f) | P(>66.7% slots) N=64 | P(>66.7% slots) N=128 | P(>66.7% slots) N=256 |
|---------------------|----------------------|----------------------|----------------------|
| 30% | 9.43×10^-10 | < 10^-10 | < 10^-10 |
| 35% | 1.61×10^-7 | < 10^-10 | < 10^-10 |
| 40% | 0.001010% | 4.78×10^-10 | < 10^-10 |
| 45% | 0.028457% | 3.45×10^-7 | < 10^-10 |
| 49% | 0.251443% | 0.002453% | 6.77×10^-7 |

*Values computed using exact binomial distribution. See Appendix: Simulation.*

**Comparison: 50% vs 66.7% Threshold**:

| Attacker Weight (f) | P(>50% slots) N=128 | P(>66.7% slots) N=128 |
|---------------------|-------------------|-----------------------|
| 30% | 7.07×10^-7 | < 10^-10 |
| 35% | 0.018% | < 10^-10 |
| 40% | 0.868% | 4.78×10^-10 |
| 45% | 11.03% | 3.45×10^-7 |
| 49% | 37.64% | 0.0025% |

### Fake Participant Attack

**Attack Model**: An attacker with `f%` of validator weight attempts to gain network weight by submitting fake participants that claim compute they don't have.

**Attack Process**:
1. Attacker has `f%` of validator weight (e.g., 49%)
2. Attacker creates K fake participants, each claiming weight W
3. Each fake participant gets independent slot assignment via `GetSlotsFromSorted()`
4. Attacker's validators vote YES for fakes; honest validators vote NO (detect fraud)
5. A fake passes if attacker controls >66.7% of its assigned slots
6. If ANY fake passes, attacker gains claimed weight, potentially dominating next epoch

**Probability Model**:

For a single fake participant, the probability it passes is:
```
P_single = P(attacker gets >66.7% of N_SLOTS)
```

This follows the binomial distribution (see Binomial Attack Model above).

For K fake participants, the probability at least one passes is:
```
P(at least 1 passes) = 1 - (1 - P_single)^K
```

**Single Fake Success Probability** (P_single):

| Attacker Weight (f) | N=64, 66.7% | N=128, 66.7% |
|---------------------|-------------|--------------|
| 40% | 0.001010% | 4.78×10^-10 |
| 45% | 0.028457% | 3.45×10^-7 |
| 49% | 0.251443% | 0.002453% |

**Expected Attempts for First Success** (1 / P_single):

| Attacker Weight (f) | N=64 | N=128 |
|---------------------|------|-------|
| 40% | ~99,000 | ~2.1 billion |
| 49% | ~398 | ~40,770 |

**Probability At Least One of K Fakes Passes**:

| K | N=64, f=40% | N=64, f=49% | N=128, f=40% | N=128, f=49% |
|---|------------|-------------|--------------|--------------|
| 10 | 0.0101% | 2.49% | <0.0001% | 0.0245% |
| 100 | 0.10% | 22.26% | <0.0001% | 0.25% |
| 1,000 | 1.00% | 91.93% | <0.0001% | 2.42% |
| 10,000 | 9.60% | ~100% | 0.0005% | 21.75% |

**Attack Feasibility**:

Security depends on what constrains K (attempts per epoch):

- Without collateral: K is limited only by gas fees and epoch duration. Sampling alone is not sufficient protection.
- With collateral proportional to claimed weight W: each attempt costs `cost(W)`, so total budget needed is K × cost(W). For N=128 at f=49%, that's ~40,770 × cost(W).

=> **Collateral proportional to claimed weight (or equivalent mechanism) is a hard requirement for this security model to hold.**

### Abstention Attack

Suppose attacker's validators don't vote. Since the threshold is checked against the full slot budget and unsampled slots are also abstentions, abstention counts against the participant. Honest validators must still reach >66.7% of the full slot budget.

P(honest cannot reach 2/3), N=128:

| Attacker Weight (f) | P(blocked) |
|---------------------|------------|
| 30% | 21.3% |
| 33.3% | 50.8% |
| 40% | 94.3% |
| 49% | 99.98% |

Mitigation: when 66.7% threshold is not met, decision falls back to guardian protection.

#### Future enhancements:
- Exclude non-voting validators from threshold calculation
- Expand to additional slots if 66.7% not reached
- Fall back to O(N^2) with >50% majority

### Slot Assignment Unpredictability

The attacker cannot predict which validators will be assigned to validate their slots, or which participants they'll be assigned to vote on. The `app_hash` used for sampling is captured at VALIDATION phase start — after participants have already committed during GENERATION phase.

### Summary

From the analysis above:

1. **N_SLOTS = 128** for production. Balances security (f=49% needs ~40,770 attempts) with performance (98.72% reduction vs O(N^2)).
2. **2/3 consensus threshold** (>66.7% of slots). Reduces attack probability by orders of magnitude vs 50%.
3. **Collateral proportional to claimed weight** is a hard requirement. Without it, sampling alone does not prevent fake participant attacks.
4. **Guardian protection** as runtime fallback when 2/3 threshold is not met. Slot expansion + O(N^2) fallback remain future work and are not implemented.

## Parameters and Configuration

| Parameter | Location | Default | Notes |
|-----------|----------|---------|-------|
| `ValidationSlots` | `PocParams` in params.proto | 0 (disabled) | Must be set to 128 via governance to enable sampling |
| Consensus threshold | hardcoded | >66.7% of full slot budget | Falls back to guardian if threshold not met |
| Hash source | `PoCValidationSnapshot.AppHash` | - | Captured at validation phase start |

**Configuration**: Set `PocParams.ValidationSlots` via governance. Value of 0 disables sampling and uses weight-based counting.

### Determinism

DAPI and chain produce identical slot assignments because both use the same shared code (`calculations.GetSlotsFromSorted`), the same sort order (alphabetical by address), and the same `PoCValidationSnapshot` for weights and `app_hash`.

## Future Work

### Slot Expansion Fallback

Not implemented. `GetSlotFromSorted()` exists in `calculations/slots.go` for this purpose.

Idea: when the initial sampled slots don't reach 2/3 consensus, expand one slot at a time using the same deterministic sampling (see `validate_host()` in `optimize.py` for prototype). Currently, no-consensus triggers guardian protection or rejection.

## Appendix: Implementation Details

### Weight Synchronization Snapshot

When validation phase begins (`poc_validation_start` or confirmation PoC `GENERATION->VALIDATION`), the chain captures a `PoCValidationSnapshot` containing:
- `app_hash`: The deterministic randomness source from the block header
- `validator_weights`: Current validator weights as `repeated ValidatorWeight` (sorted by address)
- `poc_stage_start_height`: Key for lookup (regular PoC) or `trigger_height` (confirmation PoC)

**Proto Definition** (`poc_validation_snapshot.proto`):
```protobuf
message PoCValidationSnapshot {
  int64 poc_stage_start_height = 1;
  int64 snapshot_height = 2;
  string app_hash = 3;
  repeated ValidatorWeight validator_weights = 4;
}

message ValidatorWeight {
  string address = 1;
  int64 weight = 2;
}
```

**Query Flow**:
- DAPI queries `PoCValidationSnapshot` RPC to get weights and app_hash
- Chain retrieves snapshot from keeper when computing weights
- Both use identical `GetSlotsFromSorted()` algorithm with same inputs

### Slot Algorithm (`inference-chain/x/inference/calculations/slots.go`)

Functions:
- `PrepareSortedEntries(weights)` — Filters and sorts weights alphabetically by address
- `GetSlotsFromSorted(appHash, participantAddress, sortedEntries, totalWeight, nSlots)` — Returns all assigned validators
- `GetSlotFromSorted(appHash, participantAddress, sortedEntries, totalWeight, slotIdx)` — Returns single slot (for future fallback expansion)

Random value generation per slot:
```go
func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
    seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
    hash := sha256.Sum256([]byte(seedData))
    return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}
```

### DAPI Filtering (`decentralized-api/poc/validator.go`)

DAPI filters participants to only validate those where the validator is assigned:

```go
// Query validation snapshot for sampling (if enabled)
validationSlots := int(pocParams.ValidationSlots)
var sortedValidatorEntries []calculations.WeightEntry
var validatorTotalWeight int64
if validationSlots > 0 {
    snapshotResp, err := queryClient.PoCValidationSnapshot(...)
    if err == nil && snapshotResp.Found {
        snapshotWeights := validatorWeightsSliceToMap(snapshotResp.Snapshot.ValidatorWeights)
        sortedValidatorEntries, validatorTotalWeight = calculations.PrepareSortedEntries(snapshotWeights)
    }
}

// Filter to participants where we're assigned
for _, commit := range commitsResp.Commits {
    if validationSlots > 0 && sortedValidatorEntries != nil {
        assignedValidators := calculations.GetSlotsFromSorted(
            snapshotAppHash, commit.ParticipantAddress,
            sortedValidatorEntries, validatorTotalWeight, validationSlots)
        if !slices.Contains(assignedValidators, v.validatorAddress) {
            continue // Skip - not our assignment
        }
    }
    workItems = append(workItems, participantWork{...})
}
```

**Tests**: `inference-chain/x/inference/calculations/slots_test.go`

## Appendix: Simulation

All values in this document can be reproduced using `proposals/poc/simulate.py`.

```bash
# Reproduce all tables
python3 proposals/poc/simulate.py

# Single query
python3 -c "
from proposals.poc.simulate import attack_prob, fake_participant_prob
print(attack_prob(128, 0.49, 2/3))              # P(attack) for N=128, f=49%
print(fake_participant_prob(128, 0.49, 10000))   # P(1 of 10000 fakes passes)
"
```
