package types

import (
	"slices"

	mathsdk "cosmossdk.io/math"
)

func ConfirmationWeightCoefficients(scales []*ConfirmationWeightScale) map[string]mathsdk.LegacyDec {
	return confirmationCoefficients(scales)
}

func ConfirmationWeightOfParticipant(p *ActiveParticipant, scales []*ConfirmationWeightScale) int64 {
	return ConfirmationWeightOfParticipantWithCoefficients(p, confirmationCoefficients(scales))
}

func ConfirmationWeightOfParticipantWithCoefficients(
	p *ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
) int64 {
	if p == nil {
		return 0
	}
	modelNodes := make(map[string][]*MLNodeInfo, len(p.Models))
	for i, modelID := range p.Models {
		if modelID == "" || i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			continue
		}
		modelNodes[modelID] = append(modelNodes[modelID], p.MlNodes[i].MlNodes...)
	}
	return ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, coefficients)
}

func ConfirmationWeightOfModelNodes(modelNodes map[string][]*MLNodeInfo, scales []*ConfirmationWeightScale) int64 {
	return ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, confirmationCoefficients(scales))
}

func ConfirmationWeightOfModelNodesWithCoefficients(
	modelNodes map[string][]*MLNodeInfo,
	coefficients map[string]mathsdk.LegacyDec,
) int64 {
	total := int64(0)

	modelIDs := make([]string, 0, len(modelNodes))
	for modelID := range modelNodes {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	for _, modelID := range modelIDs {
		coeff, ok := coefficients[modelID]
		if !ok {
			continue
		}
		rawModel := int64(0)
		for _, node := range modelNodes[modelID] {
			if node != nil {
				rawModel += node.PocWeight
			}
		}
		total += coeff.MulInt64(rawModel).TruncateInt64()
	}
	return total
}

func confirmationCoefficients(scales []*ConfirmationWeightScale) map[string]mathsdk.LegacyDec {
	coefficients := make(map[string]mathsdk.LegacyDec, len(scales))
	for _, scale := range scales {
		if scale == nil || scale.ModelId == "" {
			continue
		}
		coefficients[scale.ModelId] = confirmationScaleFactor(scale)
	}
	return coefficients
}

func confirmationScaleFactor(scale *ConfirmationWeightScale) mathsdk.LegacyDec {
	if scale == nil {
		return mathsdk.LegacyOneDec()
	}
	return scale.WeightScaleFactor.LegacyDecOrOne()
}
