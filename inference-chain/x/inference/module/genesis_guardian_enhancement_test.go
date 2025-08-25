package inference_test

import (
	"testing"

	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test basic genesis enhancement functionality
func TestApplyGenesisGuardianEnhancement_ImmatureNetwork(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for immature network
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
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.25),
		GenesisGuardianAddresses:                []string{"genesis_validator"}, // Set genesis guardian
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: immature network with genesis validator
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
		{OperatorAddress: "validator3", Power: 1500},
	}
	// Total: 4500 < 10M threshold (immature network)

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.True(t, result.WasEnhanced, "Should apply enhancement for immature network")

	// Calculate expected enhancement
	// Other participants total: 2000 + 1500 = 3500
	// Enhanced genesis power: 3500 * 0.52 = 1820
	// Total enhanced power: 3500 + 1820 = 5320

	require.Equal(t, int64(5320), result.TotalPower)

	// Find enhanced genesis validator
	var foundGenesis bool
	for _, cr := range result.ComputeResults {
		if cr.OperatorAddress == "genesis_validator" {
			require.Equal(t, int64(1820), cr.Power, "Genesis validator should have enhanced power")
			foundGenesis = true
		}
	}
	require.True(t, foundGenesis, "Should find enhanced genesis validator")
}

// Test that mature network skips enhancement
func TestApplyGenesisGuardianEnhancement_MatureNetwork(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for mature network
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"genesis_validator"},
		GenesisGuardianEnabled:                  true, // Feature enabled
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

	// Test case: mature network (total power > threshold)
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 5_000_000},
		{OperatorAddress: "validator2", Power: 6_000_000},
	}
	// Total: 11M > 10M threshold (mature network)

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should NOT apply enhancement for mature network")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
	require.Equal(t, int64(11_000_000), result.TotalPower)
}

// Test enhancement disabled by flag
func TestApplyGenesisGuardianEnhancement_FeatureDisabled(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with enhancement disabled
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"genesis_validator"},
		GenesisGuardianEnabled:                  false, // Feature disabled
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

	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
	}

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance when feature is disabled")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test enhancement with no genesis validator configured
func TestApplyGenesisGuardianEnhancement_NoGenesisValidator(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with no genesis validator
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{}, // No genesis guardians
		GenesisGuardianEnabled:                  true,       // Feature enabled
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

	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "validator1", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
	}

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance without genesis validator")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test enhancement with genesis validator not in results
func TestApplyGenesisGuardianEnhancement_GenesisValidatorNotFound(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with genesis validator that doesn't exist in results
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"nonexistent_validator"},
		GenesisGuardianEnabled:                  true, // Feature enabled
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

	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "validator1", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
	}

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance if genesis validator not found")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test single participant (should not enhance)
func TestApplyGenesisGuardianEnhancement_SingleParticipant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"genesis_validator"},
		GenesisGuardianEnabled:                  true, // Feature enabled
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

	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 1000},
	}

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Single participant should not be enhanced")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test empty input
func TestApplyGenesisGuardianEnhancement_EmptyInput(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, []stakingkeeper.ComputeResult{})
	err := inference.ValidateGuardianEnhancementResults([]stakingkeeper.ComputeResult{}, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced)
	require.Equal(t, int64(0), result.TotalPower)
	require.Len(t, result.ComputeResults, 0)
}

// Test different multiplier values
func TestApplyGenesisGuardianEnhancement_DifferentMultipliers(t *testing.T) {
	tests := []struct {
		name                  string
		multiplier            float64
		expectedEnhancedPower int64
		expectedTotalPower    int64
	}{
		{
			name:                  "Standard 0.52 multiplier",
			multiplier:            0.52,
			expectedEnhancedPower: 1820, // 3500 * 0.52
			expectedTotalPower:    5320, // 3500 + 1820
		},
		{
			name:                  "Higher 0.60 multiplier",
			multiplier:            0.60,
			expectedEnhancedPower: 2100, // 3500 * 0.60
			expectedTotalPower:    5600, // 3500 + 2100
		},
		{
			name:                  "Lower 0.43 multiplier",
			multiplier:            0.43,
			expectedEnhancedPower: 1505, // 3500 * 0.43
			expectedTotalPower:    5005, // 3500 + 1505
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

			// Set up parameters with different multiplier
			genesisParams := types.GenesisOnlyParams{
				GenesisGuardianNetworkMaturityThreshold: 10_000_000,
				GenesisGuardianMultiplier:               types.DecimalFromFloat(tt.multiplier),
				MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
				GenesisGuardianAddresses:                []string{"genesis_validator"},
				GenesisGuardianEnabled:                  true, // Feature enabled
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

			computeResults := []stakingkeeper.ComputeResult{
				{OperatorAddress: "genesis_validator", Power: 1000},
				{OperatorAddress: "validator2", Power: 2000},
				{OperatorAddress: "validator3", Power: 1500},
			}
			// Other participants total: 2000 + 1500 = 3500

			result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
			err := inference.ValidateGuardianEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
			require.NoError(t, err)
			require.True(t, result.WasEnhanced, "Should apply enhancement")
			require.Equal(t, tt.expectedTotalPower, result.TotalPower)

			// Find enhanced genesis validator
			var foundGenesis bool
			for _, cr := range result.ComputeResults {
				if cr.OperatorAddress == "genesis_validator" {
					require.Equal(t, tt.expectedEnhancedPower, cr.Power, "Genesis validator should have correct enhanced power")
					foundGenesis = true
				}
			}
			require.True(t, foundGenesis, "Should find enhanced genesis validator")
		})
	}
}

