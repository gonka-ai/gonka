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

type carryEpochWeights struct {
	validation               map[string]*types.ValidationWeight
	participants             map[string]*types.ActiveParticipant
	modelCoefficients        map[string]mathsdk.LegacyDec
	confirmationCoefficients map[string]mathsdk.LegacyDec
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
	current, found := am.carryEpochWeights(ctx, currentIndex)
	if !found {
		return budget
	}
	budget.coefficients = current.modelCoefficients

	anchors := am.keeper.CarriedReservationWeightEpochs(ctx, upcomingEpoch)
	loaded := map[uint64]carryEpochWeights{currentIndex: current}
	for host := range budget.carried {
		if current.validation[host] == nil || current.participants[host] == nil {
			continue
		}
		anchorIndex, anchored := anchors[host]
		if !anchored {
			continue
		}
		anchor, found := loaded[anchorIndex]
		if !found {
			if anchor, found = am.carryEpochWeights(ctx, anchorIndex); !found {
				continue
			}
			loaded[anchorIndex] = anchor
		}
		vw, p := anchor.validation[host], anchor.participants[host]
		if vw == nil || p == nil {
			continue
		}
		full := types.WeightOfModelNodesWithCoefficients(participantModelNodes(p), budget.coefficients)
		raw := types.ConfirmationWeightOfParticipantWithCoefficients(p, anchor.confirmationCoefficients)
		if raw <= 0 {
			budget.confirmed[host] = full
			continue
		}
		budget.confirmed[host] = types.EffectiveConfirmedWeight(full, vw.ConfirmationWeight, raw)
	}
	return budget
}

func (am AppModule) carryEpochWeights(ctx context.Context, epochIndex uint64) (carryEpochWeights, bool) {
	rootData, found := am.keeper.GetEpochGroupData(ctx, epochIndex, "")
	if !found {
		am.LogError("buildTrainingCarryBudget: epoch group data not found, carrying no reserved weight", types.Training,
			"epochIndex", epochIndex)
		return carryEpochWeights{}, false
	}
	active, found := am.keeper.GetActiveParticipants(ctx, epochIndex)
	if !found {
		am.LogError("buildTrainingCarryBudget: active participants not found, carrying no reserved weight", types.Training,
			"epochIndex", epochIndex)
		return carryEpochWeights{}, false
	}
	weights := carryEpochWeights{
		validation:               make(map[string]*types.ValidationWeight, len(rootData.ValidationWeights)),
		participants:             make(map[string]*types.ActiveParticipant, len(active.Participants)),
		modelCoefficients:        types.ModelWeightCoefficients(rootData.ConfirmationWeightScales),
		confirmationCoefficients: types.ConfirmationWeightCoefficients(rootData.ConfirmationWeightScales),
	}
	for _, vw := range rootData.ValidationWeights {
		if vw != nil {
			weights.validation[vw.MemberAddress] = vw
		}
	}
	for _, p := range active.Participants {
		if p != nil {
			weights.participants[p.Index] = p
		}
	}
	return weights, true
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
				node.PocWeight = weight
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
