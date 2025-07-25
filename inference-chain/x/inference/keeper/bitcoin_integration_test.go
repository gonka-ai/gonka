package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Setup function for Bitcoin reward integration tests
func setupKeeperWithMocksForBitcoinIntegration(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	return k, keeper.NewMsgServerImpl(k), ctx, &mocks
}

// TestBitcoinRewardIntegration_GovernanceFlagSwitching tests switching between reward systems
func TestBitcoinRewardIntegration_GovernanceFlagSwitching(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForBitcoinIntegration(t)

	t.Run("Test Bitcoin rewards enabled via governance", func(t *testing.T) {
		// Enable Bitcoin rewards via governance parameters
		params := types.DefaultParams()
		params.BitcoinRewardParams.UseBitcoinRewards = true
		params.BitcoinRewardParams.InitialEpochReward = 50000
		params.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(0) // No decay for predictability
		params.BitcoinRewardParams.GenesisEpoch = 0
		require.NoError(t, k.SetParams(ctx, params))

		// Verify parameters were set correctly
		retrievedParams := k.GetParams(ctx)
		require.True(t, retrievedParams.BitcoinRewardParams.UseBitcoinRewards, "Bitcoin rewards should be enabled")
		require.Equal(t, uint64(50000), retrievedParams.BitcoinRewardParams.InitialEpochReward, "Initial epoch reward should be set")
	})

	t.Run("Test Bitcoin rewards disabled (legacy system)", func(t *testing.T) {
		// Disable Bitcoin rewards (use legacy system)
		params := k.GetParams(ctx)
		params.BitcoinRewardParams.UseBitcoinRewards = false
		require.NoError(t, k.SetParams(ctx, params))

		// Verify parameters were set correctly
		retrievedParams := k.GetParams(ctx)
		require.False(t, retrievedParams.BitcoinRewardParams.UseBitcoinRewards, "Bitcoin rewards should be disabled")
	})
}

// TestBitcoinRewardIntegration_ParameterValidation tests Bitcoin reward parameter validation
func TestBitcoinRewardIntegration_ParameterValidation(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForBitcoinIntegration(t)

	// Test that module accepts valid Bitcoin reward parameters
	params := types.DefaultParams()
	params.BitcoinRewardParams.UseBitcoinRewards = true
	params.BitcoinRewardParams.InitialEpochReward = 285000
	params.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(-0.000475)
	params.BitcoinRewardParams.GenesisEpoch = 0
	params.BitcoinRewardParams.UtilizationBonusFactor = types.DecimalFromFloat(0.5)
	params.BitcoinRewardParams.FullCoverageBonusFactor = types.DecimalFromFloat(1.2)
	params.BitcoinRewardParams.PartialCoverageBonusFactor = types.DecimalFromFloat(0.1)

	// Should not error on valid parameters
	err := k.SetParams(ctx, params)
	require.NoError(t, err)

	// Retrieve and verify parameters
	retrievedParams := k.GetParams(ctx)
	require.True(t, retrievedParams.BitcoinRewardParams.UseBitcoinRewards)
	require.Equal(t, uint64(285000), retrievedParams.BitcoinRewardParams.InitialEpochReward)
	decayRateLegacy, err := retrievedParams.BitcoinRewardParams.DecayRate.ToLegacyDec()
	require.NoError(t, err)
	require.InDelta(t, -0.000475, decayRateLegacy.MustFloat64(), 0.000001, "Decay rate should match")
}

