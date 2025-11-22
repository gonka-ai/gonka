package keeper_test

import (
	"testing"

	"cosmossdk.io/log"
	"github.com/productscience/inference/testutil"
	"go.uber.org/mock/gomock"

	keeper2 "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

var tokenomicsParams = types.DefaultParams().TokenomicsParams
var defaultSettleParameters = inference.SettleParameters{
	CurrentSubsidyPercentage: 0.90,
	TotalSubsidyPaid:         0,
	StageCutoff:              0.05,
	StageDecrease:            0.20,
	TotalSubsidySupply:       600000000000,
}

// createTestLogger creates a logger for testing
func createTestLogger(t *testing.T) log.Logger {
	return log.NewTestLogger(t)
}

func calcExpectedRewards(participants []types.Participant) int64 {
	totalWorkCoins := int64(0)
	for _, p := range participants {
		totalWorkCoins += p.CoinBalance
	}
	w := decimal.NewFromInt(totalWorkCoins)
	r := decimal.NewFromInt(1).Sub(decimal.NewFromFloat32(defaultSettleParameters.CurrentSubsidyPercentage))
	rewardAmount := w.Div(r).IntPart()
	if rewardAmount < 0 {
		panic("Negative reward amount")
	}
	return rewardAmount
}

func TestReduceSubsidy(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestReduceSubsidy")

	oParams := types.TokenomicsParams{
		SubsidyReductionAmount:   types.DecimalFromFloat(0.20),
		SubsidyReductionInterval: types.DecimalFromFloat(0.05),
		CurrentSubsidyPercentage: types.DecimalFromFloat(0.90),
	}
	logger.Info("Initial tokenomics params", "subsidyPercentage", oParams.CurrentSubsidyPercentage.ToFloat32())

	params := oParams.ReduceSubsidyPercentage()
	logger.Info("After first reduction", "subsidyPercentage", params.CurrentSubsidyPercentage.ToFloat32())
	require.Equal(t, float32(0.72), params.CurrentSubsidyPercentage.ToFloat32())
	params2 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.576), params2.CurrentSubsidyPercentage.ToFloat32())
	params3 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.4608), params3.CurrentSubsidyPercentage.ToFloat32())
	params4 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.3686), params4.CurrentSubsidyPercentage.ToFloat32())
	params5 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.2949), params5.CurrentSubsidyPercentage.ToFloat32())
	params6 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.2359), params6.CurrentSubsidyPercentage.ToFloat32())
	params7 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.1887), params7.CurrentSubsidyPercentage.ToFloat32())
	params8 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.1510), params8.CurrentSubsidyPercentage.ToFloat32())
	params9 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.1208), params9.CurrentSubsidyPercentage.ToFloat32())
	params10 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.0966), params10.CurrentSubsidyPercentage.ToFloat32())
	params11 := oParams.ReduceSubsidyPercentage()
	require.Equal(t, float32(0.0773), params11.CurrentSubsidyPercentage.ToFloat32())
}

