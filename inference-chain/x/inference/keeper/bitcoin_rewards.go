package keeper

import (
	"fmt"
	"math"
	"math/big"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// BitcoinResult represents the result of Bitcoin-style reward calculation
// Similar to SubsidyResult but adapted for fixed epoch rewards
type BitcoinResult struct {
	Amount       int64  // Total epoch reward amount minted
	EpochNumber  uint64 // Current epoch number for tracking
	DecayApplied bool   // Whether decay was applied this epoch
}

// GetBitcoinSettleAmounts is the main entry point for Bitcoin-style reward calculation.
// It replaces GetSettleAmounts() while preserving WorkCoins and only changing RewardCoins calculation.
func GetBitcoinSettleAmounts(participants []types.Participant, epochGroupData *types.EpochGroupData, bitcoinParams *types.BitcoinRewardParams, settleParams *SettleParameters) ([]*SettleResult, BitcoinResult, error) {
	if participants == nil {
		return nil, BitcoinResult{Amount: 0}, fmt.Errorf("participants cannot be nil")
	}
	if epochGroupData == nil {
		return nil, BitcoinResult{Amount: 0}, fmt.Errorf("epochGroupData cannot be nil")
	}
	if bitcoinParams == nil {
		return nil, BitcoinResult{Amount: 0}, fmt.Errorf("bitcoinParams cannot be nil")
	}
	if settleParams == nil {
		return nil, BitcoinResult{Amount: 0}, fmt.Errorf("settleParams cannot be nil")
	}

	// Delegate to the main Bitcoin reward calculation function
	// This function already handles:
	// 1. WorkCoins preservation (based on actual work done)
	// 2. RewardCoins calculation (based on PoC weight and fixed epoch rewards)
	// 3. Complete distribution with remainder handling
	// 4. Invalid participant handling
	// 5. Error management
	settleResults, bitcoinResult, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams)
	if err != nil {
		return settleResults, bitcoinResult, err
	}

	// Check supply cap to prevent exceeding StandardRewardAmount (same logic as legacy system)
	if settleParams.TotalSubsidyPaid >= settleParams.TotalSubsidySupply {
		// Supply cap already reached - stop all minting
		bitcoinResult.Amount = 0
		// Zero out all participant reward amounts since no rewards can be minted
		for _, amount := range settleResults {
			if amount.Settle != nil {
				amount.Settle.RewardCoins = 0
			}
		}
	} else if settleParams.TotalSubsidyPaid+bitcoinResult.Amount > settleParams.TotalSubsidySupply {
		// Approaching supply cap - mint only remaining amount and proportionally reduce rewards
		originalAmount := bitcoinResult.Amount
		bitcoinResult.Amount = settleParams.TotalSubsidySupply - settleParams.TotalSubsidyPaid

		// Proportionally reduce all participant rewards with proper remainder handling
		if originalAmount > 0 {
			reductionRatio := float64(bitcoinResult.Amount) / float64(originalAmount)
			var totalDistributed uint64 = 0

			// Apply proportional reduction to each participant
			for _, amount := range settleResults {
				if amount.Settle != nil && amount.Error == nil {
					reducedReward := uint64(float64(amount.Settle.RewardCoins) * reductionRatio)
					amount.Settle.RewardCoins = reducedReward
					totalDistributed += reducedReward
				}
			}

			// Distribute any remainder due to integer division truncation
			// This ensures the exact available supply amount is distributed
			remainder := uint64(bitcoinResult.Amount) - totalDistributed
			if remainder > 0 && len(settleResults) > 0 {
				// Assign undistributed coins to first participant with valid rewards
				for i, result := range settleResults {
					if result.Error == nil && result.Settle != nil && result.Settle.RewardCoins > 0 {
						settleResults[i].Settle.RewardCoins += remainder
						break
					}
				}
			}
		}
	}
	// If under cap, no adjustment needed - use full amount

	return settleResults, bitcoinResult, err
}

