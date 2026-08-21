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
	trustedModels := modelsWithPositiveVotingPower(activeParticipants, eligible)

	accounting := make(map[string]bool, len(realModels)+len(trustedModels))
	for modelID := range realModels {
		accounting[modelID] = true
	}
	for modelID := range trustedModels {
		accounting[modelID] = true
	}

	scales := make([]*types.ConfirmationWeightScale, 0, len(accounting))
	for _, modelID := range sortedKeys(accounting) {
		config, _ := pocParams.GetModelConfig(modelID)
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:               modelID,
			WeightScaleFactor:     config.GetWeightScaleFactor().CloneOrOne(),
			HasTrustedVotingPower: trustedModels[modelID],
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

func modelsWithPositiveVotingPower(activeParticipants []*types.ActiveParticipant, eligible map[string]bool) map[string]bool {
	trusted := make(map[string]bool)
	for _, p := range activeParticipants {
		if p == nil {
			continue
		}
		for _, vp := range p.VotingPowers {
			if vp != nil && vp.VotingPower > 0 && eligible[vp.ModelId] {
				trusted[vp.ModelId] = true
			}
		}
	}
	return trusted
}
