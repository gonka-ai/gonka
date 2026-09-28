package inference

import "github.com/productscience/inference/x/inference/types"

func buildConfirmationWeightScales(
	eligibleModels []string,
	activeParticipants []*types.ActiveParticipant,
	pocParams *types.PocParams,
	recipe *types.PocStageRecipe,
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
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:           modelID,
			WeightScaleFactor: confirmationScaleFactor(modelID, pocParams, recipe).CloneOrOne(),
		})
	}
	return scales
}

// confirmationScaleFactor is the weight scale of the frozen regular-stage block.
// A missing recipe falls back to the live regular-slot block, then the flat field.
func confirmationScaleFactor(modelID string, pocParams *types.PocParams, recipe *types.PocStageRecipe) *types.Decimal {
	if recipe != nil {
		mc, ok := recipe.GetModelConfig(modelID)
		if !ok {
			return nil
		}
		block, ok := mc.SchemeParams(recipe.Scheme)
		if !ok || block == nil {
			return nil
		}
		return block.WeightScaleFactor
	}
	if pocParams == nil {
		return nil
	}
	config, ok := pocParams.GetModelConfig(modelID)
	if !ok || config == nil {
		return nil
	}
	if block, ok := config.SchemeParams(pocParams.PocScheme); ok && block != nil {
		return block.WeightScaleFactor
	}
	return config.GetWeightScaleFactor()
}
