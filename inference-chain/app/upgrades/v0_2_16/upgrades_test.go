package v0_2_16

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
}

func TestMigrateDynamicCoefficientParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "model-c", WeightScaleFactor: inferencetypes.DecimalFromFloat(3)},
		{ModelId: "model-a", WeightScaleFactor: inferencetypes.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: inferencetypes.DecimalFromFloat(2)},
		{ModelId: "disabled", WeightScaleFactor: &inferencetypes.Decimal{Value: 0, Exponent: 0}},
	}
	params.DelegationParams.InitialModelId = "model-b"
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.PocParams.DynamicCoefficientParams)
	require.Equal(t, uint32(500), got.PocParams.DynamicCoefficientParams.TargetZoneBps)

	targets := make(map[string]uint32)
	expectedScales := map[string]*inferencetypes.Decimal{
		"model-a": inferencetypes.DecimalFromFloat(1),
		"model-b": inferencetypes.DecimalFromFloat(2),
		"model-c": inferencetypes.DecimalFromFloat(3),
	}
	for _, model := range got.PocParams.Models {
		require.Nil(t, model.WeightScaleFactor)
		if model.ModelId == "disabled" {
			require.Nil(t, model.DynamicCoefficient)
			continue
		}
		require.NotNil(t, model.DynamicCoefficient)
		require.Equal(t, expectedScales[model.ModelId], model.DynamicCoefficient.CoeffMin)
		require.Equal(t, expectedScales[model.ModelId], model.DynamicCoefficient.CoeffMax)
		require.Equal(t, &inferencetypes.Decimal{Value: 1, Exponent: 0}, model.DynamicCoefficient.RelativeDifficulty)
		targets[model.ModelId] = model.DynamicCoefficient.TargetShareBps
	}
	require.Equal(t, uint32(3333), targets["model-a"])
	require.Equal(t, uint32(3334), targets["model-b"])
	require.Equal(t, uint32(3333), targets["model-c"])
	require.NoError(t, got.Validate())

	// The migration is idempotent once the global block exists.
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, got.PocParams, again.PocParams)
}

func TestMigrateDynamicCoefficientParamsRejectsUnrepresentableScale(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: &inferencetypes.Decimal{Value: 1234567890123, Exponent: -13},
	}}
	require.NoError(t, k.SetParams(ctx, params))

	err = migrateDynamicCoefficientParams(ctx, k)
	require.ErrorContains(t, err, "at most 12 fractional decimal places")

	got, getErr := k.GetParams(ctx)
	require.NoError(t, getErr)
	require.Nil(t, got.PocParams.DynamicCoefficientParams)
	require.Equal(t, params.PocParams.Models[0].WeightScaleFactor, got.PocParams.Models[0].WeightScaleFactor)
}

func TestMigrateCurrentEffectiveCoefficients(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ConfirmationWeightScales: []*inferencetypes.ConfirmationWeightScale{{
			ModelId:           "model-a",
			WeightScaleFactor: inferencetypes.DecimalFromFloat(2),
		}},
	})

	require.NoError(t, migrateCurrentEffectiveCoefficients(ctx, k))

	data, found := k.GetEpochGroupData(ctx, 7, "")
	require.True(t, found)
	require.Nil(t, data.ConfirmationWeightScales[0].WeightScaleFactor)
	require.Equal(t, inferencetypes.DecimalFromFloat(2), data.ConfirmationWeightScales[0].EffectiveCoefficient)
}
