package keeper_test

import (
	"strconv"
	"testing"

	"cosmossdk.io/log"
	"github.com/productscience/inference/testutil"
	"go.uber.org/mock/gomock"

	keeper2 "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// createTestLogger creates a logger for testing
func createTestLogger(t *testing.T) log.Logger {
	return log.NewTestLogger(t)
}

func calcExpectedRewards(epochIndex int64, params types.Params) uint64 {
	value, _ := inference.CalculateFixedEpochReward(uint64(epochIndex-1), params.BitcoinRewardParams.InitialEpochReward, params.BitcoinRewardParams.DecayRate)
	return value
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
	params, err := keeper.GetParams(ctx)
	require.NoError(t, err)

	keeper.SetParticipant(ctx, participant1)
	keeper.SetParticipant(ctx, participant2)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 10,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      participant1.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
			{
				MemberAddress:      participant2.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
		},
	})
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 10,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      participant1.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
			{
				MemberAddress:      participant2.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
		},

		ModelId: "model1",
	})
	// Set active participants for the epoch
	keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 10,
		Participants: []*types.ActiveParticipant{
			{Index: participant1.Address},
			{Index: participant2.Address},
		},
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

	expectedRewardCoin := calcExpectedRewards(10, params)
	expectedRewardHalf := expectedRewardCoin / 2
	expectedRewardRemainder := expectedRewardCoin % 2
	logger.Info("Calculated expected reward", "totalReward", expectedRewardCoin, "perParticipant", expectedRewardHalf)

	coins, err2 := types.GetCoins(int64(expectedRewardCoin))
	require.NoError(t, err2, "Should be able to create coins from reward amount")
	logger.Info("Created coins for minting", "coins", coins)

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	if expectedRewardRemainder != 0 {
		remainderCoins, _ := types.GetCoins(int64(expectedRewardRemainder))
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", remainderCoins, gomock.Any()).Return(nil)
	}

	err = keeper.SettleAccounts(ctx, 10, 0)
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
	// remainder goes to `gov`, not to a participant
	require.Equal(t, expectedRewardHalf, settleAmount1.RewardCoins, "Participant 1 reward coins should be half of total")
	require.Equal(t, uint64(10), settleAmount1.EpochIndex, "Epoch index should be 10")
	logger.Info("Verified participant 1 settle amount", "workCoins", settleAmount1.WorkCoins, "rewardCoins", settleAmount1.RewardCoins)

	settleAmount2, found := keeper.GetSettleAmount(ctx, participant2.Address)
	require.True(t, found, "Settle amount for participant 2 should be found")
	require.Equal(t, uint64(1000), settleAmount2.WorkCoins, "Participant 2 work coins should be 1000")
	require.Equal(t, uint64(expectedRewardHalf), settleAmount2.RewardCoins, "Participant 2 reward coins should be half of total")
	logger.Info("Verified participant 2 settle amount", "workCoins", settleAmount2.WorkCoins, "rewardCoins", settleAmount2.RewardCoins)

	logger.Info("TestActualSettle completed successfully")
}

func TestActualSettleWithManyParticipants(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestActualSettleWithManyParticipants - testing settlement with 150 participants")

	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)

	// Create "many" participants to test pagination (>100 default page size)
	many := 150
	participants := make([]types.Participant, many)
	logger.Info("Creating " + strconv.Itoa(many) + " participants to test pagination")

	for i := 0; i < many; i++ {
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
	logger.Info("Completed creating all participants", "total", many)

	weights := make([]*types.ValidationWeight, many)
	for i := 0; i < many; i++ {
		weights[i] = &types.ValidationWeight{
			MemberAddress:      participants[i].Address,
			Weight:             1000,
			Reputation:         100,
			ConfirmationWeight: 1000,
		}
	}

	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:        10,
		ValidationWeights: weights,
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

	params, err := keeper.GetParams(ctx)
	require.NoError(t, err)

	expectedRewardCoin := calcExpectedRewards(10, params)
	dividedRewards := expectedRewardCoin / uint64(many)
	dividedRewardsRemainder := expectedRewardCoin % uint64(many)
	logger.Info("Calculated expected total reward", "totalReward", expectedRewardCoin, "perParticipant", dividedRewards)

	coins, err2 := types.GetCoins(int64(expectedRewardCoin))
	require.NoError(t, err2, "Should be able to create coins from reward amount")
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	if dividedRewardsRemainder != 0 {
		remainderCoins, _ := types.GetCoins(int64(dividedRewardsRemainder))
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", remainderCoins, gomock.Any()).Return(nil)
	}

	// This should work with pagination and process all 150 participants
	logger.Info("Starting SettleAccounts for 150 participants")
	err = keeper.SettleAccounts(ctx, 10, 0)
	require.NoError(t, err, "SettleAccounts should complete successfully with 150 participants")
	logger.Info("SettleAccounts completed successfully")

	// Verify all participants were processed
	logger.Info("Starting verification of all 150 participants", "expectedRewardPerParticipant", dividedRewards)
	for i := 0; i < many; i++ {
		address := testutil.Bech32Addr(i)
		updated, found := keeper.GetParticipant(ctx, address)
		require.True(t, found, "Participant %d should be found", i)
		require.Equal(t, int64(0), updated.CoinBalance, "Participant %d coin balance should be reset", i)
		require.Equal(t, uint32(1), updated.EpochsCompleted, "Participant %d should have 1 epoch completed", i)

		settleAmount, found := keeper.GetSettleAmount(ctx, address)
		require.True(t, found, "Settle amount for participant %d should be found", i)
		require.Equal(t, uint64(1000), settleAmount.WorkCoins, "Participant %d work coins", i)
		require.Equal(t, dividedRewards, settleAmount.RewardCoins, "Participant %d reward coins", i)
		require.Equal(t, uint64(10), settleAmount.EpochIndex, "Participant %d epoch index", i)

		if i%50 == 49 {
			logger.Info("Verified participants", "count", i+1, "total", many)
		}
	}

	logger.Info("TestActualSettleWithManyParticipants completed successfully", "totalParticipants", many, "totalReward", expectedRewardCoin)
}

