package inference

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"slices"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

const (
	FlowContext    = "model_assignment"
	SubFlowContext = "allocate_mlnodes_for_poc"
)

func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// EpochMLNodeData stores ML node information indexed by [modelId][participantAddress]
type EpochMLNodeData struct {
	data map[string]map[string][]*types.MLNodeInfo
}

func NewEpochMLNodeData() *EpochMLNodeData {
	return &EpochMLNodeData{
		data: make(map[string]map[string][]*types.MLNodeInfo),
	}
}

func (e *EpochMLNodeData) Set(modelId, participantAddr string, nodes []*types.MLNodeInfo) {
	if e.data[modelId] == nil {
		e.data[modelId] = make(map[string][]*types.MLNodeInfo)
	}
	e.data[modelId][participantAddr] = nodes
}

func (e *EpochMLNodeData) Append(modelId, participantAddr string, node *types.MLNodeInfo) {
	if e.data[modelId] == nil {
		e.data[modelId] = make(map[string][]*types.MLNodeInfo)
	}
	e.data[modelId][participantAddr] = append(e.data[modelId][participantAddr], node)
}

func (e *EpochMLNodeData) GetForModel(modelId string) map[string][]*types.MLNodeInfo {
	return e.data[modelId]
}

func (e *EpochMLNodeData) GetForParticipant(modelId, participantAddr string) []*types.MLNodeInfo {
	if e.data[modelId] == nil {
		return nil
	}
	nodes := e.data[modelId][participantAddr]
	sortMLNodesByNodeId(nodes)
	return nodes
}

func (e *EpochMLNodeData) Models() []string {
	return sortedKeys(e.data)
}

func sortMLNodesByNodeId(nodes []*types.MLNodeInfo) {
	slices.SortFunc(nodes, func(a, b *types.MLNodeInfo) int {
		if a.NodeId < b.NodeId {
			return -1
		}
		if a.NodeId > b.NodeId {
			return 1
		}
		return 0
	})
}

type mlNodeDedupDecision struct {
	kept    *types.MLNodeInfo
	dropped []*types.MLNodeInfo
}

// dedupMLNodesById enforces deterministic uniqueness for ML node slices.
// Hardware node submissions already reject duplicate LocalIds (see msg_server_submit_hardware_diff.go),
// but once MLNodeInfo snapshots are persisted we double-check here before any scheduling logic runs.
// When multiple entries share the same NodeId we keep the one with the highest PocWeight, then Throughput,
// then TimeslotAllocation signature to keep behavior predictable.
func dedupMLNodesById(nodes []*types.MLNodeInfo) ([]*types.MLNodeInfo, map[string]mlNodeDedupDecision) {
	if len(nodes) == 0 {
		return nil, nil
	}

	bestById := make(map[string]*types.MLNodeInfo, len(nodes))
	stats := make(map[string]mlNodeDedupDecision)

	for _, node := range nodes {
		if node == nil {
			continue
		}
		if existing, ok := bestById[node.NodeId]; ok {
			decision := stats[node.NodeId]
			if compareMLNodePreference(node, existing) > 0 {
				decision.dropped = append(decision.dropped, existing)
				bestById[node.NodeId] = node
				decision.kept = node
			} else {
				decision.kept = existing
				decision.dropped = append(decision.dropped, node)
			}
			stats[node.NodeId] = decision
			continue
		}
		bestById[node.NodeId] = node
	}

	deduped := make([]*types.MLNodeInfo, 0, len(bestById))
	for _, node := range bestById {
		deduped = append(deduped, node)
	}

	slices.SortFunc(deduped, func(a, b *types.MLNodeInfo) int {
		switch {
		case a.NodeId < b.NodeId:
			return -1
		case a.NodeId > b.NodeId:
			return 1
		}
		return compareMLNodePreference(a, b)
	})

	if len(deduped) == 0 {
		return nil, stats
	}

	return deduped, stats
}

func compareMLNodePreference(a, b *types.MLNodeInfo) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch {
	case a.PocWeight > b.PocWeight:
		return 1
	case a.PocWeight < b.PocWeight:
		return -1
	}
	switch {
	case a.Throughput > b.Throughput:
		return 1
	case a.Throughput < b.Throughput:
		return -1
	}
	return compareBoolSlices(a.TimeslotAllocation, b.TimeslotAllocation)
}

func compareBoolSlices(a, b []bool) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] == b[i] {
			continue
		}
		if a[i] {
			return 1
		}
		return -1
	}
	switch {
	case len(a) > len(b):
		return 1
	case len(a) < len(b):
		return -1
	default:
		return 0
	}
}

func (e *EpochMLNodeData) GetAllIndividualNodeWeights() []int64 {
	weights := make([]int64, 0)
	for _, modelId := range sortedKeys(e.data) {
		modelData := e.data[modelId]
		for _, nodes := range modelData {
			for _, node := range nodes {
				weights = append(weights, node.PocWeight)
			}
		}
	}
	return weights
}

func (e *EpochMLNodeData) GetAllParticipantWeights() []int64 {
	participantWeights := make(map[string]int64)
	for _, modelId := range sortedKeys(e.data) {
		modelData := e.data[modelId]
		for participantAddr, nodes := range modelData {
			for _, node := range nodes {
				participantWeights[participantAddr] += node.PocWeight
			}
		}
	}

	weights := make([]int64, 0, len(participantWeights))
	for _, weight := range participantWeights {
		weights = append(weights, weight)
	}
	return weights
}

