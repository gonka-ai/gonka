package inference

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"slices"

	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"github.com/shopspring/decimal"
)

const (
	FlowContext    = "model_assignment"
	SubFlowContext = "sample_preserved_for_episode"
)

func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedStoreCommitKeys[V any](m map[types.PoCParticipantModelKey]V) []types.PoCParticipantModelKey {
	keys := make([]types.PoCParticipantModelKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b types.PoCParticipantModelKey) int {
		return cmp.Or(
			cmp.Compare(a.ModelID, b.ModelID),
			cmp.Compare(a.ParticipantAddress, b.ParticipantAddress),
		)
	})
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

// GetForModel returns a copy of the participant->nodes map for the given
// model. The outer map and the inner slice headers are fresh, but the
// *MLNodeInfo pointers are shared with the source so callers that mutate
// fields on a node (e.g. TimeslotAllocation) still see the change in any
// future call. Callers cannot accidentally mutate the source by appending
// to or reassigning the returned slices.
func (e *EpochMLNodeData) GetForModel(modelId string) map[string][]*types.MLNodeInfo {
	src := e.data[modelId]
	if src == nil {
		return nil
	}
	out := make(map[string][]*types.MLNodeInfo, len(src))
	for addr, nodes := range src {
		out[addr] = slices.Clone(nodes)
	}
	return out
}

// GetForParticipant returns a sorted copy of the node slice for a given
// (model, participant). The clone is sorted in-place; the source slice is
// untouched. Pointer identity per *MLNodeInfo is preserved.
func (e *EpochMLNodeData) GetForParticipant(modelId, participantAddr string) []*types.MLNodeInfo {
	if e.data[modelId] == nil {
		return nil
	}
	nodes := slices.Clone(e.data[modelId][participantAddr])
	sortMLNodesByNodeId(nodes)
	return nodes
}

func (e *EpochMLNodeData) Models() []string {
	return sortedKeys(e.data)
}

