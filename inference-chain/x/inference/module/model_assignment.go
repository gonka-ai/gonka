package inference

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"slices"

	"github.com/productscience/inference/x/inference/types"
)

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
}

func (ma *ModelAssigner) setModelsForParticipants(ctx context.Context, participants []*types.ActiveParticipant, upcomingEpoch types.Epoch) {
	// TODO: We may need to populate throughput in MLNodeInfo using the model's ThroughputPerNonce
	// This would ensure consistent throughput calculations based on governance model parameters
	// rather than relying on hardware node declarations alone.
	const flowContext = "model_assignment"
	ma.LogInfo("Starting model and slot assignment for participants", types.EpochGroup, "flow_context", flowContext, "step", "start", "num_participants", len(participants), "epoch_index", upcomingEpoch.Index)

	// Get governance models to iterate through
	governanceModels, err := ma.keeper.GetGovernanceModelsSorted(ctx)
	if err != nil {
		ma.LogError("setModelsForParticipants: Unable to get governance models", types.EpochGroup, "error", err.Error(), "flow_context", flowContext)
		return
	}
	ma.LogInfo("Retrieved governance models", types.EpochGroup, "flow_context", flowContext, "step", "get_governance_models", "num_models", len(governanceModels))

	preservedNodes := ma.getPreservedNodes(ctx, upcomingEpoch.Index)
	for _, p := range participants {
		ma.LogInfo("Processing participant", types.EpochGroup, "flow_context", flowContext, "step", "participant_loop_start", "participant_index", p.Index)
		hardwareNodes, found := ma.keeper.GetHardwareNodes(ctx, p.Index)
		if !found {
			// No hardware nodes - just set empty arrays
			ma.LogInfo("No hardware nodes found for participant, skipping model assignment.", types.EpochGroup, "flow_context", flowContext, "step", "no_hardware_nodes", "participant_index", p.Index)
			p.Models = make([]string, 0)
			p.MlNodes = make([]*types.ModelMLNodes, 0)
			continue
		}

		// Get the original MLNodes from the first array (index 0) - populated by task 5.8
		var originalMLNodes []*types.MLNodeInfo
		if len(p.MlNodes) > 0 && p.MlNodes[0] != nil {
			originalMLNodes = p.MlNodes[0].MlNodes
		}
		ma.LogInfo("Original ML nodes before legacy weight distribution", types.EpochGroup, "flow_context", flowContext, "step", "pre_legacy_distribution", "participant_index", p.Index, "ml_nodes", originalMLNodes)

		// Handle legacy PoC weight distribution for batches without NodeId
		originalMLNodes = ma.distributeLegacyWeight(originalMLNodes, hardwareNodes, preservedNodes[p.Index])
		ma.LogInfo("ML nodes after legacy weight distribution", types.EpochGroup, "flow_context", flowContext, "step", "post_legacy_distribution", "participant_index", p.Index, "ml_nodes", originalMLNodes)

		// Set PRE_POC_SLOT to true and POC_SLOT to false for all MLNodes (default to mining PoC)
		for _, mlNode := range originalMLNodes {
			// Initialize timeslot allocation vector: [PRE_POC_SLOT=true, POC_SLOT=false]
			mlNode.TimeslotAllocation = []bool{true, false} // index 0=PRE_POC_SLOT, index 1=POC_SLOT
		}
		ma.LogInfo("Initialized all ML nodes to PRE_POC_SLOT=true, POC_SLOT=false", types.EpochGroup, "flow_context", flowContext, "step", "init_slots", "participant_index", p.Index)

		// Track which MLNodes have been assigned
		assignedMLNodes := make(map[string]bool)
		var supportedModels []string
		var newMLNodeArrays []*types.ModelMLNodes

		supportedModelsByNode := supportedModelsByNode(hardwareNodes, governanceModels)

		// For each governance model, pick the available MLNodes that have the model as first supported model
		for _, model := range governanceModels {
			ma.LogInfo("Attempting to assign ML node for model", types.EpochGroup, "flow_context", flowContext, "step", "model_assignment_loop", "participant_index", p.Index, "model_id", model.Id)
			var modelMLNodes []*types.MLNodeInfo

			for _, mlNode := range originalMLNodes {
				if assignedMLNodes[mlNode.NodeId] {
					ma.LogInfo("Skipping already assigned ML node", types.EpochGroup, "flow_context", flowContext, "step", "node_already_assigned", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					continue // MLNode already assigned to another model
				}

				// Check if this MLNode supports the current governance model
				if slices.Contains(supportedModelsByNode[mlNode.NodeId], model.Id) {
					ma.LogInfo("Found supporting and unassigned ML node for model", types.EpochGroup, "flow_context", flowContext, "step", "assign_node_to_model", "participant_index", p.Index, "model_id", model.Id, "node_id", mlNode.NodeId)
					// Add this MLNode to the current model's array
					modelMLNodes = append(modelMLNodes, mlNode)
					assignedMLNodes[mlNode.NodeId] = true
				}
			}

			// Only add the model and MLNode array if we found supporting MLNodes
			if len(modelMLNodes) > 0 {
				supportedModels = append(supportedModels, model.Id)
				newMLNodeArrays = append(newMLNodeArrays, &types.ModelMLNodes{MlNodes: modelMLNodes})
				ma.LogInfo("Assigned ML nodes to model", types.EpochGroup, "flow_context", flowContext, "step", "model_assignment_complete", "participant_index", p.Index, "model_id", model.Id, "assigned_nodes", modelMLNodes)
			} else {
				ma.LogInfo("No available ML nodes support this model", types.EpochGroup, "flow_context", flowContext, "step", "no_supporting_nodes", "participant_index", p.Index, "model_id", model.Id)
			}
		}

		// Add remaining unassigned MLNodes as overflow array (if any exist)
		var unassignedMLNodes []*types.MLNodeInfo
		for _, mlNode := range originalMLNodes {
			if !assignedMLNodes[mlNode.NodeId] {
				unassignedMLNodes = append(unassignedMLNodes, mlNode)
			}
		}
		if len(unassignedMLNodes) > 0 {
			newMLNodeArrays = append(newMLNodeArrays, &types.ModelMLNodes{MlNodes: unassignedMLNodes})
			ma.LogInfo("Added unassigned ML nodes to overflow array", types.EpochGroup, "flow_context", flowContext, "step", "overflow_nodes", "participant_index", p.Index, "unassigned_nodes", unassignedMLNodes)
		}

		// Update participant with reorganized MLNode arrays and supported models
		p.MlNodes = newMLNodeArrays
		p.Models = supportedModels
		ma.LogInfo("Participant models and ML nodes updated before 50% allocation", types.EpochGroup, "flow_context", flowContext, "step", "pre_50_percent_alloc", "participant_index", p.Index, "supported_models", p.Models, "ml_nodes", p.MlNodes)

		// Task 6.2.2: Apply 50% weight allocation logic
		ma.apply50PercentWeightAllocation(upcomingEpoch, p, supportedModels)
		ma.LogInfo("Finished 50% weight allocation", types.EpochGroup, "flow_context", flowContext, "step", "post_50_percent_alloc", "participant_index", p.Index, "final_ml_nodes", p.MlNodes)
	}
	ma.LogInfo("Finished model and slot assignment for all participants", types.EpochGroup, "flow_context", flowContext, "step", "end")
}

// apply50PercentWeightAllocation implements the 50% node allocation logic for PoC slots
// For each model, at most 50% of nodes (with floor rounding) will serve inference
func (ma *ModelAssigner) apply50PercentWeightAllocation(upcomingEpoch types.Epoch, participant *types.ActiveParticipant, supportedModels []string) {
	const flowContext = "model_assignment"
	const subFlowContext = "apply_50_percent_allocation"
	ma.LogInfo("Starting 50% node allocation for PoC slots", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "start", "participant_index", participant.Index)
	// Process each model separately
	for modelIdx, modelId := range supportedModels {
		ma.LogInfo("Processing model for 50% allocation", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "model_loop_start", "participant_index", participant.Index, "model_id", modelId)
		if modelIdx >= len(participant.MlNodes) {
			ma.LogInfo("Model index is out of bounds, skipping", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "model_index_oob", "participant_index", participant.Index, "model_id", modelId, "model_idx", modelIdx)
			continue // Skip if model index is out of bounds
		}

		modelMLNodes := participant.MlNodes[modelIdx].MlNodes
		if len(modelMLNodes) == 0 {
			ma.LogInfo("No ML nodes for this model, skipping allocation", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "no_ml_nodes", "participant_index", participant.Index, "model_id", modelId)
			continue
		}

		// Create deterministic random seed from epoch ID, participant address, and model ID
		seed := fmt.Sprintf("%d_%s_%s", upcomingEpoch.Index, participant.Index, modelId)
		hash := sha256.Sum256([]byte(seed))
		seedInt := int64(binary.BigEndian.Uint64(hash[:8]))
		ma.LogInfo("Generated deterministic seed for random shuffling", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "generate_seed", "participant_index", participant.Index, "model_id", modelId, "seed_string", seed, "seed_int", seedInt)

		// Create random generator with deterministic seed for this model
		rng := rand.New(rand.NewSource(seedInt))

		// Create shuffled node indices for deterministic random order
		nodeIndices := make([]int, len(modelMLNodes))
		for i := range nodeIndices {
			nodeIndices[i] = i
		}
		rng.Shuffle(len(nodeIndices), func(i, j int) {
			nodeIndices[i], nodeIndices[j] = nodeIndices[j], nodeIndices[i]
		})
		ma.LogInfo("Shuffled node indices for model", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "shuffle_nodes", "participant_index", participant.Index, "model_id", modelId, "shuffled_indices", nodeIndices)

		// Calculate how many nodes can serve inference (at most 50% with floor rounding)
		totalNodes := len(modelMLNodes)
		nodesToInference := totalNodes / 2 // This gives us floor(totalNodes / 2)
		ma.LogInfo("Calculated node allocation for inference", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "calculate_allocation", "participant_index", participant.Index, "model_id", modelId, "total_nodes", totalNodes, "nodes_to_inference", nodesToInference)

		// Set POC_SLOT to true for the first nodesToInference shuffled nodes
		var inferenceNodeIds []string
		var pocOnlyNodeIds []string
		for i, nodeIdx := range nodeIndices {
			mlNode := modelMLNodes[nodeIdx]
			if i < nodesToInference {
				if len(mlNode.TimeslotAllocation) > 1 {
					mlNode.TimeslotAllocation[1] = true // Set POC_SLOT to true (serve inference)
					ma.LogInfo("Setting POC_SLOT=true for node", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "set_poc_slot", "participant_index", participant.Index, "model_id", modelId, "node_id", mlNode.NodeId)
				}
				inferenceNodeIds = append(inferenceNodeIds, mlNode.NodeId)
			} else {
				pocOnlyNodeIds = append(pocOnlyNodeIds, mlNode.NodeId)
			}
		}

		// Log the allocation for debugging
		ma.LogInfo("Applied 50% node allocation for model", types.EpochGroup,
			"flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "allocation_summary",
			"participantIndex", participant.Index,
			"modelId", modelId,
			"totalNodes", totalNodes,
			"nodesToInference", nodesToInference,
			"inferenceNodeIds", inferenceNodeIds,
			"nodesToPoC", totalNodes-nodesToInference,
			"pocOnlyNodeIds", pocOnlyNodeIds)
	}
	ma.LogInfo("Finished 50% node allocation for participant", types.EpochGroup, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "end", "participant_index", participant.Index)
}

// distributeLegacyWeight handles legacy PoC batches by distributing weight from
// MLNodes with empty NodeId among actual hardware nodes
func (ma *ModelAssigner) distributeLegacyWeight(originalMLNodes []*types.MLNodeInfo, hardwareNodes *types.HardwareNodes, preservedNodes map[string]*types.MLNodeInfo) []*types.MLNodeInfo {
	const flowContext = "model_assignment"
	const subFlowContext = "distribute_legacy_weight"
	ma.LogInfo("Starting legacy weight distribution", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "start")

	if len(originalMLNodes) == 0 || hardwareNodes == nil || len(hardwareNodes.HardwareNodes) == 0 {
		ma.LogInfo("Empty inputs, returning original list.", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "empty_inputs")
		return originalMLNodes
	}

	// Find MLNode with empty NodeId (legacy batches)
	var legacyMLNode *types.MLNodeInfo
	var legacyIndex int = -1

	for i, mlNode := range originalMLNodes {
		if mlNode.NodeId == "" {
			legacyMLNode = mlNode
			legacyIndex = i
			break
		}
	}

	// If no legacy MLNode found, return original list unchanged
	if legacyMLNode == nil {
		ma.LogInfo("No legacy ML Node with empty NodeId found, returning original list.", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "no_legacy_node")
		return originalMLNodes
	}
	ma.LogInfo("Found legacy ML node to distribute weight from", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "found_legacy_node", "legacy_node", legacyMLNode)

	// Remove the legacy MLNode from the list
	newMLNodes := make([]*types.MLNodeInfo, 0, len(originalMLNodes)-1)
	newMLNodes = append(newMLNodes, originalMLNodes[:legacyIndex]...)
	newMLNodes = append(newMLNodes, originalMLNodes[legacyIndex+1:]...)

	hardwareNodesIds := make(map[string]bool)
	for _, hwNode := range hardwareNodes.HardwareNodes {
		hardwareNodesIds[hwNode.LocalId] = true
	}
	var numPreservedNodes int64
	for _, preservedNode := range preservedNodes {
		if _, ok := hardwareNodesIds[preservedNode.NodeId]; ok {
			numPreservedNodes++
		}
	}

	// Calculate weight per hardware node
	totalLegacyWeight := legacyMLNode.PocWeight
	numHardwareNodes := int64(len(hardwareNodes.HardwareNodes))
	numNodesToDistributeWeight := numHardwareNodes - numPreservedNodes
	weightPerNode := totalLegacyWeight / numNodesToDistributeWeight
	remainderWeight := totalLegacyWeight % numNodesToDistributeWeight
	ma.LogInfo("Calculated weight distribution", types.PoC,
		"flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "calculate_distribution",
		"total_legacy_weight", totalLegacyWeight,
		"num_hardware_nodes", numHardwareNodes,
		"numPreservedNodes", numPreservedNodes,
		"numNodesToDistributeWeight", numNodesToDistributeWeight,
		"weight_per_node", weightPerNode,
		"remainder_weight", remainderWeight)

	var remainderCounter = int64(0)
	// Distribute weight among hardware nodes
	// Give weightPerNode to each, then distribute remainder by giving +1 to first nodes until remainder is over
	for _, hwNode := range hardwareNodes.HardwareNodes {
		preservedNode, _ := preservedNodes[hwNode.LocalId]
		nodeId := hwNode.LocalId
		distributedWeight := weightPerNode
		if remainderCounter < remainderWeight {
			distributedWeight++ // Give +1 to first remainderWeight nodes
		}

		if distributedWeight <= 0 {
			continue
		}
		ma.LogInfo("Distributing weight to hardware node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "distribute_to_node", "node_id", nodeId, "distributed_weight", distributedWeight)

		// Find existing MLNode for this hardware node
		found := false
		for _, existingMLNode := range newMLNodes {
			if existingMLNode.NodeId == nodeId {
				// Add distributed weight to existing MLNode
				if preservedNode == nil {
					if remainderCounter < remainderWeight {
						distributedWeight++
						remainderCounter++
					}
					existingMLNode.PocWeight += distributedWeight
				} else {
					// If preserved node, just set the weight without adding
					existingMLNode.PocWeight = preservedNode.PocWeight
				}

				found = true
				ma.LogInfo("Added weight to existing ML node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "add_to_existing_node", "node_id", existingMLNode.NodeId, "added_weight", distributedWeight, "new_total_weight", existingMLNode.PocWeight)
				break
			}
		}

		// If no existing MLNode found, create new one
		if !found {
			var newMLNode *types.MLNodeInfo
			if preservedNode != nil {
				newMLNode = preservedNode
				newMLNode.TimeslotAllocation = []bool{true, false} // Ensure preserved nodes are set to PRE_POC_SLOT=true, POC_SLOT=false
				ma.LogInfo("Created new ML node from PRESERVED hardware node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "create_new_ml_node", "node_id", newMLNode.NodeId, "weight", newMLNode.PocWeight)
			} else {
				if remainderCounter < remainderWeight {
					distributedWeight++
					remainderCounter++
				}
				newMLNode = &types.MLNodeInfo{
					NodeId:     nodeId,
					PocWeight:  distributedWeight,
					Throughput: 0, // Will be populated later if needed
				}
				ma.LogInfo("Created new ML node for hardware node", types.PoC, "flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "create_new_ml_node", "node_id", newMLNode.NodeId, "weight", newMLNode.PocWeight)
			}

			newMLNodes = append(newMLNodes, newMLNode)
		}
	}

	ma.LogInfo("Finished distributing legacy PoC weight", types.PoC,
		"flow_context", flowContext, "sub_flow_context", subFlowContext, "step", "end",
		"legacyWeight", totalLegacyWeight,
		"numHardwareNodes", numHardwareNodes,
		"final_ml_nodes", newMLNodes)

	return newMLNodes
}

func (ma *ModelAssigner) getPreservedNodes(ctx context.Context, upcomingEpoch uint64) map[string]map[string]*types.MLNodeInfo {
	if upcomingEpoch == 1 {
		ma.LogInfo("ModelAssigner.getPreservedNodes: No preserved nodes for epoch 0", types.EpochGroup, "upcoming_epoch", upcomingEpoch)
		return nil
	}

	activeParticipants, found := ma.keeper.GetActiveParticipants(ctx, upcomingEpoch-1)
	if !found {
		ma.LogError("ModelAssigner.getPreservedNodes: No active participants found for previous epoch", types.EpochGroup, "upcoming_epoch", upcomingEpoch)
		return nil
	}

	result := make(map[string]map[string]*types.MLNodeInfo)
	for _, p := range activeParticipants.Participants {
		preservedNodes := make(map[string]*types.MLNodeInfo)
		for _, nodeArray := range p.MlNodes {
			for _, n := range nodeArray.MlNodes {
				if len(n.TimeslotAllocation) > 1 && n.TimeslotAllocation[1] {
					preservedNodes[n.NodeId] = n
				}
			}
		}

		if len(preservedNodes) > 0 {
			ma.LogInfo("ModelAssigner.getPreservedNodes. Found preserved nodes for participant", types.EpochGroup,
				"participant_address", p.Index,
				"upcoming_epoch", upcomingEpoch,
				"len(preservedNodes)", len(preservedNodes))
			result[p.Index] = preservedNodes
		}
	}

	ma.LogInfo("ModelAssigner.getPreservedNodes: Completed collecting preserved nodes", types.EpochGroup,
		"upcoming_epoch", upcomingEpoch,
		"number_of_participants_with_preserved_nodes", len(result))

	return result
}

// Helper function to create a map of modelId to supported models
func supportedModelsByNode(hardwareNodes *types.HardwareNodes, governanceModels []*types.Model) map[string][]string {
	governanceModelsMap := make(map[string]bool)
	for _, model := range governanceModels {
		governanceModelsMap[model.Id] = true
	}

	supportedModelsByNode := make(map[string][]string)
	for _, node := range hardwareNodes.HardwareNodes {
		// keep only the models that are in the governanceModelsMap
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
