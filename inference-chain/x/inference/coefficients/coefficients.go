package coefficients

import (
	"fmt"
	"math/big"
	"slices"
	"strings"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

const bpsDenominator int64 = 10_000

// FrozenConfig is the coefficient config pinned onto an upcoming epoch group
// at PoC start.
type FrozenConfig struct {
	Params *types.DynamicCoefficientParams
	Scales []*types.ConfirmationWeightScale
}

// Result contains the completed epoch scale entries and the effective map used
// by consensus aggregation.
type Result struct {
	Effective     map[string]mathsdk.LegacyDec
	Scales        []*types.ConfirmationWeightScale
	ClampedModels []string
}

// Freeze copies live governance config into deterministic epoch scale entries.
func Freeze(pocParams *types.PocParams) (*FrozenConfig, error) {
	if pocParams == nil {
		return &FrozenConfig{}, nil
	}
	if pocParams.DynamicCoefficientParams == nil {
		scales := make([]*types.ConfirmationWeightScale, 0, len(pocParams.Models))
		for _, model := range pocParams.Models {
			if model == nil || model.ModelId == "" {
				continue
			}
			scales = append(scales, &types.ConfirmationWeightScale{
				ModelId:           model.ModelId,
				WeightScaleFactor: cloneDecimal(model.WeightScaleFactor),
			})
		}
		slices.SortFunc(scales, func(a, b *types.ConfirmationWeightScale) int {
			return strings.Compare(a.ModelId, b.ModelId)
		})
		return &FrozenConfig{Scales: scales}, nil
	}
	scales := make([]*types.ConfirmationWeightScale, 0, len(pocParams.Models))
	for _, model := range pocParams.Models {
		if model == nil || model.ModelId == "" || model.DynamicCoefficient == nil {
			continue
		}
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId: model.ModelId,
			Config:  cloneModelConfig(model.DynamicCoefficient),
		})
	}
	slices.SortFunc(scales, func(a, b *types.ConfirmationWeightScale) int {
		return strings.Compare(a.ModelId, b.ModelId)
	})
	return &FrozenConfig{
		Params: cloneParams(pocParams.DynamicCoefficientParams),
		Scales: scales,
	}, nil
}

// GovernanceCoefficients returns approval coefficients before current PoC
// allocation exists.
func GovernanceCoefficients(pocParams *types.PocParams) map[string]mathsdk.LegacyDec {
	result := make(map[string]mathsdk.LegacyDec)
	if pocParams == nil {
		return result
	}
	for _, model := range pocParams.Models {
		if model == nil || model.ModelId == "" {
			continue
		}
		if pocParams.DynamicCoefficientParams == nil {
			result[model.ModelId] = legacyWeightScaleFactor(model)
			continue
		}
		if model.DynamicCoefficient == nil {
			result[model.ModelId] = mathsdk.LegacyZeroDec()
			continue
		}
		coeff, err := positiveDecimal(
			fmt.Sprintf("coeff_min for model %q", model.ModelId),
			model.DynamicCoefficient.CoeffMin,
		)
		if err != nil {
			result[model.ModelId] = mathsdk.LegacyZeroDec()
		} else {
			result[model.ModelId] = coeff
		}
	}
	return result
}

