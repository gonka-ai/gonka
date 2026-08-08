package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func dec(value int64, exponent int32) *types.Decimal {
	return &types.Decimal{Value: value, Exponent: exponent}
}

func dynamicModel(
	id string,
	weightScale *types.Decimal,
	min *types.Decimal,
	max *types.Decimal,
	difficulty *types.Decimal,
	target uint32,
) *types.PoCModelConfig {
	return &types.PoCModelConfig{
		ModelId:           id,
		WeightScaleFactor: weightScale,
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           min,
			CoeffMax:           max,
			RelativeDifficulty: difficulty,
			TargetShareBps:     target,
		},
	}
}

func dynamicPocParams(models ...*types.PoCModelConfig) *types.PocParams {
	return &types.PocParams{
		Models: models,
		DynamicCoefficientParams: &types.DynamicCoefficientParams{
			TargetZoneBps:     500,
			StepMin:           dec(5, -3),
			StepMax:           dec(5, -2),
			BootstrapStepMax:  dec(25, -2),
			BootstrapShareBps: 100,
		},
	}
}

func TestModelCoefficients(t *testing.T) {
	t.Run("nil params", func(t *testing.T) {
		require.Empty(t, modelCoefficients(nil))
	})

	t.Run("extracts weight scale factors including zero", func(t *testing.T) {
		params := &types.PocParams{
			Models: []*types.PoCModelConfig{
				{ModelId: "model-a", WeightScaleFactor: dec(1, 0)},
				{ModelId: "model-b", WeightScaleFactor: dec(2, 0)},
				{ModelId: "disabled", WeightScaleFactor: dec(0, 0)},
			},
		}
		coeffs := modelCoefficients(params)
		require.Len(t, coeffs, 3)
		require.True(t, coeffs["model-a"].Equal(mathsdk.LegacyOneDec()))
		require.True(t, coeffs["model-b"].Equal(mathsdk.LegacyNewDec(2)))
		require.True(t, coeffs["disabled"].IsZero())
	})

	t.Run("dynamic mode ignores deprecated weight scale factors", func(t *testing.T) {
		params := dynamicPocParams(
			dynamicModel("enabled", dec(9, 0), dec(2, 0), dec(3, 0), dec(1, 0), 10000),
			&types.PoCModelConfig{ModelId: "disabled", WeightScaleFactor: dec(9, 0)},
		)
		coeffs := modelCoefficients(params)
		require.Equal(t, "2.000000000000000000", coeffs["enabled"].String())
		require.True(t, coeffs["disabled"].IsZero())
	})
}

