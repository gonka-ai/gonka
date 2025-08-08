package inference

import (
	"context"
	"fmt"
	"sort"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// ParticipantPowerInfo represents a participant with power for sorting and capping
type ParticipantPowerInfo struct {
	Participant *types.ActiveParticipant
	Power       int64
	Index       int // original index for maintaining order
}

// PowerCappingResult represents the result of power capping calculation
type PowerCappingResult struct {
	CappedParticipants []*types.ActiveParticipant // participants with capped powers
	TotalPower         int64                      // total power after capping
	WasCapped          bool                       // whether any capping was applied
}

// ApplyPowerCapping is the main entry point for universal power capping
// This applies to activeParticipants after ComputeNewWeights
func ApplyPowerCapping(ctx context.Context, k keeper.Keeper, activeParticipants []*types.ActiveParticipant) *PowerCappingResult {
	if len(activeParticipants) == 0 {
		return &PowerCappingResult{
			CappedParticipants: activeParticipants,
			TotalPower:         0,
			WasCapped:          false,
		}
	}

	// Single participant needs no capping
	if len(activeParticipants) == 1 {
		return &PowerCappingResult{
			CappedParticipants: activeParticipants,
			TotalPower:         activeParticipants[0].Weight,
			WasCapped:          false,
		}
	}

	// Get power capping parameters
	maxIndividualPowerPercentage := k.GetMaxIndividualPowerPercentage(ctx)
	if maxIndividualPowerPercentage == nil || maxIndividualPowerPercentage.ToFloat() == 0.0 {
		// If not set or set to 0, return participants unchanged (no capping)
		totalPower := int64(0)
		for _, participant := range activeParticipants {
			totalPower += participant.Weight
		}
		return &PowerCappingResult{
			CappedParticipants: activeParticipants,
			TotalPower:         totalPower,
			WasCapped:          false,
		}
	}

	// Calculate total power
	totalPower := int64(0)
	for _, participant := range activeParticipants {
		totalPower += participant.Weight
	}

	// Apply the sorting-based capping algorithm
	cappedParticipants, newTotalPower, wasCapped := calculateOptimalCap(activeParticipants, totalPower, maxIndividualPowerPercentage)

	return &PowerCappingResult{
		CappedParticipants: cappedParticipants,
		TotalPower:         newTotalPower,
		WasCapped:          wasCapped,
	}
}

// calculateOptimalCap implements the sorting and threshold detection algorithm
// Algorithm: Sort powers, iterate from smallest to largest, detect threshold, calculate cap
func calculateOptimalCap(activeParticipants []*types.ActiveParticipant, totalPower int64, maxPercentage *types.Decimal) ([]*types.ActiveParticipant, int64, bool) {
	participantCount := len(activeParticipants)
	maxPercentageFloat := maxPercentage.ToFloat()

	// Handle small networks with dynamic limits
	if participantCount < 4 {
		// For small networks, use higher limits to ensure functionality
		adjustedLimit := calculateSmallNetworkLimit(participantCount)
		if adjustedLimit > maxPercentageFloat {
			maxPercentageFloat = adjustedLimit
		}
	}

	// Create sorted participant power info for analysis
	participantPowers := make([]ParticipantPowerInfo, participantCount)
	for i, participant := range activeParticipants {
		participantPowers[i] = ParticipantPowerInfo{
			Participant: participant,
			Power:       participant.Weight,
			Index:       i,
		}
	}

	// Sort by power (smallest to largest)
	sort.Slice(participantPowers, func(i, j int) bool {
		return participantPowers[i].Power < participantPowers[j].Power
	})

	// Iterate through sorted powers to find threshold
	cap := int64(-1)
	sumPrev := int64(0) // Running sum of previous powers
	for k := 0; k < participantCount; k++ {
		// Calculate weighted total: sum_prev + current_power * (N-k)
		currentPower := participantPowers[k].Power
		weightedTotal := sumPrev + currentPower*int64(participantCount-k)

		// Check if current power exceeds threshold
		threshold := maxPercentageFloat * float64(weightedTotal)
		if float64(currentPower) > threshold {
			// Found threshold position - calculate cap
			// Formula: x = (max_percentage * sum_of_previous_steps) / (1 - max_percentage * (N-k))
			numerator := maxPercentageFloat * float64(sumPrev)
			denominator := 1.0 - maxPercentageFloat*float64(participantCount-k)
			// Note: denominator is guaranteed > 0 if threshold condition is met,
			// adding this for safety
			if denominator <= 0 {
				cap = currentPower
				break
			}
			cap = int64(numerator / denominator)
			break
		}

		// Update running sum for next iteration
		sumPrev += currentPower
	}

	// If no threshold found, no capping needed
	if cap == -1 {
		return activeParticipants, totalPower, false
	}

	// Apply cap to all participants in original order
	cappedParticipants, finalTotalPower := applyCapToDistribution(activeParticipants, cap)

	return cappedParticipants, finalTotalPower, true
}

// calculateSmallNetworkLimit returns higher limits for small networks
func calculateSmallNetworkLimit(participantCount int) float64 {
	switch participantCount {
	case 1:
		return 1.0 // 100% - single participant
	case 2:
		return 0.50 // 50% - two participants
	case 3:
		return 0.40 // 40% - three participants
	default:
		return 0.30 // 30% - four or more participants
	}
}

// applyCapToDistribution applies the calculated cap to all participants in original order
func applyCapToDistribution(activeParticipants []*types.ActiveParticipant, cap int64) ([]*types.ActiveParticipant, int64) {
	cappedParticipants := make([]*types.ActiveParticipant, len(activeParticipants))
	totalPower := int64(0)

	for i, participant := range activeParticipants {
		// Create a copy of the participant
		cappedParticipant := &types.ActiveParticipant{
			Index:        participant.Index,
			ValidatorKey: participant.ValidatorKey,
			Weight:       participant.Weight,
			InferenceUrl: participant.InferenceUrl,
			Seed:         participant.Seed,
			Models:       participant.Models,
			MlNodes:      participant.MlNodes,
		}

		// Apply cap
		if cappedParticipant.Weight > cap {
			cappedParticipant.Weight = cap
		}

		cappedParticipants[i] = cappedParticipant
		totalPower += cappedParticipant.Weight
	}

	return cappedParticipants, totalPower
}

// ValidateCappingResults ensures power conservation and mathematical correctness
// This function is kept for unit testing validation, not used in production code
func ValidateCappingResults(original []*types.ActiveParticipant, capped []*types.ActiveParticipant, finalTotalPower int64) error {
	// Check participant count consistency
	if len(original) != len(capped) {
		return fmt.Errorf("participant count mismatch: original=%d, capped=%d", len(original), len(capped))
	}

	// Verify all participants are present and have non-negative power
	for i, participant := range capped {
		if participant.Weight < 0 {
			return fmt.Errorf("negative power detected for participant %s: %d", participant.Index, participant.Weight)
		}

		// Check that power was not increased (only decreased or unchanged)
		if participant.Weight > original[i].Weight {
			return fmt.Errorf("power increased for participant %s: original=%d, capped=%d",
				participant.Index, original[i].Weight, participant.Weight)
		}

		// Verify participant identity is preserved
		if participant.Index != original[i].Index {
			return fmt.Errorf("participant order changed at index %d: original=%s, capped=%s",
				i, original[i].Index, participant.Index)
		}
	}

	// Calculate total capped power and verify it matches
	calculatedTotal := int64(0)
	for _, participant := range capped {
		calculatedTotal += participant.Weight
	}

	if calculatedTotal != finalTotalPower {
		return fmt.Errorf("total power mismatch: calculated=%d, provided=%d", calculatedTotal, finalTotalPower)
	}

	// Verify total power didn't increase (can only decrease due to capping)
	originalTotal := int64(0)
	for _, participant := range original {
		originalTotal += participant.Weight
	}

	if finalTotalPower > originalTotal {
		return fmt.Errorf("total power increased after capping: original=%d, final=%d", originalTotal, finalTotalPower)
	}

	return nil
}