// CalculateFixedEpochReward implements the exponential decay reward calculation
// Uses the formula: current_reward = initial_reward × exp(decay_rate × epochs_elapsed)
func CalculateFixedEpochReward(epochsSinceGenesis uint64, initialReward uint64, decayRate *types.Decimal) uint64 {
	// Parameter validation
	if initialReward == 0 {
		return 0
	}
	if decayRate == nil {
		return initialReward
	}

	// If no epochs have passed since genesis, return initial reward
	if epochsSinceGenesis == 0 {
		return initialReward
	}

	// Convert inputs to decimal for precise calculation
	initialRewardDecimal := decimal.NewFromInt(int64(initialReward))
	epochsDecimal := decimal.NewFromInt(int64(epochsSinceGenesis))

	// Calculate decay exponent: decay_rate × epochs_elapsed
	// Convert types.Decimal to shopspring decimal for mathematical operations
	decayRateDecimal := decayRate.ToDecimal()
	exponent := decayRateDecimal.Mul(epochsDecimal)

	// Calculate exponential decay: exp(decay_rate × epochs_elapsed)
	// Using math.Exp with float64 conversion for exponential calculation
	expValue := math.Exp(exponent.InexactFloat64())

	// Handle edge cases for exponential result
	if math.IsInf(expValue, 0) || math.IsNaN(expValue) {
		// If result is infinite or NaN, return 0 (complete decay)
		return 0
	}

	// Convert back to decimal and multiply with initial reward
	expDecimal := decimal.NewFromFloat(expValue)
	currentReward := initialRewardDecimal.Mul(expDecimal)

	// Ensure result is non-negative and convert to uint64
	if currentReward.IsNegative() || currentReward.LessThan(decimal.NewFromInt(1)) {
		return 0 // Minimum reward is 0
	}

	// Round down to nearest integer and return as uint64
	result := currentReward.IntPart()
	if result < 0 {
		return 0
	}

	return uint64(result)
}

// GetParticipantPoCWeight retrieves and calculates final PoC weight for reward distribution
// Phase 1: Extract base PoC weight from EpochGroup.ValidationWeights and apply bonus multipliers
// Phase 2: Bonus functions will provide actual utilization and coverage calculations
func GetParticipantPoCWeight(participant string, epochGroupData *types.EpochGroupData) uint64 {
	// Parameter validation
	if epochGroupData == nil {
		return 0
	}
	if participant == "" {
		return 0
	}

	// Step 1: Extract base PoC weight from ValidationWeights array
	var baseWeight uint64 = 0
	for _, validationWeight := range epochGroupData.ValidationWeights {
		if validationWeight.MemberAddress == participant {
			// Handle negative weights by treating them as 0
			if validationWeight.Weight < 0 {
				return 0
			}
			baseWeight = uint64(validationWeight.Weight)
			break
		}
	}

	// If participant not found in ValidationWeights, return 0
	if baseWeight == 0 {
		return 0
	}

	// Step 2: Apply utilization bonus (Phase 1: returns 1.0, Phase 2: actual utilization-based multiplier)
	utilizationBonuses := CalculateUtilizationBonuses([]types.Participant{{Address: participant}}, epochGroupData)
	utilizationMultiplier := utilizationBonuses[participant]
	if utilizationMultiplier <= 0 {
		utilizationMultiplier = 1.0 // Fallback to no change if invalid multiplier
	}

	// Step 3: Apply coverage bonus (Phase 1: returns 1.0, Phase 2: actual coverage-based multiplier)
	coverageBonuses := CalculateModelCoverageBonuses([]types.Participant{{Address: participant}}, epochGroupData)
	coverageMultiplier := coverageBonuses[participant]
	if coverageMultiplier <= 0 {
		coverageMultiplier = 1.0 // Fallback to no change if invalid multiplier
	}

	// Step 4: Calculate final weight with bonuses applied
	// Formula: finalWeight = baseWeight * utilizationBonus * coverageBonus
	finalWeight := float64(baseWeight) * utilizationMultiplier * coverageMultiplier

	// Ensure result is non-negative and convert back to uint64
	if finalWeight < 0 {
		return 0
	}

	return uint64(finalWeight)
}

