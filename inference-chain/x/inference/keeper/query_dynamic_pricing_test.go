package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// The epoch group data map is keyed by epoch index. The effective epoch's
// PoC start height is a block height on a different scale, so a lookup by
// height only succeeds when the two happen to coincide.
func TestGetAllModelCapacities_LooksUpEpochGroupByIndex(t *testing.T) {
	k, ctx := setupTestKeeperWithDynamicPricing(t)
	goCtx := sdk.WrapSDKContext(ctx)

	effectiveEpoch := types.Epoch{
		Index:               386,
		PocStartBlockHeight: 5952281,
	}
	require.NoError(t, k.SetEpoch(ctx, &effectiveEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, effectiveEpoch.Index))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          effectiveEpoch.Index,
		ModelId:             "",
		PocStartBlockHeight: uint64(effectiveEpoch.PocStartBlockHeight),
		SubGroupModels:      []string{"model-a", "model-b"},
	})
	require.NoError(t, k.CacheModelCapacity(goCtx, "model-a", 1000))
	require.NoError(t, k.CacheModelCapacity(goCtx, "model-b", 250))

	resp, err := k.GetAllModelCapacities(goCtx, &types.QueryGetAllModelCapacitiesRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.ModelCapacity{
		{ModelId: "model-a", Capacity: 1000},
		{ModelId: "model-b", Capacity: 250},
	}, resp.ModelCapacities)
}

func TestGetAllModelCapacities_SkipsModelsWithoutCachedCapacity(t *testing.T) {
	k, ctx := setupTestKeeperWithDynamicPricing(t)
	goCtx := sdk.WrapSDKContext(ctx)

	effectiveEpoch := types.Epoch{
		Index:               7,
		PocStartBlockHeight: 900,
	}
	require.NoError(t, k.SetEpoch(ctx, &effectiveEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, effectiveEpoch.Index))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          effectiveEpoch.Index,
		ModelId:             "",
		PocStartBlockHeight: uint64(effectiveEpoch.PocStartBlockHeight),
		SubGroupModels:      []string{"cached", "uncached"},
	})
	require.NoError(t, k.CacheModelCapacity(goCtx, "cached", 42))

	resp, err := k.GetAllModelCapacities(goCtx, &types.QueryGetAllModelCapacitiesRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.ModelCapacity{{ModelId: "cached", Capacity: 42}}, resp.ModelCapacities)
}