// Calculate completes the config entries frozen at PoC start using prior
// controller state and raw model totals.
func Calculate(
	params *types.DynamicCoefficientParams,
	frozenScales []*types.ConfirmationWeightScale,
	previousScales []*types.ConfirmationWeightScale,
	previousRawTotals map[string]int64,
	currentRawTotals map[string]int64,
	participantModelIDs []string,
	hasPriorTotals bool,
) (*Result, error) {
	effective := make(map[string]mathsdk.LegacyDec)
	for _, modelID := range participantModelIDs {
		if modelID != "" {
			effective[modelID] = mathsdk.LegacyZeroDec()
		}
	}
	if params == nil {
		scales := cloneScales(frozenScales)
		for _, scale := range scales {
			value := scale.EffectiveCoefficient
			if value == nil {
				value = scale.WeightScaleFactor
			}
			if value == nil {
				value = &types.Decimal{Value: 1, Exponent: 0}
			}
			dec, err := value.ToLegacyDec()
			if err != nil {
				return nil, err
			}
			scale.EffectiveCoefficient = cloneDecimal(value)
			effective[scale.ModelId] = dec
		}
		return &Result{Effective: effective, Scales: scales}, nil
	}

	configs := make(map[string]*types.DynamicCoefficientModelConfig)
	difficulties := make(map[string]mathsdk.LegacyDec)
	for _, scale := range frozenScales {
		if scale == nil || scale.ModelId == "" || scale.Config == nil {
			continue
		}
		difficulty, err := positiveDecimal(
			fmt.Sprintf("relative difficulty for model %q", scale.ModelId),
			scale.Config.RelativeDifficulty,
		)
		if err != nil {
			return nil, err
		}
		configs[scale.ModelId] = scale.Config
		difficulties[scale.ModelId] = difficulty
	}
	previousNormalized, previousTotal := normalizedWeights(previousRawTotals, difficulties)
	currentNormalized, currentTotal := normalizedWeights(currentRawTotals, difficulties)
	previousByModel := scalesByModel(previousScales)

	stepMin, err := positiveDecimal("dynamic step_min", params.StepMin)
	if err != nil {
		return nil, err
	}
	stepMax, err := positiveDecimal("dynamic step_max", params.StepMax)
	if err != nil {
		return nil, err
	}
	bootstrapStepMax, err := positiveDecimal("dynamic bootstrap_step_max", params.BootstrapStepMax)
	if err != nil {
		return nil, err
	}

	modelIDs := make([]string, 0, len(configs))
	for modelID := range configs {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)
	scales := make([]*types.ConfirmationWeightScale, 0, len(modelIDs))
	clampedModels := make([]string, 0)
	for _, modelID := range modelIDs {
		config := configs[modelID]
		coeffMin, err := positiveDecimal("coeff_min for model "+modelID, config.CoeffMin)
		if err != nil {
			return nil, err
		}
		coeffMax, err := positiveDecimal("coeff_max for model "+modelID, config.CoeffMax)
		if err != nil {
			return nil, err
		}
		base, step, prevSign, hadState, err := initialState(
			modelID, config, previousByModel[modelID], stepMax,
		)
		if err != nil {
			return nil, err
		}
		unclamped := base
		base = clamp(base, coeffMin, coeffMax)
		if !base.Equal(unclamped) {
			clampedModels = append(clampedModels, modelID)
		}
		if config.TargetShareBps == 0 {
			base, step, prevSign = coeffMin, stepMax.QuoInt64(2), 0
		} else if hasPriorTotals && hadState {
			base, step, prevSign = adjust(
				base, step, prevSign,
				previousNormalized[modelID], previousTotal,
				config.TargetShareBps, params.TargetZoneBps, params.BootstrapShareBps,
				coeffMin, coeffMax, stepMin, stepMax, bootstrapStepMax,
			)
		}
		baseEncoded, base, err := encodeDecimal(base)
		if err != nil {
			return nil, err
		}
		stepEncoded, step, err := encodeDecimal(step)
		if err != nil {
			return nil, err
		}
		effectiveValue := diluted(
			base, coeffMin, config.TargetShareBps,
			currentNormalized[modelID], currentTotal,
		)
		effectiveEncoded, effectiveValue, err := encodeDecimal(effectiveValue)
		if err != nil {
			return nil, err
		}
		effective[modelID] = effectiveValue
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:              modelID,
			EffectiveCoefficient: effectiveEncoded,
			Config:               cloneModelConfig(config),
			BaseCoefficient:      baseEncoded,
			AdaptiveStep:         stepEncoded,
			PrevSign:             prevSign,
		})
	}
	return &Result{
		Effective: effective, Scales: scales, ClampedModels: clampedModels,
	}, nil
}

