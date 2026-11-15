package keeper_test

import (
	"context"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
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

func TestAddTokenomicsDataReadsStoreOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	require.NoError(t, k.SetTokenomicsData(ctx, types.TokenomicsData{TotalFees: 7, TotalBurned: 3}))

	// Reference: one read plus one write of the accumulated value.
	ref := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	cur, found := k.GetTokenomicsData(ref)
	require.True(t, found)
	cur.TotalFees += 5
	require.NoError(t, k.SetTokenomicsData(ref, cur))
	want := ref.GasMeter().GasConsumed()

	require.NoError(t, k.SetTokenomicsData(ctx, types.TokenomicsData{TotalFees: 7, TotalBurned: 3}))
	metered := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.AddTokenomicsData(metered, &types.TokenomicsData{TotalFees: 5}))
	require.Equal(t, want, metered.GasMeter().GasConsumed())

	got, found := k.GetTokenomicsData(ctx)
	require.True(t, found)
	require.Equal(t, uint64(12), got.TotalFees)
	require.Equal(t, uint64(3), got.TotalBurned)
}
