package inference

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

// WeightCalculator encapsulates all the data needed to calculate new weights for participants
type WeightCalculator struct {
	CurrentValidatorWeights map[string]int64
	OriginalBatches         map[string][]types.PoCBatch
	Validations             map[string][]types.PoCValidation
	Participants            map[string]types.Participant
	Seeds                   map[string]types.RandomSeed
	EpochStartBlockHeight   int64
	Logger                  types.InferenceLogger
}

// NewWeightCalculator creates a new WeightCalculator instance
func NewWeightCalculator(
	currentValidatorWeights map[string]int64,
	originalBatches map[string][]types.PoCBatch,
	validations map[string][]types.PoCValidation,
	participants map[string]types.Participant,
	seeds map[string]types.RandomSeed,
	epochStartBlockHeight int64,
	logger types.InferenceLogger,
) *WeightCalculator {
	return &WeightCalculator{
		CurrentValidatorWeights: currentValidatorWeights,
		OriginalBatches:         originalBatches,
		Validations:             validations,
		Participants:            participants,
		Seeds:                   seeds,
		EpochStartBlockHeight:   epochStartBlockHeight,
		Logger:                  logger,
	}
}

// getCurrentValidatorWeights gets the active participants for the previous epoch and returns a map of weights
func (am AppModule) getCurrentValidatorWeights(ctx context.Context) (map[string]int64, error) {
	currentGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("getCurrentValidatorWeights: Error getting current epoch group", types.PoC, "error", err)
		return nil, err
	}
	currentMembers, err := currentGroup.GetGroupMembers(ctx)
	if err != nil {
		am.LogError("getCurrentValidatorWeights: Error getting current group members", types.PoC, "error", err)
		return nil, err
	}

	weights := make(map[string]int64)
	for _, member := range currentMembers {
		weight, err := strconv.ParseInt(member.Member.Weight, 10, 64)
		if err != nil {
			am.LogError("getCurrentValidatorWeights: Error parsing weight", types.PoC, "address", member.Member.Address, "weight", member.Member.Weight, "error", err)
			return nil, err
		}
		weights[member.Member.Address] = weight
	}

	return weights, nil
}

