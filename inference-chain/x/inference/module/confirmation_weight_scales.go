package inference

import "github.com/productscience/inference/x/inference/types"

func buildConfirmationWeightScales(
	eligibleModels []string,
	activeParticipants []*types.ActiveParticipant,
	pocParams *types.PocParams,
) []*types.ConfirmationWeightScale {
	eligible := make(map[string]bool, len(eligibleModels))
	for _, modelID := range eligibleModels {
		if modelID != "" {
			eligible[modelID] = true
		}
	}

	realModels := modelsWithRealNodes(activeParticipants, eligible)
	scales := make([]*types.ConfirmationWeightScale, 0, len(realModels))
	for _, modelID := range sortedKeys(realModels) {
		config, _ := pocParams.GetModelConfig(modelID)
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:           modelID,
			WeightScaleFactor: config.GetWeightScaleFactor().CloneOrOne(),
		})
	}
	return scales
}

func modelsWithRealNodes(activeParticipants []*types.ActiveParticipant, eligible map[string]bool) map[string]bool {
	real := make(map[string]bool)
	for _, p := range activeParticipants {
		if p == nil {
			continue
		}
		for i, modelID := range p.Models {
			if modelID == "" || !eligible[modelID] || i >= len(p.MlNodes) || p.MlNodes[i] == nil {
				continue
			}
			for _, node := range p.MlNodes[i].MlNodes {
				if node != nil && node.PocWeight > 0 {
					real[modelID] = true
					break
				}
			}
		}
	}
	return real
}