func TestRewardsNoCrossover(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestRewardsNoCrossover", "workCoins", 1000)

	subsidy := defaultSettleParameters.GetTotalSubsidy(1000)
	logger.Info("Calculated subsidy", "amount", subsidy.Amount, "crossedCutoff", subsidy.CrossedCutoff)

	require.Equal(t, int64(10000), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestRewardsNoCrossover2(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestRewardsNoCrossover2")

	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         0,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000000,
	}
	logger.Info("Test parameters", "totalSubsidySupply", params.TotalSubsidySupply, "workCoins", 340000)

	subsidy := params.GetTotalSubsidy(340000)
	logger.Info("Calculated subsidy", "amount", subsidy.Amount, "crossedCutoff", subsidy.CrossedCutoff)

	require.Equal(t, int64(3400000), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestRewardsCrossover(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestRewardsCrossover - testing subsidy cutoff crossing")

	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         9500,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	logger.Info("Test parameters", "totalSubsidyPaid", params.TotalSubsidyPaid, "totalSubsidySupply", params.TotalSubsidySupply)

	subsidy := params.GetTotalSubsidy(1000)
	logger.Info("Calculated subsidy with crossover", "amount", subsidy.Amount, "crossedCutoff", subsidy.CrossedCutoff)

	// A note: 3892 is if we truncate, 3893 is if we round
	require.Equal(t, int64(3892), subsidy.Amount)
	require.True(t, subsidy.CrossedCutoff)
}

func TestRewardsSecondCrossover(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.72,
		TotalSubsidyPaid:         19500,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	subsidy := params.GetTotalSubsidy(1000)
	require.Equal(t, int64(2528), subsidy.Amount)
	require.True(t, subsidy.CrossedCutoff)
}

func TestNoRewardsPastSupplyCrossover(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         199500,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	subsidy := params.GetTotalSubsidy(1000)
	require.Equal(t, int64(500), subsidy.Amount)
	require.True(t, subsidy.CrossedCutoff)
}

func TestNoRewardsPastSupplyEntirely(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         200000,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	subsidy := params.GetTotalSubsidy(1000)
	require.Equal(t, int64(0), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestNoCrossoverAtZero(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         0,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	subsidy := params.GetTotalSubsidy(1000)
	require.Equal(t, int64(10000), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestSingleSettle(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestSingleSettle")

	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	logger.Info("Created participant", "address", participant1.Address, "coinBalance", participant1.CoinBalance, "status", participant1.Status)

	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1})
	logger.Info("Calculated expected reward", "amount", expectedRewardCoin)

	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters, logger)
	require.NoError(t, err, "GetSettleAmounts should not return error")
	require.Equal(t, 1, len(result), "Should have exactly 1 settle result")
	require.Equal(t, expectedRewardCoin, newCoin.Amount, "New coin amount should match expected reward")

	p1Result := result[0]
	require.NotNil(t, p1Result.Settle, "Settle result should not be nil")
	require.Nil(t, p1Result.Error, "Should not have settle error")
	logger.Info("Settle result", "workCoins", p1Result.Settle.WorkCoins, "rewardCoins", p1Result.Settle.RewardCoins)

	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin), p1Result.Settle.RewardCoins)
}

func TestEvenSettle(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestEvenSettle - testing equal distribution between two participants")

	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	participant2 := types.Participant{
		Address:     "participant2",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	logger.Info("Created participants", "count", 2, "coinBalance1", participant1.CoinBalance, "coinBalance2", participant2.CoinBalance)

	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1, participant2})
	logger.Info("Calculated total expected reward", "amount", expectedRewardCoin, "perParticipant", expectedRewardCoin/2)

	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1, participant2}, &defaultSettleParameters, logger)
	require.NoError(t, err, "GetSettleAmounts should not return error")
	require.Equal(t, 2, len(result), "Should have exactly 2 settle results")
	require.Equal(t, expectedRewardCoin, newCoin.Amount, "Total reward should match expected")

	p1Result := result[0]
	require.NotNil(t, p1Result.Settle, "Participant 1 settle result should not be nil")
	require.Nil(t, p1Result.Error, "Participant 1 should not have settle error")
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), p1Result.Settle.RewardCoins)

	p2Result := result[1]
	require.NotNil(t, p2Result.Settle, "Participant 2 settle result should not be nil")
	require.Nil(t, p2Result.Error, "Participant 2 should not have settle error")
	require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), p2Result.Settle.RewardCoins)

	logger.Info("Settlement verification complete", "p1Reward", p1Result.Settle.RewardCoins, "p2Reward", p2Result.Settle.RewardCoins)
}

func TestEvenAmong3(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestEvenAmong3 - testing distribution among 3 participants with different balances")

	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 255000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	participant2 := types.Participant{
		Address:     "participant2",
		CoinBalance: 340000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	participant3 := types.Participant{
		Address:     "participant3",
		CoinBalance: 255000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	logger.Info("Created 3 participants", "p1Balance", participant1.CoinBalance, "p1Status", participant1.Status, "p2Balance", participant2.CoinBalance, "p2Status", participant2.Status, "p3Balance", participant3.CoinBalance, "p3Status", participant3.Status)

	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1, participant2, participant3}, &defaultSettleParameters, logger)
	require.NoError(t, err, "GetSettleAmounts should not return error")
	require.Equal(t, 3, len(result), "Should have exactly 3 settle results")
	require.Equal(t, int64(8500000), newCoin.Amount, "Total reward should be 8500000")

	p1Result := result[0]
	require.NotNil(t, p1Result.Settle, "Participant 1 settle should not be nil")
	require.Nil(t, p1Result.Error, "Participant 1 should not have error")
	require.Equal(t, uint64(255000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(2550000), p1Result.Settle.RewardCoins)

	p2Result := result[1]
	require.NotNil(t, p2Result.Settle, "Participant 2 settle should not be nil")
	require.Nil(t, p2Result.Error, "Participant 2 should not have error")
	require.Equal(t, uint64(340000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(3400000), p2Result.Settle.RewardCoins)

	p3Result := result[2]
	require.NotNil(t, p3Result.Settle, "Participant 3 settle should not be nil")
	require.Nil(t, p3Result.Error, "Participant 3 should not have error")
	require.Equal(t, uint64(255000), p3Result.Settle.WorkCoins)
	require.Equal(t, uint64(2550000), p3Result.Settle.RewardCoins)

	logger.Info("3-participant settlement verified", "totalReward", newCoin.Amount, "p1Reward", p1Result.Settle.RewardCoins, "p2Reward", p2Result.Settle.RewardCoins, "p3Reward", p3Result.Settle.RewardCoins)
}

func TestNoWorkBalance(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestNoWorkBalance - testing zero work scenario")

	participant1 := newParticipant(0, 0, "1")
	logger.Info("Created participant with zero balance", "address", participant1.Address, "coinBalance", participant1.CoinBalance)

	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters, logger)
	require.NoError(t, err, "GetSettleAmounts should not return error even with zero work")
	require.Equal(t, 1, len(result), "Should have exactly 1 settle result")

	// If no one works, no coin
	require.Equal(t, int64(0), newCoin.Amount, "No work should result in zero new coins")

	p1Result := result[0]
	require.NotNil(t, p1Result.Settle, "Settle result should not be nil")
	require.Nil(t, p1Result.Error, "Should not have error for zero work")
	require.Zero(t, p1Result.Settle.WorkCoins, "Work coins should be zero")
	require.Zero(t, p1Result.Settle.RewardCoins, "Reward coins should be zero")

	logger.Info("Zero work test completed successfully")
}

func TestNegativeCoinBalance(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestNegativeCoinBalance - testing error handling for negative balance")

	participant1 := newParticipant(-1, 0, "1")
	logger.Info("Created participant with negative balance", "address", participant1.Address, "coinBalance", participant1.CoinBalance)

	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters, logger)
	require.NoError(t, err, "GetSettleAmounts should not return top-level error for negative balance")
	require.Equal(t, 1, len(result), "Should have exactly 1 settle result")
	require.Equal(t, int64(0), newCoin.Amount, "Negative balance should result in zero new coins")

	p1Result := result[0]
	require.NotNil(t, p1Result.Settle, "Settle result should not be nil")
	require.Equal(t, types.ErrNegativeCoinBalance, p1Result.Error, "Should have negative coin balance error")

	logger.Info("Negative balance error handling verified", "error", p1Result.Error)
}