// GetPreviousEpochMLNodesWithInferenceAllocation retrieves MLNodes from the previous epoch that have POC_SLOT = true (inference allocation)
// and returns a map of participant addresses to their ActiveParticipant objects with preserved weights
func (am AppModule) GetPreviousEpochMLNodesWithInferenceAllocation(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	preservedParticipants := make(map[string]*types.ActiveParticipant)

	// Skip for first epoch or if we can't get current epoch (which is about to end)
	if upcomingEpoch.Index <= 1 {
		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Skipping for first epoch", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	// Get current epoch group data (the epoch that's about to end)
	// At this point in the flow, we're still in the current epoch - the transition happens later in onSetNewValidatorsStage
	currentEpochGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Unable to get current epoch group", types.PoC, "error", err.Error())
		return nil
	}
	if currentEpochGroup.GroupData.EpochIndex != upcomingEpoch.Index-1 {
		am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Current epoch group does not match upcoming epoch", types.PoC,
			"currentEpochGroup.EpochIndex", currentEpochGroup.GroupData.EpochIndex,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Processing current epoch group (about to end)", types.PoC,
		"currentEpochGroup.EpochIndex", currentEpochGroup.GroupData.EpochIndex,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"pocStartBlockHeight", currentEpochGroup.GroupData.PocStartBlockHeight,
		"len(validationWeight)", len(currentEpochGroup.GroupData.ValidationWeights))

	preservedNodesByParticipant, err := am.GetPreservedNodesByParticipant(ctx, currentEpochGroup.GroupData.EpochIndex)
	if err != nil {
		am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Error getting preserved nodes by participant", types.PoC, "error", err)
		return nil
	}

	// Iterate through all validation weights in current epoch to find inference-serving MLNodes
	for _, validationWeight := range currentEpochGroup.GroupData.ValidationWeights {
		participantAddress := validationWeight.MemberAddress

		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Processing participant", types.PoC,
			"participantAddress", participantAddress,
			"len(MlNodes)", len(validationWeight.MlNodes))

		inferenceMLNodes, ok := preservedNodesByParticipant[participantAddress]
		if !ok || len(inferenceMLNodes) == 0 {
			am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: No preserved MLNodes for participant", types.PoC,
				"participantAddress", participantAddress)
			continue
		}

		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Processing participant", types.PoC,
			"participantAddress", participantAddress,
			"len(inferenceMLNodes)", len(inferenceMLNodes))

		// If we found inference-serving MLNodes for this participant, create ActiveParticipant
		// Get participant details
		participant, found := am.keeper.GetParticipant(ctx, participantAddress)
		if !found {
			am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Participant not found", types.PoC,
				"participantAddress", participantAddress)
			continue
		}

		// Calculate total weight from preserved MLNodes
		totalWeight := int64(0)
		filteredInferenceMLNodes := make([]*types.MLNodeInfo, 0)
		for _, mlNode := range inferenceMLNodes {
			if mlNode.NodeId == "" {
				continue
			}
			totalWeight += mlNode.PocWeight
			filteredInferenceMLNodes = append(filteredInferenceMLNodes, mlNode)
		}

		// Create the double repeated structure with all MLNodes in the first array (index 0)
		firstMLNodeArray := &types.ModelMLNodes{
			MlNodes: filteredInferenceMLNodes,
		}
		modelMLNodesArray := []*types.ModelMLNodes{firstMLNodeArray}

		// Create ActiveParticipant with preserved weights
		activeParticipant := &types.ActiveParticipant{
			Index:        participant.Address,
			ValidatorKey: participant.ValidatorKey,
			Weight:       totalWeight,
			InferenceUrl: participant.InferenceUrl,
			Seed:         nil,               // Will be set later if available
			Models:       make([]string, 0), // Will be populated by setModelsForParticipants
			MlNodes:      modelMLNodesArray,
		}

		preservedParticipants[participantAddress] = activeParticipant

		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Created preserved participant", types.PoC,
			"participantAddress", participantAddress,
			"totalWeight", totalWeight,
			"numMLNodes", len(filteredInferenceMLNodes))
	}

	am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Summary", types.PoC,
		"totalPreservedParticipants", len(preservedParticipants))

	participantsSlice := make([]*types.ActiveParticipant, 0, len(preservedParticipants))
	for _, participant := range preservedParticipants {
		participantsSlice = append(participantsSlice, participant)
	}
	// Sort participants by address for consistent order
	sort.Slice(participantsSlice, func(i, j int) bool {
		return participantsSlice[i].Index < participantsSlice[j].Index
	})

	return participantsSlice
}

func (am AppModule) GetPreservedNodesByParticipant(ctx context.Context, epochId uint64) (map[string][]*types.MLNodeInfo, error) {
	participants, found := am.keeper.GetActiveParticipants(ctx, epochId)
	if !found {
		am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Active participants not found", types.PoC, "epochId", epochId)
		return nil, errors.New("GetPreviousEpochMLNodesWithInferenceAllocation: active participant not found. epochId: " + strconv.FormatUint(epochId, 10))
	}

	result := make(map[string][]*types.MLNodeInfo)

	for _, p := range participants.Participants {
		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation. GetPreservedNodesByParticipant: Processing participant", types.PoC,
			"participantAddress", p.Index, "len(p.MlNodes)", len(p.MlNodes))

		nodes := make([]*types.MLNodeInfo, 0)
		for _, nodeArray := range p.MlNodes {
			for _, mlNode := range nodeArray.MlNodes {
				if len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] { // POC_SLOT = true
					preservedMLNode := &types.MLNodeInfo{
						NodeId:             mlNode.NodeId,
						Throughput:         mlNode.Throughput,
						PocWeight:          mlNode.PocWeight,    // Preserve the weight from current epoch
						TimeslotAllocation: []bool{true, false}, // Reset to default for new epoch
					}
					nodes = append(nodes, preservedMLNode)
				}
			}
		}
		if len(nodes) > 0 {
			result[p.Index] = nodes
			am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: Found preserved MLNodes for participant", types.PoC,
				"participantAddress", p.Index,
				"numMLNodes", len(nodes))
		} else {
			am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation: No preserved MLNodes for participant", types.PoC,
				"participantAddress", p.Index)
		}
	}

	return result, nil
}

