package chain

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
)

type epochInfo struct {
	BlockHeight                  int64
	Phase                        EpochPhase
	EpochIndex                   uint64
	EffectiveEpochIndex          uint64
	PoCStartBlockHeight          int64
	NextPoCStartBlockHeight      int64
	EpochSwitchBlockHeight       int64
	IsConfirmationPoCActive      bool
	ConfirmationPoCPhase         ConfirmationPoCPhase
	ConfirmationPoCTriggerHeight int64
}

type chainEpochInfoResponse struct {
	BlockHeight             jsonInt64                         `json:"block_height"`
	Phase                   string                            `json:"phase"`
	LatestEpoch             chainLatestEpoch                  `json:"latest_epoch"`
	EpochStages             chainEpochStages                  `json:"epoch_stages"`
	NextEpochStages         chainEpochStages                  `json:"next_epoch_stages"`
	IsConfirmationPoCActive bool                              `json:"is_confirmation_poc_active"`
	ActiveConfirmationPoC   *chainConfirmationPoCEventPayload `json:"active_confirmation_poc_event,omitempty"`
}

type chainLatestEpoch struct {
	Index               jsonUint64 `json:"index"`
	PocStartBlockHeight jsonInt64  `json:"poc_start_block_height"`
}

type chainEpochStages struct {
	SetNewValidators jsonInt64 `json:"set_new_validators"`
	NextPoCStart     jsonInt64 `json:"next_poc_start"`
}

type chainConfirmationPoCEventPayload struct {
	Phase         confirmationPoCPhaseValue `json:"phase"`
	TriggerHeight jsonInt64                 `json:"trigger_height"`
}

func parseEpochInfo(body []byte) (epochInfo, error) {
	var payload chainEpochInfoResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return epochInfo{}, fmt.Errorf("parse epoch info: %w", err)
	}

	info := epochInfo{
		BlockHeight:             int64(payload.BlockHeight),
		Phase:                   EpochPhase(strings.TrimSpace(payload.Phase)),
		EpochIndex:              uint64(payload.LatestEpoch.Index),
		EffectiveEpochIndex:     deriveEffectiveEpochIndex(payload),
		PoCStartBlockHeight:     int64(payload.LatestEpoch.PocStartBlockHeight),
		NextPoCStartBlockHeight: int64(payload.EpochStages.NextPoCStart),
		EpochSwitchBlockHeight:  deriveEpochSwitchBlockHeight(payload),
		IsConfirmationPoCActive: payload.IsConfirmationPoCActive,
	}
	if payload.ActiveConfirmationPoC != nil {
		info.ConfirmationPoCPhase = ConfirmationPoCPhase(payload.ActiveConfirmationPoC.Phase)
		info.ConfirmationPoCTriggerHeight = int64(payload.ActiveConfirmationPoC.TriggerHeight)
	}
	return info, nil
}

func deriveEpochSwitchBlockHeight(payload chainEpochInfoResponse) int64 {
	blockHeight := int64(payload.BlockHeight)
	if payload.EpochStages.SetNewValidators > 0 && int64(payload.EpochStages.SetNewValidators) > blockHeight {
		return int64(payload.EpochStages.SetNewValidators)
	}
	if payload.NextEpochStages.SetNewValidators > 0 {
		return int64(payload.NextEpochStages.SetNewValidators)
	}
	if payload.EpochStages.NextPoCStart > 0 {
		return int64(payload.EpochStages.NextPoCStart)
	}
	return int64(payload.LatestEpoch.PocStartBlockHeight)
}

func deriveEffectiveEpochIndex(payload chainEpochInfoResponse) uint64 {
	latest := uint64(payload.LatestEpoch.Index)
	height := int64(payload.BlockHeight)
	pocStart := int64(payload.LatestEpoch.PocStartBlockHeight)
	switchHeight := int64(payload.EpochStages.SetNewValidators)
	switch {
	case latest == 0 || height <= 0 || pocStart <= 0 || switchHeight <= 0:
		return 0
	case height >= pocStart && height < switchHeight:
		return latest - 1
	}
	return latest
}

// See README.md, "Which nodes count as preserved".
const (
	preservationModeLegacy preservationMode = iota
	preservationModeSnapshot
	preservationModeAll
)

