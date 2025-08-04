# Tokenomics V2 Proposal: Bitcoin-Style Reward System

This document proposes a fundamental restructuring of the network's reward distribution mechanism, transitioning from the current WorkCoins-based system to a Bitcoin-inspired fixed reward model with enhanced incentives for network participation quality and diversity.

## 1. Summary of Changes

This proposal replaces the current variable RewardCoins calculation with a Bitcoin-style fixed reward system where a predetermined amount of RewardCoins is minted per epoch and distributed among participants based on their Proof of Compute (PoC) weight and performance metrics. **Important**: WorkCoins (user fees) remain unchanged and continue to be distributed based on actual work performed.

**Core Economic Rationale**: Unlike the previous system that attempted to maintain constant rewards per GPU, this model embraces decreasing gonka coin rewards per GPU as the network grows. 

**Critical Problem with Old RewardCoin System**: Network growth leads to more work done, which triggers more RewardCoin minting, creating higher inflation that can decrease gonka coin value even during positive network expansion.

**Solution with New RewardCoin System**: Fixed epoch RewardCoins create scarcity-driven value - when more GPUs join the network, each GPU earns fewer gonka coins from RewardCoins, but the increased mining cost makes each gonka coin more valuable, potentially driving gonka price growth and creating a positive feedback loop for network expansion.

**WorkCoins Remain Important**: Participants still earn user fees (WorkCoins) based on actual inference work performed, ensuring direct compensation for services provided.