func TestCalculateEpochCoefficients_AdjustsAndDilutes(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("under", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		dynamicModel("over", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{
		{ModelId: "under", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
		{ModelId: "over", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
	}}

	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"under": 100, "over": 900},
		map[string]int64{"under": 100, "over": 900},
		[]string{"under", "over", "unknown"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "1.050000000000000000", result.effective["under"].String())
	require.Equal(t, "0.750000000000000000", result.effective["over"].String())
	require.True(t, result.effective["unknown"].IsZero())
	require.Len(t, result.snapshot.Models, 2)
	require.Equal(t, "over", result.snapshot.Models[0].ModelId)
	require.Equal(t, "under", result.snapshot.Models[1].ModelId)
	require.Equal(t, int32(1), result.snapshot.Models[1].PrevSign)
	require.Equal(t, int32(-1), result.snapshot.Models[0].PrevSign)
	require.Equal(t, dec(5, -2), result.snapshot.Models[0].AdaptiveStep)
}

func TestCalculateEpochCoefficients_UsesRelativeDifficultyForBothShares(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("hard", dec(1, 0), dec(5, -1), dec(2, 0), dec(2, 0), 5000),
		dynamicModel("base", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{
		{ModelId: "hard", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
		{ModelId: "base", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
	}}
	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"hard": 100, "base": 100},
		map[string]int64{"hard": 100, "base": 100},
		[]string{"hard", "base"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "0.837500000000000000", result.effective["hard"].String())
	require.Equal(t, "1.050000000000000000", result.effective["base"].String())
}

func TestCalculateEpochCoefficients_DeadbandAndSignFlip(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("a", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		dynamicModel("b", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{
		Models: []*types.DynamicCoefficientModelState{
			{ModelId: "a", BaseCoefficient: dec(12, -1), AdaptiveStep: dec(5, -2), PrevSign: -1},
			{ModelId: "b", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(1, -1), PrevSign: 1},
		},
	}

	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"a": 400, "b": 600},
		map[string]int64{"a": 500, "b": 500},
		[]string{"a", "b"},
		true,
	)
	require.NoError(t, err)
	states := coefficientStates(result.snapshot)
	require.Equal(t, dec(123, -2), states["a"].BaseCoefficient)
	require.Equal(t, dec(25, -3), states["a"].AdaptiveStep)
	require.Equal(t, int32(1), states["a"].PrevSign)
	require.Equal(t, dec(95, -2), states["b"].BaseCoefficient)
	require.Equal(t, dec(5, -2), states["b"].AdaptiveStep)
	require.Equal(t, int32(-1), states["b"].PrevSign)
}

func TestCalculateEpochCoefficients_GenesisSkipsAdjustment(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("a", dec(12, -1), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		dynamicModel("b", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	result, err := calculateEpochCoefficients(
		params,
		nil,
		nil,
		map[string]int64{"a": 0, "b": 100},
		[]string{"a", "b"},
		false,
	)
	require.NoError(t, err)
	states := coefficientStates(result.snapshot)
	require.Equal(t, dec(5, -1), states["a"].BaseCoefficient)
	require.Equal(t, int32(0), states["a"].PrevSign)
	require.Equal(t, dec(25, -3), states["a"].AdaptiveStep)
}

func TestCalculateEpochCoefficients_NewModelStartsAtMinimum(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("new", dec(15, -1), dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		dynamicModel("existing", dec(1, 0), dec(1, 0), dec(1, 0), dec(1, 0), 5000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{{
		ModelId:         "existing",
		BaseCoefficient: dec(1, 0),
		AdaptiveStep:    dec(25, -3),
	}}}
	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"new": 0, "existing": 100},
		map[string]int64{"new": 0, "existing": 100},
		[]string{"new", "existing"},
		true,
	)
	require.NoError(t, err)
	state := coefficientStates(result.snapshot)["new"]
	require.Equal(t, dec(5, -1), state.BaseCoefficient)
	require.Equal(t, dec(25, -3), state.AdaptiveStep)
	require.Zero(t, state.PrevSign)
}

func TestCalculateEpochCoefficients_DropsDisabledStateAndReturnsZero(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("enabled", dec(1, 0), dec(1, 0), dec(1, 0), dec(1, 0), 10000),
		&types.PoCModelConfig{
			ModelId:           "disabled",
			WeightScaleFactor: dec(0, 0),
		},
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{
		{ModelId: "enabled", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
		{ModelId: "disabled", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(25, -3)},
	}}
	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"enabled": 100, "disabled": 100},
		map[string]int64{"enabled": 100, "disabled": 100},
		[]string{"enabled", "disabled"},
		true,
	)
	require.NoError(t, err)
	require.True(t, result.effective["disabled"].IsZero())
	require.Nil(t, coefficientStates(result.snapshot)["disabled"])
}

func TestCalculateEpochCoefficients_PinnedBoundsPreserveStaticWeight(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("a", dec(2, 0), dec(2, 0), dec(2, 0), dec(1, 0), 5000),
		dynamicModel("b", dec(3, 0), dec(3, 0), dec(3, 0), dec(1, 0), 5000),
	)
	result, err := calculateEpochCoefficients(
		params,
		nil,
		map[string]int64{"a": 1, "b": 999},
		map[string]int64{"a": 1, "b": 999},
		[]string{"a", "b"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "2.000000000000000000", result.effective["a"].String())
	require.Equal(t, "3.000000000000000000", result.effective["b"].String())
}

func TestCalculateEpochCoefficients_ZeroTargetResetsState(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("zero", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 0),
		dynamicModel("all", dec(1, 0), dec(1, 0), dec(1, 0), dec(1, 0), 10000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{{
		ModelId:         "zero",
		BaseCoefficient: dec(15, -1),
		AdaptiveStep:    dec(2, -1),
		PrevSign:        -1,
	}}}
	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"zero": 500, "all": 500},
		map[string]int64{"zero": 500, "all": 500},
		[]string{"zero", "all"},
		true,
	)
	require.NoError(t, err)
	state := coefficientStates(result.snapshot)["zero"]
	require.Equal(t, dec(5, -1), state.BaseCoefficient)
	require.Equal(t, dec(25, -3), state.AdaptiveStep)
	require.Zero(t, state.PrevSign)
	require.Equal(t, "0.500000000000000000", result.effective["zero"].String())
}

