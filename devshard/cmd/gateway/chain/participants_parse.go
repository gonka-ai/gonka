package chain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type participantNode struct {
	Model  string
	NodeID string
	Weight float64
}

// participantsState is the parsed current-participants response, keyed throughout by gonka address.
type participantsState struct {
	Weights            map[string]float64
	WeightsByModel     map[string]map[string]float64
	FullWeights        map[string]float64
	FullWeightsByModel map[string]map[string]float64
	Preserved          []string
	PreservedByModel   map[string][]string
	Excluded           []string
	InferenceURLs      map[string]string
	NodesByParticipant map[string][]participantNode
}
type chainCurrentParticipantsResponse struct {
	ActiveParticipants chainActiveParticipantsGroup `json:"active_participants"`
}
type chainActiveParticipantsGroup struct {
	Participants []chainActiveParticipant `json:"participants"`
}
type chainActiveParticipant struct {
	Index        string              `json:"index"`
	InferenceURL string              `json:"inference_url"`
	Models       []string            `json:"models,omitempty"`
	MLNodes      []chainModelMLNodes `json:"ml_nodes"`
}

// chainModelMLNodes is the raw per-model-slot ML node list (indexed in parallel with Models).
type chainModelMLNodes struct {
	MLNodes []chainMLNodeInfo `json:"ml_nodes"`
}
type chainMLNodeInfo struct {
	NodeID             string     `json:"node_id"`
	TimeslotAllocation []bool     `json:"timeslot_allocation"`
	PoCWeight          jsonUint64 `json:"poc_weight,omitempty"`
}

// preservationMode selects which ML nodes count as preserved when folding participants.
type preservationMode int

// parseParticipants folds the current-participants body under the given preservation rule. See README.md, "Folding the participants response".
func parseParticipants(body []byte, preservation preservationMode, preservedNodes preservedSnapshotState) (participantsState, error) {
	var payload chainCurrentParticipantsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return participantsState{}, fmt.Errorf("parse participants: %w", err)
	}

	participants := payload.ActiveParticipants.Participants
	state := participantsState{
		Weights:            make(map[string]float64, len(participants)),
		WeightsByModel:     make(map[string]map[string]float64),
		FullWeights:        make(map[string]float64, len(participants)),
		FullWeightsByModel: make(map[string]map[string]float64),
		PreservedByModel:   make(map[string][]string),
		InferenceURLs:      make(map[string]string, len(participants)),
		NodesByParticipant: make(map[string][]participantNode, len(participants)),
	}

	seenPreserved := make(map[string]struct{}, len(participants))
	seenPreservedByModel := make(map[string]map[string]struct{})
	seenExcluded := make(map[string]struct{}, len(participants))

	for _, participant := range participants {
		key := strings.TrimSpace(participant.Index)
		if key == "" {
			continue
		}
		if url := strings.TrimSpace(participant.InferenceURL); url != "" {
			state.InferenceURLs[key] = url
		}
		if nodes := extractParticipantNodes(participant); len(nodes) > 0 {
			state.NodesByParticipant[key] = nodes
		}

		state.Weights[key] = participantWeight(participant, preservation, preservedNodes)
		state.FullWeights[key] = participantWeight(participant, preservationModeAll, preservedSnapshotState{})
		addModelWeights(state.WeightsByModel, key, participantWeightsByModel(participant, preservation, preservedNodes))
		addModelWeights(state.FullWeightsByModel, key, participantWeightsByModel(participant, preservationModeAll, preservedSnapshotState{}))

		if participantHasPreservedNode(participant, preservation, preservedNodes) {
			if _, ok := seenPreserved[key]; !ok {
				seenPreserved[key] = struct{}{}
				state.Preserved = append(state.Preserved, key)
			}
			for _, model := range preservedModelsForParticipant(participant, preservation, preservedNodes) {
				modelSeen := seenPreservedByModel[model]
				if modelSeen == nil {
					modelSeen = map[string]struct{}{}
					seenPreservedByModel[model] = modelSeen
				}
				if _, ok := modelSeen[key]; ok {
					continue
				}
				modelSeen[key] = struct{}{}
				state.PreservedByModel[model] = append(state.PreservedByModel[model], key)
			}
			continue
		}
		if _, ok := seenExcluded[key]; !ok {
			seenExcluded[key] = struct{}{}
			state.Excluded = append(state.Excluded, key)
		}
	}
	sort.Strings(state.Preserved)
	for model := range state.PreservedByModel {
		sort.Strings(state.PreservedByModel[model])
	}
	return state, nil
}

func addModelWeights(dst map[string]map[string]float64, key string, weights map[string]float64) {
	for model, weight := range weights {
		modelWeights := dst[model]
		if modelWeights == nil {
			modelWeights = map[string]float64{}
			dst[model] = modelWeights
		}
		modelWeights[key] = weight
	}
}

func nodePreserved(participantID, model string, node chainMLNodeInfo, preservation preservationMode, preservedNodes preservedSnapshotState) bool {
	switch preservation {
	case preservationModeAll:
		return true
	case preservationModeSnapshot:
		return preservedNodes.Has(model, participantID, node.NodeID)
	default:
		return len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1]
	}
}

func participantModelAt(participant chainActiveParticipant, index int) string {
	if index < 0 || index >= len(participant.Models) {
		return ""
	}
	return strings.TrimSpace(participant.Models[index])
}

// usableModelAt reports the model at index and whether it has both a trimmed name and a matching ML-node entry.
func usableModelAt(participant chainActiveParticipant, index int) (model string, ok bool) {
	model = participantModelAt(participant, index)
	return model, model != "" && index < len(participant.MLNodes)
}

func modelNodePoCWeight(participantID, model string, modelNodes chainModelMLNodes, preservation preservationMode, preservedNodes preservedSnapshotState) uint64 {
	var weight uint64
	for _, node := range modelNodes.MLNodes {
		if nodePreserved(participantID, model, node, preservation, preservedNodes) {
			weight += uint64(node.PoCWeight)
		}
	}
	return weight
}

func participantWeight(participant chainActiveParticipant, preservation preservationMode, preservedNodes preservedSnapshotState) float64 {
	var weight uint64
	for i, modelNodes := range participant.MLNodes {
		weight += modelNodePoCWeight(participant.Index, participantModelAt(participant, i), modelNodes, preservation, preservedNodes)
	}
	return float64(weight)
}

func participantWeightsByModel(participant chainActiveParticipant, preservation preservationMode, preservedNodes preservedSnapshotState) map[string]float64 {
	weights := make(map[string]float64, len(participant.Models))
	for i, rawModel := range participant.Models {
		model := strings.TrimSpace(rawModel)
		if model == "" {
			continue
		}
		if i >= len(participant.MLNodes) {
			weights[model] = 0
			continue
		}
		weights[model] = float64(modelNodePoCWeight(participant.Index, model, participant.MLNodes[i], preservation, preservedNodes))
	}
	return weights
}