**Key Changes:**
- **Fixed Epoch Rewards**: Set amount of RewardCoins minted per epoch (similar to Bitcoin's block rewards)
- **PoC Weight Distribution**: RewardCoins distributed proportionally based on participants' PoC weight
- **WorkCoins Unchanged**: User fees (WorkCoins) continue to be distributed based on actual work performed
- **Gradual Halving**: Smooth reduction in RewardCoins over 4-year cycles instead of discrete halving events
- **Utilization Bonuses**: Higher RewardCoins for MLNodes serving high-demand models (requires simple-schedule-v1 implementation)
- **Model Coverage Incentives**: Additional RewardCoins for participants supporting all governance models (requires simple-schedule-v1 implementation)

The existing variable reward system in `inference-chain/x/inference/keeper/accountsettle.go` will be replaced with a predictable, Bitcoin-inspired reward mechanism that creates stronger long-term incentives for network participation.

## 2. Current vs. New Reward System

### 2.1. Current WorkCoins-Based System

The existing system distributes two types of rewards:
```
WorkCoins: User fees distributed based on actual work performed (STAYS THE SAME)
RewardCoins: Variable subsidies based on total network work (CHANGES TO FIXED)

Current RewardCoins = (Participant Work Done / Total Work) × Variable Total Subsidy
```

**Limitations of Variable RewardCoins:**
- **Inflationary Pressure**: Network growth increases coin minting, potentially decreasing coin value during expansion
- Unpredictable RewardCoin amounts per epoch
- Complex dependency on total network work
- No incentive for model diversity or utilization quality
- Difficult for participants to forecast RewardCoin earnings

### 2.2. New Bitcoin-Style Fixed Reward System

The new system maintains WorkCoins distribution while providing fixed RewardCoin epochs:
```
WorkCoins: User fees distributed based on actual work performed (UNCHANGED)
RewardCoins: Fixed epoch rewards distributed by PoC weight (NEW APPROACH)

WorkCoin Distribution = (Participant Work Done / Total Work) × Total User Fees
RewardCoin Distribution = (Participant PoC Weight / Total PoC Weight) × Fixed Epoch Reward

Total Participant Reward = WorkCoins + RewardCoins
```

**Benefits:**
- **Scarcity Creates Value**: Fixed RewardCoins per epoch means when more GPUs join, each GPU earns fewer gonka coins, making gonka coins harder to mine and more valuable
- **WorkCoins Preserved**: Participants still get user fees based on actual work performed
- Predictable RewardCoin amounts enable better economic planning
- Encourages long-term network participation
- Incentivizes quality of service through utilization bonuses
- Promotes model diversity through coverage incentives
- Simplified RewardCoin calculation and distribution

## 3. Fixed Reward Per Epoch Mechanism

### 3.1. Epoch Reward Structure

Similar to Bitcoin's block rewards, the network will mint a fixed amount of RewardCoins per epoch:

**Initial Epoch Reward**: A governance-defined base amount (285,000 gonka coins per epoch)
**Reward Calculation**: The fixed reward is distributed among all active participants based on their relative PoC weight and performance multipliers
**Predictable Issuance**: Network participants can calculate expected rewards based on their PoC weight and network participation

### 3.2. Distribution Formula

**Phase 1: Basic Bitcoin-Style Rewards (Immediate Implementation)**

In the initial phase, rewards are distributed using a simple proportional system based on each participant's Proof of Compute weight:

1. **Extract Base PoC Weights**: Read each participant's PoC weight from `EpochGroup.ValidationWeights`
2. **Calculate Network Total**: Sum all participants' base PoC weights (available as `EpochGroup.TotalWeight`)
3. **Proportional Distribution**: Each participant receives a share of the fixed epoch reward equal to their percentage of the total network weight

**Example**: If Participant A has 100 PoC weight and the total network has 1,000 PoC weight, Participant A receives 10% of the epoch reward (100/1,000 = 0.1).

```
// Simple PoC weight-based distribution
for each participant:
    participant_weight = participant_poc_weight

total_weight = sum(all_participants.participant_weight)
participant_reward = (participant_weight / total_weight) × fixed_epoch_reward
```

**Future Enhancement: Enhanced Rewards with Bonuses**

After the `simple-schedule-v1-plan.md` system is implemented, the basic PoC weight distribution will be enhanced with additional bonus mechanisms:

- **Utilization Bonuses**: MLNodes serving high-demand models will receive reward multipliers (detailed in Section 5)
- **Model Coverage Incentives**: Participants supporting all governance models will receive additional bonuses (detailed in Section 6)

**Critical Data Source Change**: In Phase 2, the final PoC weights used for distribution will NO LONGER match the raw `EpochGroup.ValidationWeights` because bonuses modify the base weights:

```
Phase 1: Final Weight = EpochGroup.ValidationWeights[participant]
Phase 2: Final Weight = EpochGroup.ValidationWeights[participant] × Utilization Bonus × Coverage Bonus
```

These enhancements will create more sophisticated economic incentives while maintaining the core Bitcoin-style fixed reward structure.

This mechanism will be implemented in `GetBitcoinSettleAmounts()` within `inference-chain/x/inference/keeper/bitcoin_rewards.go`, called by `SettleAccounts()` in `accountsettle.go`.

## 4. Gradual Halving Mechanism

### 4.1. Smooth Reduction Instead of Discrete Halving

Unlike Bitcoin's discrete halving events every 4 years, the network will implement a gradual reduction:

**Decay Rate**: -0.000475 per epoch (equivalent to halving approximately every 1,460 epochs or 4 years)
**Reduction Method**: Continuous exponential decay per epoch
**Formula**: `current_reward = initial_reward × exp(decay_rate × epochs_elapsed)`

### 4.2. Mathematical Implementation

```
decay_rate = -0.000475  // exact decay rate per epoch
initial_epoch_reward = 285,000  // gonka coins per epoch

current_epoch_reward = initial_epoch_reward × exp(decay_rate × epochs_since_genesis)
// Results in total supply: 285,000 / 0.000475 = 600,000,000 gonka coins
```

**Benefits of Gradual Halving:**
- Smoother economic transitions without shock events
- More predictable long-term inflation schedule
- Participants can plan for gradual reward reductions
- Maintains Bitcoin's deflationary characteristics

This logic will be implemented in `CalculateFixedEpochReward(epochsSinceGenesis, initialReward, decayRate)` function within `inference-chain/x/inference/keeper/bitcoin_rewards.go`, called by `GetBitcoinSettleAmounts()` in the same file.

## 5. Utilization-Based Reward Bonuses (Post Simple-Schedule-V1)

### 5.1. Prerequisites for Implementation

**Dependency**: This feature requires the completion of `simple-schedule-v1-plan.md` implementation because it needs:
- Per-MLNode PoC weight tracking (`MLNodeInfo.poc_weight`)
- Model assignment data per MLNode (`MLNode.assigned_model`)
- Per-model utilization data from the dynamic pricing system

**Why Current System Cannot Support This**: The existing reward system only tracks aggregate PoC weight per participant, not the granular per-MLNode, per-model breakdown needed for fair utilization-based bonuses.

### 5.2. Per-MLNode Utilization Bonus Calculation

After `simple-schedule-v1` implementation, the system will apply utilization bonuses at the MLNode level:

**Per-MLNode Bonus Calculation:**
```
for each participant:
    for each mlnode owned by participant:
        // Get the model this MLNode is assigned to serve
        assigned_model = mlnode.assigned_model
        
        // Get average utilization for that model
        model_avg_utilization = get_model_average_utilization(assigned_model)
        
        // Apply utilization bonus to this MLNode's PoC weight
        utilization_multiplier = 1.0 + (model_avg_utilization × utilization_bonus_factor)
        adjusted_poc_weight = mlnode.poc_weight × utilization_multiplier
        
        // Sum all adjusted MLNode weights for this participant
        participant_total_weight += adjusted_poc_weight
```

**Integration with Dynamic Pricing**: This system leverages the per-model utilization data from the dynamic pricing system described in `dynamic-pricing.md`, creating synergy between efficient pricing and reward incentives.

**Implementation Location**: The utilization bonus calculation will be implemented in `CalculateUtilizationBonuses()` function within `inference-chain/x/inference/keeper/bitcoin_rewards.go`, called by `GetBitcoinSettleAmounts()` in the same file, reading data from:
- **Model Assignment**: Per-model `EpochGroupData.ml_nodes` to determine MLNode assignments
- **Utilization Data**: Dynamic pricing system's utilization tracking from `inference-chain/x/inference/keeper/utilization_tracker.go`

## 6. Full Model Coverage Incentives

### 6.1. Model Diversity Bonus (Post Simple-Schedule-V1)

When the multi-model system from `simple-schedule-v1-plan.md` is implemented, participants who support ALL governance models will receive additional reward multipliers.

**Coverage Requirements:**
- Participant must have at least one MLNode allocated to each governance model
- MLNodes must be operational and capable of serving inference requests
- Coverage is verified during epoch formation using the model assignment system

### 6.2. Coverage Bonus Calculation

```
governance_models = get_all_governance_models()
participant_models = get_participant_supported_models(participant)

if participant_models.contains_all(governance_models):
    coverage_multiplier = full_coverage_bonus_factor  // e.g., 1.2 (20% bonus)
else:
    coverage_ratio = participant_models.count / governance_models.count
    coverage_multiplier = 1.0 + (coverage_ratio × partial_coverage_bonus_factor)
```

**Benefits of Model Coverage Incentives:**
- Prevents centralization around popular models only
- Ensures network capacity for specialized/emerging models
- Creates economic incentive for comprehensive model support
- Improves overall network resilience and diversity

**Implementation Integration**: This will be implemented in `CalculateModelCoverageBonuses()` function within `inference-chain/x/inference/keeper/bitcoin_rewards.go`, called by `GetBitcoinSettleAmounts()` in the same file. It will read model assignment data directly from per-model epoch groups (`EpochGroupData.ml_nodes`) to determine which governance models each participant supports, ensuring consistency with the utilization bonus data access patterns.

## 7. Implementation Details

### 7.1. Phased Implementation Approach

**Phase 1: Basic Bitcoin-Style Rewards (Immediate)**
- Fixed epoch rewards with gradual halving
- Simple PoC weight-based distribution
- No dependency on simple-schedule-v1

**Phase 2: Enhanced Rewards with Bonuses (Post Simple-Schedule-V1)**
- Per-MLNode utilization bonuses
- Model coverage incentives
- Requires per-MLNode PoC weight tracking

### 7.2. Centralized Reward Calculation

**In `inference-chain/x/inference/keeper/bitcoin_rewards.go` (new file):**

**Main Bitcoin Reward Function:**
- `GetBitcoinSettleAmounts(participants, epochGroupData, bitcoinParams)` - replaces `GetSettleAmounts()`, main entry point called by `SettleAccounts()`

**Bitcoin Reward Calculation Functions:**
- `CalculateFixedEpochReward(epochsSinceGenesis, initialReward, decayRate)` - applies exponential decay formula
- `CalculateParticipantBitcoinRewards(participants, epochGroupData, epochParams)` - main Bitcoin-style distribution logic
- `GetParticipantPoCWeight(participant string, epochGroupData)` - retrieves and calculates final PoC weight (Phase 1: from epoch group data, Phase 2: base weight + bonuses)

**Phase 2 Enhancement Functions (Post Simple-Schedule-V1):**
- `CalculateUtilizationBonuses(participants, epochGroupData)` - calculates per-MLNode utilization bonuses
- `CalculateModelCoverageBonuses(participants, epochGroupData)` - calculates model diversity bonuses  
- `GetMLNodeAssignments(participant, epochGroupData)` - reads model assignments from epoch groups

**In `inference-chain/x/inference/keeper/accountsettle.go` (existing file):**

**Integration Change:**
- `SettleAccounts()` calls `GetBitcoinSettleAmounts()` from `bitcoin_rewards.go` instead of the current `GetSettleAmounts()`

**Integration Flow:**
```
SettleAccounts() (in accountsettle.go) calls:
  GetBitcoinSettleAmounts() (in bitcoin_rewards.go) which internally calls:
    1. CalculateFixedEpochReward() - get epoch reward amount
    2. CalculateParticipantBitcoinRewards() - distribute based on PoC weight
    3. [Phase 2] CalculateUtilizationBonuses() - apply utilization multipliers  
    4. [Phase 2] CalculateModelCoverageBonuses() - apply coverage bonuses
    5. Return SettleResult records (same format as current system)
```

**Critical: Preserve All Existing SettleAccounts Functionality**

Only the reward calculation logic changes by replacing `GetSettleAmounts()` with `GetBitcoinSettleAmounts()`. All other settlement responsibilities remain identical:

- **Downtime Checking**: Continue calling `CheckAndSlashForDowntime()` for each participant
- **Old Reward Cleanup**: Burn unclaimed `SettleAmount` records from previous epochs using `previousEpochPocStartHeight`
- **Participant State Management**: Reset `CoinBalance = 0` and `CurrentEpochStats` after processing
- **Performance Tracking**: Create `EpochPerformanceSummary` records for epoch statistics
- **Seed Signature Handling**: Process and attach seed signatures from `MemberSeedSignatures`
- **Error Handling**: Maintain existing error handling patterns and logging
- **Transaction Safety**: Preserve existing transaction boundaries and rollback behavior

**Minimal Change Approach** - Only the reward calculation changes, ensuring `ClaimRewards` and dependent systems continue functioning without modification.

### 7.3. Integration Points

**Epoch Transition Integration**: In `inference-chain/x/inference/module/module.go`, the `onEndOfPoCValidationStage` function continues to call `SettleAccounts()` (in `accountsettle.go`), but internally `SettleAccounts()` will call `GetBitcoinSettleAmounts()` (from `bitcoin_rewards.go`) instead of the current `GetSettleAmounts()` function.

**Parameter Storage**: New parameters will be added to `inference-chain/x/inference/types/params.go`:

**Phase 1 Parameters:**
- `InitialEpochReward`: Base reward amount per epoch (default: 285,000)
- `DecayRate`: Exponential decay rate per epoch (default: -0.000475)

**Phase 2 Parameters (Post Simple-Schedule-V1):**
- `UtilizationBonusFactor`: Multiplier for utilization bonuses (default: 0.5)
- `FullCoverageBonusFactor`: Multiplier for complete model coverage (default: 1.2)
- `PartialCoverageBonusFactor`: Multiplier for partial model coverage (default: 0.1)

**Data Integration**: 
- **Phase 1**: PoC weight data from existing validation systems
- **Phase 2**: Additional data sources accessed through the centralized reward function:
  - **Model Assignment Data**: Read from per-model `EpochGroupData.ml_nodes` (same source as Section 5 utilization bonuses)
  - **Utilization Data**: Retrieved from dynamic pricing system's utilization tracking
  - **Per-MLNode PoC Weight**: Extracted from `MLNodeInfo.poc_weight` within epoch group data

### 7.4. Migration Strategy

**Transition Plan**: 
1. Deploy new `bitcoin_rewards.go` file with `GetBitcoinSettleAmounts()` function
2. Use governance flag within `SettleAccounts()` to switch between calling `GetSettleAmounts()` (current) and `GetBitcoinSettleAmounts()` (new Bitcoin-style, should be the default)
3. All other settlement logic in `accountsettle.go` remains unchanged, ensuring smooth transition

## 8. Economic Parameters and Governance

### 8.1. Configurable Parameters

All reward system parameters will be governance-adjustable grouped in `BitcoinRewardParams`:

**Initial Epoch Reward** - The fixed amount of gonka coins that will be minted and distributed to all participants each epoch. This replaces the variable reward system. Default should be set to 285,000 gonka coins per epoch, which results in approximately 600 million total coin supply.

**Decay Rate** - The exponential decay rate applied per epoch to gradually reduce rewards over time. Default value of -0.000475 per epoch creates a halving effect approximately every 1,460 epochs (4 years), resulting in exactly 600 million total supply when combined with the 285,000 initial reward.

**Genesis Epoch** - The starting epoch number for the Bitcoin-style reward calculations, allowing the system to know when the new reward mechanism began and calculate proper halving progression.

**Utilization Bonus Factor** - Controls how much extra reward MLNodes receive for serving high-demand models. A factor of 0.5 means MLNodes serving models with 100% utilization get 1.5 times normal rewards, while those serving 0% utilized models get normal rewards.

**Full Coverage Bonus Factor** - The reward multiplier given to participants who support all governance models. A factor of 1.2 means participants supporting every available model receive 20% more rewards, encouraging network diversity.

**Partial Coverage Bonus Factor** - The scaling factor for participants who support some but not all governance models. A factor of 0.1 means a participant supporting half the models would get a 5% bonus (0.5 coverage × 0.1 factor).

### 8.2. Economic Impact Analysis

**Inflation Schedule**: Predictable token issuance following exponential decay model
**Participation Incentives**: Clear economic signals for quality participation and model diversity
**Long-term Sustainability**: Gradual reduction in inflation maintains token value while incentivizing early participation

**Network Security**: Fixed rewards create stable incentives for maintaining network security through PoC participation, while bonuses ensure quality of service and comprehensive model support.

### 8.3. Core Economic Justification: Scarcity-Driven Value Creation

The primary economic justification for the Bitcoin-style reward system is based on fundamental supply-demand economics:

**Network Growth → Increased Mining Competition → Higher Coin Value**

```
Network State: 100 GPUs, 285,000 gonka coins/epoch → 2,850 gonka coins per GPU
Network Growth: 1000 GPUs, 285,000 gonka coins/epoch → 285 gonka coins per GPU (10x reduction)
Result: Mining cost per gonka coin increases 10x → intrinsic gonka coin value increases
```

**Economic Mechanics:**
1. **Fixed Supply**: Total epoch rewards remain constant regardless of network size
2. **Increased Competition**: More GPUs compete for the same reward pool
3. **Higher Mining Costs**: Each gonka coin requires more computational investment to mine
4. **Value Creation**: Higher production costs create intrinsic gonka coin value and price support

**Contrast with Previous System:**
- **Old Model**: Attempted to maintain constant rewards per GPU as network grew
  - **Inflation Problem**: Network growth → more work → more coins minted → higher inflation → potential gonka coin value decrease even during positive network growth
- **New Model**: Embraces decreasing gonka coin rewards per GPU as a value creation mechanism
  - **Scarcity Solution**: Network growth → same fixed coins → more competition → higher scarcity → potential gonka coin value increase during network growth
- **Economic Benefit**: Creates natural deflationary pressure similar to Bitcoin's model

**Gonka Price Growth Incentives:**
- New participants must invest more computational resources per gonka coin
- Existing gonka holders benefit from increased scarcity
- Network security increases as economic stake per participant grows
- Creates positive feedback loop: higher gonka value → more participants → higher mining costs → higher gonka value

This scarcity-driven model transforms network growth from a dilutive force into a value-creation mechanism, aligning the interests of early adopters with long-term network expansion.

## 9. Testing and Validation

### 9.1. Implementation Testing

**Unit Tests**: In `inference-chain/x/inference/keeper/bitcoin_rewards_test.go`:
- Reward calculation accuracy under various scenarios
- Utilization bonus calculations
- Model coverage bonus calculations
- Gradual halving formula verification

**Integration Tests**: End-to-end testing of reward distribution during epoch transitions, ensuring proper integration with existing validation and consensus systems.

### 9.2. Economic Modeling

Comprehensive economic simulations will be conducted to validate:
- Long-term token supply projections
- Participant behavior under new incentive structures
- Network stability under various utilization scenarios
- Impact of model coverage requirements on network decentralization

## 10. Future Enhancements

### 10.1. Advanced Utilization Metrics

Future iterations could incorporate more sophisticated utilization metrics:
- Quality of service measurements
- Response time performance bonuses
- Model-specific computational efficiency rewards

### 10.2. Dynamic Parameter Adjustment

Potential for algorithmic adjustment of bonus parameters based on network conditions and participation patterns, similar to Bitcoin's difficulty adjustment mechanism.

This Bitcoin-style reward system creates a more predictable, fair, and incentive-aligned economic model that encourages both network security and service quality while maintaining long-term token value through controlled inflation. 