// EffectiveForAllocation computes dilution while keeping base state fixed.
func EffectiveForAllocation(
	params *types.DynamicCoefficientParams,
	scales []*types.ConfirmationWeightScale,
	currentRawTotals map[string]int64,
	modelIDs []string,
) (map[string]mathsdk.LegacyDec, error) {
	if params == nil {
		return nil, fmt.Errorf("dynamic coefficient params are required")
	}
	effective := make(map[string]mathsdk.LegacyDec, len(modelIDs))
	for _, modelID := range modelIDs {
		effective[modelID] = mathsdk.LegacyZeroDec()
	}
	difficulties := make(map[string]mathsdk.LegacyDec)
	scaleByModel := scalesByModel(scales)
	for modelID, scale := range scaleByModel {
		if scale.Config == nil {
			continue
		}
		difficulty, err := positiveDecimal(
			"relative difficulty for model "+modelID,
			scale.Config.RelativeDifficulty,
		)
		if err != nil {
			return nil, err
		}
		difficulties[modelID] = difficulty
	}
	normalized, total := normalizedWeights(currentRawTotals, difficulties)
	for modelID, scale := range scaleByModel {
		if scale.Config == nil {
			continue
		}
		base, err := positiveDecimal("base coefficient for model "+modelID, scale.BaseCoefficient)
		if err != nil {
			return nil, err
		}
		coeffMin, err := positiveDecimal("coeff_min for model "+modelID, scale.Config.CoeffMin)
		if err != nil {
			return nil, err
		}
		value := diluted(base, coeffMin, scale.Config.TargetShareBps, normalized[modelID], total)
		_, value, err = encodeDecimal(value)
		if err != nil {
			return nil, err
		}
		effective[modelID] = value
	}
	return effective, nil
}

func normalizedWeights(
	rawTotals map[string]int64,
	difficulties map[string]mathsdk.LegacyDec,
) (map[string]mathsdk.LegacyDec, mathsdk.LegacyDec) {
	normalized := make(map[string]mathsdk.LegacyDec, len(difficulties))
	total := mathsdk.LegacyZeroDec()
	for modelID, difficulty := range difficulties {
		raw := rawTotals[modelID]
		if raw < 0 {
			raw = 0
		}
		value := difficulty.MulInt64(raw)
		normalized[modelID] = value
		total = total.Add(value)
	}
	return normalized, total
}

func initialState(
	modelID string,
	config *types.DynamicCoefficientModelConfig,
	previous *types.ConfirmationWeightScale,
	stepMax mathsdk.LegacyDec,
) (mathsdk.LegacyDec, mathsdk.LegacyDec, int32, bool, error) {
	if previous != nil && previous.BaseCoefficient != nil && previous.AdaptiveStep != nil {
		base, err := positiveDecimal(
			fmt.Sprintf("previous base coefficient for model %q", modelID),
			previous.BaseCoefficient,
		)
		if err != nil {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false, err
		}
		step, err := positiveDecimal(
			fmt.Sprintf("previous adaptive step for model %q", modelID),
			previous.AdaptiveStep,
		)
		if err != nil {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false, err
		}
		if previous.PrevSign < -1 || previous.PrevSign > 1 {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false,
				fmt.Errorf("previous sign for model %q must be -1, 0, or 1", modelID)
		}
		return base, step, previous.PrevSign, true, nil
	}
	base, err := positiveDecimal(
		fmt.Sprintf("coeff_min for new model %q", modelID),
		config.CoeffMin,
	)
	return base, stepMax.QuoInt64(2), 0, false, err
}

func scalesByModel(scales []*types.ConfirmationWeightScale) map[string]*types.ConfirmationWeightScale {
	result := make(map[string]*types.ConfirmationWeightScale)
	for _, scale := range scales {
		if scale != nil && scale.ModelId != "" {
			result[scale.ModelId] = scale
		}
	}
	return result
}