// ForModel returns a single-model copy of the data.
// Threshold calculations use this to avoid comparing weights across models.
// The returned EpochMLNodeData has its own outer/inner maps and slice
// headers; *MLNodeInfo pointers are shared with the source so node-field
// mutations remain visible.
func (e *EpochMLNodeData) ForModel(modelId string) *EpochMLNodeData {
	view := NewEpochMLNodeData()
	if modelData, ok := e.data[modelId]; ok {
		modelDataCopy := make(map[string][]*types.MLNodeInfo, len(modelData))
		for addr, nodes := range modelData {
			modelDataCopy[addr] = slices.Clone(nodes)
		}
		view.data[modelId] = modelDataCopy
	}
	return view
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

// GetParticipantModelNodes returns model-grouped nodes for one participant.
// Shape matches keeper.CoefficientAdjustedWeight input.
func (e *EpochMLNodeData) GetParticipantModelNodes(participantAddr string) map[string][]*types.MLNodeInfo {
	result := make(map[string][]*types.MLNodeInfo)
	for _, modelId := range sortedKeys(e.data) {
		modelData := e.data[modelId]
		if nodes, ok := modelData[participantAddr]; ok {
			result[modelId] = nodes
		}
	}
	return result
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
	GetEpochPerformanceSummary(ctx context.Context, epochIndex uint64, participantId string) (val types.EpochPerformanceSummary, found bool)
	GetParams(ctx context.Context) (types.Params, error)
	GetGenesisGuardianAddresses(ctx context.Context) []string
	GetRootGroupDataWithLiveMembers(ctx context.Context) (types.EpochGroupData, map[string]bool, error)
	GetLiveSubGroupsForCurrentEpoch(ctx context.Context) (map[string]types.EpochGroupData, map[string]map[string]bool, error)
}

func sumLiveRootTotalWeight(rootData types.EpochGroupData, liveRootSet map[string]bool) int64 {
	var total int64
	for _, vw := range rootData.ValidationWeights {
		if vw == nil || (liveRootSet != nil && !liveRootSet[vw.MemberAddress]) {
			continue
		}
		total += vw.Weight
	}
	return total
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
		// TODO: should we do that? does it makes sense to rely on hardware nodes in general?
		// Seems like makes pipeline complicated
		if !found {
			// Hardware not registered yet (e.g. genesis bootstrap race).
			// Keep per-model assignments from Calculator -- the participant proved
			// compute for those models. Only initialize TimeslotAllocation.
			ma.LogInfo("No hardware nodes found, keeping Calculator assignments", types.Allocation,
				"flow_context", FlowContext, "step", "no_hardware_nodes",
				"participant_index", p.Index, "models", p.Models)
			for _, modelNodes := range p.MlNodes {
				if modelNodes != nil {
					for _, mlNode := range modelNodes.MlNodes {
						mlNode.TimeslotAllocation = []bool{true, false}
					}
				}
			}
			continue
		}

		var originalMLNodes []*types.MLNodeInfo
		for _, modelNodes := range p.MlNodes {
			if modelNodes != nil {
				originalMLNodes = append(originalMLNodes, modelNodes.MlNodes...)
			}
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
		}

		for _, mlNode := range originalMLNodes {
			mlNode.TimeslotAllocation = []bool{true, false} // [PRE_POC_SLOT, POC_SLOT]
		}
		ma.LogInfo("Initialized all ML nodes to PRE_POC_SLOT=true, POC_SLOT=false", types.Allocation, "flow_context", FlowContext, "step", "init_slots", "participant_index", p.Index)

		assignedMLNodes := make(map[string]bool)
		var supportedModels []string
		var newMLNodeArrays []*types.ModelMLNodes

		supportedModelsByNode := supportedModelsByNode(hardwareNodes, governanceModels)
		for _, nodeId := range sortedKeys(supportedModelsByNode) {
			supportedModels := supportedModelsByNode[nodeId]
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
		ma.LogInfo("Participant models and ML nodes updated", types.Allocation, "flow_context", FlowContext, "step", "participant_updated", "participant_index", p.Index, "supported_models", p.Models, "ml_nodes", p.MlNodes)
	}
	ma.LogInfo("Finished model assignment for all participants", types.Allocation, "flow_context", FlowContext, "step", "model_assignment_complete")
}

// SamplePreservedForEpisode returns the preserved-node snapshot for a single PoC episode.
// The seed mixes in anchorHeight so each episode in the same epoch samples independently.
// Participants removed mid-epoch are excluded via live root/subgroup member sets,
// symmetric with getEffectiveValidationBaseState.
func (ma *ModelAssigner) SamplePreservedForEpisode(
	ctx context.Context,
	epoch types.Epoch,
	anchorHeight int64,
) (types.PreservedNodesSnapshot, error) {
	params, err := ma.keeper.GetParams(ctx)
	if err != nil {
		return types.PreservedNodesSnapshot{}, err
	}
	allocationFraction := params.EpochParams.PocSlotAllocation
	if allocationFraction == nil || allocationFraction.ToDecimal().IsZero() {
		ma.LogInfo("PocSlotAllocation is nil or 0, using default 0.5", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "default_allocation")
		allocationFraction = &types.Decimal{Value: 5, Exponent: -1}
	}

	rootData, liveRootSet, err := ma.keeper.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return types.PreservedNodesSnapshot{EpisodeAnchorHeight: anchorHeight}, nil
	}
	liveRootTotalWeight := sumLiveRootTotalWeight(rootData, liveRootSet)

	guardianAddresses := ma.keeper.GetGenesisGuardianAddresses(ctx)
	guardianSet := make(map[string]bool, len(guardianAddresses))
	for _, addr := range guardianAddresses {
		accAddr, err := utils.OperatorAddressToAccAddress(addr)
		if err != nil {
			ma.LogWarn("SamplePreservedForEpisode: Failed to convert guardian address", types.Allocation,
				"operatorAddress", addr, "error", err)
			continue
		}
		guardianSet[accAddr] = true
	}

	sortedModelIds := slices.Clone(rootData.SubGroupModels)
	slices.Sort(sortedModelIds)

	currentEpochData := NewEpochMLNodeData()
	previousEpochData := NewEpochMLNodeData()
	participantVotingPowers := make(map[string]map[string]int64)

	subGroupDataByModel, liveSetsByModel, subErr := ma.keeper.GetLiveSubGroupsForCurrentEpoch(ctx)
	if subErr != nil {
		ma.LogWarn("SamplePreservedForEpisode: unable to fetch live subgroups for current epoch",
			types.Allocation, "error", subErr)
	}

	for _, modelId := range sortedModelIds {
		currentSubData := subGroupDataByModel[modelId]
		liveSubSet := liveSetsByModel[modelId]
		if len(currentSubData.ValidationWeights) > 0 {
			for _, vw := range currentSubData.ValidationWeights {
				if liveSubSet != nil && !liveSubSet[vw.MemberAddress] {
					continue
				}
				dedupedNodes, dedupStats := dedupMLNodesById(vw.MlNodes)
				ma.logMLNodeDedupStats(
					"Duplicate ML nodes detected in current epoch subgroup",
					dedupStats,
					"flow_context", FlowContext,
					"sub_flow_context", SubFlowContext,
					"step", "dedup_current_subgroup_nodes",
					"model_id", modelId,
					"participant", vw.MemberAddress,
					"epoch_index", epoch.Index,
				)
				currentEpochData.Set(modelId, vw.MemberAddress, dedupedNodes)
				if vw.VotingPower > 0 {
					if participantVotingPowers[modelId] == nil {
						participantVotingPowers[modelId] = make(map[string]int64)
					}
					participantVotingPowers[modelId][vw.MemberAddress] = vw.VotingPower
				}
			}
		}

		if epoch.Index > 0 {
			previousEpochIndex := epoch.Index - 1
			prevSubData, foundPrev := ma.keeper.GetEpochGroupData(ctx, previousEpochIndex, modelId)
			if foundPrev {
				for _, vw := range prevSubData.ValidationWeights {
					// EpochPerformanceSummary persists across reward claims (SettleAmount does not),
					// so this check remains stable no matter how many blocks after settlement the
					// sampler runs.
					summary, found := ma.keeper.GetEpochPerformanceSummary(ctx, previousEpochIndex, vw.MemberAddress)
					if !found || summary.RewardedCoins == 0 {
						continue
					}
					dedupedNodes, _ := dedupMLNodesById(vw.MlNodes)
					previousEpochData.Set(modelId, vw.MemberAddress, dedupedNodes)
				}
			}
		}
	}

	eligibleNodesData := ma.filterEligibleMLNodes(epoch, previousEpochData, currentEpochData, liveRootTotalWeight, anchorHeight, guardianSet, participantVotingPowers)

	modelPreservedNodes := make([]*types.ModelPreservedNodes, 0, len(sortedModelIds))
	for _, modelId := range sortedModelIds {
		preservedByParticipant := ma.samplePreservedForModel(modelId, currentEpochData, eligibleNodesData, allocationFraction)
		if len(preservedByParticipant) == 0 {
			continue
		}
		participantAddrs := make([]string, 0, len(preservedByParticipant))
		for addr := range preservedByParticipant {
			participantAddrs = append(participantAddrs, addr)
		}
		slices.Sort(participantAddrs)

		participantEntries := make([]*types.ParticipantPreservedNodes, 0, len(participantAddrs))
		for _, addr := range participantAddrs {
			nodeIdSet := preservedByParticipant[addr]
			nodeIds := make([]string, 0, len(nodeIdSet))
			for id := range nodeIdSet {
				nodeIds = append(nodeIds, id)
			}
			slices.Sort(nodeIds)
			participantEntries = append(participantEntries, &types.ParticipantPreservedNodes{
				ParticipantId: addr,
				NodeIds:       nodeIds,
			})
		}
		modelPreservedNodes = append(modelPreservedNodes, &types.ModelPreservedNodes{
			ModelId:      modelId,
			Participants: participantEntries,
		})
	}

	return types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: anchorHeight,
		ModelPreservedNodes: modelPreservedNodes,
	}, nil
}

