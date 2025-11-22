package keeper

import (
	"fmt"
	"math"
	"math/big"

	"cosmossdk.io/log"
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
func GetBitcoinSettleAmounts(
	participants []types.Participant,
	epochGroupData *types.EpochGroupData,
	bitcoinParams *types.BitcoinRewardParams,
	settleParams *SettleParameters,
	participantMLNodes map[string][]*types.MLNodeInfo,
	logger log.Logger,
) ([]*SettleResult, BitcoinResult, error) {
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
	settleResults, bitcoinResult, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, participantMLNodes, logger)
	if err != nil {
		logger.Error("Error calculating participant bitcoin rewards", "error", err)
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

// GetPreservedWeight calculates the weight of nodes with POC_SLOT=true
// These nodes continue serving inference during confirmation PoC and are not subject to verification
func GetPreservedWeight(participant string, epochGroupData *types.EpochGroupData) int64 {
	for _, validationWeight := range epochGroupData.ValidationWeights {
		if validationWeight.MemberAddress == participant {
			var preservedWeight int64 = 0

			// Sum weights from nodes with POC_SLOT=true (index 1)
			for _, mlNode := range validationWeight.MlNodes {
				if mlNode != nil && len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] {
					preservedWeight += mlNode.PocWeight
				}
			}

			return preservedWeight
		}
	}
	return 0
}

// RecomputeEffectiveWeightFromMLNodes recalculates participant weight from uncapped MLNode weights
// This allows integration of confirmation_weight for nodes subject to verification
func RecomputeEffectiveWeightFromMLNodes(vw *types.ValidationWeight, mlNodes []*types.MLNodeInfo) int64 {
	preservedWeight := int64(0) // Sum POC_SLOT=true nodes only

	// Use provided mlNodes if available (from model subgroups), otherwise fall back to vw.MlNodes
	nodesToUse := mlNodes
	if len(nodesToUse) == 0 {
		nodesToUse = vw.MlNodes
	}

	for _, mlNode := range nodesToUse {
		if mlNode == nil || len(mlNode.TimeslotAllocation) < 2 {
			continue
		}

		if mlNode.TimeslotAllocation[1] { // POC_SLOT=true
			preservedWeight += mlNode.PocWeight
		}
	}

	// ConfirmationWeight always initialized - holds verified weight for POC_SLOT=false nodes
	return preservedWeight + vw.ConfirmationWeight
}

// GetParticipantPoCWeight retrieves and calculates final PoC weight for reward distribution
// Note: This function is used for display/query purposes and returns original base weight.
// For settlement, CalculateParticipantBitcoinRewards applies confirmation weight capping
// directly with formula: effectiveWeight = preservedWeight + confirmationWeight
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

// ApplyPowerCappingForWeights applies 30% power capping to a list of participants
// This is a shared utility that can be used both during PoC weight calculation and settlement
func ApplyPowerCappingForWeights(participants []*types.ActiveParticipant) ([]*types.ActiveParticipant, bool) {
	if len(participants) == 0 {
		return participants, false
	}

	if len(participants) == 1 {
		return participants, false
	}

	// Calculate total weight
	totalWeight := int64(0)
	for _, p := range participants {
		totalWeight += p.Weight
	}

	// Use standard 30% cap
	maxPercentageDecimal := types.DecimalFromFloat(0.30)

	// Apply dynamic limits for small networks
	participantCount := len(participants)
	if participantCount < 4 {
		adjustedLimit := getSmallNetworkLimit(participantCount)
		if adjustedLimit.ToDecimal().GreaterThan(maxPercentageDecimal.ToDecimal()) {
			maxPercentageDecimal = adjustedLimit
		}
	}

	// Call the core capping algorithm
	cappedParticipants, _, wasCapped := CalculateOptimalCap(participants, totalWeight, maxPercentageDecimal)

	return cappedParticipants, wasCapped
}

// CalculateOptimalCap implements the power capping algorithm
// Returns capped participants, new total power, and whether capping was applied
func CalculateOptimalCap(participants []*types.ActiveParticipant, totalPower int64, maxPercentage *types.Decimal) ([]*types.ActiveParticipant, int64, bool) {
	participantCount := len(participants)
	maxPercentageDecimal := maxPercentage.ToDecimal()

	// Create sorted participant power info for analysis
	type ParticipantPowerInfo struct {
		Participant *types.ActiveParticipant
		Power       int64
		Index       int
	}

	participantPowers := make([]ParticipantPowerInfo, participantCount)
	for i, participant := range participants {
		participantPowers[i] = ParticipantPowerInfo{
			Participant: participant,
			Power:       participant.Weight,
			Index:       i,
		}
	}

	// Sort by power (smallest to largest) - simple bubble sort for small arrays
	for i := 0; i < len(participantPowers)-1; i++ {
		for j := i + 1; j < len(participantPowers); j++ {
			if participantPowers[i].Power > participantPowers[j].Power {
				participantPowers[i], participantPowers[j] = participantPowers[j], participantPowers[i]
			}
		}
	}

	// Iterate through sorted powers to find threshold
	cap := int64(-1)
	sumPrev := int64(0)
	for k := 0; k < participantCount; k++ {
		currentPower := participantPowers[k].Power
		weightedTotal := sumPrev + currentPower*int64(participantCount-k)

		weightedTotalDecimal := decimal.NewFromInt(weightedTotal)
		threshold := maxPercentageDecimal.Mul(weightedTotalDecimal)
		currentPowerDecimal := decimal.NewFromInt(currentPower)

		if currentPowerDecimal.GreaterThan(threshold) {
			sumPrevDecimal := decimal.NewFromInt(sumPrev)
			numerator := maxPercentageDecimal.Mul(sumPrevDecimal)

			remainingParticipants := decimal.NewFromInt(int64(participantCount - k))
			maxPercentageTimesRemaining := maxPercentageDecimal.Mul(remainingParticipants)
			denominator := decimal.NewFromInt(1).Sub(maxPercentageTimesRemaining)

			if denominator.LessThanOrEqual(decimal.Zero) {
				cap = currentPower
				break
			}

			capDecimal := numerator.Div(denominator)
			cap = capDecimal.IntPart()
			break
		}

		sumPrev += currentPower
	}

	// If no threshold found, no capping needed
	if cap == -1 {
		return participants, totalPower, false
	}

	// Apply cap to all participants in original order
	cappedParticipants := make([]*types.ActiveParticipant, len(participants))
	finalTotalPower := int64(0)

	for i, participant := range participants {
		cappedParticipant := &types.ActiveParticipant{
			Index:        participant.Index,
			ValidatorKey: participant.ValidatorKey,
			Weight:       participant.Weight,
			InferenceUrl: participant.InferenceUrl,
			Seed:         participant.Seed,
			Models:       participant.Models,
			MlNodes:      participant.MlNodes,
		}

		if cappedParticipant.Weight > cap {
			cappedParticipant.Weight = cap
		}

		cappedParticipants[i] = cappedParticipant
		finalTotalPower += cappedParticipant.Weight
	}

	return cappedParticipants, finalTotalPower, true
}

// getSmallNetworkLimit returns higher limits for small networks
func getSmallNetworkLimit(participantCount int) *types.Decimal {
	switch participantCount {
	case 1:
		return types.DecimalFromFloat(1.0) // 100%
	case 2:
		return types.DecimalFromFloat(0.50) // 50%
	case 3:
		return types.DecimalFromFloat(0.40) // 40%
	default:
		return types.DecimalFromFloat(0.30) // 30%
	}
}

// CalculateParticipantBitcoinRewards implements the main Bitcoin reward distribution logic
// Preserves WorkCoins distribution while implementing fixed RewardCoins based on PoC weight
func CalculateParticipantBitcoinRewards(
	participants []types.Participant,
	epochGroupData *types.EpochGroupData,
	bitcoinParams *types.BitcoinRewardParams,
	participantMLNodes map[string][]*types.MLNodeInfo,
	logger log.Logger,
) ([]*SettleResult, BitcoinResult, error) {
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
	currentEpoch := epochGroupData.GetEpochIndex()
	epochsSinceGenesis := currentEpoch - bitcoinParams.GenesisEpoch

	// 1. Calculate fixed epoch reward using exponential decay
	fixedEpochReward := CalculateFixedEpochReward(epochsSinceGenesis, bitcoinParams.InitialEpochReward, bitcoinParams.DecayRate)

	// 2. Calculate effective weights with confirmation capping
	participantWeights := make(map[string]uint64)

	// Calculate effectiveWeight for each participant using helper function
	effectiveWeights := make([]*types.ActiveParticipant, 0, len(participants))
	for _, participant := range participants {
		// Skip invalid participants from PoC weight calculations
		if participant.Status != types.ParticipantStatus_ACTIVE {
			logger.Info("Invalid/inactive participant found in PoC weight calculations, skipping", "participant", participant.Address)
			participantWeights[participant.Address] = 0
			continue
		}

		// Find ValidationWeight for this participant
		var vw *types.ValidationWeight
		for _, validationWeight := range epochGroupData.ValidationWeights {
			if validationWeight.MemberAddress == participant.Address {
				vw = validationWeight
				break
			}
		}

		if vw == nil || vw.Weight <= 0 {
			logger.Info("Bitcoin Rewards: No valid weight found, skipping", "participant", participant.Address)
			participantWeights[participant.Address] = 0
			continue
		}

		// Recompute effective weight from MLNodes (includes confirmation capping)
		mlNodes := participantMLNodes[participant.Address]
		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, mlNodes)
		if effectiveWeight < 0 {
			effectiveWeight = 0
		}

		logger.Info("Bitcoin Rewards: Calculated effective weight",
			"participant", participant.Address,
			"baseWeight", vw.Weight,
			"confirmationWeight", vw.ConfirmationWeight,
			"effectiveWeight", effectiveWeight)

		effectiveWeights = append(effectiveWeights, &types.ActiveParticipant{
			Index:  participant.Address,
			Weight: effectiveWeight,
		})
	}

	// 3. Apply power capping to effective weights
	cappedParticipants, wasCapped := ApplyPowerCappingForWeights(effectiveWeights)

	// Map capped weights back to participants
	for _, cappedParticipant := range cappedParticipants {
		if cappedParticipant.Weight < 0 {
			participantWeights[cappedParticipant.Index] = 0
		} else {
			participantWeights[cappedParticipant.Index] = uint64(cappedParticipant.Weight)
		}
	}

	logger.Info("Bitcoin Rewards: Applied power capping to effective weights",
		"participantCount", len(effectiveWeights),
		"wasCapped", wasCapped)

	// Calculate total weight
	totalPoCWeight := uint64(0)
	for _, weight := range participantWeights {
		totalPoCWeight += weight
	}

	// 4. Check and punish for downtime
	logger.Info("Bitcoin Rewards: Checking downtime for participants", "participants", len(participants))
	CheckAndPunishForDowntimeForParticipants(participants, participantWeights, logger)
	logger.Info("Bitcoin Rewards: weights after downtime check", "participants", participantWeights)

	// Recalculate total weight after downtime punishment
	// This ensures fair distribution based on actual eligible weights
	totalPoCWeight = uint64(0)
	for _, weight := range participantWeights {
		totalPoCWeight += weight
	}
	logger.Info("Bitcoin Rewards: total weight after downtime punishment", "totalPoCWeight", totalPoCWeight)

	// 5. Create settle results for each participant
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
		if participant.CoinBalance > 0 && participant.Status == types.ParticipantStatus_ACTIVE {
			workCoins = uint64(participant.CoinBalance)
		}
		settleAmount.WorkCoins = workCoins

		// Calculate RewardCoins (NEW Bitcoin-style distribution by PoC weight)
		rewardCoins := uint64(0)
		if participant.Status == types.ParticipantStatus_ACTIVE && totalPoCWeight > 0 {
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

	// 6. Distribute any remainder due to integer division truncation
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

	// 7. Create BitcoinResult (similar to SubsidyResult)
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
