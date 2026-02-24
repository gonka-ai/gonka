package keeper_test

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpdateDynamicPricing_UsesLightweightModelUsage(t *testing.T) {
	k, ctx := setupTestKeeperWithDynamicPricing(t)
	ctx = ctx.WithBlockTime(time.Unix(1000, 0))
	goCtx := sdk.WrapSDKContext(ctx)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DynamicPricingParams.StabilityZoneLowerBound = types.DecimalFromFloat(0.40)
	params.DynamicPricingParams.StabilityZoneUpperBound = types.DecimalFromFloat(0.60)
	params.DynamicPricingParams.PriceElasticity = types.DecimalFromFloat(0.05)
	params.DynamicPricingParams.MinPerTokenPrice = 1
	params.DynamicPricingParams.BasePerTokenPrice = 1000
	params.DynamicPricingParams.GracePeriodEndEpoch = 0
	params.DynamicPricingParams.UtilizationWindowDuration = 60
	k.SetParams(ctx, params)

	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 1}
	require.NoError(t, k.SetEpoch(ctx, epoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch.Index))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:      epoch.Index,
		ModelId:         "",
		SubGroupModels:  []string{"model-a"},
		UnitOfComputePrice: 1,
	})

	require.NoError(t, k.CacheModelCapacity(goCtx, "model-a", 100))
	require.NoError(t, k.SetModelCurrentPrice(goCtx, "model-a", 1000))

	sampleTs := ctx.BlockTime().UnixMilli() - 10_000
	require.NoError(t, k.AddModelUsageSample(goCtx, "model-a", sampleTs, 6000, 10))

	require.NoError(t, k.UpdateDynamicPricing(goCtx))

	price, err := k.GetModelCurrentPrice(goCtx, "model-a")
	require.NoError(t, err)
	require.Equal(t, uint64(1020), price)
}