func participantHasPreservedNode(participant chainActiveParticipant, preservation preservationMode, preservedNodes preservedSnapshotState) bool {
	if preservation == preservationModeAll {
		return true
	}
	for i, modelNodes := range participant.MLNodes {
		model := participantModelAt(participant, i)
		for _, node := range modelNodes.MLNodes {
			if nodePreserved(participant.Index, model, node, preservation, preservedNodes) {
				return true
			}
		}
	}
	return false
}

// preservedModelsForParticipant lists, sorted, the models where a participant has a preserved node.
func preservedModelsForParticipant(participant chainActiveParticipant, preservation preservationMode, preservedNodes preservedSnapshotState) []string {
	seen := make(map[string]struct{}, len(participant.Models))
	var models []string
	for i := range participant.Models {
		model, ok := usableModelAt(participant, i)
		if !ok {
			continue
		}
		for _, node := range participant.MLNodes[i].MLNodes {
			if !nodePreserved(participant.Index, model, node, preservation, preservedNodes) {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
			break
		}
	}
	sort.Strings(models)
	return models
}

// extractParticipantNodes flattens a participant's (model, node, weight) triples for the validation-capable merge.
func extractParticipantNodes(participant chainActiveParticipant) []participantNode {
	var nodes []participantNode
	for i := range participant.Models {
		model, ok := usableModelAt(participant, i)
		if !ok {
			continue
		}
		for _, node := range participant.MLNodes[i].MLNodes {
			nodeID := strings.TrimSpace(node.NodeID)
			if nodeID == "" {
				continue
			}
			nodes = append(nodes, participantNode{Model: model, NodeID: nodeID, Weight: float64(node.PoCWeight)})
		}
	}
	return nodes
}

func cloneModelWeights(weights map[string]map[string]float64) map[string]map[string]float64 {
	cloned := make(map[string]map[string]float64, len(weights))
	for model, modelWeights := range weights {
		clonedWeights := make(map[string]float64, len(modelWeights))
		maps.Copy(clonedWeights, modelWeights)
		cloned[model] = clonedWeights
	}
	return cloned
}

func participantValidationInferenceWeights(nodes []participantNode, miner string, capable func(miner, nodeID string) bool) (map[string]float64, float64) {
	weights := map[string]float64{}
	if capable == nil {
		return weights, 0
	}
	var total float64
	for _, node := range nodes {
		if capable(miner, node.NodeID) {
			weights[node.Model] += node.Weight
			total += node.Weight
		}
	}
	return weights, total
}

// mergePreservedWithValidationCapable returns the PoC-validation availability views without mutating state. See README.md, "PoC validation rejoins capable miners".
func mergePreservedWithValidationCapable(
	state participantsState,
	capable func(miner, nodeID string) bool,
) (preserved []string, preservedByModel map[string][]string, currentWeights map[string]float64, currentWeightsByModel map[string]map[string]float64) {
	preserved = append(preserved, state.Preserved...)

	preservedByModel = make(map[string][]string, len(state.PreservedByModel))
	for model, miners := range state.PreservedByModel {
		preservedByModel[model] = append([]string(nil), miners...)
	}

	currentWeights = make(map[string]float64, len(state.Weights))
	maps.Copy(currentWeights, state.Weights)
	currentWeightsByModel = cloneModelWeights(state.WeightsByModel)

	for _, miner := range state.Excluded {
		weightsByModel, total := participantValidationInferenceWeights(state.NodesByParticipant[miner], miner, capable)
		if total <= 0 {
			continue
		}
		preserved = append(preserved, miner)
		currentWeights[miner] = total
		for model, weight := range weightsByModel {
			if currentWeightsByModel[model] == nil {
				currentWeightsByModel[model] = map[string]float64{}
			}
			currentWeightsByModel[model][miner] = weight
			preservedByModel[model] = append(preservedByModel[model], miner)
		}
	}
	sort.Strings(preserved)
	for model := range preservedByModel {
		sort.Strings(preservedByModel[model])
	}
	return preserved, preservedByModel, currentWeights, currentWeightsByModel
}

type preservedSnapshotStatus int

const (
	preservedSnapshotUnavailable preservedSnapshotStatus = iota
	preservedSnapshotCurrent
	preservedSnapshotMissingCurrent
)

type preservedSnapshotState struct {
	byModel map[string]map[string]map[string]struct{}
}

// Has reports whether nodeID is preserved for participantID under model; nil-safe on a zero value.
func (s preservedSnapshotState) Has(model, participantID, nodeID string) bool {
	model = strings.TrimSpace(model)
	participantID = strings.TrimSpace(participantID)
	nodeID = strings.TrimSpace(nodeID)
	if model == "" || participantID == "" || nodeID == "" {
		return false
	}
	byParticipant := s.byModel[model]
	if byParticipant == nil {
		return false
	}
	nodes := byParticipant[participantID]
	if nodes == nil {
		return false
	}
	_, ok := nodes[nodeID]
	return ok
}

func newPreservedSnapshotState(snapshot *PreservedNodes) preservedSnapshotState {
	state := preservedSnapshotState{byModel: map[string]map[string]map[string]struct{}{}}
	for _, modelNodes := range snapshot.Models {
		model := strings.TrimSpace(modelNodes.ModelID)
		if model == "" {
			continue
		}
		byParticipant := state.byModel[model]
		if byParticipant == nil {
			byParticipant = map[string]map[string]struct{}{}
			state.byModel[model] = byParticipant
		}
		for _, participant := range modelNodes.Participants {
			participantID := strings.TrimSpace(participant.ParticipantID)
			if participantID == "" {
				continue
			}
			nodeSet := byParticipant[participantID]
			if nodeSet == nil {
				nodeSet = map[string]struct{}{}
				byParticipant[participantID] = nodeSet
			}
			for _, nodeID := range participant.NodeIDs {
				nodeID = strings.TrimSpace(nodeID)
				if nodeID != "" {
					nodeSet[nodeID] = struct{}{}
				}
			}
		}
	}
	return state
}

// jsonInt64 decodes an int64 field that may arrive as a JSON number or a numeric string.
type jsonInt64 int64

func (n *jsonInt64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleInt64(data)
	if err != nil {
		return err
	}
	*n = jsonInt64(parsed)
	return nil
}

// jsonUint64 decodes a uint64 field that may arrive as a JSON number or a numeric string.
type jsonUint64 uint64

func (n *jsonUint64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleUint64(data)
	if err != nil {
		return err
	}
	*n = jsonUint64(parsed)
	return nil
}

// confirmationPoCPhaseValue decodes a confirmation-PoC phase as either its string enum name or the chain's numeric (0-4) protobuf enum value.
type confirmationPoCPhaseValue string

func (p *confirmationPoCPhaseValue) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*p = confirmationPoCPhaseValue(asString)
		return nil
	}

	var asInt int
	if err := json.Unmarshal(data, &asInt); err == nil {
		switch asInt {
		case confirmationPoCPhaseNumberInactive:
			*p = confirmationPoCPhaseValue(ConfirmationPoCInactive)
		case confirmationPoCPhaseNumberGracePeriod:
			*p = confirmationPoCPhaseValue(ConfirmationPoCGracePeriod)
		case confirmationPoCPhaseNumberGeneration:
			*p = confirmationPoCPhaseValue(ConfirmationPoCGeneration)
		case confirmationPoCPhaseNumberValidation:
			*p = confirmationPoCPhaseValue(ConfirmationPoCValidation)
		case confirmationPoCPhaseNumberCompleted:
			*p = confirmationPoCPhaseValue(ConfirmationPoCCompleted)
		default:
			*p = confirmationPoCPhaseValue(strconv.Itoa(asInt))
		}
		return nil
	}

	return fmt.Errorf("unsupported confirmation PoC phase %s", string(data))
}

func parseFlexibleInt64(data []byte) (int64, error) {
	var asInt int64
	if err := json.Unmarshal(data, &asInt); err == nil {
		return asInt, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return strconv.ParseInt(strings.TrimSpace(asString), 10, 64)
	}

	return 0, fmt.Errorf("unsupported int64 value %s", string(data))
}

func parseFlexibleUint64(data []byte) (uint64, error) {
	var asUint uint64
	if err := json.Unmarshal(data, &asUint); err == nil {
		return asUint, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return strconv.ParseUint(strings.TrimSpace(asString), 10, 64)
	}

	return 0, fmt.Errorf("unsupported uint64 value %s", string(data))
}
