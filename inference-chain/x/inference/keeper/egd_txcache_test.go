package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func egdFixture() types.EpochGroupData {
	return types.EpochGroupData{
		EpochIndex:          4,
		PocStartBlockHeight: 100,
		SubGroupModels:      []string{"model-a"},
		ValidationWeights:   []*types.ValidationWeight{{MemberAddress: "addr1", Weight: 10}},
	}
}

func TestEpochGroupDataTxCache_ReadsStoreOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := egdFixture()
	k.SetEpochGroupData(ctx, want)

	gasOf := func(cached bool) []storetypes.Gas {
		c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		if cached {
			c = keeper.WithTxParamsCache(c)
		}
		var used []storetypes.Gas
		for i := 0; i < 3; i++ {
			before := c.GasMeter().GasConsumed()
			got, found, err := k.GetEpochGroupDataWithError(c, 4, "")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, want, got)
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

	c := keeper.WithTxParamsCache(ctx)
	_, found := k.GetEpochGroupData(c, 9, "")
	require.False(t, found)
}

func TestEpochGroupDataTxCache_CallerMutationDoesNotLeak(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	k.SetEpochGroupData(ctx, egdFixture())
	ctx = keeper.WithTxParamsCache(ctx)

	got, _ := k.GetEpochGroupData(ctx, 4, "")
	got.ValidationWeights[0].Weight = 99
	got.SubGroupModels = append(got.SubGroupModels, "model-b")

	again, _ := k.GetEpochGroupData(ctx, 4, "")
	require.Equal(t, egdFixture(), again)
}

func TestEpochGroupDataTxCache_WriteInDiscardedCacheContext(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	k.SetEpochGroupData(ctx, egdFixture())
	ctx = keeper.WithTxParamsCache(ctx)
	_, _ = k.GetEpochGroupData(ctx, 4, "")

	sub, _ := ctx.CacheContext()
	changed := egdFixture()
	changed.PocStartBlockHeight = 7
	k.SetEpochGroupData(sub, changed)
	got, _ := k.GetEpochGroupData(sub, 4, "")
	require.Equal(t, uint64(7), got.PocStartBlockHeight)

	got, found := k.GetEpochGroupData(ctx, 4, "")
	require.True(t, found)
	require.Equal(t, uint64(100), got.PocStartBlockHeight)

	sub, _ = ctx.CacheContext()
	k.RemoveEpochGroupData(sub, 4, "")
	_, found = k.GetEpochGroupData(sub, 4, "")
	require.False(t, found)
	_, found = k.GetEpochGroupData(ctx, 4, "")
	require.True(t, found)
}

func TestEpochGroupDataTxCache_SeesItsOwnWrites(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	k.SetEpochGroupData(ctx, egdFixture())
	ctx = keeper.WithTxParamsCache(ctx)
	_, _ = k.GetEpochGroupData(ctx, 4, "")

	changed := egdFixture()
	changed.PocStartBlockHeight = 7
	k.SetEpochGroupData(ctx, changed)
	got, _ := k.GetEpochGroupData(ctx, 4, "")
	require.Equal(t, uint64(7), got.PocStartBlockHeight)

}

func TestEpochGroupDataTxCache_StaysOnAfterSetParams(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	k.SetEpochGroupData(ctx, egdFixture())
	c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
	_, _ = k.GetEpochGroupData(c, 4, "")

	require.NoError(t, k.SetParams(c, types.DefaultParams()))
	before := c.GasMeter().GasConsumed()
	got, found := k.GetEpochGroupData(c, 4, "")
	require.True(t, found)
	require.Equal(t, egdFixture(), got)
	require.Equal(t, before, c.GasMeter().GasConsumed(), "params write must not turn off the group data part")
}