// Test validator identity preservation
func TestApplyGenesisGuardianEnhancement_ValidatorIdentityPreserved(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		GenesisGuardianNetworkMaturityThreshold: 10_000_000,
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"genesis_validator"},
		GenesisGuardianEnabled:                  true, // Feature enabled
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

	originalResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
		{OperatorAddress: "validator3", Power: 1500},
	}

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, originalResults)
	err := inference.ValidateGuardianEnhancementResults(originalResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.True(t, result.WasEnhanced)

	// Verify all original validators are present (order may change due to sorting)
	originalAddresses := make(map[string]bool)
	for _, original := range originalResults {
		originalAddresses[original.OperatorAddress] = true
	}

	resultAddresses := make(map[string]bool)
	for _, result := range result.ComputeResults {
		resultAddresses[result.OperatorAddress] = true
	}

	require.Equal(t, originalAddresses, resultAddresses, "All validator addresses should be preserved")
	require.Equal(t, len(originalResults), len(result.ComputeResults), "Number of validators should be preserved")
}

// Test distributed enhancement with 2 genesis guardians
func TestApplyGenesisGuardianEnhancement_TwoGuardians(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for immature network with 2 guardians
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
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"guardian1", "guardian2"}, // 2 guardians
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: immature network with 2 genesis guardians
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "guardian1", Power: 800},
		{OperatorAddress: "guardian2", Power: 1200},
		{OperatorAddress: "validator3", Power: 2000},
		{OperatorAddress: "validator4", Power: 1500},
	}
	// Total: 5500, Others (non-guardians): 3500, Guardian total: 2000
	// Enhancement calculation: 3500 * 0.52 = 1820
	// Since 1820 < 2000 (guardian total), enhancement should be skipped

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	require.False(t, result.WasEnhanced, "Should NOT apply enhancement when guardians already have enough power")

	// Find guardian powers in results - they should remain unchanged
	guardian1Power := int64(0)
	guardian2Power := int64(0)
	for _, cr := range result.ComputeResults {
		if cr.OperatorAddress == "guardian1" {
			guardian1Power = cr.Power
		} else if cr.OperatorAddress == "guardian2" {
			guardian2Power = cr.Power
		}
	}

	// Guardians should keep their original power since enhancement is skipped
	// Enhancement (1820) < Total guardian power (2000), so no enhancement applied
	require.Equal(t, int64(800), guardian1Power, "Guardian1 should keep original power")
	require.Equal(t, int64(1200), guardian2Power, "Guardian2 should keep original power")

	// Verify total power calculation - should be original total
	expectedTotal := int64(800 + 1200 + 2000 + 1500) // original powers = 5500
	require.Equal(t, expectedTotal, result.TotalPower, "Total power should remain unchanged")
}

