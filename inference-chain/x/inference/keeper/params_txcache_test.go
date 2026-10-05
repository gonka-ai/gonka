package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestTxParamsCache_ReadsStoreOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	gasOf := func(cached bool) []storetypes.Gas {
		c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		if cached {
			c = keeper.WithTxParamsCache(c)
		}
		var used []storetypes.Gas
		for i := 0; i < 3; i++ {
			before := c.GasMeter().GasConsumed()
			p, err := k.GetParams(c)
			require.NoError(t, err)
			require.Equal(t, types.DefaultParams(), p)
			used = append(used, c.GasMeter().GasConsumed()-before)
		}
		return used
	}

	plain := gasOf(false)
	cached := gasOf(true)
	require.Equal(t, plain[0], cached[0], "first read pays the same")
	require.Equal(t, plain[0], plain[2], "uncached pays every time")
	require.Zero(t, cached[1])
	require.Zero(t, cached[2])
}

func TestTxParamsCache_CallerMutationDoesNotLeak(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	ctx = keeper.WithTxParamsCache(ctx)

	p, err := k.GetParams(ctx)
	require.NoError(t, err)
	p.EpochParams.EpochLength = 7

	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultParams().EpochParams.EpochLength, again.EpochParams.EpochLength)
}

func TestTxParamsCache_OffAfterSetParams(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	ctx = keeper.WithTxParamsCache(ctx)
	_, err := k.GetParams(ctx)
	require.NoError(t, err)

	// SetParams inside a CacheContext that is then discarded: later reads
	// must see the store, not the discarded value.
	sub, _ := ctx.CacheContext()
	changed := types.DefaultParams()
	changed.EpochParams.EpochLength = 7
	require.NoError(t, k.SetParams(sub, changed))
	got, err := k.GetParams(sub)
	require.NoError(t, err)
	require.Equal(t, int64(7), got.EpochParams.EpochLength)

	got, err = k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultParams().EpochParams.EpochLength, got.EpochParams.EpochLength)
}

func TestTxParamsCache_SPRTValuesReadOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	require.NoError(t, k.PrecomputeSPRTValues(ctx))
	want := k.GetPrecomputedSPRTValues(ctx)

	c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
	require.Equal(t, want, k.GetPrecomputedSPRTValues(c))
	before := c.GasMeter().GasConsumed()
	require.Equal(t, want, k.GetPrecomputedSPRTValues(c))
	require.Equal(t, before, c.GasMeter().GasConsumed(), "second read must not touch the store")

	// Recomputing in the tx turns the cache off: later reads see the store.
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.2)
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.PrecomputeSPRTValues(c))
	require.NotEqual(t, want, k.GetPrecomputedSPRTValues(c))
	require.Equal(t, k.GetPrecomputedSPRTValues(ctx), k.GetPrecomputedSPRTValues(c))
}

func TestTxParamsCache_EffectiveEpochIndexReadOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))

	c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
	got, found := k.GetEffectiveEpochIndex(c)
	require.True(t, found)
	require.Equal(t, uint64(5), got)
	before := c.GasMeter().GasConsumed()
	got, _ = k.GetEffectiveEpochIndex(c)
	require.Equal(t, uint64(5), got)
	require.Equal(t, before, c.GasMeter().GasConsumed(), "second read must not touch the store")

	// A write inside a discarded CacheContext must not leak into later reads.
	sub, _ := c.CacheContext()
	require.NoError(t, k.SetEffectiveEpochIndex(sub, 6))
	got, _ = k.GetEffectiveEpochIndex(sub)
	require.Equal(t, uint64(6), got)
	got, _ = k.GetEffectiveEpochIndex(c)
	require.Equal(t, uint64(5), got)
}
