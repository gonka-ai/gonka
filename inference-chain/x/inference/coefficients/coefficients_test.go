package coefficients

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func dec(value int64, exponent int32) *types.Decimal {
	return &types.Decimal{Value: value, Exponent: exponent}
}

func model(
	id string,
	minValue *types.Decimal,
	maxValue *types.Decimal,
	difficulty *types.Decimal,
	target uint32,
) *types.PoCModelConfig {
	return &types.PoCModelConfig{
		ModelId: id,
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           minValue,
			CoeffMax:           maxValue,
			RelativeDifficulty: difficulty,
			TargetShareBps:     target,
		},
	}
}

func params(models ...*types.PoCModelConfig) *types.PocParams {
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

func state(modelID string, base, step *types.Decimal, sign int32) *types.ConfirmationWeightScale {
	return &types.ConfirmationWeightScale{
		ModelId:         modelID,
		BaseCoefficient: base,
		AdaptiveStep:    step,
		PrevSign:        sign,
	}
}

func stateMap(scales []*types.ConfirmationWeightScale) map[string]*types.ConfirmationWeightScale {
	return scalesByModel(scales)
}

func calculateForTest(
	config *types.PocParams,
	previous []*types.ConfirmationWeightScale,
	previousRaw map[string]int64,
	currentRaw map[string]int64,
	modelIDs []string,
	hasPrior bool,
) (*Result, error) {
	frozen, err := Freeze(config)
	if err != nil {
		return nil, err
	}
	return Calculate(
		frozen.Params, frozen.Scales, previous,
		previousRaw, currentRaw, modelIDs, hasPrior,
	)
}

func TestGovernanceCoefficients(t *testing.T) {
	config := params(
		model("enabled", dec(2, 0), dec(3, 0), dec(1, 0), 10000),
		&types.PoCModelConfig{ModelId: "disabled", WeightScaleFactor: dec(9, 0)},
	)
	config.Models[0].WeightScaleFactor = dec(9, 0)
	coefficients := GovernanceCoefficients(config)
	require.Equal(t, "2.000000000000000000", coefficients["enabled"].String())
	require.True(t, coefficients["disabled"].IsZero())
}

func TestLegacyCalculateCarriesExactEffectiveEncoding(t *testing.T) {
	legacy := &types.PocParams{Models: []*types.PoCModelConfig{{
		ModelId:           "legacy",
		WeightScaleFactor: dec(1234567890123, -13),
	}}}
	frozen, err := Freeze(legacy)
	require.NoError(t, err)
	result, err := Calculate(
		frozen.Params,
		frozen.Scales,
		nil,
		nil,
		map[string]int64{"legacy": 100},
		[]string{"legacy"},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "0.123456789012300000", result.Effective["legacy"].String())
	require.Equal(t, dec(1234567890123, -13), result.Scales[0].EffectiveCoefficient)
}

func TestTransitionScaleWithoutControllerStateSeedsMinimum(t *testing.T) {
	config := params(model("a", dec(5, -1), dec(2, 0), dec(1, 0), 10000))
	frozen, err := Freeze(config)
	require.NoError(t, err)
	previous := []*types.ConfirmationWeightScale{{
		ModelId:           "a",
		WeightScaleFactor: dec(15, -1),
	}}
	result, err := Calculate(
		frozen.Params,
		frozen.Scales,
		previous,
		map[string]int64{"a": 100},
		map[string]int64{"a": 100},
		[]string{"a"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, dec(5, -1), result.Scales[0].BaseCoefficient)
}

func TestCalculateUsesConfigFrozenAtPoCStart(t *testing.T) {
	live := params(model("a", dec(5, -1), dec(2, 0), dec(1, 0), 10000))
	frozen, err := Freeze(live)
	require.NoError(t, err)
	live.Models[0].DynamicCoefficient.CoeffMin = dec(15, -1)
	live.Models[0].DynamicCoefficient.TargetShareBps = 9000

	result, err := Calculate(
		frozen.Params,
		frozen.Scales,
		nil,
		nil,
		map[string]int64{"a": 100},
		[]string{"a"},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, dec(5, -1), result.Scales[0].BaseCoefficient)
	require.Equal(t, uint32(10000), result.Scales[0].Config.TargetShareBps)
}

func TestCalculateAdjustsAndDilutes(t *testing.T) {
	config := params(
		model("under", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		model("over", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := []*types.ConfirmationWeightScale{
		state("under", dec(1, 0), dec(25, -3), 0),
		state("over", dec(1, 0), dec(25, -3), 0),
	}
	result, err := calculateForTest(
		config,
		previous,
		map[string]int64{"under": 100, "over": 900},
		map[string]int64{"under": 100, "over": 900},
		[]string{"under", "over", "unknown"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "1.050000000000000000", result.Effective["under"].String())
	require.Equal(t, "0.750000000000000000", result.Effective["over"].String())
	require.True(t, result.Effective["unknown"].IsZero())
	require.Equal(t, "over", result.Scales[0].ModelId)
	require.Equal(t, "under", result.Scales[1].ModelId)
}

func TestCalculateUsesRelativeDifficulty(t *testing.T) {
	config := params(
		model("hard", dec(5, -1), dec(2, 0), dec(2, 0), 5000),
		model("base", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := []*types.ConfirmationWeightScale{
		state("hard", dec(1, 0), dec(25, -3), 0),
		state("base", dec(1, 0), dec(25, -3), 0),
	}
	result, err := calculateForTest(
		config,
		previous,
		map[string]int64{"hard": 100, "base": 100},
		map[string]int64{"hard": 100, "base": 100},
		[]string{"hard", "base"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "0.837500000000000000", result.Effective["hard"].String())
	require.Equal(t, "1.050000000000000000", result.Effective["base"].String())
}

func TestNewModelStartsAtMinimum(t *testing.T) {
	config := params(
		model("new", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		model("existing", dec(1, 0), dec(1, 0), dec(1, 0), 5000),
	)
	previous := []*types.ConfirmationWeightScale{
		state("existing", dec(1, 0), dec(25, -3), 0),
	}
	result, err := calculateForTest(
		config,
		previous,
		map[string]int64{"existing": 100},
		map[string]int64{"existing": 100},
		[]string{"new", "existing"},
		true,
	)
	require.NoError(t, err)
	newState := stateMap(result.Scales)["new"]
	require.Equal(t, dec(5, -1), newState.BaseCoefficient)
	require.Equal(t, dec(25, -3), newState.AdaptiveStep)
	require.Zero(t, newState.PrevSign)
}

func TestZeroTargetResetsState(t *testing.T) {
	config := params(
		model("zero", dec(5, -1), dec(2, 0), dec(1, 0), 0),
		model("all", dec(1, 0), dec(1, 0), dec(1, 0), 10000),
	)
	previous := []*types.ConfirmationWeightScale{
		state("zero", dec(15, -1), dec(2, -1), -1),
		state("all", dec(1, 0), dec(25, -3), 0),
	}
	result, err := calculateForTest(
		config,
		previous,
		map[string]int64{"zero": 500, "all": 500},
		map[string]int64{"zero": 500, "all": 500},
		[]string{"zero", "all"},
		true,
	)
	require.NoError(t, err)
	zero := stateMap(result.Scales)["zero"]
	require.Equal(t, dec(5, -1), zero.BaseCoefficient)
	require.Equal(t, dec(25, -3), zero.AdaptiveStep)
	require.Zero(t, zero.PrevSign)
}

func TestPinnedBoundsPreserveCoefficient(t *testing.T) {
	config := params(
		model("a", dec(2, 0), dec(2, 0), dec(1, 0), 5000),
		model("b", dec(3, 0), dec(3, 0), dec(1, 0), 5000),
	)
	result, err := calculateForTest(
		config,
		nil,
		map[string]int64{"a": 1, "b": 999},
		map[string]int64{"a": 1, "b": 999},
		[]string{"a", "b"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "2.000000000000000000", result.Effective["a"].String())
	require.Equal(t, "3.000000000000000000", result.Effective["b"].String())
}

func TestDeadbandBoundariesAreInclusive(t *testing.T) {
	for _, normalized := range []int64{4500, 5500} {
		base, step, sign := adjust(
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

func TestEncodeDecimalPreservesMaximumPrecision(t *testing.T) {
	encoded, value, err := encodeDecimal(mathsdk.LegacyMustNewDecFromStr("1.123456789012999999"))
	require.NoError(t, err)
	require.Equal(t, &types.Decimal{Value: 1123456789012999999, Exponent: -18}, encoded)
	require.Equal(t, "1.123456789012999999", value.String())

	encoded, value, err = encodeDecimal(mathsdk.LegacyMustNewDecFromStr("12.123456789012345678"))
	require.NoError(t, err)
	require.Equal(t, &types.Decimal{Value: 1212345678901234567, Exponent: -17}, encoded)
	require.Equal(t, "12.123456789012345670", value.String())
}
