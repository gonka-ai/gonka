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
