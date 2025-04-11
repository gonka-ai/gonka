package keeper_test

import (
	keeper2 "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

var tokenomicsParams = types.DefaultParams().TokenomicsParams
var defaultSettleParameters = inference.SettleParameters{
	CurrentSubsidyPercentage: 0.90,
	TotalSubsidyPaid:         0,
	StageCutoff:              0.05,
	StageDecrease:            0.20,
	TotalSubsidySupply:       600000000000,
}

func calcExpectedRewards(participants []types.Participant) int64 {
	totalWorkCoins := int64(0)
	for _, p := range participants {
		totalWorkCoins += p.CoinBalance
	}
	w := decimal.NewFromInt(totalWorkCoins)
	r := decimal.NewFromInt(1).Sub(decimal.NewFromFloat32(defaultSettleParameters.CurrentSubsidyPercentage))
	return w.Div(r).IntPart()
}

func TestReduceSubsidy(t *testing.T) {
	oParams := types.TokenomicsParams{
		SubsidyReductionAmount:   types.DecimalFromFloat(0.20),
		SubsidyReductionInterval: types.DecimalFromFloat(0.05),
		CurrentSubsidyPercentage: types.DecimalFromFloat(0.90),
	}
	params := oParams.ReduceSubsidyPercentage()
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
	subsidy := defaultSettleParameters.GetTotalSubsidy(1000)
	require.Equal(t, int64(10000), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestRewardsNoCrossover2(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         0,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000000,
	}
	subsidy := params.GetTotalSubsidy(340000)
	require.Equal(t, int64(3400000), subsidy.Amount)
	require.False(t, subsidy.CrossedCutoff)
}

func TestRewardsCrossover(t *testing.T) {
	params := inference.SettleParameters{
		CurrentSubsidyPercentage: 0.90,
		TotalSubsidyPaid:         9500,
		StageCutoff:              0.05,
		StageDecrease:            0.20,
		TotalSubsidySupply:       200000,
	}
	subsidy := params.GetTotalSubsidy(1000)
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
	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
	}
	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1})
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, expectedRewardCoin, newCoin.Amount)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin), p1Result.Settle.RewardCoins)
}

func TestEvenSettle(t *testing.T) {
	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
	}
	participant2 := types.Participant{
		Address:     "participant2",
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
	}
	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1, participant2})
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1, participant2}, &defaultSettleParameters)
	require.NoError(t, err)
	require.Equal(t, 2, len(result))
	require.Equal(t, expectedRewardCoin, newCoin.Amount)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), p1Result.Settle.RewardCoins)
	p2Result := result[1]
	require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), p2Result.Settle.RewardCoins)
}

func TestEvenAmong3(t *testing.T) {
	participant1 := types.Participant{
		Address:     "participant1",
		CoinBalance: 255000,
		Status:      types.ParticipantStatus_RAMPING,
	}
	participant2 := types.Participant{
		Address:     "participant2",
		CoinBalance: 340000,
		Status:      types.ParticipantStatus_ACTIVE,
	}
	participant3 := types.Participant{
		Address:     "participant3",
		CoinBalance: 255000,
		Status:      types.ParticipantStatus_RAMPING,
	}
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1, participant2, participant3}, &defaultSettleParameters)
	require.NoError(t, err)
	require.Equal(t, 3, len(result))
	require.Equal(t, int64(8500000), newCoin.Amount)
	p1Result := result[0]
	require.Equal(t, uint64(255000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(2550000), p1Result.Settle.RewardCoins)
	p2Result := result[1]
	require.Equal(t, uint64(340000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(3400000), p2Result.Settle.RewardCoins)
	p3Result := result[2]
	require.Equal(t, uint64(255000), p3Result.Settle.WorkCoins)
	require.Equal(t, uint64(2550000), p3Result.Settle.RewardCoins)
}

func TestNoWorkBalance(t *testing.T) {
	participant1 := newParticipant(0, 0, "1")
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	// If no one works, no coin
	require.Equal(t, int64(0), newCoin.Amount)
	p1Result := result[0]
	require.Zero(t, p1Result.Settle.WorkCoins)
	require.Zero(t, p1Result.Settle.RewardCoins)
}

func TestNegativeCoinBalance(t *testing.T) {
	participant1 := newParticipant(-1, 0, "1")
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, &defaultSettleParameters)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, int64(0), newCoin.Amount)
	p1Result := result[0]
	require.Equal(t, types.ErrNegativeCoinBalance, p1Result.Error)
}

func newParticipant(coinBalance int64, refundBalance int64, id string) types.Participant {
	return types.Participant{
		Address:     "participant" + id,
		CoinBalance: coinBalance,
		Status:      types.ParticipantStatus_ACTIVE,
	}
}

func TestActualSettle(t *testing.T) {
	participant1 := types.Participant{
		Index:             "cosmos1sjjvddfrhdv6dn4m27wudcx53x5tzdzl67ah98",
		Address:           "cosmos1sjjvddfrhdv6dn4m27wudcx53x5tzdzl67ah98",
		CoinBalance:       1000,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{},
	}
	participant2 := types.Participant{
		Index:             "cosmos1jj7kves6pwdn7whd06cjf7e8q4543s92v984fa",
		Address:           "cosmos1jj7kves6pwdn7whd06cjf7e8q4543s92v984fa",
		CoinBalance:       1000,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{},
	}
	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)
	keeper.SetParticipant(ctx, participant1)
	keeper.SetParticipant(ctx, participant2)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		PocStartBlockHeight: 10,
	})
	expectedRewardCoin := calcExpectedRewards([]types.Participant{participant1, participant2})

	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.ModuleName, types.GetCoins(expectedRewardCoin)).Return(nil)
	err := keeper.SettleAccounts(ctx, 10)
	require.NoError(t, err)
	updated1, found := keeper.GetParticipant(ctx, participant1.Address)
	require.True(t, found)
	require.Equal(t, int64(0), updated1.CoinBalance)
	require.Equal(t, uint32(1), updated1.EpochsCompleted)
	updated2, found := keeper.GetParticipant(ctx, participant2.Address)
	require.True(t, found)
	require.Equal(t, int64(0), updated2.CoinBalance)
	require.Equal(t, uint32(1), updated2.EpochsCompleted)
	settleAmount1, found := keeper.GetSettleAmount(ctx, participant1.Address)
	require.True(t, found)
	require.Equal(t, uint64(1000), settleAmount1.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), settleAmount1.RewardCoins)
	require.Equal(t, uint64(10), settleAmount1.PocStartHeight)
	settleAmount2, found := keeper.GetSettleAmount(ctx, participant2.Address)
	require.True(t, found)
	require.Equal(t, uint64(1000), settleAmount2.WorkCoins)
	require.Equal(t, uint64(expectedRewardCoin/2), settleAmount2.RewardCoins)
}
