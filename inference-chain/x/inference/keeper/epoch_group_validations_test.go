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

func createNEpochGroupValidations(keeper keeper.Keeper, ctx context.Context, n int) []types.EpochGroupValidations {
	items := make([]types.EpochGroupValidations, n)
	for i := range items {
		items[i].Participant = strconv.Itoa(i)
		items[i].EpochIndex = uint64(i)

		keeper.SetEpochGroupValidations(ctx, items[i])
	}
	return items
}

func TestEpochGroupValidationsGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupValidations(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetEpochGroupValidations(ctx,
			item.Participant,
			item.EpochIndex,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestEpochGroupValidationsRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupValidations(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveEpochGroupValidations(ctx,
			item.Participant,
			item.EpochIndex,
		)
		_, found := keeper.GetEpochGroupValidations(ctx,
			item.Participant,
			item.EpochIndex,
		)
		require.False(t, found)
	}
}

func TestEpochGroupValidationsGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupValidations(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllEpochGroupValidations(ctx)),
	)
}