func (e *EpochMLNodeData) GetAllParticipantsHash() string {
	uniqueParticipants := make(map[string]bool)
	for _, modelData := range e.data {
		for participantAddr := range modelData {
			uniqueParticipants[participantAddr] = true
		}
	}

	sortedParticipants := sortedKeys(uniqueParticipants)

	allParticipantsStr := fmt.Sprintf("%v", sortedParticipants)
	allParticipantsHash := sha256.Sum256([]byte(allParticipantsStr))
	return fmt.Sprintf("%x", allParticipantsHash[:8])
}

func (e *EpochMLNodeData) GetTotalWeightForModel(modelId string) int64 {
	var total int64
	participantNodes := e.GetForModel(modelId)
	for _, nodes := range participantNodes {
		for _, node := range nodes {
			total += node.PocWeight
		}
	}
	return total
}

func (e *EpochMLNodeData) GetParticipantWeight(participantAddr string) int64 {
	var weight int64
	for _, modelData := range e.data {
		if nodes, ok := modelData[participantAddr]; ok {
			for _, node := range nodes {
				weight += node.PocWeight
			}
		}
	}
	return weight
}

type ModelAssigner struct {
	types.InferenceLogger
	keeper KeeperForModelAssigner
}

func NewModelAssigner(keeper KeeperForModelAssigner, logger types.InferenceLogger) *ModelAssigner {
	return &ModelAssigner{
		keeper:          keeper,
		InferenceLogger: logger,
	}
}

type KeeperForModelAssigner interface {
	GetGovernanceModelsSorted(ctx context.Context) ([]*types.Model, error)
	GetHardwareNodes(ctx context.Context, participantId string) (*types.HardwareNodes, bool)
	GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool)
	GetEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) (val types.EpochGroupData, found bool)
	GetSettleAmount(ctx context.Context, participant string) (val types.SettleAmount, found bool)
	GetParams(ctx context.Context) (types.Params, error)
}

