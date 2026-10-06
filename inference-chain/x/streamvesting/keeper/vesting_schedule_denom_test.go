package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

func TestVestingSchedule_SingleDenomStoredOnce(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)
	want := vestingSchedule(testutil.Creator)
	// An epoch whose share rounded to zero holds no coins.
	want.EpochAmounts[7] = types.EpochCoins{}

	trimmedAddr := want
	trimmedAddr.ParticipantAddress = ""
	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.VestingSchedules.Set(gctx, addr, trimmedAddr))
	t.Logf("address-trimmed Set gas: %d, value %d B", gctx.GasMeter().GasConsumed(), trimmedAddr.Size())
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := k.VestingSchedules.Get(gctx, addr)
	require.NoError(t, err)
	t.Logf("address-trimmed Get gas: %d", gctx.GasMeter().GasConsumed())

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetVestingSchedule(gctx, want))
	t.Logf("SetVestingSchedule gas: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.VestingSchedules.Get(ctx, addr)
	require.NoError(t, err)
	t.Logf("stored value %d B", raw.Size())
	require.Empty(t, raw.EpochAmounts)
	require.Equal(t, "ngonka", raw.Denom)
	require.Equal(t, "", raw.Amounts[7])
	require.Equal(t, "1234567890", raw.Amounts[0])

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetVestingSchedule(gctx, testutil.Creator)
	t.Logf("GetVestingSchedule gas: %d", gctx.GasMeter().GasConsumed())
	require.True(t, found)
	require.Equal(t, want, got)
	require.Empty(t, got.Denom)
	require.Nil(t, got.Amounts)

	all, err := k.GetAllVestingSchedules(ctx)
	require.NoError(t, err)
	require.Equal(t, []types.VestingSchedule{want}, all)

	res, err := k.VestingSchedule(ctx, &types.QueryVestingScheduleRequest{ParticipantAddress: testutil.Creator})
	require.NoError(t, err)
	require.Equal(t, want, *res.VestingSchedule)
}

// Schedules the denom-once form cannot restore exactly are stored with their epochs.
func TestVestingSchedule_OtherShapesKeepEpochs(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)

	twoCoins := vestingSchedule(testutil.Creator)
	twoCoins.EpochAmounts[3].Coins = sdk.NewCoins(sdk.NewInt64Coin("ngonka", 5), sdk.NewInt64Coin("uatom", 7))
	twoDenoms := vestingSchedule(testutil.Creator)
	twoDenoms.EpochAmounts[3].Coins = sdk.NewCoins(sdk.NewInt64Coin("uatom", 7))
	zeroCoin := vestingSchedule(testutil.Creator)
	zeroCoin.EpochAmounts[3].Coins = sdk.Coins{{Denom: "ngonka", Amount: math.ZeroInt()}}
	noCoins := types.VestingSchedule{ParticipantAddress: testutil.Creator, EpochAmounts: make([]types.EpochCoins, 3)}

	for name, s := range map[string]types.VestingSchedule{
		"two coins": twoCoins, "two denoms": twoDenoms, "zero coin": zeroCoin, "no coins": noCoins,
	} {
		require.NoError(t, k.SetVestingSchedule(ctx, s), name)
		raw, err := k.VestingSchedules.Get(ctx, addr)
		require.NoError(t, err, name)
		require.Empty(t, raw.Denom, name)
		require.Len(t, raw.EpochAmounts, len(s.EpochAmounts), name)
		got, found := k.GetVestingSchedule(ctx, testutil.Creator)
		require.True(t, found, name)
		require.Equal(t, s.EpochAmounts, got.EpochAmounts, name)
	}

	stored := types.VestingSchedule{ParticipantAddress: testutil.Creator, Denom: "ngonka", Amounts: []string{"1"}}
	require.Error(t, k.SetVestingSchedule(ctx, stored))
}
