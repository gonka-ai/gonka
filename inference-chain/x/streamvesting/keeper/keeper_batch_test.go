package keeper_test

import (
	"errors"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

// The two vested payments of a ClaimRewards (work 9154743497, reward 35989755837623 ngonka,
// both over 180 epochs, mainnet tx at height 6353077) on top of an existing 180-epoch schedule.
func TestAddVestedRewardsBatch_SameScheduleAsSequentialCalls(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := testutil.Creator
	epochs := uint64(180)
	prior := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 5260142515387237))
	work := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 9154743497))
	reward := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 35989755837623))

	run := func(batch bool) (types.VestingSchedule, storetypes.Gas) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		require.NoError(t, k.AddVestedRewards(ctx, participant, "inference", prior, &epochs, ""))

		gasCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		if batch {
			require.NoError(t, k.AddVestedRewardsBatch(gasCtx, participant, "inference", []types.VestedReward{
				{Amount: work, VestingEpochs: &epochs, Memo: "work_coins"},
				{Amount: reward, VestingEpochs: &epochs, Memo: "reward_coins"},
			}))
		} else {
			require.NoError(t, k.AddVestedRewards(gasCtx, participant, "inference", work, &epochs, "work_coins"))
			require.NoError(t, k.AddVestedRewards(gasCtx, participant, "inference", reward, &epochs, "reward_coins"))
		}
		schedule, found := k.GetVestingSchedule(ctx, participant)
		require.True(t, found)
		return schedule, gasCtx.GasMeter().GasConsumed()
	}

	sequential, sequentialGas := run(false)
	batched, batchedGas := run(true)
	require.Equal(t, sequential, batched)
	require.Less(t, batchedGas, sequentialGas)
	t.Logf("schedule entries=%d gas: two calls %d, batch %d", len(batched.EpochAmounts), sequentialGas, batchedGas)
}

func TestAddVestedRewardsBatch_FailedRewardLeavesScheduleAndNamesIndex(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
	participant := testutil.Creator
	epochs := uint64(10)
	first := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1000))
	second := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 2000))
	transferErr := errors.New("insufficient funds")
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, first, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", types.ModuleName, second, gomock.Any()).Return(transferErr)

	err := k.AddVestedRewardsBatch(ctx, participant, "inference", []types.VestedReward{
		{Amount: first, VestingEpochs: &epochs},
		{Amount: second, VestingEpochs: &epochs},
	})
	var rewardErr *types.VestedRewardError
	require.ErrorAs(t, err, &rewardErr)
	require.Equal(t, 1, rewardErr.Index)
	require.ErrorIs(t, err, transferErr)
	_, found := k.GetVestingSchedule(ctx, participant)
	require.False(t, found)
}