func newParticipant(coinBalance int64, refundBalance int64, id string) types.Participant {
	return types.Participant{
		Address:     "participant" + id,
		CoinBalance: coinBalance,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
}

func TestActualSettle(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestActualSettle - testing full settlement integration")

	participant1 := types.Participant{
		Index:       testutil.Executor,
		Address:     testutil.Executor,
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	participant2 := types.Participant{
		Index:       testutil.Executor2,
		Address:     testutil.Executor2,
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
			MissedRequests: 0,
		},
	}
	logger.Info("Created test participants", "p1Address", participant1.Address, "p2Address", participant2.Address, "coinBalance", participant1.CoinBalance)

	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)

	// Configure to use legacy reward system for this test
	params := keeper.GetParams(ctx)
	params.BitcoinRewardParams.UseBitcoinRewards = false
	keeper.SetParams(ctx, params)
	logger.Info("Configured to use legacy reward system")

	keeper.SetParticipant(ctx, participant1)
	keeper.SetParticipant(ctx, participant2)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 10,
	})
	// Set active participants for the epoch
	keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 10,
		Participants: []*types.ActiveParticipant{
			{Index: participant1.Address},
			{Index: participant2.Address},
		},
	})
	logger.Info("Set participants and epoch data", "epochIndex", 10)

	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1, participant2})
	logger.Info("Calculated expected reward", "totalReward", expectedRewardCoin, "perParticipant", expectedRewardCoin/2)

	coins, err2 := types.GetCoins(expectedRewardCoin)
	require.NoError(t, err2, "Should be able to create coins from reward amount")
	logger.Info("Created coins for minting", "coins", coins)

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	err := keeper.SettleAccounts(ctx, 10, 0)
	require.NoError(t, err, "SettleAccounts should complete successfully")
	logger.Info("SettleAccounts completed successfully")
	updated1, found := keeper.GetParticipant(ctx, participant1.Address)
	require.True(t, found, "Participant 1 should be found after settlement")
	require.Equal(t, int64(0), updated1.CoinBalance, "Participant 1 coin balance should be reset to 0")
	require.Equal(t, uint32(1), updated1.EpochsCompleted, "Participant 1 should have 1 epoch completed")
	logger.Info("Verified participant 1 updates", "coinBalance", updated1.CoinBalance, "epochsCompleted", updated1.EpochsCompleted)

	updated2, found := keeper.GetParticipant(ctx, participant2.Address)
	require.True(t, found, "Participant 2 should be found after settlement")
	require.Equal(t, int64(0), updated2.CoinBalance, "Participant 2 coin balance should be reset to 0")
	require.Equal(t, uint32(1), updated2.EpochsCompleted, "Participant 2 should have 1 epoch completed")
	logger.Info("Verified participant 2 updates", "coinBalance", updated2.CoinBalance, "epochsCompleted", updated2.EpochsCompleted)
	settleAmount1, found := keeper.GetSettleAmount(ctx, participant1.Address)
	require.True(t, found, "Settle amount for participant 1 should be found")
	require.Equal(t, uint64(1000), settleAmount1.WorkCoins, "Participant 1 work coins should be 1000")
	require.Equal(t, uint64(expectedRewardCoin/2), settleAmount1.RewardCoins, "Participant 1 reward coins should be half of total")
	require.Equal(t, uint64(10), settleAmount1.EpochIndex, "Epoch index should be 10")
	logger.Info("Verified participant 1 settle amount", "workCoins", settleAmount1.WorkCoins, "rewardCoins", settleAmount1.RewardCoins)

	settleAmount2, found := keeper.GetSettleAmount(ctx, participant2.Address)
	require.True(t, found, "Settle amount for participant 2 should be found")
	require.Equal(t, uint64(1000), settleAmount2.WorkCoins, "Participant 2 work coins should be 1000")
	require.Equal(t, uint64(expectedRewardCoin/2), settleAmount2.RewardCoins, "Participant 2 reward coins should be half of total")
	logger.Info("Verified participant 2 settle amount", "workCoins", settleAmount2.WorkCoins, "rewardCoins", settleAmount2.RewardCoins)

	logger.Info("TestActualSettle completed successfully")
}

