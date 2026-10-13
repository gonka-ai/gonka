package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDynamicCoefficientsQuery(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	params := &types.DynamicCoefficientParams{
		TargetZoneBps: 500,
		StepMin:       &types.Decimal{Value: 5, Exponent: -3},
		StepMax:       &types.Decimal{Value: 5, Exponent: -2},
	}
	scales := []*types.ConfirmationWeightScale{{
		ModelId:              "model-a",
		EffectiveCoefficient: &types.Decimal{Value: 2, Exponent: 0},
		BaseCoefficient:      &types.Decimal{Value: 2, Exponent: 0},
		AdaptiveStep:         &types.Decimal{Value: 5, Exponent: -2},
	}}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:               7,
		DynamicCoefficientParams: params,
		ConfirmationWeightScales: scales,
	})

	response, err := k.DynamicCoefficients(ctx, &types.QueryDynamicCoefficientsRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(7), response.EpochIndex)
	require.Equal(t, params, response.Params)
	require.Equal(t, scales, response.Coefficients)
}