// Test distributed enhancement with 3 genesis guardians
func TestApplyGenesisGuardianEnhancement_ThreeGuardians(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for immature network with 3 guardians
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
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"guardian1", "guardian2", "guardian3"}, // 3 guardians
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: immature network with 3 genesis guardians
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "guardian1", Power: 500},
		{OperatorAddress: "guardian2", Power: 700},
		{OperatorAddress: "guardian3", Power: 800},
		{OperatorAddress: "validator4", Power: 2000},
		{OperatorAddress: "validator5", Power: 1500},
	}
	// Total: 5500, Others (non-guardians): 3500, Guardian total: 2000
	// Enhancement calculation: 3500 * 0.52 = 1820
	// Since 1820 < 2000 (guardian total), enhancement should be skipped

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	require.False(t, result.WasEnhanced, "Should NOT apply enhancement when guardians already have enough power")

	// Find guardian powers in results - they should remain unchanged
	guardianPowers := make(map[string]int64)
	for _, cr := range result.ComputeResults {
		if cr.OperatorAddress == "guardian1" || cr.OperatorAddress == "guardian2" || cr.OperatorAddress == "guardian3" {
			guardianPowers[cr.OperatorAddress] = cr.Power
		}
	}

	// Guardians should keep their original power since enhancement is skipped
	// Enhancement (1820) < Total guardian power (2000), so no enhancement applied
	require.Equal(t, int64(500), guardianPowers["guardian1"], "Guardian1 should keep original power")
	require.Equal(t, int64(700), guardianPowers["guardian2"], "Guardian2 should keep original power")
	require.Equal(t, int64(800), guardianPowers["guardian3"], "Guardian3 should keep original power")

	// Verify total power calculation - should be original total
	expectedTotal := int64(500 + 700 + 800 + 2000 + 1500) // original powers = 5500
	require.Equal(t, expectedTotal, result.TotalPower, "Total power should remain unchanged")
}

// Test partial guardian identification (some guardians not found in compute results)
func TestApplyGenesisGuardianEnhancement_PartialGuardians(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with 3 guardians configured
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
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"guardian1", "guardian2", "guardian3"}, // 3 guardians configured
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: only 2 out of 3 configured guardians are present in compute results
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "guardian1", Power: 500},
		{OperatorAddress: "guardian2", Power: 700},
		// guardian3 is missing
		{OperatorAddress: "validator4", Power: 2000},
		{OperatorAddress: "validator5", Power: 1500},
	}
	// Total: 4700, Guardian total: 1200, Others: 3500
	// Expected enhancement per guardian (2 found): (3500 * 0.52) / 2 = 910

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	require.True(t, result.WasEnhanced, "Should apply enhancement even with partial guardians")

	// Find guardian powers in results
	guardian1Power := int64(0)
	guardian2Power := int64(0)
	for _, cr := range result.ComputeResults {
		if cr.OperatorAddress == "guardian1" {
			guardian1Power = cr.Power
		} else if cr.OperatorAddress == "guardian2" {
			guardian2Power = cr.Power
		}
	}

	// Both present guardians should have equal enhanced power: (3500 * 0.52) / 2 = 910
	expectedPower := int64(910)
	require.Equal(t, expectedPower, guardian1Power, "Guardian1 should have distributed enhanced power")
	require.Equal(t, expectedPower, guardian2Power, "Guardian2 should have distributed enhanced power")

	// Verify total power calculation
	expectedTotal := int64(3500 + 910*2) // others + 2*guardian_power = 5320
	require.Equal(t, expectedTotal, result.TotalPower, "Total power should be correctly calculated")
}

// Test single guardian fallback (same as original behavior)
func TestApplyGenesisGuardianEnhancement_SingleGuardianFallback(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for immature network with 1 guardian
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
		GenesisGuardianNetworkMaturityThreshold: 10_000_000, // 10M threshold
		GenesisGuardianMultiplier:               types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:                  true, // Feature enabled
		MaxIndividualPowerPercentage:            types.DecimalFromFloat(0.30),
		GenesisGuardianAddresses:                []string{"guardian1"}, // 1 guardian
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: immature network with 1 genesis guardian (fallback behavior)
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "guardian1", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
		{OperatorAddress: "validator3", Power: 1500},
	}
	// Total: 4500, Others: 3500
	// Expected enhancement: 3500 * 0.52 = 1820 (same as original single validator behavior)

	result := inference.ApplyGenesisGuardianEnhancement(ctx, k, computeResults)
	require.True(t, result.WasEnhanced, "Should apply enhancement for immature network")

	// Find guardian power in results
	guardian1Power := int64(0)
	for _, cr := range result.ComputeResults {
		if cr.OperatorAddress == "guardian1" {
			guardian1Power = cr.Power
		}
	}

	// Single guardian should get full enhancement: 3500 * 0.52 = 1820
	expectedPower := int64(1820)
	require.Equal(t, expectedPower, guardian1Power, "Single guardian should have full enhanced power")

	// Verify total power calculation
	expectedTotal := int64(3500 + 1820) // others + guardian = 5320
	require.Equal(t, expectedTotal, result.TotalPower, "Total power should be correctly calculated")
}
