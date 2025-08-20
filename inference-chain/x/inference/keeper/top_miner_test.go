package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNTopMiner(keeper keeper.Keeper, ctx context.Context, n int) []types.TopMiner {
	items := make([]types.TopMiner, n)
	for i := range items {
		items[i].Address = testutil.Bech32Addr(i)

		keeper.SetTopMiner(ctx, items[i])
	}
	return items
}

func TestTopMinerGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNTopMiner(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetTopMiner(ctx,
			item.Address,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestTopMinerRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNTopMiner(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveTopMiner(ctx,
			item.Address,
		)
		_, found := keeper.GetTopMiner(ctx,
			item.Address,
		)
		require.False(t, found)
	}
}

func TestTopMinerGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNTopMiner(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllTopMiner(ctx)),
	)
}