func cloneScales(scales []*types.ConfirmationWeightScale) []*types.ConfirmationWeightScale {
	result := make([]*types.ConfirmationWeightScale, 0, len(scales))
	for _, scale := range scales {
		if scale == nil {
			continue
		}
		result = append(result, &types.ConfirmationWeightScale{
			ModelId:                 scale.ModelId,
			WeightScaleFactor:       cloneDecimal(scale.WeightScaleFactor),
			EffectiveCoefficient:    cloneDecimal(scale.EffectiveCoefficient),
			Config:                  cloneModelConfig(scale.Config),
			BaseCoefficient:         cloneDecimal(scale.BaseCoefficient),
			AdaptiveStep:            cloneDecimal(scale.AdaptiveStep),
			PrevSign:                scale.PrevSign,
			ExcludeFromConfirmation: scale.ExcludeFromConfirmation,
		})
	}
	return result
}

func adjust(
	base mathsdk.LegacyDec,
	step mathsdk.LegacyDec,
	prevSign int32,
	normalized mathsdk.LegacyDec,
	total mathsdk.LegacyDec,
	targetBPS uint32,
	zoneBPS uint32,
	bootstrapShareBPS uint32,
	coeffMin mathsdk.LegacyDec,
	coeffMax mathsdk.LegacyDec,
	stepMin mathsdk.LegacyDec,
	stepMax mathsdk.LegacyDec,
	bootstrapStepMax mathsdk.LegacyDec,
) (mathsdk.LegacyDec, mathsdk.LegacyDec, int32) {
	if total.IsZero() {
		normalized = mathsdk.LegacyZeroDec()
	}
	scaledShare := normalized.MulInt64(bpsDenominator)
	lower := int64(targetBPS) - int64(zoneBPS)
	upper := int64(targetBPS) + int64(zoneBPS)
	if !total.IsZero() &&
		!scaledShare.LT(total.MulInt64(lower)) &&
		!scaledShare.GT(total.MulInt64(upper)) {
		return base, stepMax.QuoInt64(2), 0
	}

	sign := int32(1)
	if !total.IsZero() && scaledShare.GT(total.MulInt64(int64(targetBPS))) {
		sign = -1
	}
	cap := stepMax
	if total.IsZero() || scaledShare.LT(total.MulInt64(int64(bootstrapShareBPS))) {
		cap = bootstrapStepMax
	}
	if prevSign == 0 || prevSign == sign {
		step = min(step.MulInt64(2), cap)
	} else {
		step = min(max(step.QuoInt64(2), stepMin), cap)
	}

	multiplier := mathsdk.LegacyOneDec()
	if sign > 0 {
		multiplier = multiplier.Add(step)
	} else {
		multiplier = multiplier.Sub(step)
	}
	return clamp(base.Mul(multiplier), coeffMin, coeffMax), step, sign
}

func diluted(
	base mathsdk.LegacyDec,
	coeffMin mathsdk.LegacyDec,
	targetBPS uint32,
	normalized mathsdk.LegacyDec,
	total mathsdk.LegacyDec,
) mathsdk.LegacyDec {
	if total.IsZero() || normalized.IsZero() {
		return base
	}
	scaledShare := normalized.MulInt64(bpsDenominator)
	targetWeight := total.MulInt64(int64(targetBPS))
	if !scaledShare.GT(targetWeight) {
		return base
	}
	return coeffMin.Add(base.Sub(coeffMin).Mul(targetWeight.Quo(scaledShare)))
}

func clamp(value, lower, upper mathsdk.LegacyDec) mathsdk.LegacyDec {
	if value.LT(lower) {
		return lower
	}
	if value.GT(upper) {
		return upper
	}
	return value
}

func min(a, b mathsdk.LegacyDec) mathsdk.LegacyDec {
	if a.LT(b) {
		return a
	}
	return b
}

