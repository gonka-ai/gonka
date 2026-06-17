package keeper_test

import (
	"math"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Validation sampling must not silently drop an inference when summed epoch weights exceed uint32.
func TestClaimRewards_WeightAggregationAboveUint32_StillSamples(t *testing.T) {
	k, ms, ctx, _ := setupKeeperWithMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	epochIndex := uint64(100)
	epoch := types.Epoch{Index: epochIndex, PocStartBlockHeight: 1000}
	k.SetEpoch(sdkCtx, &epoch)
	require.NoError(t, k.SetParams(sdkCtx, types.DefaultParams()))

	const each = int64(2_200_000_000)
	require.LessOrEqual(t, each, int64(math.MaxUint32))
	require.Greater(t, each+each, int64(math.MaxUint32))

	epochData := types.EpochGroupData{
		EpochIndex:          epochIndex,
		EpochGroupId:        100,
		PocStartBlockHeight: epochIndex,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Creator, Weight: each},
			{MemberAddress: testutil.Executor, Weight: each},
		},
	}
	k.SetEpochGroupData(sdkCtx, epochData)

	inf := types.InferenceValidationDetails{
		EpochId:              epochIndex,
		InferenceId:          "inf-weight-sum-above-uint32",
		ExecutorId:           testutil.Executor,
		ExecutorReputation:   50,
		TrafficBasis:         1000,
		Model:                "",
		CreatedAtBlockHeight: 0,
	}
	k.SetInferenceValidationDetails(sdkCtx, inf)

	got, err := keeper.GetMustBeValidatedInferencesForTesting(ms, sdkCtx, &types.MsgClaimRewards{
		Creator:    testutil.Creator,
		EpochIndex: epochIndex,
		Seed:       42,
	})
	require.NoError(t, err)
	require.NotEmpty(t, got, "validation sampling must not silently drop the inference when summed weights exceed uint32")
	require.Contains(t, got, "inf-weight-sum-above-uint32")
}
