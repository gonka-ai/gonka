package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSingleSettle(t *testing.T) {
	participant1 := types.Participant{
		Address:       "participant1",
		CoinBalance:   1000,
		RefundBalance: 0,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, 50)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, int64(inference.EpochNewCoin), newCoin)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin), p1Result.Settle.RewardCoins)
	require.Equal(t, uint64(0), p1Result.Settle.RefundCoins)
}

func TestEvenSettle(t *testing.T) {
	participant1 := types.Participant{
		Address:       "participant1",
		CoinBalance:   1000,
		RefundBalance: 0,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	participant2 := types.Participant{
		Address:       "participant2",
		CoinBalance:   1000,
		RefundBalance: 500,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1, participant2}, 50)
	require.NoError(t, err)
	require.Equal(t, 2, len(result))
	require.Equal(t, int64(inference.EpochNewCoin), newCoin)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2), p1Result.Settle.RewardCoins)
	require.Equal(t, uint64(0), p1Result.Settle.RefundCoins)
	p2Result := result[1]
	require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2), p2Result.Settle.RewardCoins)
	require.Equal(t, uint64(500), p2Result.Settle.RefundCoins)
}

func TestRewardHalf(t *testing.T) {
	participant1 := types.Participant{
		Address:       "participant1",
		CoinBalance:   1000,
		RefundBalance: 0,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, inference.CoinHalvingHeight)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, int64(inference.EpochNewCoin/2), newCoin)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2), p1Result.Settle.RewardCoins)
	require.Equal(t, uint64(0), p1Result.Settle.RefundCoins)
}

func TestNoWorkBalance(t *testing.T) {
	participant1 := newParticipant(0, 0, "1")
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, 50)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	// If no one works, no coin
	require.Equal(t, int64(0), newCoin)
	p1Result := result[0]
	require.Zero(t, p1Result.Settle.WorkCoins)
	require.Zero(t, p1Result.Settle.RewardCoins)
	require.Zero(t, p1Result.Settle.RefundCoins)
}

func TestNegativeCoinBalance(t *testing.T) {
	participant1 := newParticipant(-1, 0, "1")
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, 50)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, int64(0), newCoin)
	p1Result := result[0]
	require.Equal(t, types.ErrNegativeCoinBalance, p1Result.Error)
}

func TestNegativeRefundBalance(t *testing.T) {
	// Unclear how we might get a negative balance, I'm not going to belabor the behavior
	participant1 := newParticipant(1, -1, "1")
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{participant1}, 50)
	require.NoError(t, err)
	require.Equal(t, 1, len(result))
	require.Equal(t, int64(0), newCoin)
	p1Result := result[0]
	require.Equal(t, types.ErrNegativeRefundBalance, p1Result.Error)
}

func TestInvalidParticipantBalanceGetsDistributed(t *testing.T) {
	p1 := newParticipant(1000, 0, "1")
	p2 := newParticipant(1000, 0, "2")
	invalidParticipant := types.Participant{
		Address:       "invalid",
		CoinBalance:   2000,
		RefundBalance: 0,
		Status:        types.ParticipantStatus_INVALID,
	}
	result, newCoin, err := inference.GetSettleAmounts([]types.Participant{p1, p2, invalidParticipant}, 50)
	require.NoError(t, err)
	require.Equal(t, 3, len(result))
	require.Equal(t, int64(inference.EpochNewCoin), newCoin)
	p1Result := result[0]
	require.Equal(t, uint64(1000), p1Result.Settle.WorkCoins)
	require.Equal(t, int64(inference.EpochNewCoin/2+1000), int64(p1Result.Settle.RewardCoins))
	require.Equal(t, uint64(0), p1Result.Settle.RefundCoins)
	p2Result := result[1]
	require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2+1000), p2Result.Settle.RewardCoins)
	require.Equal(t, uint64(0), p2Result.Settle.RefundCoins)
	invalidResult := result[2]
	require.Equal(t, uint64(0), invalidResult.Settle.WorkCoins)
	require.Equal(t, uint64(0), invalidResult.Settle.RewardCoins)
	require.Equal(t, uint64(0), invalidResult.Settle.RefundCoins)
}

func newParticipant(coinBalance int64, refundBalance int64, id string) types.Participant {
	return types.Participant{
		Address:       "participant" + id,
		CoinBalance:   coinBalance,
		RefundBalance: refundBalance,
		Status:        types.ParticipantStatus_ACTIVE,
	}
}

func TestActualSettle(t *testing.T) {
	participant1 := types.Participant{
		Index:         "cosmos1sjjvddfrhdv6dn4m27wudcx53x5tzdzl67ah98",
		Address:       "cosmos1sjjvddfrhdv6dn4m27wudcx53x5tzdzl67ah98",
		CoinBalance:   1000,
		RefundBalance: 0,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	participant2 := types.Participant{
		Index:         "cosmos1jj7kves6pwdn7whd06cjf7e8q4543s92v984fa",
		Address:       "cosmos1jj7kves6pwdn7whd06cjf7e8q4543s92v984fa",
		CoinBalance:   1000,
		RefundBalance: 500,
		Status:        types.ParticipantStatus_ACTIVE,
	}
	keeper, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)
	keeper.SetParticipant(ctx, participant1)
	keeper.SetParticipant(ctx, participant2)
	keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		PocStartBlockHeight: 10,
	})

	mocks.BankKeeper.EXPECT().MintCoins(ctx, types.ModuleName, inference.GetCoins(inference.EpochNewCoin)).Return(nil)
	// Issue refund immediately
	participant2Address, err := sdk.AccAddressFromBech32(participant2.Address)
	require.NoError(t, err)

	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, types.ModuleName, participant2Address, inference.GetCoins(500)).Return(nil)
	err = keeper.SettleAccounts(ctx, 10)
	require.NoError(t, err)
	updated1, found := keeper.GetParticipant(ctx, participant1.Address)
	require.True(t, found)
	require.Equal(t, int64(0), updated1.CoinBalance)
	require.Equal(t, int64(0), updated1.RefundBalance)
	require.Equal(t, float32(0.01), updated1.Reputation)
	updated2, found := keeper.GetParticipant(ctx, participant2.Address)
	require.True(t, found)
	require.Equal(t, int64(0), updated2.CoinBalance)
	require.Equal(t, int64(0), updated2.RefundBalance)
	require.Equal(t, float32(0.01), updated2.Reputation)
	settleAmount1, found := keeper.GetSettleAmount(ctx, participant1.Address)
	require.True(t, found)
	require.Equal(t, uint64(1000), settleAmount1.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2), settleAmount1.RewardCoins)
	require.Equal(t, uint64(0), settleAmount1.RefundCoins)
	require.Equal(t, uint64(10), settleAmount1.PocStartHeight)
	settleAmount2, found := keeper.GetSettleAmount(ctx, participant2.Address)
	require.True(t, found)
	require.Equal(t, uint64(1000), settleAmount2.WorkCoins)
	require.Equal(t, uint64(inference.EpochNewCoin/2), settleAmount2.RewardCoins)
	// Refund should already have been issued
	require.Equal(t, uint64(0), settleAmount2.RefundCoins)
}
