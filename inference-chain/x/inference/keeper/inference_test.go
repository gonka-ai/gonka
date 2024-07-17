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

func createNInference(keeper keeper.Keeper, ctx context.Context, n int) []types.Inference {
	items := make([]types.Inference, n)
	for i := range items {
		items[i].Index = strconv.Itoa(i)

		keeper.SetInference(ctx, items[i])
	}
	return items
}

func TestInferenceGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInference(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetInference(ctx,
			item.Index,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestInferenceRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInference(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveInference(ctx,
			item.Index,
		)
		_, found := keeper.GetInference(ctx,
			item.Index,
		)
		require.False(t, found)
	}
}

func TestInferenceGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInference(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllInference(ctx)),
	)
}
