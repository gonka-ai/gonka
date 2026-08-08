package inference

import "github.com/productscience/inference/x/inference/types"

func buildConfirmationWeightScales(
	eligibleModels []string,
	activeParticipants []*types.ActiveParticipant,
	coefficients *epochCoefficientResult,
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

	scales := make([]*types.ConfirmationWeightScale, 0, len(confirmable))
	for _, modelID := range sortedKeys(confirmable) {
		scale := cloneDecimal(coefficients.effectiveDecimal[modelID])
		if scale == nil {
			encoded, _, err := quantizeCoefficient(coefficients.effective[modelID])
			if err == nil {
				scale = encoded
			}
		}
		if scale == nil {
			scale = &types.Decimal{Value: 0, Exponent: 0}
		}
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:              modelID,
			EffectiveCoefficient: scale,
		})
	}
	return scales
}