func (ma *ModelAssigner) setModelsForParticipants(ctx context.Context, participants []*types.ActiveParticipant, upcomingEpoch types.Epoch) {
	// TODO: We may need to populate throughput in MLNodeInfo using the model's ThroughputPerNonce
	// This would ensure consistent throughput calculations based on governance model parameters
	// rather than relying on hardware node declarations alone.
	ma.LogInfo("Starting model and slot assignment for participants", types.Allocation, "flow_context", FlowContext, "step", "start", "num_participants", len(participants), "epoch_index", upcomingEpoch.Index)

	governanceModels, err := ma.keeper.GetGovernanceModelsSorted(ctx)
	if err != nil {
		ma.LogError("setModelsForParticipants: Unable to get governance models", types.Allocation, "error", err.Error(), "flow_context", FlowContext)
		return
	}
	ma.LogInfo("Retrieved governance models", types.Allocation, "flow_context", FlowContext, "step", "get_governance_models", "num_models", len(governanceModels))

	for _, p := range participants {
		ma.LogInfo("Processing participant", types.Allocation, "flow_context", FlowContext, "step", "participant_loop_start", "participant_index", p.Index)
		hardwareNodes, found := ma.keeper.GetHardwareNodes(ctx, p.Index)
		if !found {
			ma.LogInfo("No hardware nodes found for participant, skipping model assignment.", types.Allocation, "flow_context", FlowContext, "step", "no_hardware_nodes", "participant_index", p.Index)
			p.Models = make([]string, 0)
			p.MlNodes = make([]*types.ModelMLNodes, 0)
			continue
		}

		var originalMLNodes []*types.MLNodeInfo
		if len(p.MlNodes) > 0 && p.MlNodes[0] != nil {
			originalMLNodes = p.MlNodes[0].MlNodes
		}
		ma.LogInfo("Original MLNodes", types.Allocation, "flow_context", FlowContext, "step", "pre_legacy_distribution", "participant_index", p.Index, "ml_nodes", originalMLNodes)

		if len(originalMLNodes) > 0 {
			dedupedNodes, dedupStats := dedupMLNodesById(originalMLNodes)
			ma.logMLNodeDedupStats(
				"Duplicate ML nodes detected before participant assignment",
				dedupStats,
				"flow_context", FlowContext,
				"step", "dedup_participant_nodes",
				"participant_index", p.Index,
			)
			originalMLNodes = dedupedNodes
			if len(p.MlNodes) > 0 && p.MlNodes[0] != nil {
				p.MlNodes[0].MlNodes = dedupedNodes
			}
		}

		for _, mlNode := range originalMLNodes {
			mlNode.TimeslotAllocation = []bool{true, false} // [PRE_POC_SLOT, POC_SLOT]
		}
		ma.LogInfo("Initialized all ML nodes to PRE_POC_SLOT=true, POC_SLOT=false", types.Allocation, "flow_context", FlowContext, "step", "init_slots", "participant_index", p.Index)

		assignedMLNodes := make(map[string]bool)
		var supportedModels []string
		var newMLNodeArrays []*types.ModelMLNodes

		supportedModelsByNode := supportedModelsByNode(hardwareNodes, governanceModels)
		for nodeId, supportedModels := range supportedModelsByNode {
			ma.LogInfo("Supported models by node", types.Allocation, "flow_context", FlowContext, "step", "supported_models_by_node", "node_id", nodeId, "supported_models", supportedModels)
		}

		// For each governance model, pick the available MLNodes that have the model as first supported model
		for _, model := range governanceModels {
			ma.LogInfo("Attempting to assign ML node for model", types.Allocation, "flow_context", FlowContext, "step", "model_assignment_loop", "participant_index", p.Index, "model_id", model.Id)
			var modelMLNodes []*types.MLNodeInfo

			for _, mlNode := range originalMLNodes {
				if assignedMLNodes[mlNode.NodeId] {
					ma.LogInfo("Skipping already assigned ML node", types.Allocation, "flow_context", FlowContext, "step", "node_already_assigned", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					continue
				}

				if slices.Contains(supportedModelsByNode[mlNode.NodeId], model.Id) {
					ma.LogInfo("Found supporting and unassigned ML node for model", types.Allocation, "flow_context", FlowContext, "step", "assign_node_to_model", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					modelMLNodes = append(modelMLNodes, mlNode)
					assignedMLNodes[mlNode.NodeId] = true
				}
			}

			if len(modelMLNodes) > 0 {
				supportedModels = append(supportedModels, model.Id)
				newMLNodeArrays = append(newMLNodeArrays, &types.ModelMLNodes{MlNodes: modelMLNodes})
				ma.LogInfo("Assigned ML nodes to model", types.Allocation, "flow_context", FlowContext, "step", "model_assignment_complete", "participant_index", p.Index, "model_id", model.Id, "assigned_nodes", modelMLNodes)
			} else {
				ma.LogInfo("No available ML nodes support this model", types.Allocation, "flow_context", FlowContext, "step", "no_supporting_nodes", "participant_index", p.Index, "model_id", model.Id)
			}
		}

		var unassignedMLNodes []*types.MLNodeInfo
		for _, mlNode := range originalMLNodes {
			if !assignedMLNodes[mlNode.NodeId] {
				unassignedMLNodes = append(unassignedMLNodes, mlNode)
			}
		}
		ma.LogInfo("Unassigned MLNodes", types.Allocation, "flow_context", FlowContext, "step", "unassigned_nodes", "participant_index", p.Index, "unassigned_nodes", unassignedMLNodes)

		p.MlNodes = newMLNodeArrays
		p.Models = supportedModels
		p.Weight = RecalculateWeight(p)
		ma.LogInfo("Participant models and ML nodes updated", types.Allocation, "flow_context", FlowContext, "step", "participant_updated", "participant_index", p.Index, "supported_models", p.Models, "ml_nodes", p.MlNodes)
	}
	ma.LogInfo("Finished model assignment for all participants", types.Allocation, "flow_context", FlowContext, "step", "model_assignment_complete")
}

func (ma *ModelAssigner) AllocateMLNodesForPoC(ctx context.Context, upcomingEpoch types.Epoch, participants []*types.ActiveParticipant) {
	ma.LogInfo("Starting ML node allocation for PoC slots", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "start", "num_participants", len(participants))

	params, err := ma.keeper.GetParams(ctx)
	if err != nil {
		ma.LogError("AllocateMLNodesForPoC: Unable to get params", types.Allocation, "error", err.Error())
		return
	}
	allocationFraction := params.EpochParams.PocSlotAllocation
	if allocationFraction == nil || allocationFraction.ToDecimal().IsZero() {
		ma.LogInfo("PocSlotAllocation is nil or 0, using default 0.5", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "default_allocation")
		allocationFraction = &types.Decimal{Value: 5, Exponent: -1}
	}

	previousEpochData := NewEpochMLNodeData()

	uniqueModels := make(map[string]bool)
	for _, participant := range participants {
		for _, modelId := range participant.Models {
			uniqueModels[modelId] = true
		}
	}
	ma.LogDebug("Collected unique models", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "collect_unique_models", "num_unique_models", len(uniqueModels))

	sortedModelIds := sortedKeys(uniqueModels)
	if upcomingEpoch.Index > 0 {
		previousEpochIndex := upcomingEpoch.Index - 1
		for _, modelId := range sortedModelIds {
			previousEpochGroupData, found := ma.keeper.GetEpochGroupData(ctx, previousEpochIndex, modelId)
			if found {
				for _, vw := range previousEpochGroupData.ValidationWeights {
					// Use keeper settlement results: zero reward despite having weight => slashed (downtime/confirmation).
					// Settlement was performed before model assignment, so we need to check the settle amount here.
					settle, foundSettle := ma.keeper.GetSettleAmount(ctx, vw.MemberAddress)
					if !foundSettle || settle.EpochIndex != previousEpochIndex || settle.RewardCoins == 0 {
						// Skip participants if they didn't get reward for the previous epoch
						// Only rewarded participants can be eligible for POC_SLOT=true allocation
						// Participants that are not added to previousEpochData will be filtered by filterEligibleMLNodes
						ma.LogInfo("Collecting rewarded participants", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext,
							"step", "filter_rewarded_participants", "participant_without_reward", vw.MemberAddress)
						continue
					}
					dedupedNodes, dedupStats := dedupMLNodesById(vw.MlNodes)
					ma.logMLNodeDedupStats(
						"Duplicate ML nodes detected in previous epoch data",
						dedupStats,
						"flow_context", FlowContext,
						"sub_flow_context", SubFlowContext,
						"step", "dedup_previous_epoch_nodes",
						"model_id", modelId,
						"participant", vw.MemberAddress,
						"epoch_index", previousEpochIndex,
					)
					previousEpochData.Set(modelId, vw.MemberAddress, dedupedNodes)
				}
				ma.LogInfo("Loaded previous epoch data for model", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "load_prev_epoch_data", "model_id", modelId, "num_validation_weights", len(previousEpochGroupData.ValidationWeights))
			}
		}
	}

	totalCurrentEpochWeight := int64(0)
	currentEpochData := NewEpochMLNodeData()
	for _, participant := range participants {
		for modelIdx, modelId := range participant.Models {
			if modelIdx >= len(participant.MlNodes) {
				ma.LogWarn("Model index out of bounds, skipping", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "model_index_oob", "participant_index", participant.Index, "model_id", modelId, "model_idx", modelIdx)
				continue
			}
			if participant.MlNodes[modelIdx] == nil {
				continue
			}
			dedupedNodes, dedupStats := dedupMLNodesById(participant.MlNodes[modelIdx].MlNodes)
			ma.logMLNodeDedupStats(
				"Duplicate ML nodes detected in current epoch data",
				dedupStats,
				"flow_context", FlowContext,
				"sub_flow_context", SubFlowContext,
				"step", "dedup_current_epoch_nodes",
				"model_id", modelId,
				"participant", participant.Index,
			)
			participant.MlNodes[modelIdx].MlNodes = dedupedNodes
			currentEpochData.Set(modelId, participant.Index, dedupedNodes)
		}
		totalCurrentEpochWeight += participant.Weight
	}
	ma.LogInfo("Built current epoch data map", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "build_current_epoch_data", "num_models", len(currentEpochData.Models()))

	// Participants not in previousEpochData (no nodes in previous epoch for a model) cannot be selected as eligible:
	// sampleEligibleParticipantsWithHistory only appends participants that have previousEpochData.GetForParticipant(modelId, addr) != nil.
	eligibleNodesData := ma.filterEligibleMLNodes(upcomingEpoch, previousEpochData, currentEpochData, totalCurrentEpochWeight)
	ma.LogInfo("Filtered eligible nodes for all models", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "filter_all_eligible", "num_models", len(eligibleNodesData.Models()))

	for _, modelId := range sortedModelIds {
		ma.LogInfo("Processing model for PoC allocation", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "model_loop_start", "model_id", modelId)
		ma.allocateMLNodePerPoCForModel(modelId, currentEpochData, eligibleNodesData, allocationFraction)
	}
}

// thresholdSet holds the calculated thresholds for participant and node weight filtering
type thresholdSet struct {
	participantMinNodeWeights map[string]int64 // per-participant minimum node weight (25% rule)
	participantNodeCounts     map[string]int   // per-participant target node count (for uniform weights)
	globalMaxNodeWeight       int64            // global outlier threshold (IQR method)
}

func (ma *ModelAssigner) calculateThresholds(currentEpochData *EpochMLNodeData) thresholdSet {
	allParticipantsWeights := currentEpochData.GetAllParticipantWeights()
	participantWeightThreshold := calculateParticipantWeightThreshold75Percent(allParticipantsWeights)
	ma.LogInfo("Calculated participant weight threshold (75% rule)", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_participant_threshold", "threshold", participantWeightThreshold, "total_participants", len(allParticipantsWeights))

	participantMinNodeWeightThresholds, participantNodeCounts := calculatePerParticipantThreshold(currentEpochData, participantWeightThreshold)
	ma.LogInfo("Calculated per-participant node thresholds (25% rule)", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_per_participant_thresholds", "total_participants", len(participantMinNodeWeightThresholds))

	allNodesWeights := currentEpochData.GetAllIndividualNodeWeights()
	globalMaxNodeWeightThreshold := calculateNodeWeightThresholdIQR(allNodesWeights)
	ma.LogInfo("Calculated node weight threshold (IQR method)", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_node_threshold", "threshold", globalMaxNodeWeightThreshold, "total_nodes", len(allNodesWeights))

	return thresholdSet{
		participantMinNodeWeights: participantMinNodeWeightThresholds,
		participantNodeCounts:     participantNodeCounts,
		globalMaxNodeWeight:       globalMaxNodeWeightThreshold,
	}
}

// filterNodesByThresholds applies effective threshold filtering to nodes for a participant
func filterNodesByThresholds(nodes []*types.MLNodeInfo, participantAddr string, thresholds thresholdSet) []*types.MLNodeInfo {
	threshold := calculateEffectiveNodeThreshold(
		thresholds.participantMinNodeWeights[participantAddr],
		thresholds.globalMaxNodeWeight,
	)
	targetCount := thresholds.participantNodeCounts[participantAddr]
	return filterNodesByWeightAndCount(nodes, threshold, targetCount)
}

func buildEligibleParticipantSet(currentEpochData *EpochMLNodeData, thresholds thresholdSet) map[string]bool {
	eligibleParticipantAddrs := make(map[string]bool)
	for _, modelData := range currentEpochData.data {
		for participantAddr, nodes := range modelData {
			if eligibleParticipantAddrs[participantAddr] {
				continue
			}
			filteredNodes := filterNodesByThresholds(nodes, participantAddr, thresholds)
			if len(filteredNodes) > 0 {
				eligibleParticipantAddrs[participantAddr] = true
			}
		}
	}
	return eligibleParticipantAddrs
}

// filterEligibleMLNodes filters which nodes are eligible for POC_SLOT=true allocation across all models.
//
// PURPOSE:
// Determines which ML nodes can be allocated POC_SLOT=true (serve inference during PoC phase).
// Uses multi-phase filtering to ensure sufficient PoC validation participation while filtering outliers.
//
// FILTERING PHASES:
//
// Phase 1 - Top Participant Participation (75% + 25% rule):
//
//	Ensures participants with top 75% of weight have at least 25% of their nodes participating.
//	Calculates per-participant minimum node weight thresholds to include their top 25% nodes.
//
// Phase 2 - Outlier Node Filtering (IQR method):
//
//	Filters out suspiciously large nodes using statistical outlier detection (Q3 + 1.5*IQR).
//	Prevents single large nodes from dominating the eligible set.
//
// Phase 3 - Voting Constraint Check (<34% non-voting):
//
//	Ensures at least 75% of total capped weight can vote in PoC validation.
//	Tracks participants that become "non-voting" and limits them to <34% of capped total weight.
//
// KEY CONCEPTS:
//   - Eligible node: Can have POC_SLOT=true (serve inference during PoC phase)
//   - Voting participant: Has some nodes with POC_SLOT=false (can participate in PoC validation)
//   - Non-voting participant: All nodes have POC_SLOT=true (cannot participate in PoC validation)
//
// SAMPLING:
//
//	Selects N/2+1 participants with previous epoch history deterministically per model to rotate eligibility.
func (ma *ModelAssigner) filterEligibleMLNodes(
	upcomingEpoch types.Epoch,
	previousEpochData *EpochMLNodeData,
	currentEpochData *EpochMLNodeData,
	totalCappedWeight int64,
) *EpochMLNodeData {
	allParticipantsHashStr := currentEpochData.GetAllParticipantsHash()

	// Step 1: Calculate all thresholds (75% + 25% rule, IQR outlier detection)
	thresholds := ma.calculateThresholds(currentEpochData)

	// Step 2: Build set of eligible participants (those with nodes passing weight thresholds)
	eligibleParticipantAddrs := buildEligibleParticipantSet(currentEpochData, thresholds)
	for participantAddr, eligible := range eligibleParticipantAddrs {
		ma.LogInfo("Eligible participant", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "eligible_participant", "participant", participantAddr, "eligible", eligible)
	}
	ma.LogInfo("Eligible participants", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "participant_min_node_weights", thresholds.participantMinNodeWeights, "global_max_node_weight", thresholds.globalMaxNodeWeight)

	// Step 3: Calculate Phase 3 voting constraint (max 34% non-voting weight)
	maxAllowedNonVotingWeight := decimal.NewFromInt(34).Div(decimal.NewFromInt(100)).Mul(decimal.NewFromInt(totalCappedWeight)).IntPart()
	totalNonVotingWeight := int64(0)
	ma.LogInfo("Calculated voting constraint threshold", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_voting_constraint", "max_allowed_non_voting_weight", maxAllowedNonVotingWeight, "total_capped_weight", totalCappedWeight)

	// Step 4: Apply thresholds and sample participants per model
	eligibleNodesData := NewEpochMLNodeData()
	for _, modelId := range currentEpochData.Models() {
		participantNodes := currentEpochData.GetForModel(modelId)
		sortedParticipantAddrs := sortedKeys(participantNodes)

		var filteredParticipantAddrs []string
		for _, addr := range sortedParticipantAddrs {
			if eligibleParticipantAddrs[addr] {
				filteredParticipantAddrs = append(filteredParticipantAddrs, addr)
			}
		}

		// Sample N/2+1 participants with history for rotation (deterministic per epoch+model)
		eligibleParticipantsPerModel := ma.sampleEligibleParticipantsWithHistory(
			filteredParticipantAddrs,
			previousEpochData,
			modelId,
			upcomingEpoch,
			allParticipantsHashStr,
		)

		for _, participantAddr := range eligibleParticipantsPerModel {
			currentNodes := participantNodes[participantAddr]
			filteredNodes := filterNodesByThresholds(currentNodes, participantAddr, thresholds)

			// Add nodes with Phase 3 voting constraint check
			totalParticipantWeight := currentEpochData.GetParticipantWeight(participantAddr)
			for _, node := range filteredNodes {
				currentParticipantWeight := eligibleNodesData.GetParticipantWeight(participantAddr)
				eligibleNodesWeightIfAdded := currentParticipantWeight + node.PocWeight

				// Phase 3: Check if adding this node would violate voting constraints
				canAllocate, updatedWeight := canAllocateParticipantNode(
					eligibleNodesWeightIfAdded,
					totalParticipantWeight,
					totalNonVotingWeight,
					maxAllowedNonVotingWeight,
				)
				if !canAllocate {
					// Stop adding nodes for this participant - would violate constraints
					ma.LogInfo("Stopped adding nodes due to voting constraint", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "voting_constraint_limit", "participant", participantAddr, "model_id", modelId, "total_non_voting_weight", totalNonVotingWeight, "max_allowed", maxAllowedNonVotingWeight)
					break
				}
				totalNonVotingWeight = updatedWeight
				eligibleNodesData.Append(modelId, participantAddr, node)
			}
		}
	}

	return eligibleNodesData
}

// canAllocateParticipantNode checks if a node can be allocated without violating voting constraints.
//
// VOTING CONSTRAINTS:
// A participant can only vote in PoC validation if they have at least some nodes with POC_SLOT=false.
// If all a participant's nodes have POC_SLOT=true (all in eligible set), they become "non-voting".
//
// To ensure sufficient PoC validation, we limit non-voting participants to <34% of capped total weight.
// This guarantees at least 75% of capped weight can participate in PoC validation.
//
// PARAMETERS:
//   - eligibleNodesWeightIfAdded: Total weight of eligible nodes if we add the current node being considered
//   - totalParticipantWeight: Total weight of all the participant's nodes
//   - totalNonVotingWeight: Current sum of weights for all non-voting participants
//   - maxAllowedNonVotingWeight: Maximum allowed total weight for non-voting participants (34% threshold)
//
// RETURNS:
//   - canAllocate: Whether this node can be added to eligible set
//   - updatedNonVotingWeight: Updated total non-voting weight if node is allocated
func canAllocateParticipantNode(
	eligibleNodesWeightIfAdded, totalParticipantWeight int64,
	totalNonVotingWeight, maxAllowedNonVotingWeight int64,
) (canAllocate bool, updatedNonVotingWeight int64) {
	// Check if adding this node would make all participant's nodes eligible (participant becomes non-voting)
	if eligibleNodesWeightIfAdded >= totalParticipantWeight {
		// Check if adding this participant's weight to non-voting group would exceed 34% threshold
		if totalNonVotingWeight+totalParticipantWeight < maxAllowedNonVotingWeight {
			// Can allocate - participant becomes non-voting but total non-voting weight still under limit
			return true, totalNonVotingWeight + totalParticipantWeight
		}
		// Cannot allocate - would exceed non-voting weight limit
		return false, totalNonVotingWeight
	}
	// Can allocate - participant will still have nodes for voting (POC_SLOT=false)
	return true, totalNonVotingWeight
}

func (ma *ModelAssigner) allocateMLNodePerPoCForModel(
	modelId string,
	currentEpochData *EpochMLNodeData,
	eligibleNodesData *EpochMLNodeData,
	fraction *types.Decimal,
) {
	ma.LogInfo("Starting allocation for model", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "model_allocation_start", "model_id", modelId)

	totalWeight := currentEpochData.GetTotalWeightForModel(modelId)

	fractionDecimal := fraction.ToDecimal()
	targetPoCWeightDecimal := fractionDecimal.Mul(decimal.NewFromInt(totalWeight))
	targetPoCWeight := targetPoCWeightDecimal.IntPart()

	ma.LogInfo("Calculated target weight for model", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_target_weight", "model_id", modelId, "total_weight", totalWeight, "fraction", fractionDecimal.String(), "target_weight", targetPoCWeight)

	eligibleModelNodes := eligibleNodesData.GetForModel(modelId)
	eligibleParticipantAddrs := sortedKeys(eligibleModelNodes)

	ma.LogInfo("Built participant list", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "build_participants", "model_id", modelId, "num_participants", len(eligibleParticipantAddrs))

	if len(eligibleParticipantAddrs) == 0 {
		ma.LogInfo("No participants with eligible nodes for this model", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "no_participants", "model_id", modelId)
		return
	}

	var currentWeight int64
	currentParticipantIdx := 0
	allocatedInRound := false

	for currentWeight < targetPoCWeight {
		participantAddr := eligibleParticipantAddrs[currentParticipantIdx]
		nodes := eligibleNodesData.GetForParticipant(modelId, participantAddr)

		nextMLNode := getSmallestMLNodeWithPOCSLotFalse(nodes)

		if nextMLNode == nil {
			currentParticipantIdx = (currentParticipantIdx + 1) % len(eligibleParticipantAddrs)

			if currentParticipantIdx == 0 {
				if !allocatedInRound {
					ma.LogInfo("Completed full round without allocation, exiting", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "exit_no_nodes", "model_id", modelId, "current_weight", currentWeight, "target_weight", targetPoCWeight)
					break
				}
				allocatedInRound = false
			}
			continue
		}

		nextMLNode.TimeslotAllocation[1] = true
		currentWeight += nextMLNode.PocWeight
		allocatedInRound = true

		ma.LogInfo("Allocated node to PoC slot", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "allocate_node", "model_id", modelId, "participant", participantAddr, "node_id", nextMLNode.NodeId, "node_weight", nextMLNode.PocWeight, "current_weight", currentWeight, "target_weight", targetPoCWeight)

		currentParticipantIdx = (currentParticipantIdx + 1) % len(eligibleParticipantAddrs)

		if currentParticipantIdx == 0 {
			allocatedInRound = false
		}
	}

	for _, participantAddr := range eligibleParticipantAddrs {
		nodes := eligibleNodesData.GetForParticipant(modelId, participantAddr)
		var allocatedCount int
		var allocatedWeight int64
		var allocatedNodeIds []string

		for _, node := range nodes {
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				allocatedCount++
				allocatedWeight += node.PocWeight
				allocatedNodeIds = append(allocatedNodeIds, node.NodeId)
			}
		}

		ma.LogInfo("Participant allocation summary", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "participant_summary", "model_id", modelId, "participant", participantAddr, "total_nodes", len(nodes), "allocated_nodes", allocatedCount, "allocated_weight", allocatedWeight, "allocated_node_ids", allocatedNodeIds)
	}

	ma.LogInfo("Finished allocation for model", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "model_allocation_end", "model_id", modelId, "achieved_weight", currentWeight, "target_weight", targetPoCWeight, "total_weight", totalWeight)
}

func getSmallestMLNodeWithPOCSLotFalse(nodes []*types.MLNodeInfo) *types.MLNodeInfo {
	var smallest *types.MLNodeInfo
	for _, node := range nodes {
		if len(node.TimeslotAllocation) > 1 && !node.TimeslotAllocation[1] {
			if smallest == nil || node.PocWeight < smallest.PocWeight {
				smallest = node
			}
		}
	}
	return smallest
}

// calculateWeightThresholdWithCount calculates both the weight threshold and target node count.
// Returns (threshold, count) where threshold filters nodes and count limits uniform weight selections.
func calculateWeightThresholdWithCount(weights []int64, targetPercent int) (int64, int) {
	if len(weights) == 0 {
		return 0, 0
	}
	if len(weights) == 1 {
		// Single node: choose Option B (0% eligible, 100% voting)
		return weights[0] - 1, 0
	}

	totalWeight := int64(0)
	for _, w := range weights {
		totalWeight += w
	}
	targetWeight := (totalWeight * int64(targetPercent)) / 100

	// Sort descending
	sorted := make([]int64, len(weights))
	copy(sorted, weights)
	slices.SortFunc(sorted, func(a, b int64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	})

	// Accumulate until reaching target
	sum := int64(0)
	nodeCount := 0
	for _, w := range sorted {
		nodeCount++
		sum += w
		if sum >= targetWeight {
			// Check if remaining nodes have the same weight (uniform at cutoff)
			hasLowerWeight := false
			for i := nodeCount; i < len(sorted); i++ {
				if sorted[i] < w {
					hasLowerWeight = true
					break
				}
			}

			// If all remaining weights are same as current weight (uniform at cutoff)
			// Return exact weight and the target node count
			if !hasLowerWeight {
				return w, nodeCount
			}
			return w - 1, 0
		}
	}

	return 0, len(weights)
}

// calculateWeightThreshold calculates minimum weight threshold to reach targetPercent of total weight.
// Returns (w - 1) where w reaches targetPercent. Returns 0 if all weights needed.
// For uniform weights at the cutoff point, returns the exact weight value instead of (w - 1).
func calculateWeightThreshold(weights []int64, targetPercent int) int64 {
	if len(weights) == 0 {
		return 0
	}
	if len(weights) == 1 {
		// Single node: choose Option B (0% eligible, 100% voting)
		// Ensures at least 25% weight preserved for voting
		return weights[0] - 1 // Exclude node (weight > threshold in filter)
	}

	totalWeight := int64(0)
	for _, w := range weights {
		totalWeight += w
	}
	targetWeight := (totalWeight * int64(targetPercent)) / 100

	// Sort descending
	sorted := make([]int64, len(weights))
	copy(sorted, weights)
	slices.SortFunc(sorted, func(a, b int64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	})

	// Accumulate until reaching target
	sum := int64(0)
	nodeCount := 0
	for _, w := range sorted {
		nodeCount++
		sum += w
		if sum >= targetWeight {
			// Check if remaining nodes have the same weight (uniform at cutoff)
			hasLowerWeight := false
			for i := nodeCount; i < len(sorted); i++ {
				if sorted[i] < w {
					hasLowerWeight = true
					break
				}
			}

			// If all remaining weights are same as current weight, return exact value
			// This enables count-based filtering for uniform weights
			if !hasLowerWeight {
				return w
			}
			return w - 1
		}
	}

	return 0
}

// calculateParticipantWeightThreshold75Percent calculates the minimum participant weight threshold
// to ensure participants with top 75% of total weight are included.
//
// Returns the weight threshold such that participants with weight > threshold sum to >= 75% of total weight.
// Returns 0 if all participants are needed (edge cases: 0, 1 participant, or cumulative includes all).
func calculateParticipantWeightThreshold75Percent(weights []int64) int64 {
	return calculateWeightThreshold(weights, 75)
}

// calculatePerParticipantThreshold calculates node weight thresholds for top 75% participants.
// For each participant, ensures top 25% of their nodes (by weight) are included.
// Returns both weight thresholds and target node counts (for uniform weight handling).
func calculatePerParticipantThreshold(epochData *EpochMLNodeData, participantWeightThreshold int64) (map[string]int64, map[string]int) {
	thresholds := make(map[string]int64)
	counts := make(map[string]int)

	uniqueParticipants := make(map[string]bool)
	for _, modelId := range sortedKeys(epochData.data) {
		modelData := epochData.data[modelId]
		for participantAddr := range modelData {
			uniqueParticipants[participantAddr] = true
		}
	}

	for participantAddr := range uniqueParticipants {
		participantWeight := epochData.GetParticipantWeight(participantAddr)

		if participantWeight < participantWeightThreshold {
			continue
		}

		nodeWeights := make([]int64, 0)
		for _, modelId := range sortedKeys(epochData.data) {
			modelData := epochData.data[modelId]
			if nodes, ok := modelData[participantAddr]; ok {
				for _, node := range nodes {
					nodeWeights = append(nodeWeights, node.PocWeight)
				}
			}
		}

		threshold, targetCount := calculateWeightThresholdWithCount(nodeWeights, 25)
		thresholds[participantAddr] = threshold
		counts[participantAddr] = targetCount
	}

	return thresholds, counts
}

// calculateNodeWeightThresholdIQR calculates outlier threshold using IQR method (Q3 + 1.5*IQR).
// Uses integer arithmetic for blockchain determinism.
// Returns 0 when IQR=0 (uniform weights), which means no filtering should be applied.
func calculateNodeWeightThresholdIQR(weights []int64) int64 {
	if len(weights) == 0 {
		return 0
	}
	if len(weights) == 1 {
		return weights[0]
	}

	sortedWeights := make([]int64, len(weights))
	copy(sortedWeights, weights)
	slices.Sort(sortedWeights)

	n := len(sortedWeights)
	q1Index := n / 4
	q3Index := (n * 3) / 4

	if q3Index >= n {
		q3Index = n - 1
	}

	q1 := sortedWeights[q1Index]
	q3 := sortedWeights[q3Index]
	iqr := q3 - q1

	// If IQR is 0, weights are uniform - no outlier filtering needed
	if iqr == 0 {
		return 0
	}

	// 1.5*IQR = IQR + IQR/2
	threshold := q3 + iqr + (iqr / 2)
	threshold = threshold + 1

	return threshold
}

// filterNodesByWeightAndCount filters nodes by weight threshold and optional count limit.
// - threshold=0 means no weight filtering
// - targetCount=0 means no count limiting
// - targetCount>0 means select exactly targetCount nodes (for uniform weights)
// Returns nodes sorted ascending for deterministic allocation.
func filterNodesByWeightAndCount(nodes []*types.MLNodeInfo, threshold int64, targetCount int) []*types.MLNodeInfo {
	filtered := make([]*types.MLNodeInfo, 0, len(nodes))

	// First apply weight filtering
	if threshold == 0 {
		filtered = append(filtered, nodes...)
	} else {
		for _, node := range nodes {
			if node.PocWeight <= threshold {
				filtered = append(filtered, node)
			}
		}
	}

	// Sort ascending for deterministic allocation
	slices.SortFunc(filtered, func(a, b *types.MLNodeInfo) int {
		if a.PocWeight < b.PocWeight {
			return -1
		}
		if a.PocWeight > b.PocWeight {
			return 1
		}
		// For same weight, sort by node ID for determinism
		if a.NodeId < b.NodeId {
			return -1
		}
		if a.NodeId > b.NodeId {
			return 1
		}
		return 0
	})

	// Apply count limit if specified (for uniform weight handling)
	if targetCount > 0 && len(filtered) > targetCount {
		filtered = filtered[:targetCount]
	}

	return filtered
}

func calculateEffectiveNodeThreshold(participantThreshold, globalThreshold int64) int64 {
	if participantThreshold == 0 {
		return globalThreshold
	}
	if globalThreshold == 0 {
		return participantThreshold
	}
	return min(participantThreshold, globalThreshold)
}

// sampleEligibleParticipantsWithHistory selects N/2+1 eligible participants per model.
// Only participants present in previousEpochData for this model can be selected; participants
// who did not work in the previous epoch (not in previousEpochData) are skipped and cannot be eligible.
func (ma *ModelAssigner) sampleEligibleParticipantsWithHistory(
	sortedParticipantAddrs []string,
	previousEpochData *EpochMLNodeData,
	modelId string,
	upcomingEpoch types.Epoch,
	allParticipantsHashStr string,
) []string {
	participantsWithHistory := make([]string, 0)
	for _, participantAddr := range sortedParticipantAddrs {
		previousValidationWeight := previousEpochData.GetForParticipant(modelId, participantAddr)

		if previousValidationWeight == nil {
			continue
		}

		participantsWithHistory = append(participantsWithHistory, participantAddr)
	}

	if len(participantsWithHistory) == 0 || upcomingEpoch.Index == 0 {
		return []string{}
	}

	seed := fmt.Sprintf("filter_%d_%s_%s", upcomingEpoch.Index, allParticipantsHashStr, modelId)
	hash := sha256.Sum256([]byte(seed))
	seedInt := int64(binary.BigEndian.Uint64(hash[:8]))
	rng := rand.New(rand.NewSource(seedInt))

	ma.LogInfo("Generated deterministic seed for participant selection", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "generate_filter_seed", "model_id", modelId, "seed_string", seed, "seed_int", seedInt)

	shuffledParticipants := make([]string, len(participantsWithHistory))
	copy(shuffledParticipants, participantsWithHistory)
	rng.Shuffle(len(shuffledParticipants), func(i, j int) {
		shuffledParticipants[i], shuffledParticipants[j] = shuffledParticipants[j], shuffledParticipants[i]
	})

	numEligible := min(len(sortedParticipantAddrs)/2+1, len(shuffledParticipants))
	eligibleParticipantsPerModel := make([]string, 0, numEligible)
	for i := 0; i < numEligible && i < len(shuffledParticipants); i++ {
		eligibleParticipantsPerModel = append(eligibleParticipantsPerModel, shuffledParticipants[i])
	}

	ma.LogInfo("Selected eligible participants", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "select_eligible_participants", "model_id", modelId, "total_participants", len(participantsWithHistory), "eligible_participants", numEligible)

	return eligibleParticipantsPerModel
}

func supportedModelsByNode(hardwareNodes *types.HardwareNodes, governanceModels []*types.Model) map[string][]string {
	governanceModelsMap := make(map[string]bool)
	for _, model := range governanceModels {
		governanceModelsMap[model.Id] = true
	}

	supportedModelsByNode := make(map[string][]string)
	for _, node := range hardwareNodes.HardwareNodes {
		supportedModels := make([]string, 0)
		for _, model := range node.Models {
			if governanceModelsMap[model] {
				supportedModels = append(supportedModels, model)
			}
		}
		supportedModelsByNode[node.LocalId] = supportedModels
	}

	return supportedModelsByNode
}

func (ma *ModelAssigner) logMLNodeDedupStats(message string, stats map[string]mlNodeDedupDecision, keyvals ...interface{}) {
	if len(stats) == 0 {
		return
	}

	for nodeId, decision := range stats {
		if len(decision.dropped) == 0 {
			continue
		}

		droppedWeights := make([]int64, 0, len(decision.dropped))
		droppedThroughputs := make([]int64, 0, len(decision.dropped))
		for _, dropped := range decision.dropped {
			if dropped == nil {
				continue
			}
			droppedWeights = append(droppedWeights, dropped.PocWeight)
			droppedThroughputs = append(droppedThroughputs, dropped.Throughput)
		}

		fields := append([]interface{}{}, keyvals...)
		fields = append(
			fields,
			"node_id", nodeId,
			"kept_weight", mlNodeWeight(decision.kept),
			"kept_throughput", mlNodeThroughput(decision.kept),
			"dropped_count", len(decision.dropped),
			"dropped_weights", droppedWeights,
			"dropped_throughputs", droppedThroughputs,
		)
		ma.LogWarn(message, types.Allocation, fields...)
	}
}

func mlNodeWeight(node *types.MLNodeInfo) int64 {
	if node == nil {
		return 0
	}
	return node.PocWeight
}

func mlNodeThroughput(node *types.MLNodeInfo) int64 {
	if node == nil {
		return 0
	}
	return node.Throughput
}