func TestActualSettleWithManyParticipants(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestActualSettleWithManyParticipants - testing settlement with 150 participants")

	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)

	// Configure to use legacy reward system for this test
	params := keeper.GetParams(ctx)
	params.BitcoinRewardParams.UseBitcoinRewards = false
	keeper.SetParams(ctx, params)
	logger.Info("Configured to use legacy reward system")

	// Create 150 participants to test pagination (>100 default page size)
	participants := make([]types.Participant, 150)
	logger.Info("Creating 150 participants to test pagination")

	for i := 0; i < 150; i++ {
		address := testutil.Bech32Addr(i)
		participant := types.Participant{
			Index:       address,
			Address:     address,
			CoinBalance: 1000,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		}
		participants[i] = participant
		keeper.SetParticipant(ctx, participant)
		if i%50 == 0 {
			logger.Info("Created participants", "count", i+1)
		}
	}
	logger.Info("Completed creating all participants", "total", 150)

	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 10,
	})
	// Set active participants for the epoch
	activeParticipantInfos := make([]*types.ActiveParticipant, 150)
	for i := 0; i < 150; i++ {
		activeParticipantInfos[i] = &types.ActiveParticipant{
			Index: participants[i].Address,
		}
	}
	keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      10,
		Participants: activeParticipantInfos,
	})
	logger.Info("Set epoch data", "epochIndex", 10)

	expectedRewardCoin := calcExpectedRewards(participants)
	logger.Info("Calculated expected total reward", "totalReward", expectedRewardCoin, "perParticipant", expectedRewardCoin/150)

	coins, err2 := types.GetCoins(expectedRewardCoin)
	require.NoError(t, err2, "Should be able to create coins from reward amount")
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	// This should work with pagination and process all 150 participants
	logger.Info("Starting SettleAccounts for 150 participants")
	err := keeper.SettleAccounts(ctx, 10, 0)
	require.NoError(t, err, "SettleAccounts should complete successfully with 150 participants")
	logger.Info("SettleAccounts completed successfully")

	// Verify all participants were processed
	expectedRewardPerParticipant := expectedRewardCoin / 150
	logger.Info("Starting verification of all 150 participants", "expectedRewardPerParticipant", expectedRewardPerParticipant)
	for i := 0; i < 150; i++ {
		address := testutil.Bech32Addr(i)
		updated, found := keeper.GetParticipant(ctx, address)
		require.True(t, found, "Participant %d should be found", i)
		require.Equal(t, int64(0), updated.CoinBalance, "Participant %d coin balance should be reset", i)
		require.Equal(t, uint32(1), updated.EpochsCompleted, "Participant %d should have 1 epoch completed", i)

		settleAmount, found := keeper.GetSettleAmount(ctx, address)
		require.True(t, found, "Settle amount for participant %d should be found", i)
		require.Equal(t, uint64(1000), settleAmount.WorkCoins, "Participant %d work coins", i)
		require.Equal(t, uint64(expectedRewardPerParticipant), settleAmount.RewardCoins, "Participant %d reward coins", i)
		require.Equal(t, uint64(10), settleAmount.EpochIndex, "Participant %d epoch index", i)

		if i%50 == 49 {
			logger.Info("Verified participants", "count", i+1, "total", 150)
		}
	}

	logger.Info("TestActualSettleWithManyParticipants completed successfully", "totalParticipants", 150, "totalReward", expectedRewardCoin)
}
