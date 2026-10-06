package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestDevshardHostStats_ValueOmitsKeyFields(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	slot := types.DevshardSettlementHostStats{Missed: 1, Invalid: 2, Cost: 1_000_000, RequiredValidations: 4, CompletedValidations: 3}

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.AggregateDevshardHostStats(gctx, 415, addr, slot))
	t.Logf("AggregateDevshardHostStats gas, new record: %d", gctx.GasMeter().GasConsumed())
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.IncrementDevshardHostEscrowCount(gctx, 415, addr))
	t.Logf("IncrementDevshardHostEscrowCount gas, existing record: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.DevshardHostEpochStatsMap.Get(ctx, collections.Join(uint64(415), addr))
	require.NoError(t, err)
	require.Empty(t, raw.Participant)
	require.Zero(t, raw.EpochIndex)

	want := types.DevshardHostEpochStats{
		Participant: testutil.Executor, EpochIndex: 415,
		Missed: 1, Invalid: 2, Cost: 1_000_000, RequiredValidations: 4, CompletedValidations: 3, EscrowCount: 1,
	}
	got, found := k.GetDevshardHostEpochStats(ctx, 415, addr)
	require.True(t, found)
	require.Equal(t, want, got)
}

func TestDevshardHostStats_ZeroCountersStoredWhole(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	require.NoError(t, k.AggregateDevshardHostStats(ctx, 416, addr, types.DevshardSettlementHostStats{}))
	raw, err := k.DevshardHostEpochStatsMap.Get(ctx, collections.Join(uint64(416), addr))
	require.NoError(t, err)
	require.Equal(t, types.DevshardHostEpochStats{Participant: testutil.Executor, EpochIndex: 416}, raw)
}

func TestDevshardHostStats_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	key := collections.Join(uint64(415), addr)
	legacy := types.DevshardHostEpochStats{Participant: testutil.Executor, EpochIndex: 415, Missed: 2, EscrowCount: 1}
	require.NoError(t, k.DevshardHostEpochStatsMap.Set(ctx, key, legacy))

	got, found := k.GetDevshardHostEpochStats(ctx, 415, addr)
	require.True(t, found)
	require.Equal(t, legacy, got)

	require.NoError(t, k.IncrementDevshardHostEscrowCount(ctx, 415, addr))
	legacy.EscrowCount = 2
	got, found = k.GetDevshardHostEpochStats(ctx, 415, addr)
	require.True(t, found)
	require.Equal(t, legacy, got)
}
