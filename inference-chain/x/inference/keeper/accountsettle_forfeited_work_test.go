package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Work coins of a participant that is not ACTIVE at settle are not paid (no SettleAmount), but they
// are already held by the inference module. SettleAccounts zeroes CoinBalance, so they must leave
// the module the same way as the undistributed reward share: to governance.
func TestSettleAccounts_ForfeitedWorkCoinsGoToGovernance(t *testing.T) {
	invalid := types.Participant{
		Index: testutil.Executor, Address: testutil.Executor,
		CoinBalance: 500, Status: types.ParticipantStatus_INVALID,
		CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100},
	}
	inactive := types.Participant{
		Index: testutil.Validator, Address: testutil.Validator,
		CoinBalance: 300, Status: types.ParticipantStatus_INACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100},
	}
	active := types.Participant{
		Index: testutil.Executor2, Address: testutil.Executor2,
		CoinBalance: 1000, Status: types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100},
	}
	forfeited := invalid.CoinBalance + inactive.CoinBalance

	k, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	var vws []*types.ValidationWeight
	var actives []*types.ActiveParticipant
	for _, p := range []types.Participant{invalid, inactive, active} {
		k.SetParticipant(ctx, p)
		vws = append(vws, &types.ValidationWeight{MemberAddress: p.Address, Weight: 1000, Reputation: 100, ConfirmationWeight: 1000, MlNodes: []*types.MLNodeInfo{{PocWeight: 1000}}})
		actives = append(actives, &types.ActiveParticipant{Index: p.Address})
	}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:               10,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{{ModelId: "model1", WeightScaleFactor: types.DecimalFromFloat(1)}},
		ValidationWeights:        vws,
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 10, ValidationWeights: vws, ModelId: "model1"})
	k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 10, Participants: actives})

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	var toGov int64
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, "gov", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, amt sdk.Coins, _ string) error {
			toGov += amt.AmountOf(types.BaseCoin).Int64()
			return nil
		}).AnyTimes()

	_, err = k.SettleAccounts(ctx, 10, 0)
	require.NoError(t, err)

	for _, p := range []types.Participant{invalid, inactive} {
		got, found := k.GetParticipant(ctx, p.Address)
		require.True(t, found)
		require.Zero(t, got.CoinBalance)
		_, found = k.GetSettleAmount(ctx, p.Address)
		require.False(t, found, "a non-ACTIVE participant is not paid")
	}
	activeSettle, found := k.GetSettleAmount(ctx, active.Address)
	require.True(t, found)
	require.Equal(t, uint64(1000), activeSettle.WorkCoins)

	// Everything that left the participant ledger either became a SettleAmount or went to governance.
	undistributedReward := int64(calcExpectedRewards(10, params)) - int64(activeSettle.RewardCoins)
	require.Equal(t, undistributedReward+forfeited, toGov)
}
