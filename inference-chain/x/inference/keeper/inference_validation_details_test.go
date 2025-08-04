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

func createNInferenceValidationDetails(keeper keeper.Keeper, ctx context.Context, n int) []types.InferenceValidationDetails {
	items := make([]types.InferenceValidationDetails, n)
	for i := range items {
		items[i].EpochId = uint64(i)
		items[i].InferenceId = strconv.Itoa(i)

		keeper.SetInferenceValidationDetails(ctx, items[i])
	}
	return items
}

func TestInferenceValidationDetailsGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceValidationDetails(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetInferenceValidationDetails(ctx,
			item.EpochId,
			item.InferenceId,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestInferenceValidationDetailsRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceValidationDetails(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveInferenceValidationDetails(ctx,
			item.EpochId,
			item.InferenceId,
		)
		_, found := keeper.GetInferenceValidationDetails(ctx,
			item.EpochId,
			item.InferenceId,
		)
		require.False(t, found)
	}
}

func TestInferenceValidationDetailsGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNInferenceValidationDetails(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllInferenceValidationDetails(ctx)),
	)
}
