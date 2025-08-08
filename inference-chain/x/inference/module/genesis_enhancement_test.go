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
func TestApplyGenesisEnhancement_ImmatureNetwork(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for immature network
	genesisParams := types.GenesisOnlyParams{
		// Required fields
		TotalSupply:                  1_000_000_000,
		OriginatorSupply:             160_000_000,
		TopRewardAmount:              120_000_000,
		PreProgrammedSaleAmount:      120_000_000,
		TopRewards:                   3,
		SupplyDenom:                  "gonka",
		TopRewardPeriod:              365 * 24 * 60 * 60,
		TopRewardPayouts:             12,
		TopRewardPayoutsPerMiner:     4,
		TopRewardMaxDuration:         4 * 365 * 24 * 60 * 60,
		NetworkMaturityThreshold:     10_000_000, // 10M threshold
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		GenesisVetoEnabled:           true, // Feature enabled
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "genesis_validator", // Set genesis validator
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Test case: immature network with genesis validator
	computeResults := []stakingkeeper.ComputeResult{
		{OperatorAddress: "genesis_validator", Power: 1000},
		{OperatorAddress: "validator2", Power: 2000},
		{OperatorAddress: "validator3", Power: 1500},
	}
	// Total: 4500 < 10M threshold (immature network)

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
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
func TestApplyGenesisEnhancement_MatureNetwork(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters for mature network
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000, // 10M threshold
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "genesis_validator",
		GenesisVetoEnabled:           true, // Feature enabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should NOT apply enhancement for mature network")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
	require.Equal(t, int64(11_000_000), result.TotalPower)
}

// Test enhancement disabled by flag
func TestApplyGenesisEnhancement_FeatureDisabled(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with enhancement disabled
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000,
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "genesis_validator",
		GenesisVetoEnabled:           false, // Feature disabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance when feature is disabled")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test enhancement with no genesis validator configured
func TestApplyGenesisEnhancement_NoGenesisValidator(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with no genesis validator
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000,
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "",   // No genesis validator
		GenesisVetoEnabled:           true, // Feature enabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance without genesis validator")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test enhancement with genesis validator not in results
func TestApplyGenesisEnhancement_GenesisValidatorNotFound(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters with genesis validator that doesn't exist in results
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000,
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "nonexistent_validator",
		GenesisVetoEnabled:           true, // Feature enabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Should not enhance if genesis validator not found")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test single participant (should not enhance)
func TestApplyGenesisEnhancement_SingleParticipant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000,
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "genesis_validator",
		GenesisVetoEnabled:           true, // Feature enabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
	err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced, "Single participant should not be enhanced")
	require.Equal(t, computeResults, result.ComputeResults, "Results should be unchanged")
}

// Test empty input
func TestApplyGenesisEnhancement_EmptyInput(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	result := inference.ApplyGenesisEnhancement(ctx, k, []stakingkeeper.ComputeResult{})
	err := inference.ValidateEnhancementResults([]stakingkeeper.ComputeResult{}, result.ComputeResults, result.TotalPower)
	require.NoError(t, err)
	require.False(t, result.WasEnhanced)
	require.Equal(t, int64(0), result.TotalPower)
	require.Len(t, result.ComputeResults, 0)
}

// Test different multiplier values
func TestApplyGenesisEnhancement_DifferentMultipliers(t *testing.T) {
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
				NetworkMaturityThreshold:     10_000_000,
				GenesisVetoMultiplier:        types.DecimalFromFloat(tt.multiplier),
				MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
				FirstGenesisValidatorAddress: "genesis_validator",
				GenesisVetoEnabled:           true, // Feature enabled
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

			result := inference.ApplyGenesisEnhancement(ctx, k, computeResults)
			err := inference.ValidateEnhancementResults(computeResults, result.ComputeResults, result.TotalPower)
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
func TestApplyGenesisEnhancement_ValidatorIdentityPreserved(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set up parameters
	genesisParams := types.GenesisOnlyParams{
		NetworkMaturityThreshold:     10_000_000,
		GenesisVetoMultiplier:        types.DecimalFromFloat(0.52),
		MaxIndividualPowerPercentage: types.DecimalFromFloat(0.30),
		FirstGenesisValidatorAddress: "genesis_validator",
		GenesisVetoEnabled:           true, // Feature enabled
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

	result := inference.ApplyGenesisEnhancement(ctx, k, originalResults)
	err := inference.ValidateEnhancementResults(originalResults, result.ComputeResults, result.TotalPower)
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