// CalculateParticipantBitcoinRewards implements the main Bitcoin reward distribution logic
// Preserves WorkCoins distribution while implementing fixed RewardCoins based on PoC weight
func CalculateParticipantBitcoinRewards(participants []types.Participant, epochGroupData *types.EpochGroupData, bitcoinParams *types.BitcoinRewardParams) ([]*SettleResult, BitcoinResult, error) {
	// Parameter validation
	if participants == nil {
		return nil, BitcoinResult{}, fmt.Errorf("participants cannot be nil")
	}
	if epochGroupData == nil {
		return nil, BitcoinResult{}, fmt.Errorf("epoch group data cannot be nil")
	}
	if bitcoinParams == nil {
		return nil, BitcoinResult{}, fmt.Errorf("bitcoin parameters cannot be nil")
	}

	// Calculate current epoch number from genesis
	currentEpoch := epochGroupData.GetEpochId()
	epochsSinceGenesis := currentEpoch - bitcoinParams.GenesisEpoch

	// 1. Calculate fixed epoch reward using exponential decay
	fixedEpochReward := CalculateFixedEpochReward(epochsSinceGenesis, bitcoinParams.InitialEpochReward, bitcoinParams.DecayRate)

	// 2. Calculate total PoC weight across all participants
	var totalPoCWeight uint64 = 0
	participantWeights := make(map[string]uint64)

	for _, participant := range participants {
		// Skip invalid participants from PoC weight calculations
		if participant.Status == types.ParticipantStatus_INVALID {
			participantWeights[participant.Address] = 0
			continue
		}

		pocWeight := GetParticipantPoCWeight(participant.Address, epochGroupData)
		participantWeights[participant.Address] = pocWeight
		totalPoCWeight += pocWeight
	}

	// 3. Create settle results for each participant
	settleResults := make([]*SettleResult, 0, len(participants))
	var totalDistributed uint64 = 0

	for _, participant := range participants {
		// Create SettleAmount for this participant
		settleAmount := &types.SettleAmount{
			Participant: participant.Address,
		}

		// Handle error cases
		var settleError error
		if participant.CoinBalance < 0 {
			settleError = types.ErrNegativeCoinBalance
		}

		// Calculate WorkCoins (UNCHANGED from current system - direct user fees)
		workCoins := uint64(0)
		if participant.CoinBalance > 0 && participant.Status != types.ParticipantStatus_INVALID {
			workCoins = uint64(participant.CoinBalance)
		}
		settleAmount.WorkCoins = workCoins

		// Calculate RewardCoins (NEW Bitcoin-style distribution by PoC weight)
		rewardCoins := uint64(0)
		if participant.Status != types.ParticipantStatus_INVALID && totalPoCWeight > 0 {
			participantWeight := participantWeights[participant.Address]
			if participantWeight > 0 {
				// Use big.Int to prevent overflow with large numbers
				// Proportional distribution: (participant_weight / total_weight) × fixed_epoch_reward
				participantBig := new(big.Int).SetUint64(participantWeight)
				rewardBig := new(big.Int).SetUint64(fixedEpochReward)
				totalWeightBig := new(big.Int).SetUint64(totalPoCWeight)

				// Calculate: (participantWeight * fixedEpochReward) / totalPoCWeight
				result := new(big.Int).Mul(participantBig, rewardBig)
				result = result.Div(result, totalWeightBig)

				// Convert back to uint64 (should be safe after division)
				if result.IsUint64() {
					rewardCoins = result.Uint64()
				} else {
					// If still too large, participant gets maximum possible uint64
					rewardCoins = ^uint64(0) // Max uint64
				}
				totalDistributed += rewardCoins
			}
		}
		settleAmount.RewardCoins = rewardCoins

		// Create SettleResult
		settleResults = append(settleResults, &SettleResult{
			Settle: settleAmount,
			Error:  settleError,
		})
	}

	// 4. Distribute any remainder due to integer division truncation
	// This ensures the complete fixed epoch reward is always distributed
	remainder := fixedEpochReward - totalDistributed
	if remainder > 0 && len(settleResults) > 0 {
		// Simple approach: assign undistributed coins to first participant
		// This ensures complete distribution while keeping logic minimal
		for i, result := range settleResults {
			if result.Error == nil && result.Settle.RewardCoins > 0 {
				settleResults[i].Settle.RewardCoins += remainder
				break
			}
		}
	}

	// 5. Create BitcoinResult (similar to SubsidyResult)
	bitcoinResult := BitcoinResult{
		Amount:       int64(fixedEpochReward),
		EpochNumber:  currentEpoch,
		DecayApplied: epochsSinceGenesis > 0, // Decay applied if past genesis epoch
	}

	return settleResults, bitcoinResult, nil
}

// Phase 2 Enhancement Stubs (Future Implementation after simple-schedule-v1)

// CalculateUtilizationBonuses calculates per-MLNode utilization bonuses
// Returns 1.0 multiplier for Phase 1, will implement utilization-based bonuses in Phase 2
func CalculateUtilizationBonuses(participants []types.Participant, epochGroupData *types.EpochGroupData) map[string]float64 {
	// TODO: Phase 2 - Implement utilization bonus calculation
	// Requires simple-schedule-v1 system with per-MLNode PoC weight tracking

	// Phase 1 stub - return 1.0 (no change) for all participants
	bonuses := make(map[string]float64)
	for _, participant := range participants {
		bonuses[participant.Address] = 1.0
	}
	return bonuses
}

// CalculateModelCoverageBonuses calculates model diversity bonuses
// Returns 1.0 multiplier for Phase 1, will implement coverage-based bonuses in Phase 2
func CalculateModelCoverageBonuses(participants []types.Participant, epochGroupData *types.EpochGroupData) map[string]float64 {
	// TODO: Phase 2 - Implement model coverage bonus calculation
	// Rewards participants who support all governance models

	// Phase 1 stub - return 1.0 (no change) for all participants
	bonuses := make(map[string]float64)
	for _, participant := range participants {
		bonuses[participant.Address] = 1.0
	}
	return bonuses
}

// GetMLNodeAssignments retrieves model assignments for Phase 2 enhancements
// Returns empty list for Phase 1, will read from epoch group data in Phase 2
func GetMLNodeAssignments(participant string, epochGroupData *types.EpochGroupData) []string {
	// TODO: Phase 2 - Implement MLNode assignment retrieval
	// Read model assignments from epoch group data

	// Phase 1 stub - return empty list
	return []string{}
}
