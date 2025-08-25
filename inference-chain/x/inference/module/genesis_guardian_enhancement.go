package inference

import (
	"context"
	"fmt"

	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/shopspring/decimal"
)

// GenesisGuardianEnhancementResult represents the result of genesis guardian enhancement
type GenesisGuardianEnhancementResult struct {
	ComputeResults []stakingkeeper.ComputeResult // validator compute results with enhanced power
	TotalPower     int64                         // total power after enhancement
	WasEnhanced    bool                          // whether enhancement was applied
}

// ShouldApplyGenesisGuardianEnhancement checks if network maturity and guardian identification conditions are met
func ShouldApplyGenesisGuardianEnhancement(ctx context.Context, k keeper.Keeper, totalNetworkPower int64, computeResults []stakingkeeper.ComputeResult) bool {
	// Enhancement only applies if feature is enabled
	if !k.GetGenesisGuardianEnabled(ctx) {
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

	// Enhancement only applies if genesis guardians are identified
	genesisGuardianAddresses := k.GetGenesisGuardianAddresses(ctx)
	if len(genesisGuardianAddresses) == 0 {
		return false
	}

	// Check if at least one genesis guardian exists in compute results
	guardianAddressMap := make(map[string]bool)
	for _, address := range genesisGuardianAddresses {
		guardianAddressMap[address] = true
	}

	for _, result := range computeResults {
		if guardianAddressMap[result.OperatorAddress] {
			return true
		}
	}

	return false
}

// ApplyGenesisGuardianEnhancement applies distributed enhancement to genesis guardians
// This system only applies to staking powers when network is immature
func ApplyGenesisGuardianEnhancement(ctx context.Context, k keeper.Keeper, computeResults []stakingkeeper.ComputeResult) *GenesisGuardianEnhancementResult {
	if len(computeResults) == 0 {
		return &GenesisGuardianEnhancementResult{
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
	if !ShouldApplyGenesisGuardianEnhancement(ctx, k, totalNetworkPower, computeResults) {
		// Return original results unchanged
		return &GenesisGuardianEnhancementResult{
			ComputeResults: computeResults,
			TotalPower:     totalNetworkPower,
			WasEnhanced:    false,
		}
	}

	// Apply enhancement
	enhancedResults, enhancedTotalPower := calculateEnhancedPower(ctx, k, computeResults, totalNetworkPower)

	// Detect if enhancement was applied by comparing total power
	wasEnhanced := enhancedTotalPower != totalNetworkPower

	return &GenesisGuardianEnhancementResult{
		ComputeResults: enhancedResults,
		TotalPower:     enhancedTotalPower,
		WasEnhanced:    wasEnhanced,
	}
}

// calculateEnhancedPower computes distributed enhanced power across multiple genesis guardians
func calculateEnhancedPower(ctx context.Context, k keeper.Keeper, computeResults []stakingkeeper.ComputeResult, totalNetworkPower int64) ([]stakingkeeper.ComputeResult, int64) {
	// Get genesis guardian addresses
	genesisGuardianAddresses := k.GetGenesisGuardianAddresses(ctx)
	if len(genesisGuardianAddresses) == 0 {
		return computeResults, totalNetworkPower
	}

	// Get genesis guardian multiplier
	genesisGuardianMultiplier := k.GetGenesisGuardianMultiplier(ctx)
	if genesisGuardianMultiplier == nil {
		return computeResults, totalNetworkPower
	}

	// Create guardian address map for quick lookup
	guardianAddressMap := make(map[string]bool)
	for _, address := range genesisGuardianAddresses {
		guardianAddressMap[address] = true
	}

	// Calculate total guardian power and identify guardian indices
	guardianIndices := []int{}
	totalGuardianPower := int64(0)
	for i, result := range computeResults {
		if guardianAddressMap[result.OperatorAddress] {
			guardianIndices = append(guardianIndices, i)
			totalGuardianPower += result.Power
		}
	}

	// If no guardians found in compute results, return unchanged
	if len(guardianIndices) == 0 {
		return computeResults, totalNetworkPower
	}

	// Calculate other participants' total power (excluding all guardians)
	otherParticipantsTotal := totalNetworkPower - totalGuardianPower

	// Calculate total enhancement amount: other_participants_total * genesis_guardian_multiplier
	multiplierDecimal := genesisGuardianMultiplier.ToDecimal()
	otherParticipantsTotalDecimal := decimal.NewFromInt(otherParticipantsTotal)
	totalEnhancementDecimal := otherParticipantsTotalDecimal.Mul(multiplierDecimal)

	// If the calculated enhancement is less than total guardian power, don't do adjustment
	totalGuardianPowerDecimal := decimal.NewFromInt(totalGuardianPower)
	if totalEnhancementDecimal.LessThan(totalGuardianPowerDecimal) {
		return computeResults, totalNetworkPower
	}

	// Calculate per-guardian enhancement: total_enhancement / number_of_guardians
	guardianCount := len(guardianIndices)
	perGuardianEnhancementDecimal := totalEnhancementDecimal.Div(decimal.NewFromInt(int64(guardianCount)))
	perGuardianEnhancement := perGuardianEnhancementDecimal.IntPart()

	// Create enhanced results
	enhancedResults := make([]stakingkeeper.ComputeResult, len(computeResults))
	enhancedTotalPower := int64(0)

	for i, result := range computeResults {
		enhancedResults[i] = result
		// Apply enhancement to genesis guardians
		if guardianAddressMap[result.OperatorAddress] {
			enhancedResults[i].Power = perGuardianEnhancement
		}
		enhancedTotalPower += enhancedResults[i].Power
	}

	return enhancedResults, enhancedTotalPower
}

// validateGuardianEnhancementResults ensures enhancement was applied correctly
func ValidateGuardianEnhancementResults(original []stakingkeeper.ComputeResult, enhanced []stakingkeeper.ComputeResult, enhancedTotalPower int64) error {
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
