package inference

import (
	"cmp"
	"slices"

	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
)

func buildConfirmationWeightScales(
	eligibleModels []string,
	activeParticipants []*types.ActiveParticipant,
	coefficients *coefficient.Result,
) []*types.ConfirmationWeightScale {
	eligible := make(map[string]bool, len(eligibleModels))
	for _, modelID := range eligibleModels {
		if modelID != "" {
			eligible[modelID] = true
		}
	}

	confirmable := make(map[string]bool)
	for _, p := range activeParticipants {
		for _, vp := range p.VotingPowers {
			if vp != nil && vp.VotingPower > 0 && eligible[vp.ModelId] {
				confirmable[vp.ModelId] = true
			}
		}
	}

	scales := make([]*types.ConfirmationWeightScale, 0, len(coefficients.Scales))
	for _, scale := range coefficients.Scales {
		if scale == nil || scale.ModelId == "" {
			continue
		}
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:                 scale.ModelId,
			WeightScaleFactor:       cloneCoefficientDecimal(scale.WeightScaleFactor),
			EffectiveCoefficient:    cloneCoefficientDecimal(scale.EffectiveCoefficient),
			Config:                  cloneDynamicCoefficientConfig(scale.Config),
			BaseCoefficient:         cloneCoefficientDecimal(scale.BaseCoefficient),
			AdaptiveStep:            cloneCoefficientDecimal(scale.AdaptiveStep),
			PrevSign:                scale.PrevSign,
			ExcludeFromConfirmation: !confirmable[scale.ModelId],
		})
	}
	slices.SortFunc(scales, func(a, b *types.ConfirmationWeightScale) int {
		return cmp.Compare(a.ModelId, b.ModelId)
	})
	return scales
}

func cloneCoefficientDecimal(value *types.Decimal) *types.Decimal {
	if value == nil {
		return nil
	}
	return &types.Decimal{Value: value.Value, Exponent: value.Exponent}
}

func cloneDynamicCoefficientConfig(
	value *types.DynamicCoefficientModelConfig,
) *types.DynamicCoefficientModelConfig {
	if value == nil {
		return nil
	}
	return &types.DynamicCoefficientModelConfig{
		CoeffMin:           cloneCoefficientDecimal(value.CoeffMin),
		CoeffMax:           cloneCoefficientDecimal(value.CoeffMax),
		RelativeDifficulty: cloneCoefficientDecimal(value.RelativeDifficulty),
		TargetShareBps:     value.TargetShareBps,
	}
}
