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
	snapshot := &types.DynamicCoefficientEpochSnapshot{
		Models: []*types.DynamicCoefficientModelState{{
			ModelId:         "model-a",
			BaseCoefficient: &types.Decimal{Value: 2, Exponent: 0},
			AdaptiveStep:    &types.Decimal{Value: 5, Exponent: -2},
		}},
	}
	scales := []*types.ConfirmationWeightScale{{
		ModelId:              "model-a",
		EffectiveCoefficient: &types.Decimal{Value: 2, Exponent: 0},
	}}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:                 7,
		DynamicCoefficientSnapshot: snapshot,
		ConfirmationWeightScales:   scales,
	})

	response, err := k.DynamicCoefficients(ctx, &types.QueryDynamicCoefficientsRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(7), response.EpochIndex)
	require.Equal(t, snapshot, response.Snapshot)
	require.Equal(t, scales, response.EffectiveCoefficients)
}