// TestBitcoinRewardIntegration_RewardCalculationFunctions tests the Bitcoin reward calculation functions
func TestBitcoinRewardIntegration_RewardCalculationFunctions(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForBitcoinIntegration(t)

	// Setup Bitcoin reward parameters
	params := types.DefaultParams()
	params.BitcoinRewardParams.UseBitcoinRewards = true
	params.BitcoinRewardParams.InitialEpochReward = 100000
	params.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(-0.001) // 0.1% decay per epoch
	params.BitcoinRewardParams.GenesisEpoch = 0
	require.NoError(t, k.SetParams(ctx, params))

	t.Run("Test epoch reward calculation", func(t *testing.T) {
		// Test the CalculateFixedEpochReward function directly
		epochReward := keeper.CalculateFixedEpochReward(0, 100000, params.BitcoinRewardParams.DecayRate)
		require.Equal(t, uint64(100000), epochReward, "Epoch 0 should return initial reward")

		// Test decay after some epochs
		epochReward10 := keeper.CalculateFixedEpochReward(10, 100000, params.BitcoinRewardParams.DecayRate)
		require.Less(t, epochReward10, uint64(100000), "Epoch 10 should have lower reward due to decay")

		epochReward100 := keeper.CalculateFixedEpochReward(100, 100000, params.BitcoinRewardParams.DecayRate)
		require.Less(t, epochReward100, epochReward10, "Epoch 100 should have lower reward than epoch 10")
	})

	t.Run("Test PoC weight retrieval", func(t *testing.T) {
		// Create test epoch group data
		epochGroupData := &types.EpochGroupData{
			EpochId: 10,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "participant1",
					Weight:        1000,
					Reputation:    100,
				},
				{
					MemberAddress: "participant2",
					Weight:        2000,
					Reputation:    150,
				},
			},
			TotalWeight: 3000,
		}

		// Test GetParticipantPoCWeight function
		weight1 := keeper.GetParticipantPoCWeight("participant1", epochGroupData)
		require.Equal(t, uint64(1000), weight1, "Participant1 should have correct PoC weight")

		weight2 := keeper.GetParticipantPoCWeight("participant2", epochGroupData)
		require.Equal(t, uint64(2000), weight2, "Participant2 should have correct PoC weight")

		weightNonExistent := keeper.GetParticipantPoCWeight("nonexistent", epochGroupData)
		require.Equal(t, uint64(0), weightNonExistent, "Non-existent participant should have zero weight")
	})
}

// TestBitcoinRewardIntegration_DistributionLogic tests the Bitcoin reward distribution logic
func TestBitcoinRewardIntegration_DistributionLogic(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForBitcoinIntegration(t)

	// Setup test data
	epochGroupData := &types.EpochGroupData{
		EpochId: 25,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: "participant1",
				Weight:        1000,
				Reputation:    100,
			},
			{
				MemberAddress: "participant2",
				Weight:        3000,
				Reputation:    150,
			},
		},
		TotalWeight: 4000,
	}

	participants := []types.Participant{
		{
			Address:     "participant1",
			CoinBalance: 2000, // WorkCoins from user fees
			Status:      types.ParticipantStatus_ACTIVE,
		},
		{
			Address:     "participant2",
			CoinBalance: 6000, // WorkCoins from user fees
			Status:      types.ParticipantStatus_ACTIVE,
		},
	}

	// Setup Bitcoin parameters
	params := types.DefaultParams()
	params.BitcoinRewardParams.UseBitcoinRewards = true
	params.BitcoinRewardParams.InitialEpochReward = 80000
	params.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(0) // No decay for predictable testing
	params.BitcoinRewardParams.GenesisEpoch = 0
	require.NoError(t, k.SetParams(ctx, params))

	t.Run("Test Bitcoin reward distribution calculation", func(t *testing.T) {
		// Test GetBitcoinSettleAmounts function
		settleResults, bitcoinResult, err := keeper.GetBitcoinSettleAmounts(participants, epochGroupData, params.BitcoinRewardParams)
		require.NoError(t, err, "Bitcoin settle amounts calculation should succeed")
		require.Len(t, settleResults, 2, "Should have settle results for both participants")

		// Verify BitcoinResult
		require.Equal(t, int64(80000), bitcoinResult.Amount, "Bitcoin result should have correct epoch reward amount")
		require.Equal(t, uint64(25), bitcoinResult.EpochNumber, "Bitcoin result should have correct epoch number")

		// Create map for easier testing
		settleMap := make(map[string]*keeper.SettleResult)
		for _, result := range settleResults {
			settleMap[result.Settle.Participant] = result
		}

		// Verify participant1 rewards
		p1Result := settleMap["participant1"]
		require.NotNil(t, p1Result, "Participant1 should have settle result")
		require.Equal(t, uint64(2000), p1Result.Settle.WorkCoins, "Participant1 WorkCoins should be preserved")
		// Participant1 has 1000/4000 = 25% of total weight, so should get 25% of 80000 = 20000 RewardCoins
		require.Equal(t, uint64(20000), p1Result.Settle.RewardCoins, "Participant1 should get 25% of epoch reward")

		// Verify participant2 rewards
		p2Result := settleMap["participant2"]
		require.NotNil(t, p2Result, "Participant2 should have settle result")
		require.Equal(t, uint64(6000), p2Result.Settle.WorkCoins, "Participant2 WorkCoins should be preserved")
		// Participant2 has 3000/4000 = 75% of total weight, so should get 75% of 80000 = 60000 RewardCoins
		require.Equal(t, uint64(60000), p2Result.Settle.RewardCoins, "Participant2 should get 75% of epoch reward")

		// Verify total distribution equals epoch reward exactly
		totalDistributed := p1Result.Settle.RewardCoins + p2Result.Settle.RewardCoins
		require.Equal(t, uint64(80000), totalDistributed, "Total distributed rewards must equal fixed epoch reward")
	})
}