// thresholdSet holds the calculated thresholds for participant and node weight filtering
type thresholdSet struct {
	participantMinNodeWeights map[string]int64 // per-participant minimum node weight (25% rule)
	participantNodeCounts     map[string]int   // per-participant target node count (for uniform weights)
	globalMaxNodeWeight       int64            // global outlier threshold (IQR method)
}

func (ma *ModelAssigner) calculateThresholds(currentEpochData *EpochMLNodeData, participantVotingPowersPerModel map[string]int64, liveRootTotalWeight int64) thresholdSet {
	allParticipantsWeights := getParticipantWeightsForThreshold(currentEpochData, participantVotingPowersPerModel)
	participantWeightThreshold := calculateParticipantWeightThreshold75Percent(allParticipantsWeights, liveRootTotalWeight)
	ma.LogInfo("Calculated participant weight threshold (75% rule)", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_participant_threshold", "threshold", participantWeightThreshold, "total_participants", len(allParticipantsWeights))

	participantMinNodeWeightThresholds, participantNodeCounts := calculatePerParticipantThreshold(currentEpochData, participantWeightThreshold, participantVotingPowersPerModel)
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

// filterEligibleMLNodes returns the set of nodes that can be preserved, per model.
//
// Thresholds are computed per model: raw PocWeights across different models are not
// comparable, so applying a single global threshold would bias preservation toward models
// with larger weight scales.
//
// The non-voting cap uses the same units as PoC validation: per-model voting
// power over live root total weight.
func (ma *ModelAssigner) filterEligibleMLNodes(
	upcomingEpoch types.Epoch,
	previousEpochData *EpochMLNodeData,
	currentEpochData *EpochMLNodeData,
	liveRootTotalWeight int64,
	anchorHeight int64,
	guardianSet map[string]bool,
	participantVotingPowers map[string]map[string]int64,
) *EpochMLNodeData {
	allParticipantsHashStr := currentEpochData.GetAllParticipantsHash()

	maxNonVotingVP := decimal.NewFromInt(34).Div(decimal.NewFromInt(100)).Mul(decimal.NewFromInt(liveRootTotalWeight)).IntPart()
	ma.LogInfo("Calculated voting constraint threshold", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "calculate_voting_constraint", "max_non_voting_vp", maxNonVotingVP, "live_root_total_weight", liveRootTotalWeight)

	eligibleNodesData := NewEpochMLNodeData()
	for _, modelId := range currentEpochData.Models() {
		nonVotingVPForModel := int64(0)
		modelView := currentEpochData.ForModel(modelId)
		participantVotingPowersPerModel := participantVotingPowers[modelId]
		thresholds := ma.calculateThresholds(modelView, participantVotingPowersPerModel, liveRootTotalWeight)
		ma.LogInfo("Calculated per-model thresholds", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext,
			"step", "per_model_thresholds", "model_id", modelId,
			"participant_min_node_weights", thresholds.participantMinNodeWeights,
			"global_max_node_weight", thresholds.globalMaxNodeWeight)

		participantNodes := currentEpochData.GetForModel(modelId)
		sortedParticipantAddrs := sortedKeys(participantNodes)

		// Build eligible set for this model using per-model thresholds
		var filteredParticipantAddrs []string
		for _, addr := range sortedParticipantAddrs {
			if guardianSet[addr] {
				continue
			}
			nodes := filterNodesByThresholds(participantNodes[addr], addr, thresholds)
			if len(nodes) > 0 {
				filteredParticipantAddrs = append(filteredParticipantAddrs, addr)
			}
		}

		// Sample N/2+1 participants with history for rotation (deterministic per epoch+anchor+model)
		eligibleParticipantsPerModel := ma.sampleEligibleParticipantsWithHistory(
			filteredParticipantAddrs,
			previousEpochData,
			modelId,
			upcomingEpoch,
			allParticipantsHashStr,
			anchorHeight,
		)

		for _, participantAddr := range eligibleParticipantsPerModel {
			currentNodes := participantNodes[participantAddr]
			filteredNodes := filterNodesByThresholds(currentNodes, participantAddr, thresholds)

			var participantModelWeight int64
			for _, n := range currentNodes {
				if n != nil {
					participantModelWeight += n.PocWeight
				}
			}
			participantVP := participantVotingPowersPerModel[participantAddr]
			eligibleParticipantModelWeight := int64(0)

			for _, node := range filteredNodes {
				eligibleParticipantModelWeight += node.PocWeight

				canAllocate, updatedNonVotingVP := canAllocateParticipantNode(
					eligibleParticipantModelWeight,
					participantModelWeight,
					nonVotingVPForModel,
					participantVP,
					maxNonVotingVP,
				)
				if !canAllocate {
					ma.LogInfo("Stopped adding nodes due to voting constraint", types.Allocation, "flow_context", FlowContext, "sub_flow_context", SubFlowContext, "step", "voting_constraint_limit", "participant", participantAddr, "model_id", modelId, "non_voting_vp_for_model", nonVotingVPForModel, "max_non_voting_vp", maxNonVotingVP)
					break
				}
				nonVotingVPForModel = updatedNonVotingVP
				eligibleNodesData.Append(modelId, participantAddr, node)
			}
		}
	}

	return eligibleNodesData
}

// canAllocateParticipantNode updates non-voting VP if this node would make all
// of the participant's model nodes eligible for preservation.
func canAllocateParticipantNode(
	eligibleParticipantModelWeight, participantModelWeight int64,
	nonVotingVPForModel, participantVP, maxNonVotingVP int64,
) (canAllocate bool, updatedNonVotingVP int64) {
	if eligibleParticipantModelWeight < participantModelWeight {
		return true, nonVotingVPForModel
	}
	if nonVotingVPForModel+participantVP < maxNonVotingVP {
		return true, nonVotingVPForModel + participantVP
	}
	return false, nonVotingVPForModel
}

// samplePreservedForModel runs a round-robin allocator over eligible nodes for one
// model and returns preserved node IDs grouped by participant. Pure: no mutation of inputs.
func (ma *ModelAssigner) samplePreservedForModel(
	modelId string,
	currentEpochData *EpochMLNodeData,
	eligibleNodesData *EpochMLNodeData,
	fraction *types.Decimal,
) map[string]map[string]struct{} {
	allocated := make(map[string]map[string]struct{})

	totalWeight := currentEpochData.GetTotalWeightForModel(modelId)
	targetPoCWeight := fraction.ToDecimal().Mul(decimal.NewFromInt(totalWeight)).IntPart()

	eligibleParticipantAddrs := sortedKeys(eligibleNodesData.GetForModel(modelId))
	if len(eligibleParticipantAddrs) == 0 {
		return allocated
	}

	var currentWeight int64
	var totalAllocated int
	currentParticipantIdx := 0
	allocatedInRound := false

	for currentWeight < targetPoCWeight {
		participantAddr := eligibleParticipantAddrs[currentParticipantIdx]
		nodes := eligibleNodesData.GetForParticipant(modelId, participantAddr)

		nextMLNode := getSmallestUnallocatedMLNode(nodes, allocated[participantAddr])
		if nextMLNode == nil {
			currentParticipantIdx = (currentParticipantIdx + 1) % len(eligibleParticipantAddrs)
			if currentParticipantIdx == 0 {
				if !allocatedInRound {
					break
				}
				allocatedInRound = false
			}
			continue
		}

		participantAllocated := allocated[participantAddr]
		if participantAllocated == nil {
			participantAllocated = make(map[string]struct{})
			allocated[participantAddr] = participantAllocated
		}
		participantAllocated[nextMLNode.NodeId] = struct{}{}
		currentWeight += nextMLNode.PocWeight
		totalAllocated++
		allocatedInRound = true

		currentParticipantIdx = (currentParticipantIdx + 1) % len(eligibleParticipantAddrs)
		if currentParticipantIdx == 0 {
			allocatedInRound = false
		}
	}

	ma.LogInfo("samplePreservedForModel", types.Allocation,
		"model_id", modelId,
		"total_weight", totalWeight,
		"target_weight", targetPoCWeight,
		"achieved_weight", currentWeight,
		"num_preserved", totalAllocated)
	return allocated
}

func getSmallestUnallocatedMLNode(nodes []*types.MLNodeInfo, allocated map[string]struct{}) *types.MLNodeInfo {
	var smallest *types.MLNodeInfo
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if _, ok := allocated[node.NodeId]; ok {
			continue
		}
		if smallest == nil || node.PocWeight < smallest.PocWeight {
			smallest = node
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

// weightThresholdForTarget returns the minimum weight cutoff so participants with
// weight > threshold sum to at least targetWeight.
func weightThresholdForTarget(weights []int64, targetWeight int64) int64 {
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

	sum := int64(0)
	nodeCount := 0
	for _, w := range sorted {
		nodeCount++
		sum += w
		if sum >= targetWeight {
			hasLowerWeight := false
			for i := nodeCount; i < len(sorted); i++ {
				if sorted[i] < w {
					hasLowerWeight = true
					break
				}
			}
			if !hasLowerWeight {
				return w
			}
			return w - 1
		}
	}
	return 0
}

// calculateWeightThreshold calculates minimum weight threshold to reach targetPercent of total weight.
// Returns (w - 1) where w reaches targetPercent. Returns 0 if all weights needed.
// For uniform weights at the cutoff point, returns the exact weight value instead of (w - 1).
func calculateWeightThreshold(weights []int64, targetPercent int) int64 {
	if len(weights) == 0 {
		return 0
	}
	if len(weights) == 1 {
		return weights[0] - 1
	}
	totalWeight := int64(0)
	for _, w := range weights {
		totalWeight += w
	}
	return weightThresholdForTarget(weights, (totalWeight*int64(targetPercent))/100)
}

// getParticipantWeightsForThreshold returns voting-power-based weights for the 75% threshold.
// Uses the same delegation-resolved VotingPower that PoC/CPoC validation uses.
func getParticipantWeightsForThreshold(epochData *EpochMLNodeData, participantVotingPowers map[string]int64) []int64 {
	uniqueParticipants := make(map[string]bool)
	for _, modelId := range sortedKeys(epochData.data) {
		modelData := epochData.data[modelId]
		for _, participantAddr := range sortedKeys(modelData) {
			uniqueParticipants[participantAddr] = true
		}
	}

	weights := make([]int64, 0, len(uniqueParticipants))
	for _, addr := range sortedKeys(uniqueParticipants) {
		w := participantVotingPowers[addr]
		weights = append(weights, w)
	}
	return weights
}

// calculateParticipantWeightThreshold75Percent ranks by per-model VP but targets
// 75% of live root total weight. Returns 0 when the model cannot reach that target.
func calculateParticipantWeightThreshold75Percent(weights []int64, liveRootTotalWeight int64) int64 {
	if len(weights) == 0 || liveRootTotalWeight <= 0 {
		return 0
	}
	targetWeight := liveRootTotalWeight * 75 / 100
	if len(weights) == 1 {
		if weights[0] < targetWeight {
			return 0
		}
		return weights[0] - 1
	}
	modelVPTotal := int64(0)
	for _, w := range weights {
		modelVPTotal += w
	}
	if modelVPTotal < targetWeight {
		return 0
	}
	return weightThresholdForTarget(weights, targetWeight)
}

// calculatePerParticipantThreshold calculates node weight thresholds for top 75% participants.
// For each participant, ensures top 25% of their nodes (by weight) are included.
// Returns both weight thresholds and target node counts (for uniform weight handling).
// Uses participantVotingPowers (delegation-resolved) for the 75% threshold comparison.
func calculatePerParticipantThreshold(epochData *EpochMLNodeData, participantWeightThreshold int64, participantVotingPowers map[string]int64) (map[string]int64, map[string]int) {
	thresholds := make(map[string]int64)
	counts := make(map[string]int)

	uniqueParticipants := make(map[string]bool)
	for _, modelId := range sortedKeys(epochData.data) {
		modelData := epochData.data[modelId]
		for _, participantAddr := range sortedKeys(modelData) {
			uniqueParticipants[participantAddr] = true
		}
	}

	for _, participantAddr := range sortedKeys(uniqueParticipants) {
		effectiveWeight := participantVotingPowers[participantAddr]
		if effectiveWeight < participantWeightThreshold {
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
	anchorHeight int64,
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

	// Episode anchor height is mixed into the seed so each PoC episode (regular and each
	// confirmation event) produces an independent sample, preserving late-binding.
	seed := fmt.Sprintf("filter_%d_%d_%s_%s", upcomingEpoch.Index, anchorHeight, allParticipantsHashStr, modelId)
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
