package keeper_test

import (
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func settledAmount() types.SettleAmount {
	return types.SettleAmount{
		Participant:   testutil.Executor,
		RewardCoins:   1_234_567_890_123,
		WorkCoins:     98_765_432,
		EpochIndex:    416,
		SeedSignature: strings.Repeat("ab", 64),
	}
}

func TestSettleAmount_ValueOmitsParticipant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := settledAmount()

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetSettleAmount(gctx, want))
	t.Logf("SetSettleAmount gas: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.SettleAmounts.Get(ctx, sdk.MustAccAddressFromBech32(testutil.Executor))
	require.NoError(t, err)
	require.Empty(t, raw.Participant)
	require.Equal(t, want.RewardCoins, raw.RewardCoins)

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetSettleAmount(gctx, testutil.Executor)
	t.Logf("GetSettleAmount gas: %d", gctx.GasMeter().GasConsumed())
	require.True(t, found)
	require.Equal(t, want, got)

	require.Equal(t, []types.SettleAmount{want}, k.GetAllSettleAmount(ctx))
	res, err := k.SettleAmountAll(ctx, &types.QueryAllSettleAmountRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.SettleAmount{want}, res.SettleAmount)
}

func TestSettleAmount_LegacyAndNonCanonicalStoredWhole(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	legacy := settledAmount()
	require.NoError(t, k.SettleAmounts.Set(ctx, addr, legacy))
	got, found := k.GetSettleAmount(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, legacy, got)

	upper := settledAmount()
	upper.Participant = strings.ToUpper(testutil.Executor)
	require.NoError(t, k.SetSettleAmount(ctx, upper))
	raw, err := k.SettleAmounts.Get(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, upper, raw)

	// Nothing but the participant would leave an empty value; it is stored whole.
	empty := types.SettleAmount{Participant: testutil.Executor}
	require.NoError(t, k.SetSettleAmount(ctx, empty))
	raw, err = k.SettleAmounts.Get(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, empty, raw)
}
