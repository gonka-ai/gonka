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

func createNInferenceTimeout(keeper keeper.Keeper, ctx context.Context, n int) []types.InferenceTimeout {
	items := make([]types.InferenceTimeout, n)
	for i := range items {
		items[i].ExpirationHeight = uint64(i)
		items[i].InferenceId = strconv.Itoa(i)

		keeper.SetInferenceTimeout(ctx, items[i])
	}
	return items
}

func TestInferenceTimeoutGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceTimeout(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetInferenceTimeout(ctx,
			item.ExpirationHeight,
			item.InferenceId,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestInferenceTimeoutRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceTimeout(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveInferenceTimeout(ctx,
			item.ExpirationHeight,
			item.InferenceId,
		)
		_, found := keeper.GetInferenceTimeout(ctx,
			item.ExpirationHeight,
			item.InferenceId,
		)
		require.False(t, found)
	}
}

func TestInferenceTimeoutGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceTimeout(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllInferenceTimeout(ctx)),
	)
}