func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	epochStartBlockHeight := upcomingEpoch.PocStartBlockHeight
	am.LogInfo("ComputeNewWeights: computing new weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
	// STEP 1: Get preserved weights from inference-serving MLNodes in current epoch
	preservedParticipants := am.GetPreviousEpochMLNodesWithInferenceAllocation(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeights: Retrieved preserved participants", types.PoC,
		"numPreservedParticipants", len(preservedParticipants))

	// Get current active participants weights
	currentValidatorWeights, err := am.getCurrentValidatorWeights(ctx)
	am.LogInfo("ComputeNewWeights: Retrieved current validator weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"weights", currentValidatorWeights)

	if err != nil {
		am.LogError("ComputeNewWeights: Error getting current validator weights", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// STEP 2: Get PoC batches and filter out batches from inference-serving nodes
	allOriginalBatches, err := am.keeper.GetPoCBatchesByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting batches by PoC stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// Build a set of inference-serving node IDs that should be excluded from PoC mining
	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeights: Found inference-serving nodes", types.PoC,
		"inferenceServingNodeIds", inferenceServingNodeIds)

	// Filter out PoC batches from inference-serving nodes
	originalBatches := am.filterPoCBatchesFromInferenceNodes(allOriginalBatches, inferenceServingNodeIds)

	am.LogInfo("ComputeNewWeights: Filtered PoC batches", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"originalBatchesCount", len(allOriginalBatches),
		"filteredBatchesCount", len(originalBatches))

	validations, err := am.keeper.GetPoCValidationByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting PoC validations by stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
	}

	validators := make([]string, len(validations))
	var i = 0
	for address, _ := range validations {
		validators[i] = address
		i += 1
	}
	am.LogInfo("ComputeNewWeights: Retrieved PoC validations", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(validations)", len(validations),
		"validators", validators)

	// Collect all participants and seeds
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)

	var sortedBatchKeys []string
	for key := range originalBatches {
		sortedBatchKeys = append(sortedBatchKeys, key)
	}
	sort.Strings(sortedBatchKeys)

	for _, participantAddress := range sortedBatchKeys {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("ComputeNewWeights: Error getting participant", types.PoC,
				"address", participantAddress,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress)
		if !found {
			am.LogError("ComputeNewWeights: Participant didn't submit the seed for the upcoming epoch", types.PoC,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
				"participant", participantAddress)
			continue
		}
		seeds[participantAddress] = seed
	}

	// STEP 3: Add seeds for preserved participants if they have submitted seeds
	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index
		if seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress); found {
			preservedParticipant.Seed = &seed
			seeds[participantAddress] = seed
			am.LogInfo("ComputeNewWeights: Added seed for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		} else {
			am.LogWarn("ComputeNewWeights: No seed found for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		}
	}

	// STEP 4: Create WeightCalculator and calculate PoC mining participants (excluding inference-serving nodes)
	calculator := NewWeightCalculator(
		currentValidatorWeights,
		originalBatches,
		validations,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
	)
	pocMiningParticipants := calculator.Calculate()

	// STEP 4: Merge preserved participants with PoC mining participants
	var allActiveParticipants []*types.ActiveParticipant

	// Add preserved participants first
	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index
		// Check if this participant also has PoC mining activity
		if pocParticipant := findParticipantByAddress(pocMiningParticipants, participantAddress); pocParticipant != nil {
			// Merge: combine weights and MLNodes from both sources
			combinedMLNodes := mergeMLNodeArrays(preservedParticipant.MlNodes, pocParticipant.MlNodes)
			combinedWeight := int64(0)
			for _, mlNode := range combinedMLNodes[0].MlNodes {
				combinedWeight += mlNode.PocWeight
			}

			mergedParticipant := &types.ActiveParticipant{
				Index:        participantAddress,
				ValidatorKey: preservedParticipant.ValidatorKey,
				Weight:       combinedWeight,
				InferenceUrl: preservedParticipant.InferenceUrl,
				Seed:         pocParticipant.Seed, // Use PoC participant's seed
				Models:       make([]string, 0),   // Will be populated by setModelsForParticipants
				MlNodes:      combinedMLNodes,
			}

			allActiveParticipants = append(allActiveParticipants, mergedParticipant)

			am.LogInfo("ComputeNewWeights: Merged preserved and PoC participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight,
				"pocWeight", pocParticipant.Weight,
				"combinedWeight", combinedWeight,
				"combinedMLNodes", combinedMLNodes)
		} else {
			// Only preserved participant (no PoC mining activity)
			allActiveParticipants = append(allActiveParticipants, preservedParticipant)

			am.LogInfo("ComputeNewWeights: Added preserved-only participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight)
		}
	}

	preservedParticipantsSet := make(map[string]bool)
	for _, preservedParticipant := range preservedParticipants {
		preservedParticipantsSet[preservedParticipant.Index] = true
	}

	// Add remaining PoC mining participants that weren't already merged
	for _, pocParticipant := range pocMiningParticipants {
		if _, alreadyPreserved := preservedParticipantsSet[pocParticipant.Index]; !alreadyPreserved {
			allActiveParticipants = append(allActiveParticipants, pocParticipant)

			am.LogInfo("ComputeNewWeights: Added PoC-only participant", types.PoC,
				"participantAddress", pocParticipant.Index,
				"pocWeight", pocParticipant.Weight)
		}
	}

	am.LogInfo("ComputeNewWeights: Final summary", types.PoC,
		"preservedParticipants", len(preservedParticipants),
		"pocMiningParticipants", len(pocMiningParticipants),
		"totalActiveParticipants", len(allActiveParticipants))

	return allActiveParticipants
}

func findParticipantByAddress(participants []*types.ActiveParticipant, address string) *types.ActiveParticipant {
	for _, participant := range participants {
		if participant.Index == address {
			return participant
		}
	}
	return nil
}

// Helper function to merge MLNode arrays from preserved and PoC participants
func mergeMLNodeArrays(preservedMLNodes, pocMLNodes []*types.ModelMLNodes) []*types.ModelMLNodes {
	if len(preservedMLNodes) == 0 {
		return pocMLNodes
	}
	if len(pocMLNodes) == 0 {
		return preservedMLNodes
	}

	// Merge the first arrays (index 0) which contain all MLNodes before model assignment
	var mergedMLNodes []*types.MLNodeInfo

	// Add preserved MLNodes first
	if len(preservedMLNodes) > 0 && preservedMLNodes[0] != nil {
		mergedMLNodes = append(mergedMLNodes, preservedMLNodes[0].MlNodes...)
	}

	// Add PoC MLNodes, avoiding duplicates by NodeId
	if len(pocMLNodes) > 0 && pocMLNodes[0] != nil {
		existingNodeIds := make(map[string]bool)
		for _, mlNode := range mergedMLNodes {
			existingNodeIds[mlNode.NodeId] = true
		}

		for _, pocMLNode := range pocMLNodes[0].MlNodes {
			if !existingNodeIds[pocMLNode.NodeId] {
				mergedMLNodes = append(mergedMLNodes, pocMLNode)
			}
		}
	}

	filteredMergedMLNodes := make([]*types.MLNodeInfo, 0)
	for _, mlNode := range mergedMLNodes {
		if mlNode.NodeId == "" {
			continue
		}
		filteredMergedMLNodes = append(filteredMergedMLNodes, mlNode)
	}

	// Return merged array in the first position
	return []*types.ModelMLNodes{{MlNodes: filteredMergedMLNodes}}
}

func RecalculateWeight(p *types.ActiveParticipant) int64 {
	weight := int64(0)
	countedNodeIds := make(map[string]bool)
	for _, nodeMLNodes := range p.MlNodes {
		for _, mlNode := range nodeMLNodes.MlNodes {
			if mlNode.NodeId == "" {
				continue
			}
			if _, ok := countedNodeIds[mlNode.NodeId]; !ok {
				countedNodeIds[mlNode.NodeId] = true
				weight += mlNode.PocWeight
			}
		}
	}
	return weight
}

// Calculate computes the new weights for active participants based on the data in the WeightCalculator
func (wc *WeightCalculator) Calculate() []*types.ActiveParticipant {
	sortedBatchKeys := wc.getSortedBatchKeys()

	var activeParticipants []*types.ActiveParticipant
	for _, participantAddress := range sortedBatchKeys {
		activeParticipant := wc.validatedParticipant(participantAddress)
		if activeParticipant != nil {
			activeParticipants = append(activeParticipants, activeParticipant)
			wc.Logger.LogInfo("Calculate: Setting compute validator.", types.PoC, "activeParticipant", activeParticipant)
		}
	}

	return activeParticipants
}

func (wc *WeightCalculator) getSortedBatchKeys() []string {
	var sortedBatchKeys []string
	for key := range wc.OriginalBatches {
		sortedBatchKeys = append(sortedBatchKeys, key)
	}
	sort.Strings(sortedBatchKeys)
	return sortedBatchKeys
}

func (wc *WeightCalculator) validatedParticipant(participantAddress string) *types.ActiveParticipant {
	participant, ok := wc.Participants[participantAddress]
	if !ok {
		// This should not happen since we already checked when collecting participants
		wc.Logger.LogError("Calculate: Participant not found", types.PoC, "address", participantAddress)
		return nil
	}

	vals := wc.getParticipantValidations(participantAddress)
	if len(vals) == 0 {
		wc.Logger.LogError("Calculate: No validations for participant found", types.PoC, "participant", participantAddress)
		return nil
	}

	nodeWeights, claimedWeight := calculateParticipantWeight(wc.OriginalBatches[participantAddress])
	if claimedWeight < 1 {
		wc.Logger.LogWarn("Calculate: Participant has non-positive claimedWeight.", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)
		return nil
	}
	wc.Logger.LogInfo("Calculate: participant claims weight", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)

	if participant.ValidatorKey == "" {
		wc.Logger.LogError("Calculate: Participant hasn't provided their validator key.", types.PoC, "participant", participantAddress)
		return nil
	}

	if !wc.pocValidated(vals, participantAddress) {
		return nil
	}

	seed, found := wc.Seeds[participantAddress]
	if !found {
		// This should not happen since we already checked when collecting seeds
		wc.Logger.LogError("Calculate: Seed not found", types.PoC, "blockHeight", wc.EpochStartBlockHeight, "participant", participantAddress)
		return nil
	}

	mlNodes := make([]*types.MLNodeInfo, 0, len(nodeWeights))
	for _, n := range nodeWeights {
		mlNodes = append(mlNodes, &types.MLNodeInfo{
			NodeId:    n.nodeId,
			PocWeight: n.weight,
		})
	}

	wc.Logger.LogInfo("Calculate: mlNodes", types.PoC, "mlNodes", mlNodes)

	// Create the double repeated structure with all MLNodes in the first array (index 0)
	firstMLNodeArray := &types.ModelMLNodes{
		MlNodes: mlNodes,
	}
	modelMLNodesArray := []*types.ModelMLNodes{firstMLNodeArray}

	activeParticipant := &types.ActiveParticipant{
		Index:        participant.Address,
		ValidatorKey: participant.ValidatorKey,
		Weight:       claimedWeight,
		InferenceUrl: participant.InferenceUrl,
		Seed:         &seed,
		Models:       make([]string, 0),
		MlNodes:      modelMLNodesArray, // Now using the double repeated structure
	}
	return activeParticipant
}

func (wc *WeightCalculator) getParticipantValidations(participantAddress string) []types.PoCValidation {
	vals := wc.Validations[participantAddress]

	validators := make([]string, len(vals))
	for i, v := range vals {
		validators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("Calculate: Found ALL submitted validations for participant", types.PoC,
		"participant", participantAddress, "len(vals)", len(vals), "validators", validators)

	filteredVals := make([]types.PoCValidation, 0, len(vals))
	for _, v := range vals {
		if _, ok := wc.CurrentValidatorWeights[v.ValidatorParticipantAddress]; ok {
			filteredVals = append(filteredVals, v)
		}
	}

	filteredValidators := make([]string, len(filteredVals))
	for i, v := range filteredVals {
		filteredValidators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("Calculate: filtered validations to include only current validators", types.PoC,
		"participant", participantAddress, "len(vals)", len(filteredVals), "validators", filteredValidators)

	return filteredVals
}

func (wc *WeightCalculator) pocValidated(vals []types.PoCValidation, participantAddress string) bool {
	totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
	halfWeight := int64(totalWeight / 2)
	shouldContinue := false

	if len(wc.CurrentValidatorWeights) > 0 {
		valOutcome := calculateValidationOutcome(wc.CurrentValidatorWeights, vals)
		votedWeight := valOutcome.ValidWeight + valOutcome.InvalidWeight // For logging only
		if valOutcome.ValidWeight > halfWeight {
			shouldContinue = true
			wc.Logger.LogInfo("Calculate: Participant received valid validations from more than half of participants by weight. Accepting",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		} else if valOutcome.InvalidWeight > halfWeight {
			shouldContinue = false
			wc.Logger.LogWarn("Calculate: Participant received invalid validations from more than half of participants by weight. Rejecting",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		} else {
			shouldContinue = false
			wc.Logger.LogWarn("Calculate: Participant did not receive a majority of either valid or invalid validations. Rejecting.",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		}
	} else {
		// NEEDREVIEW: what are we doing here now? This is an illegal state after my recent changes!
		// Probably just forbid creating weightCalculator with nil values??
		shouldContinue = true
		if wc.EpochStartBlockHeight > 0 {
			wc.Logger.LogError("Calculate: No current validator weights found. Accepting the participant.", types.PoC, "participant", participantAddress)
		}
	}

	return shouldContinue
}

type nodeWeight struct {
	nodeId string
	weight int64
}

func calculateParticipantWeight(batches []types.PoCBatch) ([]nodeWeight, int64) {
	nodeWeights := make(map[string]int64)
	totalWeight := int64(0)

	uniqueNonces := make(map[int64]struct{})
	for _, batch := range batches {
		weight := int64(0)
		for _, nonce := range batch.Nonces {
			if _, exists := uniqueNonces[nonce]; !exists {
				uniqueNonces[nonce] = struct{}{}
				weight++
			}
		}

		nodeId := batch.NodeId // Keep empty string for legacy batches without node_id
		nodeWeights[nodeId] += weight
		totalWeight += weight
	}

	nodeWeightsSlice := make([]nodeWeight, 0, len(nodeWeights))
	for nodeId, weight := range nodeWeights {
		nodeWeightsSlice = append(nodeWeightsSlice, nodeWeight{nodeId: nodeId, weight: weight})
	}
	sort.Slice(nodeWeightsSlice, func(i, j int) bool {
		return nodeWeightsSlice[i].nodeId < nodeWeightsSlice[j].nodeId
	})

	return nodeWeightsSlice, totalWeight
}

// calculateTotalWeight calculates the total weight of all validators
func calculateTotalWeight(validatorWeights map[string]int64) uint64 {
	if validatorWeights == nil {
		return 0
	}

	totalWeight := uint64(0)
	for participant, weight := range validatorWeights {
		if weight < 0 {
			slog.Error("calculateTotalWeight: Negative weight found", "participant", participant, "weight", weight)
			continue
		}
		totalWeight += uint64(weight)
	}

	return totalWeight
}

type validationOutcome struct {
	ValidWeight   int64
	InvalidWeight int64
}

func calculateValidationOutcome(currentValidatorsSet map[string]int64, validations []types.PoCValidation) validationOutcome {
	validWeight := int64(0)
	invalidWeight := int64(0)
	for _, v := range validations {
		if weight, ok := currentValidatorsSet[v.ValidatorParticipantAddress]; ok {
			if v.FraudDetected {
				invalidWeight += weight
			} else {
				validWeight += weight
			}
		}
	}
	return validationOutcome{
		ValidWeight:   validWeight,
		InvalidWeight: invalidWeight,
	}
}

// getInferenceServingNodeIds returns a set of node IDs that have POC_SLOT = true in the current epoch
func (am AppModule) getInferenceServingNodeIds(ctx context.Context, upcomingEpoch types.Epoch) map[string]bool {
	inferenceServingNodeIds := make(map[string]bool)

	// Skip for first epoch
	if upcomingEpoch.Index <= 1 {
		return inferenceServingNodeIds
	}

	// Get current epoch group data
	currentEpochGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("getInferenceServingNodeIds: Unable to get current epoch group", types.PoC, "error", err.Error())
		return inferenceServingNodeIds
	}

	// Find all nodes with POC_SLOT = true
	for _, validationWeight := range currentEpochGroup.GroupData.ValidationWeights {
		for _, mlNode := range validationWeight.MlNodes {
			if len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] { // POC_SLOT = true
				inferenceServingNodeIds[mlNode.NodeId] = true
				am.LogInfo("getInferenceServingNodeIds: Found inference-serving node", types.PoC,
					"nodeId", mlNode.NodeId,
					"participantAddress", validationWeight.MemberAddress)
			}
		}
	}

	return inferenceServingNodeIds
}

// filterPoCBatchesFromInferenceNodes removes PoC batches from nodes that should be serving inference
func (am AppModule) filterPoCBatchesFromInferenceNodes(allBatches map[string][]types.PoCBatch, inferenceServingNodeIds map[string]bool) map[string][]types.PoCBatch {
	filteredBatches := make(map[string][]types.PoCBatch)
	excludedBatchCount := 0

	for participantAddress, batches := range allBatches {
		var validBatches []types.PoCBatch

		for _, batch := range batches {
			// Check if this batch is from an inference-serving node
			if inferenceServingNodeIds[batch.NodeId] {
				// Exclude this batch - the node should have been serving inference, not mining PoC
				excludedBatchCount++
				am.LogWarn("filterPoCBatchesFromInferenceNodes: Excluding PoC batch from inference-serving node", types.PoC,
					"participantAddress", participantAddress,
					"nodeId", batch.NodeId,
					"batchNonceCount", len(batch.Nonces))
			} else {
				// Include this batch - it's from a legitimate PoC mining node
				validBatches = append(validBatches, batch)
			}
		}

		// Only include participant if they have valid batches remaining
		if len(validBatches) > 0 {
			filteredBatches[participantAddress] = validBatches
		}
	}

	am.LogInfo("filterPoCBatchesFromInferenceNodes: Summary", types.PoC,
		"excludedBatchCount", excludedBatchCount,
		"originalParticipants", len(allBatches),
		"filteredParticipants", len(filteredBatches))

	return filteredBatches
}
