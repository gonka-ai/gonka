package keeper_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func createTestTokenomicsData(keeper keeper.Keeper, ctx context.Context) types.TokenomicsData {
	item := types.TokenomicsData{}
	keeper.SetTokenomicsData(ctx, item)
	return item
}

func TestTokenomicsDataGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	item := createTestTokenomicsData(keeper, ctx)
	rst, found := keeper.GetTokenomicsData(ctx)
	require.True(t, found)
	require.Equal(t,
		nullify.Fill(&item),
		nullify.Fill(&rst),
	)
}