func max(a, b mathsdk.LegacyDec) mathsdk.LegacyDec {
	if a.GT(b) {
		return a
	}
	return b
}

func positiveDecimal(name string, value *types.Decimal) (mathsdk.LegacyDec, error) {
	if value == nil {
		return mathsdk.LegacyDec{}, fmt.Errorf("%s cannot be nil", name)
	}
	dec, err := value.ToLegacyDec()
	if err != nil {
		return mathsdk.LegacyDec{}, fmt.Errorf("%s: %w", name, err)
	}
	if !dec.IsPositive() {
		return mathsdk.LegacyDec{}, fmt.Errorf("%s must be positive", name)
	}
	return dec, nil
}

func encodeDecimal(value mathsdk.LegacyDec) (*types.Decimal, mathsdk.LegacyDec, error) {
	if value.IsNegative() {
		return nil, mathsdk.LegacyDec{}, fmt.Errorf("value cannot be negative")
	}
	if value.IsZero() {
		return &types.Decimal{Value: 0, Exponent: 0}, mathsdk.LegacyZeroDec(), nil
	}

	parts := strings.SplitN(value.String(), ".", 2)
	digits := parts[0]
	exponent := int32(0)
	if len(parts) == 2 {
		digits += parts[1]
		exponent = -int32(len(parts[1]))
	}
	coefficient, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, mathsdk.LegacyDec{}, fmt.Errorf("cannot encode decimal %q", value.String())
	}
	ten := big.NewInt(10)
	remainder := new(big.Int)
	for exponent < 0 {
		remainder.Mod(coefficient, ten)
		if remainder.Sign() != 0 {
			break
		}
		coefficient.Quo(coefficient, ten)
		exponent++
	}
	for !coefficient.IsInt64() {
		coefficient.Quo(coefficient, ten)
		exponent++
	}
	if exponent > types.MaxDecimalExponentAbs {
		return nil, mathsdk.LegacyDec{}, fmt.Errorf("decimal %s exceeds storage range", value.String())
	}
	encoded := &types.Decimal{Value: coefficient.Int64(), Exponent: exponent}
	quantized, err := encoded.ToLegacyDec()
	if err != nil {
		return nil, mathsdk.LegacyDec{}, err
	}
	return encoded, quantized, nil
}

func legacyWeightScaleFactor(model *types.PoCModelConfig) mathsdk.LegacyDec {
	if model == nil || model.WeightScaleFactor == nil {
		return mathsdk.LegacyOneDec()
	}
	dec, err := model.WeightScaleFactor.ToLegacyDec()
	if err != nil {
		return mathsdk.LegacyOneDec()
	}
	return dec
}

func cloneParams(params *types.DynamicCoefficientParams) *types.DynamicCoefficientParams {
	if params == nil {
		return nil
	}
	return &types.DynamicCoefficientParams{
		TargetZoneBps:     params.TargetZoneBps,
		StepMin:           cloneDecimal(params.StepMin),
		StepMax:           cloneDecimal(params.StepMax),
		BootstrapStepMax:  cloneDecimal(params.BootstrapStepMax),
		BootstrapShareBps: params.BootstrapShareBps,
	}
}

func cloneModelConfig(config *types.DynamicCoefficientModelConfig) *types.DynamicCoefficientModelConfig {
	if config == nil {
		return nil
	}
	return &types.DynamicCoefficientModelConfig{
		CoeffMin:           cloneDecimal(config.CoeffMin),
		CoeffMax:           cloneDecimal(config.CoeffMax),
		RelativeDifficulty: cloneDecimal(config.RelativeDifficulty),
		TargetShareBps:     config.TargetShareBps,
	}
}

func cloneDecimal(value *types.Decimal) *types.Decimal {
	if value == nil {
		return nil
	}
	return &types.Decimal{Value: value.Value, Exponent: value.Exponent}
}
