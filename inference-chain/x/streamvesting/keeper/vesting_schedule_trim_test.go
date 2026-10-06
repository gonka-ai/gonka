package keeper_test

import (
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

// vestingSchedule has the mainnet shape: 180 epochs of one ngonka coin.
func vestingSchedule(participant string) types.VestingSchedule {
	s := types.VestingSchedule{ParticipantAddress: participant}
	for i := 0; i < 180; i++ {
		s.EpochAmounts = append(s.EpochAmounts, types.EpochCoins{
			Coins: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1_234_567_890+int64(i))),
		})
	}
	return s
}

func TestVestingSchedule_ValueOmitsParticipant(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)
	want := vestingSchedule(testutil.Creator)

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.VestingSchedules.Set(gctx, addr, want))
	t.Logf("whole record Set gas: %d", gctx.GasMeter().GasConsumed())
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := k.VestingSchedules.Get(gctx, addr)
	require.NoError(t, err)
	t.Logf("whole record Get gas: %d", gctx.GasMeter().GasConsumed())

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetVestingSchedule(gctx, want))
	t.Logf("SetVestingSchedule gas: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.VestingSchedules.Get(ctx, addr)
	require.NoError(t, err)
	require.Empty(t, raw.ParticipantAddress)
	require.Equal(t, want.EpochAmounts, raw.EpochAmounts)

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetVestingSchedule(gctx, testutil.Creator)
	t.Logf("GetVestingSchedule gas: %d", gctx.GasMeter().GasConsumed())
	require.True(t, found)
	require.Equal(t, want, got)

	all, err := k.GetAllVestingSchedules(ctx)
	require.NoError(t, err)
	require.Equal(t, []types.VestingSchedule{want}, all)

	res, err := k.VestingSchedule(ctx, &types.QueryVestingScheduleRequest{ParticipantAddress: testutil.Creator})
	require.NoError(t, err)
	require.Equal(t, want, *res.VestingSchedule)
}

func TestVestingSchedule_LegacyAndNonCanonicalStoredWhole(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)

	legacy := vestingSchedule(testutil.Creator)
	require.NoError(t, k.VestingSchedules.Set(ctx, addr, legacy))
	got, found := k.GetVestingSchedule(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, legacy, got)

	upper := vestingSchedule(strings.ToUpper(testutil.Creator))
	require.NoError(t, k.SetVestingSchedule(ctx, upper))
	raw, err := k.VestingSchedules.Get(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, upper, raw)

	// Nothing but the participant would leave an empty value; it is stored whole.
	empty := types.VestingSchedule{ParticipantAddress: testutil.Creator}
	require.NoError(t, k.SetVestingSchedule(ctx, empty))
	raw, err = k.VestingSchedules.Get(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, empty, raw)
}

// The epoch unlock pays the address restored from the key and rewrites the record trimmed.
func TestVestingSchedule_EpochUnlockPaysRestoredAddress(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Creator)
	s := vestingSchedule(testutil.Creator)
	require.NoError(t, k.SetVestingSchedule(ctx, s))

	first := s.EpochAmounts[0].Coins
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr, first, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), testutil.Creator, types.ModuleName, "vesting", gomock.Any(), gomock.Any()).AnyTimes()

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.ProcessEpochUnlocks(gctx))
	t.Logf("ProcessEpochUnlocks gas (1 schedule, bank mocked): %d", gctx.GasMeter().GasConsumed())

	raw, err := k.VestingSchedules.Get(ctx, addr)
	require.NoError(t, err)
	require.Empty(t, raw.ParticipantAddress)
	require.Equal(t, s.EpochAmounts[1:], raw.EpochAmounts)
}