// TestBitcoinRewardIntegration_DefaultParameters tests the default Bitcoin reward parameters
func TestBitcoinRewardIntegration_DefaultParameters(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocksForBitcoinIntegration(t)

	// Get default parameters
	defaultParams := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, defaultParams))

	// Verify default Bitcoin reward parameters
	retrievedParams := k.GetParams(ctx)
	bitcoinParams := retrievedParams.BitcoinRewardParams

	// Test the default values match our specifications
	require.False(t, bitcoinParams.UseBitcoinRewards, "Bitcoin rewards should be disabled by default for safe deployment")
	require.Equal(t, uint64(285000), bitcoinParams.InitialEpochReward, "Default initial epoch reward should be 285,000")

	decayRateLegacy, err := bitcoinParams.DecayRate.ToLegacyDec()
	require.NoError(t, err)
	require.InDelta(t, -0.000475, decayRateLegacy.MustFloat64(), 0.000001, "Default decay rate should be -0.000475")

	require.Equal(t, uint64(0), bitcoinParams.GenesisEpoch, "Default genesis epoch should be 0")

	// Test Phase 2 bonus parameters
	utilBonusFactor, err := bitcoinParams.UtilizationBonusFactor.ToLegacyDec()
	require.NoError(t, err)
	require.InDelta(t, 0.5, utilBonusFactor.MustFloat64(), 0.000001, "Default utilization bonus factor should be 0.5")

	fullCoverageFactor, err := bitcoinParams.FullCoverageBonusFactor.ToLegacyDec()
	require.NoError(t, err)
	require.InDelta(t, 1.2, fullCoverageFactor.MustFloat64(), 0.000001, "Default full coverage bonus factor should be 1.2")

	partialCoverageFactor, err := bitcoinParams.PartialCoverageBonusFactor.ToLegacyDec()
	require.NoError(t, err)
	require.InDelta(t, 0.1, partialCoverageFactor.MustFloat64(), 0.000001, "Default partial coverage bonus factor should be 0.1")
}

// TestBitcoinRewardIntegration_Phase2Stubs tests the Phase 2 enhancement stub functions
func TestBitcoinRewardIntegration_Phase2Stubs(t *testing.T) {
	// Setup test data
	epochGroupData := &types.EpochGroupData{
		EpochId: 15,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: "participant1",
				Weight:        1000,
				Reputation:    100,
			},
		},
		TotalWeight: 1000,
	}

	participants := []types.Participant{
		{
			Address: "participant1",
			Status:  types.ParticipantStatus_ACTIVE,
		},
	}

	t.Run("Test utilization bonus stubs return 1.0", func(t *testing.T) {
		// Test CalculateUtilizationBonuses function (Phase 2 stub)
		bonuses := keeper.CalculateUtilizationBonuses(participants, epochGroupData)
		require.NotNil(t, bonuses, "Utilization bonuses should not be nil")
		require.Len(t, bonuses, 1, "Should have bonus entry for participant")
		require.Equal(t, 1.0, bonuses["participant1"], "Phase 1 stub should return 1.0 multiplier")
	})

	t.Run("Test coverage bonus stubs return 1.0", func(t *testing.T) {
		// Test CalculateModelCoverageBonuses function (Phase 2 stub)
		bonuses := keeper.CalculateModelCoverageBonuses(participants, epochGroupData)
		require.NotNil(t, bonuses, "Coverage bonuses should not be nil")
		require.Len(t, bonuses, 1, "Should have bonus entry for participant")
		require.Equal(t, 1.0, bonuses["participant1"], "Phase 1 stub should return 1.0 multiplier")
	})

	t.Run("Test MLNode assignment stubs return empty", func(t *testing.T) {
		// Test GetMLNodeAssignments function (Phase 2 stub)
		assignments := keeper.GetMLNodeAssignments("participant1", epochGroupData)
		require.NotNil(t, assignments, "MLNode assignments should not be nil")
		require.Len(t, assignments, 0, "Phase 1 stub should return empty assignments")
	})

	t.Run("Test bonus integration in PoC weight calculation", func(t *testing.T) {
		// Test that GetParticipantPoCWeight applies bonuses correctly (should be 1.0 in Phase 1)
		weight := keeper.GetParticipantPoCWeight("participant1", epochGroupData)
		require.Equal(t, uint64(1000), weight, "PoC weight should be base weight (1000) * 1.0 * 1.0 = 1000")
	})
}