func TestCalculateEpochCoefficients_ClampsInsideDeadband(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("a", dec(1, 0), dec(8, -1), dec(11, -1), dec(1, 0), 5000),
		dynamicModel("b", dec(1, 0), dec(1, 0), dec(1, 0), dec(1, 0), 5000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{
		{ModelId: "a", BaseCoefficient: dec(12, -1), AdaptiveStep: dec(5, -2), PrevSign: 1},
		{ModelId: "b", BaseCoefficient: dec(1, 0), AdaptiveStep: dec(5, -2), PrevSign: 0},
	}}
	result, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"a": 500, "b": 500},
		map[string]int64{"a": 500, "b": 500},
		[]string{"a", "b"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, dec(11, -1), coefficientStates(result.snapshot)["a"].BaseCoefficient)
	require.Equal(t, []string{"a"}, result.clampedModels)
}

func TestQuantizeCoefficient(t *testing.T) {
	value := mathsdk.LegacyMustNewDecFromStr("1.123456789012999999")
	encoded, quantized, err := quantizeCoefficient(value)
	require.NoError(t, err)
	require.Equal(t, &types.Decimal{Value: 1123456789012, Exponent: -12}, encoded)
	require.Equal(t, "1.123456789012000000", quantized.String())
}

func TestCalculateEpochCoefficientsRejectsNilPreviousState(t *testing.T) {
	params := dynamicPocParams(
		dynamicModel("a", dec(1, 0), dec(5, -1), dec(2, 0), dec(1, 0), 10000),
	)
	previous := &types.DynamicCoefficientEpochSnapshot{Models: []*types.DynamicCoefficientModelState{{
		ModelId: "a",
	}}}
	_, err := calculateEpochCoefficients(
		params,
		previous,
		map[string]int64{"a": 100},
		map[string]int64{"a": 100},
		[]string{"a"},
		true,
	)
	require.ErrorContains(t, err, "previous base coefficient")
}

func TestAdjustBaseCoefficient_DeadbandBoundariesAreInclusive(t *testing.T) {
	for _, normalized := range []int64{4500, 5500} {
		base, step, sign := adjustBaseCoefficient(
			mathsdk.LegacyOneDec(),
			mathsdk.LegacyMustNewDecFromStr("0.1"),
			1,
			mathsdk.LegacyNewDec(normalized),
			mathsdk.LegacyNewDec(10000),
			5000,
			500,
			100,
			mathsdk.LegacyMustNewDecFromStr("0.5"),
			mathsdk.LegacyMustNewDecFromStr("2"),
			mathsdk.LegacyMustNewDecFromStr("0.005"),
			mathsdk.LegacyMustNewDecFromStr("0.05"),
			mathsdk.LegacyMustNewDecFromStr("0.25"),
		)
		require.True(t, base.Equal(mathsdk.LegacyOneDec()))
		require.Equal(t, "0.025000000000000000", step.String())
		require.Zero(t, sign)
	}
}

func TestAdjustBaseCoefficient_BootstrapThresholdIsStrict(t *testing.T) {
	_, atThreshold, _ := adjustBaseCoefficient(
		mathsdk.LegacyOneDec(),
		mathsdk.LegacyMustNewDecFromStr("0.05"),
		1,
		mathsdk.LegacyNewDec(100),
		mathsdk.LegacyNewDec(10000),
		5000,
		500,
		100,
		mathsdk.LegacyMustNewDecFromStr("0.5"),
		mathsdk.LegacyMustNewDecFromStr("2"),
		mathsdk.LegacyMustNewDecFromStr("0.005"),
		mathsdk.LegacyMustNewDecFromStr("0.05"),
		mathsdk.LegacyMustNewDecFromStr("0.25"),
	)
	_, belowThreshold, _ := adjustBaseCoefficient(
		mathsdk.LegacyOneDec(),
		mathsdk.LegacyMustNewDecFromStr("0.05"),
		1,
		mathsdk.LegacyNewDec(99),
		mathsdk.LegacyNewDec(10000),
		5000,
		500,
		100,
		mathsdk.LegacyMustNewDecFromStr("0.5"),
		mathsdk.LegacyMustNewDecFromStr("2"),
		mathsdk.LegacyMustNewDecFromStr("0.005"),
		mathsdk.LegacyMustNewDecFromStr("0.05"),
		mathsdk.LegacyMustNewDecFromStr("0.25"),
	)
	require.Equal(t, "0.050000000000000000", atThreshold.String())
	require.Equal(t, "0.100000000000000000", belowThreshold.String())
}
