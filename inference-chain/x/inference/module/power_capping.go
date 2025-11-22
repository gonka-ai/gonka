package inference

import (
	"context"
	"fmt"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// PowerCappingResult represents the result of power capping calculation
type PowerCappingResult struct {
	CappedParticipants []*types.ActiveParticipant // participants with capped powers
	TotalPower         int64                      // total power after capping
	WasCapped          bool                       // whether any capping was applied
}

// ApplyPowerCapping is the main entry point for universal power capping
// This applies to activeParticipants after ComputeNewWeights
// Now delegates to the shared implementation in keeper package
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
	if maxIndividualPowerPercentage == nil || maxIndividualPowerPercentage.ToDecimal().IsZero() {
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

	// Calculate total power before capping
	totalPower := int64(0)
	for _, participant := range activeParticipants {
		totalPower += participant.Weight
	}

	// Use shared implementation from keeper package
	cappedParticipants, wasCapped := keeper.ApplyPowerCappingForWeights(activeParticipants)

	// Calculate new total power
	newTotalPower := int64(0)
	for _, participant := range cappedParticipants {
		newTotalPower += participant.Weight
	}

	return &PowerCappingResult{
		CappedParticipants: cappedParticipants,
		TotalPower:         newTotalPower,
		WasCapped:          wasCapped,
	}
}

// NOTE: Core capping algorithm moved to keeper.ApplyPowerCappingForWeights()
// for code reuse between module (PoC weight calculation) and keeper (settlement).

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
