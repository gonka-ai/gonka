package inference

import (
	"context"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

type trainingCarryBudget struct {
	carried      map[string]map[string]int64
	confirmed    map[string]int64
	coefficients map[string]mathsdk.LegacyDec
}

func (am AppModule) buildTrainingCarryBudget(ctx context.Context, upcomingEpoch types.Epoch) trainingCarryBudget {
	budget := trainingCarryBudget{
		carried:   make(map[string]map[string]int64),
		confirmed: make(map[string]int64),
	}
	for host, nodes := range am.keeper.CollectCarriedReservedNodeWeights(ctx, upcomingEpoch) {
		weights := make(map[string]int64, len(nodes))
		for _, n := range nodes {
			if n == nil || n.NodeId == "" {
				continue
			}
			if prev, seen := weights[n.NodeId]; !seen || n.PocWeight > prev {
				weights[n.NodeId] = n.PocWeight
			}
		}
		budget.carried[host] = weights
	}
	if len(budget.carried) == 0 {
		return budget
	}

	currentIndex := upcomingEpoch.Index - 1
	rootData, found := am.keeper.GetEpochGroupData(ctx, currentIndex, "")
	if !found {
		am.LogError("buildTrainingCarryBudget: current epoch group data not found, carrying no reserved weight", types.Training,
			"epochIndex", currentIndex)
		return budget
	}
	active, found := am.keeper.GetActiveParticipants(ctx, currentIndex)
	if !found {
		am.LogError("buildTrainingCarryBudget: current active participants not found, carrying no reserved weight", types.Training,
			"epochIndex", currentIndex)
		return budget
	}

	budget.coefficients = types.ModelWeightCoefficients(rootData.ConfirmationWeightScales)
	confirmationCoefficients := types.ConfirmationWeightCoefficients(rootData.ConfirmationWeightScales)
	byAddr := make(map[string]*types.ActiveParticipant, len(active.Participants))
	for _, p := range active.Participants {
		if p != nil {
			byAddr[p.Index] = p
		}
	}
	for _, vw := range rootData.ValidationWeights {
		if vw == nil {
			continue
		}
		if _, carries := budget.carried[vw.MemberAddress]; !carries {
			continue
		}
		p := byAddr[vw.MemberAddress]
		if p == nil {
			continue
		}
		full := types.WeightOfModelNodesWithCoefficients(participantModelNodes(p), budget.coefficients)
		raw := types.ConfirmationWeightOfParticipantWithCoefficients(p, confirmationCoefficients)
		if raw <= 0 {
			budget.confirmed[vw.MemberAddress] = full
			continue
		}
		budget.confirmed[vw.MemberAddress] = types.EffectiveConfirmedWeight(full, vw.ConfirmationWeight, raw)
	}
	return budget
}

func (b trainingCarryBudget) apply(participants []*types.ActiveParticipant) {
	for _, p := range participants {
		if p == nil {
			continue
		}
		carried := b.carried[p.Index]
		if len(carried) == 0 {
			continue
		}
		free := make(map[string][]*types.MLNodeInfo)
		reserved := make(map[string][]*types.MLNodeInfo)
		for i, model := range p.Models {
			if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
				continue
			}
			for _, node := range p.MlNodes[i].MlNodes {
				if node == nil {
					continue
				}
				weight, isReserved := carried[node.NodeId]
				if !isReserved {
					free[model] = append(free[model], node)
					continue
				}
				node.PocWeight = min(node.PocWeight, weight)
				reserved[model] = append(reserved[model], node)
			}
		}
		frozen := types.WeightOfModelNodesWithCoefficients(reserved, b.coefficients)
		allowed := max(0, b.confirmed[p.Index]-types.WeightOfModelNodesWithCoefficients(free, b.coefficients))
		if frozen > allowed {
			for _, nodes := range reserved {
				for _, node := range nodes {
					node.PocWeight = mathsdk.LegacyNewDec(node.PocWeight).MulInt64(allowed).QuoInt64(frozen).TruncateInt64()
				}
			}
		}
		p.Weight = RecalculateWeight(p)
	}
}

func participantModelNodes(p *types.ActiveParticipant) map[string][]*types.MLNodeInfo {
	modelNodes := make(map[string][]*types.MLNodeInfo, len(p.Models))
	for i, model := range p.Models {
		if model == "" || i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			continue
		}
		modelNodes[model] = append(modelNodes[model], p.MlNodes[i].MlNodes...)
	}
	return modelNodes
}
