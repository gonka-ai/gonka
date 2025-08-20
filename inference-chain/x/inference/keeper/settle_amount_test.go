package keeper_test

import (
	"context"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func createNSettleAmount(keeper keeper.Keeper, ctx context.Context, n int) []types.SettleAmount {
	items := make([]types.SettleAmount, n)
	for i := range items {
		items[i].Participant = sample.AccAddress()
		keeper.SetSettleAmount(ctx, items[i])
	}
	return items
}

func TestSettleAmountGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNSettleAmount(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetSettleAmount(ctx,
			item.Participant,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestSettleAmountRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNSettleAmount(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveSettleAmount(ctx,
			item.Participant,
		)
		_, found := keeper.GetSettleAmount(ctx,
			item.Participant,
		)
		require.False(t, found)
	}
}

func TestSettleAmountGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNSettleAmount(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllSettleAmount(ctx)),
	)
}
