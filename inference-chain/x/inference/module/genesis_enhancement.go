package inference

import (
	"context"
	"fmt"

	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/shopspring/decimal"
)

// GenesisEnhancementResult represents the result of genesis validator enhancement
type GenesisEnhancementResult struct {
	ComputeResults []stakingkeeper.ComputeResult // validator compute results with enhanced power
	TotalPower     int64                         // total power after enhancement
	WasEnhanced    bool                          // whether enhancement was applied
}

// ShouldApplyGenesisEnhancement checks if network maturity and validator identification conditions are met
func ShouldApplyGenesisEnhancement(ctx context.Context, k keeper.Keeper, totalNetworkPower int64, computeResults []stakingkeeper.ComputeResult) bool {
	// Enhancement only applies if feature is enabled
	if !k.GetGenesisVetoEnabled(ctx) {
		return false
	}

	// Enhancement only applies if network is below maturity threshold
	if k.IsNetworkMature(ctx, totalNetworkPower) {
		return false
	}

	// Enhancement only applies if we have at least 2 participants
	if len(computeResults) < 2 {
		return false
	}

	// Enhancement only applies if first genesis validator is identified
	firstGenesisValidatorAddress := k.GetFirstGenesisValidatorAddress(ctx)
	if firstGenesisValidatorAddress == "" {
		return false
	}

	// Check if first genesis validator exists in compute results
	for _, result := range computeResults {
		if result.OperatorAddress == firstGenesisValidatorAddress {
			return true
		}
	}

	return false
}

// ApplyGenesisEnhancement applies 0.52 multiplier to first genesis validator
// This system only applies to staking powers when network is immature
func ApplyGenesisEnhancement(ctx context.Context, k keeper.Keeper, computeResults []stakingkeeper.ComputeResult) *GenesisEnhancementResult {
	if len(computeResults) == 0 {
		return &GenesisEnhancementResult{
			ComputeResults: computeResults,
			TotalPower:     0,
			WasEnhanced:    false,
		}
	}

	// Calculate total network power
	totalNetworkPower := int64(0)
	for _, result := range computeResults {
		totalNetworkPower += result.Power
	}

	// Check if enhancement should be applied
	if !ShouldApplyGenesisEnhancement(ctx, k, totalNetworkPower, computeResults) {
		// Return original results unchanged
		return &GenesisEnhancementResult{
			ComputeResults: computeResults,
			TotalPower:     totalNetworkPower,
			WasEnhanced:    false,
		}
	}

	// Apply enhancement
	enhancedResults, enhancedTotalPower := calculateEnhancedPower(ctx, k, computeResults, totalNetworkPower)

	return &GenesisEnhancementResult{
		ComputeResults: enhancedResults,
		TotalPower:     enhancedTotalPower,
		WasEnhanced:    true,
	}
}

// calculateEnhancedPower computes enhanced power based on others' total using 0.52 multiplier
func calculateEnhancedPower(ctx context.Context, k keeper.Keeper, computeResults []stakingkeeper.ComputeResult, totalNetworkPower int64) ([]stakingkeeper.ComputeResult, int64) {
	// Find first genesis validator
	firstGenesisValidatorAddress := k.GetFirstGenesisValidatorAddress(ctx)
	if firstGenesisValidatorAddress == "" {
		return computeResults, totalNetworkPower
	}

	// Get genesis enhancement parameters
	genesisVetoMultiplier := k.GetGenesisVetoMultiplier(ctx)
	if genesisVetoMultiplier == nil {
		return computeResults, totalNetworkPower
	}

	// Create enhanced results
	enhancedResults := make([]stakingkeeper.ComputeResult, len(computeResults))
	enhancedTotalPower := int64(0)

	for i, result := range computeResults {
		enhancedResults[i] = result
		if result.OperatorAddress == firstGenesisValidatorAddress {
			// Calculate other participants' total power
			otherParticipantsTotal := totalNetworkPower - result.Power

			// Calculate enhanced power for first genesis validator using decimal arithmetic
			// enhanced_power = other_participants_total * 0.52
			multiplierDecimal := genesisVetoMultiplier.ToDecimal()
			otherParticipantsTotalDecimal := decimal.NewFromInt(otherParticipantsTotal)
			enhancedGenesisPowerDecimal := otherParticipantsTotalDecimal.Mul(multiplierDecimal)
			enhancedGenesisPower := enhancedGenesisPowerDecimal.IntPart()

			// Apply enhancement to first genesis validator
			enhancedResults[i].Power = enhancedGenesisPower
		}
		enhancedTotalPower += enhancedResults[i].Power
	}

	return enhancedResults, enhancedTotalPower
}

// validateEnhancementResults ensures enhancement was applied correctly
func ValidateEnhancementResults(original []stakingkeeper.ComputeResult, enhanced []stakingkeeper.ComputeResult, enhancedTotalPower int64) error {
	// Check participant count consistency
	if len(original) != len(enhanced) {
		return fmt.Errorf("participant count mismatch: original=%d, enhanced=%d", len(original), len(enhanced))
	}

	// Verify all participants have non-negative power
	calculatedTotal := int64(0)
	for _, result := range enhanced {
		if result.Power < 0 {
			return fmt.Errorf("negative power detected for validator %s: %d", result.OperatorAddress, result.Power)
		}
		calculatedTotal += result.Power
	}

	// Verify total power matches
	if calculatedTotal != enhancedTotalPower {
		return fmt.Errorf("total power mismatch: calculated=%d, provided=%d", calculatedTotal, enhancedTotalPower)
	}

	// Verify that only power values changed, not validator identities
	originalAddresses := make(map[string]bool)
	for _, result := range original {
		originalAddresses[result.OperatorAddress] = true
	}

	enhancedAddresses := make(map[string]bool)
	for _, result := range enhanced {
		enhancedAddresses[result.OperatorAddress] = true
	}

	if len(originalAddresses) != len(enhancedAddresses) {
		return fmt.Errorf("validator set changed during enhancement")
	}

	for address := range originalAddresses {
		if !enhancedAddresses[address] {
			return fmt.Errorf("validator %s missing from enhanced results", address)
		}
	}

	return nil
}
