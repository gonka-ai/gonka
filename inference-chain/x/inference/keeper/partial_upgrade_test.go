package keeper_test

import (
	"context"
	"strconv"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNPartialUpgrade(keeper keeper.Keeper, ctx context.Context, n int) []types.PartialUpgrade {
	items := make([]types.PartialUpgrade, n)
	for i := range items {
		items[i].Height = uint64(i)

		keeper.SetPartialUpgrade(ctx, items[i])
	}
	return items
}

func TestPartialUpgradeGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNPartialUpgrade(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetPartialUpgrade(ctx,
			item.Height,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestPartialUpgradeRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNPartialUpgrade(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemovePartialUpgrade(ctx,
			item.Height,
		)
		_, found := keeper.GetPartialUpgrade(ctx,
			item.Height,
		)
		require.False(t, found)
	}
}

func TestPartialUpgradeGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNPartialUpgrade(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllPartialUpgrade(ctx)),
	)
}