// TestSettleWithGraceEpoch verifies that grace epoch relaxes downtime punishment.
// Without grace epoch: participant with 50% miss rate would be punished (reward = 0).
// With grace epoch (BinomTestP0 = 0.5): participant with 50% miss rate is NOT punished.
func TestSettleWithGraceEpoch(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestSettleWithGraceEpoch")

	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)

	epochIndex := uint64(10)

	// Create participant with high miss rate (50% missed)
	participantHighMiss := types.Participant{
		Index:       testutil.Executor,
		Address:     testutil.Executor,
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 50,  // 50 successful
			MissedRequests: 50,  // 50 missed = 50% miss rate
		},
	}

	keeper.SetParticipant(ctx, participantHighMiss)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      participantHighMiss.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
		},
	})
	keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epochIndex,
		Participants: []*types.ActiveParticipant{
			{Index: participantHighMiss.Address},
		},
	})

	// Add grace epoch with relaxed BinomTestP0 = 0.5 (allows 50% miss rate)
	binomTestP0 := &types.Decimal{Value: 5, Exponent: -1} // 0.5
	err := keeper.AddPunishmentGraceEpoch(ctx, epochIndex, binomTestP0, 3000)
	require.NoError(t, err)
	logger.Info("Added grace epoch", "epoch", epochIndex, "binomTestP0", "0.5")

	params, err := keeper.GetParams(ctx)
	require.NoError(t, err)

	expectedRewardCoin := calcExpectedRewards(int64(epochIndex), params)
	logger.Info("Expected reward", "amount", expectedRewardCoin)

	coins, err2 := types.GetCoins(int64(expectedRewardCoin))
	require.NoError(t, err2)

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	err = keeper.SettleAccounts(ctx, epochIndex, 0)
	require.NoError(t, err)

	// Verify participant got rewards (not punished due to grace epoch)
	settleAmount, found := keeper.GetSettleAmount(ctx, participantHighMiss.Address)
	require.True(t, found)
	require.Greater(t, settleAmount.RewardCoins, uint64(0), "Participant should receive rewards with grace epoch despite high miss rate")
	logger.Info("Verified participant received rewards with grace epoch", "rewardCoins", settleAmount.RewardCoins)
}

// TestSettleWithoutGraceEpoch verifies that without grace epoch, high miss rate leads to punishment.
func TestSettleWithoutGraceEpoch(t *testing.T) {
	logger := createTestLogger(t)
	logger.Info("Starting TestSettleWithoutGraceEpoch")

	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)

	epochIndex := uint64(10)

	// Create participant with high miss rate (50% missed)
	participantHighMiss := types.Participant{
		Index:       testutil.Executor,
		Address:     testutil.Executor,
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 50,  // 50 successful
			MissedRequests: 50,  // 50 missed = 50% miss rate
		},
	}

	keeper.SetParticipant(ctx, participantHighMiss)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      participantHighMiss.Address,
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000,
			},
		},
	})
	keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epochIndex,
		Participants: []*types.ActiveParticipant{
			{Index: participantHighMiss.Address},
		},
	})

	// NO grace epoch added - default BinomTestP0 (0.1) should punish 50% miss rate

	params, err := keeper.GetParams(ctx)
	require.NoError(t, err)

	expectedRewardCoin := calcExpectedRewards(int64(epochIndex), params)
	logger.Info("Expected reward", "amount", expectedRewardCoin)

	coins, err2 := types.GetCoins(int64(expectedRewardCoin))
	require.NoError(t, err2)

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, coins, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	// Expect remainder to go to governance (punished participant's reward)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", gomock.Any(), gomock.Any()).Return(nil)

	err = keeper.SettleAccounts(ctx, epochIndex, 0)
	require.NoError(t, err)

	// Verify participant was punished (reward = 0)
	settleAmount, found := keeper.GetSettleAmount(ctx, participantHighMiss.Address)
	require.True(t, found)
	require.Equal(t, uint64(0), settleAmount.RewardCoins, "Participant should be punished without grace epoch")
	logger.Info("Verified participant was punished without grace epoch", "rewardCoins", settleAmount.RewardCoins)
}
