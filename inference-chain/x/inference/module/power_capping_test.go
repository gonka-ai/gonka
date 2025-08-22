package inference_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test basic power capping functionality
func TestApplyPowerCapping_Basic(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true,                         // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30), // 30% limit
		GenesisGuardianAddresses:                []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: one participant has too much power (should be capped)
	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000}, // 10%
		{Index: "participant2", Weight: 2000}, // 20%
		{Index: "participant3", Weight: 7000}, // 70% - should be capped
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)
	require.True(t, result.WasCapped, "Should apply capping when participant exceeds 30%")

	// Verify that large participant was capped
	foundLargeCapped := false
	for _, participant := range result.CappedParticipants {
		if participant.Index == "participant3" {
			require.Less(t, participant.Weight, int64(7000), "Large participant should be capped")
			foundLargeCapped = true
		}
	}
	require.True(t, foundLargeCapped, "Should find the capped large participant")
}

// Test power capping with no threshold exceeded
func TestApplyPowerCapping_NoCappingNeeded(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: all participants well under 30% limit
	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 2500}, // 25%
		{Index: "participant2", Weight: 2500}, // 25%
		{Index: "participant3", Weight: 2500}, // 25%
		{Index: "participant4", Weight: 2500}, // 25%
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)
	require.False(t, result.WasCapped, "No participant should exceed 30% threshold")

	// Powers should remain unchanged
	for i, participant := range result.CappedParticipants {
		require.Equal(t, activeParticipants[i].Weight, participant.Weight)
	}
}

// Test single participant (no capping needed)
func TestApplyPowerCapping_SingleParticipant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000},
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)
	require.False(t, result.WasCapped, "Single participant should not be capped")
	require.Equal(t, int64(1000), result.TotalPower)
	require.Len(t, result.CappedParticipants, 1)
	require.Equal(t, int64(1000), result.CappedParticipants[0].Weight)
}

// Test empty input
func TestApplyPowerCapping_EmptyInput(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	result := inference.ApplyPowerCapping(ctx, k, []*types.ActiveParticipant{})
	require.False(t, result.WasCapped)
	require.Equal(t, int64(0), result.TotalPower)
	require.Len(t, result.CappedParticipants, 0)
}

// Test the sorting algorithm with the user's example
func TestApplyPowerCapping_SortingAlgorithm(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test the user's described example: [1000, 2000, 4000, 8000] (Total: 15,000)
	// Algorithm should detect threshold at k=2 (power 4000) and cap to 2250
	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000},
		{Index: "participant2", Weight: 2000},
		{Index: "participant3", Weight: 4000}, // Triggers threshold at k=2
		{Index: "participant4", Weight: 8000}, // Will be capped along with participant3
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)
	require.True(t, result.WasCapped, "Should apply capping when threshold detected")

	// Expected result: [1000, 2000, 2250, 2250] = 7500 total
	expectedTotal := int64(7500)
	expectedCap := int64(2250)

	require.Equal(t, expectedTotal, result.TotalPower, "Total power should be 7500 after capping")
	require.Len(t, result.CappedParticipants, 4, "Should have same number of participants")

	// Verify specific expected values
	expectedWeights := map[string]int64{
		"participant1": 1000, // Unchanged
		"participant2": 2000, // Unchanged
		"participant3": 2250, // Capped from 4000
		"participant4": 2250, // Capped from 8000
	}

	for _, participant := range result.CappedParticipants {
		expectedWeight, exists := expectedWeights[participant.Index]
		require.True(t, exists, "Participant should exist: %s", participant.Index)
		require.Equal(t, expectedWeight, participant.Weight,
			"Participant %s should have weight %d, got %d",
			participant.Index, expectedWeight, participant.Weight)
	}

	// Verify that largest participant has exactly 30% of total
	largestPercentage := float64(expectedCap) / float64(expectedTotal)
	require.InDelta(t, 0.30, largestPercentage, 0.001,
		"Largest participant should have exactly 30%% of total power")
}

// Test power capping disabled when parameter not set
func TestApplyPowerCapping_ParameterNotSet(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters WITHOUT MaxIndividualPowerPercentage
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true,
		// MaxIndividualPowerPercentage: NOT SET - should disable capping
		GenesisGuardianAddresses: []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: one participant has way too much power (but should NOT be capped)
	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000}, // 10%
		{Index: "participant2", Weight: 2000}, // 20%
		{Index: "participant3", Weight: 7000}, // 70% - normally would be capped, but parameter not set
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)
	require.False(t, result.WasCapped, "Should NOT apply capping when parameter not set")

	// Powers should remain exactly unchanged
	require.Equal(t, int64(10000), result.TotalPower)
	for i, participant := range result.CappedParticipants {
		require.Equal(t, activeParticipants[i].Weight, participant.Weight,
			"Power should remain unchanged when parameter not set")
	}
}

// Test power conservation with validation function
func TestApplyPowerCapping_PowerConservation(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                             1_000_000_000,
		OriginatorSupply:                        160_000_000,
		TopRewardAmount:                         120_000_000,
		PreProgrammedSaleAmount:                 120_000_000,
		TopRewards:                              3,
		SupplyDenom:                             "gonka",
		TopRewardPeriod:                         365 * 24 * 60 * 60,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    4 * 365 * 24 * 60 * 60,
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{},
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000},
		{Index: "participant2", Weight: 2000},
		{Index: "participant3", Weight: 7000}, // Will be capped
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)

	// Use the validation function in tests to verify algorithm correctness
	err := inference.ValidateCappingResults(activeParticipants, result.CappedParticipants, result.TotalPower)
	require.NoError(t, err, "Power capping algorithm should produce mathematically valid results")

	// Verify all participants have non-negative power
	for _, participant := range result.CappedParticipants {
		require.GreaterOrEqual(t, participant.Weight, int64(0), "No participant should have negative power")
	}
}

func TestApplyPowerCapping_ZeroPercentage_NoLimitApplied(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set genesis parameters with MaxIndividualPowerPercentage = 0 (no limit)
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.0), // Zero means no limit
		GenesisGuardianAddresses:                []string{},
		GenesisGuardianEnabled:                  true,
		// Required fields
		TotalSupply:              1_000_000_000,
		OriginatorSupply:         160_000_000,
		TopRewardAmount:          120_000_000,
		PreProgrammedSaleAmount:  120_000_000,
		TopRewards:               3,
		SupplyDenom:              "gonka",
		TopRewardPeriod:          365 * 24 * 60 * 60,
		TopRewardPayouts:         12,
		TopRewardPayoutsPerMiner: 4,
		TopRewardMaxDuration:     4 * 365 * 24 * 60 * 60,
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	activeParticipants := []*types.ActiveParticipant{
		{Index: "participant1", Weight: 1000},
		{Index: "participant2", Weight: 2000},
		{Index: "participant3", Weight: 9000}, // Would normally be capped, but zero percentage means no limit
	}

	result := inference.ApplyPowerCapping(ctx, k, activeParticipants)

	// With zero percentage, no capping should be applied
	require.False(t, result.WasCapped, "No capping should be applied when MaxIndividualPowerPercentage is 0")
	require.Equal(t, int64(12000), result.TotalPower, "Total power should remain unchanged")

	// Verify all participants maintain their original power
	require.Equal(t, int64(1000), result.CappedParticipants[0].Weight, "Participant 1 should maintain original power")
	require.Equal(t, int64(2000), result.CappedParticipants[1].Weight, "Participant 2 should maintain original power")
	require.Equal(t, int64(9000), result.CappedParticipants[2].Weight, "Participant 3 should maintain original power (no capping)")
}
