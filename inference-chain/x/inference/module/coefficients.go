package inference

import (
	"fmt"
	"slices"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

const (
	coefficientBPSDenominator int64 = 10_000
	coefficientDecimalScale   int64 = 1_000_000_000_000
)

type epochCoefficientResult struct {
	effective        map[string]mathsdk.LegacyDec
	effectiveDecimal map[string]*types.Decimal
	snapshot         *types.DynamicCoefficientEpochSnapshot
	clampedModels    []string
}

func modelCoefficients(pocParams *types.PocParams) map[string]mathsdk.LegacyDec {
	coeffs := make(map[string]mathsdk.LegacyDec)
	if pocParams == nil {
		return coeffs
	}
	for _, config := range pocParams.GetModelConfigs() {
		if config != nil && config.ModelId != "" {
			if pocParams.DynamicCoefficientParams == nil {
				coeffs[config.ModelId] = legacyWeightScaleFactor(config)
			} else if config.DynamicCoefficient == nil {
				coeffs[config.ModelId] = mathsdk.LegacyZeroDec()
			} else {
				coeff, err := coefficientDecimal(
					fmt.Sprintf("coeff_min for model %q", config.ModelId),
					config.DynamicCoefficient.CoeffMin,
				)
				if err != nil {
					coeffs[config.ModelId] = mathsdk.LegacyZeroDec()
				} else {
					coeffs[config.ModelId] = coeff
				}
			}
		}
	}
	return coeffs
}

func calculateEpochCoefficients(
	pocParams *types.PocParams,
	previousSnapshot *types.DynamicCoefficientEpochSnapshot,
	previousRawTotals map[string]int64,
	currentRawTotals map[string]int64,
	participantModelIDs []string,
	hasPriorTotals bool,
) (*epochCoefficientResult, error) {
	if pocParams == nil || pocParams.DynamicCoefficientParams == nil {
		return &epochCoefficientResult{effective: modelCoefficients(pocParams)}, nil
	}

	effective := make(map[string]mathsdk.LegacyDec)
	effectiveDecimal := make(map[string]*types.Decimal)
	for _, modelID := range participantModelIDs {
		if modelID != "" {
			effective[modelID] = mathsdk.LegacyZeroDec()
		}
	}

	enabledConfigs := make(map[string]*types.PoCModelConfig)
	difficulties := make(map[string]mathsdk.LegacyDec)
	for _, model := range pocParams.Models {
		if model == nil || model.ModelId == "" {
			continue
		}
		if model.DynamicCoefficient == nil {
			effective[model.ModelId] = mathsdk.LegacyZeroDec()
			continue
		}
		difficulty, err := coefficientDecimal(
			fmt.Sprintf("relative difficulty for model %q", model.ModelId),
			model.DynamicCoefficient.RelativeDifficulty,
		)
		if err != nil {
			return nil, err
		}
		enabledConfigs[model.ModelId] = model
		difficulties[model.ModelId] = difficulty
	}

	previousNormalized, previousNormalizedTotal := normalizedModelWeights(previousRawTotals, difficulties)
	currentNormalized, currentNormalizedTotal := normalizedModelWeights(currentRawTotals, difficulties)
	previousStates := coefficientStates(previousSnapshot)

	params := pocParams.DynamicCoefficientParams
	stepMin, err := coefficientDecimal("dynamic step_min", params.StepMin)
	if err != nil {
		return nil, err
	}
	stepMax, err := coefficientDecimal("dynamic step_max", params.StepMax)
	if err != nil {
		return nil, err
	}
	bootstrapStepMax, err := coefficientDecimal("dynamic bootstrap_step_max", params.BootstrapStepMax)
	if err != nil {
		return nil, err
	}

	modelIDs := make([]string, 0, len(enabledConfigs))
	for modelID := range enabledConfigs {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	nextStates := make([]*types.DynamicCoefficientModelState, 0, len(modelIDs))
	clampedModels := make([]string, 0)
	for _, modelID := range modelIDs {
		model := enabledConfigs[modelID]
		config := model.DynamicCoefficient
		coeffMin, err := coefficientDecimal(
			fmt.Sprintf("coeff_min for model %q", modelID),
			config.CoeffMin,
		)
		if err != nil {
			return nil, err
		}
		coeffMax, err := coefficientDecimal(
			fmt.Sprintf("coeff_max for model %q", modelID),
			config.CoeffMax,
		)
		if err != nil {
			return nil, err
		}

		base, step, prevSign, hadState, err := initialCoefficientState(
			model,
			previousStates[modelID],
			stepMax,
		)
		if err != nil {
			return nil, err
		}
		unclampedBase := base
		base = clampCoefficient(base, coeffMin, coeffMax)
		if !base.Equal(unclampedBase) {
			clampedModels = append(clampedModels, modelID)
		}

		if config.TargetShareBps == 0 {
			base = coeffMin
			step = stepMax.QuoInt64(2)
			prevSign = 0
		} else if hasPriorTotals && hadState {
			base, step, prevSign = adjustBaseCoefficient(
				base,
				step,
				prevSign,
				previousNormalized[modelID],
				previousNormalizedTotal,
				config.TargetShareBps,
				params.TargetZoneBps,
				params.BootstrapShareBps,
				coeffMin,
				coeffMax,
				stepMin,
				stepMax,
				bootstrapStepMax,
			)
		}

		baseDec, base, err := quantizeCoefficient(base)
		if err != nil {
			return nil, fmt.Errorf("base coefficient for model %q: %w", modelID, err)
		}
		stepDec, step, err := quantizeCoefficient(step)
		if err != nil {
			return nil, fmt.Errorf("adaptive step for model %q: %w", modelID, err)
		}

		effectiveCoeff := effectiveCoefficient(
			base,
			coeffMin,
			config.TargetShareBps,
			currentNormalized[modelID],
			currentNormalizedTotal,
		)
		effectiveDec, effectiveCoeff, err := quantizeCoefficient(effectiveCoeff)
		if err != nil {
			return nil, fmt.Errorf("effective coefficient for model %q: %w", modelID, err)
		}
		effective[modelID] = effectiveCoeff
		effectiveDecimal[modelID] = effectiveDec

		nextStates = append(nextStates, &types.DynamicCoefficientModelState{
			ModelId:         modelID,
			Config:          cloneDynamicModelConfig(config),
			BaseCoefficient: baseDec,
			AdaptiveStep:    stepDec,
			PrevSign:        prevSign,
		})
	}

	return &epochCoefficientResult{
		effective:        effective,
		effectiveDecimal: effectiveDecimal,
		clampedModels:    clampedModels,
		snapshot: &types.DynamicCoefficientEpochSnapshot{
			Params: cloneDynamicParams(params),
			Models: nextStates,
		},
	}, nil
}

func normalizedModelWeights(
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

func coefficientStates(
	snapshot *types.DynamicCoefficientEpochSnapshot,
) map[string]*types.DynamicCoefficientModelState {
	states := make(map[string]*types.DynamicCoefficientModelState)
	if snapshot == nil {
		return states
	}
	for _, state := range snapshot.Models {
		if state != nil && state.ModelId != "" {
			states[state.ModelId] = state
		}
	}
	return states
}

func initialCoefficientState(
	model *types.PoCModelConfig,
	previous *types.DynamicCoefficientModelState,
	stepMax mathsdk.LegacyDec,
) (mathsdk.LegacyDec, mathsdk.LegacyDec, int32, bool, error) {
	if previous != nil {
		base, err := coefficientDecimal(
			fmt.Sprintf("previous base coefficient for model %q", model.ModelId),
			previous.BaseCoefficient,
		)
		if err != nil {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false, err
		}
		step, err := coefficientDecimal(
			fmt.Sprintf("previous adaptive step for model %q", model.ModelId),
			previous.AdaptiveStep,
		)
		if err != nil {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false, err
		}
		if previous.PrevSign < -1 || previous.PrevSign > 1 {
			return mathsdk.LegacyDec{}, mathsdk.LegacyDec{}, 0, false,
				fmt.Errorf("previous sign for model %q must be -1, 0, or 1", model.ModelId)
		}
		return base, step, previous.PrevSign, true, nil
	}
	base, err := coefficientDecimal(
		fmt.Sprintf("coeff_min for new model %q", model.ModelId),
		model.DynamicCoefficient.CoeffMin,
	)
	return base, stepMax.QuoInt64(2), 0, false, err
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

func coefficientDecimal(name string, value *types.Decimal) (mathsdk.LegacyDec, error) {
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

func adjustBaseCoefficient(
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
	scaledShare := normalized.MulInt64(coefficientBPSDenominator)
	lower := int64(targetBPS) - int64(zoneBPS)
	upper := int64(targetBPS) + int64(zoneBPS)
	lowerBound := total.MulInt64(lower)
	upperBound := total.MulInt64(upper)
	if !total.IsZero() && !scaledShare.LT(lowerBound) && !scaledShare.GT(upperBound) {
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
		step = minLegacyDec(step.MulInt64(2), cap)
	} else {
		step = minLegacyDec(maxLegacyDec(step.QuoInt64(2), stepMin), cap)
	}
	prevSign = sign

	multiplier := mathsdk.LegacyOneDec()
	if sign > 0 {
		multiplier = multiplier.Add(step)
	} else {
		multiplier = multiplier.Sub(step)
	}
	base = clampCoefficient(base.Mul(multiplier), coeffMin, coeffMax)
	return base, step, prevSign
}

func effectiveCoefficient(
	base mathsdk.LegacyDec,
	coeffMin mathsdk.LegacyDec,
	targetBPS uint32,
	normalized mathsdk.LegacyDec,
	total mathsdk.LegacyDec,
) mathsdk.LegacyDec {
	if total.IsZero() || normalized.IsZero() {
		return base
	}
	scaledShare := normalized.MulInt64(coefficientBPSDenominator)
	targetWeight := total.MulInt64(int64(targetBPS))
	if !scaledShare.GT(targetWeight) {
		return base
	}
	targetFractionOfShare := targetWeight.Quo(scaledShare)
	return coeffMin.Add(base.Sub(coeffMin).Mul(targetFractionOfShare))
}

func clampCoefficient(value, min, max mathsdk.LegacyDec) mathsdk.LegacyDec {
	if value.LT(min) {
		return min
	}
	if value.GT(max) {
		return max
	}
	return value
}

func minLegacyDec(a, b mathsdk.LegacyDec) mathsdk.LegacyDec {
	if a.LT(b) {
		return a
	}
	return b
}

func maxLegacyDec(a, b mathsdk.LegacyDec) mathsdk.LegacyDec {
	if a.GT(b) {
		return a
	}
	return b
}

func quantizeCoefficient(value mathsdk.LegacyDec) (*types.Decimal, mathsdk.LegacyDec, error) {
	if value.IsNegative() {
		return nil, mathsdk.LegacyDec{}, fmt.Errorf("value cannot be negative")
	}
	if value.IsZero() {
		return &types.Decimal{Value: 0, Exponent: 0}, mathsdk.LegacyZeroDec(), nil
	}
	scaled := value.MulInt64(coefficientDecimalScale).TruncateInt()
	if !scaled.IsInt64() {
		return nil, mathsdk.LegacyDec{}, fmt.Errorf("12-decimal representation overflows int64")
	}
	coefficient := scaled.Int64()
	exponent := int32(-12)
	for coefficient%10 == 0 {
		coefficient /= 10
		exponent++
	}
	encoded := &types.Decimal{Value: coefficient, Exponent: exponent}
	quantized, err := encoded.ToLegacyDec()
	if err != nil {
		return nil, mathsdk.LegacyDec{}, err
	}
	return encoded, quantized, nil
}

func cloneDynamicParams(params *types.DynamicCoefficientParams) *types.DynamicCoefficientParams {
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

func cloneDynamicModelConfig(config *types.DynamicCoefficientModelConfig) *types.DynamicCoefficientModelConfig {
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
