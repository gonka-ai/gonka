package inference

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// WeightCalculator encapsulates all the data needed to calculate new weights for participants.
// Uses off-chain store commits and weight distributions instead of on-chain batches.
type WeightCalculator struct {
	CurrentValidatorWeights map[string]int64
	StoreCommits            map[string]types.PoCV2StoreCommit
	NodeWeightDistributions map[string]types.MLNodeWeightDistribution
	Validations             map[string][]types.PoCValidationV2
	Participants            map[string]types.Participant
	Seeds                   map[string]types.RandomSeed
	EpochStartBlockHeight   int64
	Logger                  types.InferenceLogger
	WeightScaleFactor       mathsdk.LegacyDec
	GuardianEnabled         bool
	GuardianAddresses       map[string]bool
}

// NewWeightCalculator creates a new WeightCalculator instance.
func NewWeightCalculator(
	currentValidatorWeights map[string]int64,
	storeCommits map[string]types.PoCV2StoreCommit,
	nodeWeightDistributions map[string]types.MLNodeWeightDistribution,
	validations map[string][]types.PoCValidationV2,
	participants map[string]types.Participant,
	seeds map[string]types.RandomSeed,
	epochStartBlockHeight int64,
	logger types.InferenceLogger,
	weightScaleFactor mathsdk.LegacyDec,
	guardianEnabled bool,
	guardianAddresses map[string]bool,
) *WeightCalculator {
	return &WeightCalculator{
		CurrentValidatorWeights: currentValidatorWeights,
		StoreCommits:            storeCommits,
		NodeWeightDistributions: nodeWeightDistributions,
		Validations:             validations,
		Participants:            participants,
		Seeds:                   seeds,
		EpochStartBlockHeight:   epochStartBlockHeight,
		Logger:                  logger,
		WeightScaleFactor:       weightScaleFactor,
		GuardianEnabled:         guardianEnabled,
		GuardianAddresses:       guardianAddresses,
	}
}

// Calculate computes the new weights for active participants.
func (wc *WeightCalculator) Calculate() []*types.ActiveParticipant {
	sortedParticipants := wc.getSortedParticipantKeys()

	var activeParticipants []*types.ActiveParticipant
	for _, participantAddress := range sortedParticipants {
		activeParticipant := wc.validatedParticipant(participantAddress)
		if activeParticipant != nil {
			activeParticipants = append(activeParticipants, activeParticipant)
			wc.Logger.LogInfo("Calculate: Setting compute validator.", types.PoC, "activeParticipant", activeParticipant)
		}
	}

	return activeParticipants
}

func (wc *WeightCalculator) getSortedParticipantKeys() []string {
	var sortedKeys []string
	for key := range wc.StoreCommits {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	return sortedKeys
}

func (wc *WeightCalculator) validatedParticipant(participantAddress string) *types.ActiveParticipant {
	participant, ok := wc.Participants[participantAddress]
	if !ok {
		wc.Logger.LogError("Calculate: Participant not found", types.PoC, "address", participantAddress)
		return nil
	}

	vals := wc.getParticipantValidations(participantAddress)
	if len(vals) == 0 {
		wc.Logger.LogError("Calculate: No validations for participant found", types.PoC, "participant", participantAddress)
		return nil
	}

	// Get claimed weight from store commit and per-node weights from distribution
	nodeWeights, claimedWeight := wc.calculateParticipantWeight(participantAddress)
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
		MlNodes:      modelMLNodesArray,
	}
	return activeParticipant
}

func (wc *WeightCalculator) getParticipantValidations(participantAddress string) []types.PoCValidationV2 {
	vals := wc.Validations[participantAddress]

	validators := make([]string, len(vals))
	for i, v := range vals {
		validators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("Calculate: Found ALL submitted validations for participant", types.PoC,
		"participant", participantAddress, "len(vals)", len(vals), "validators", validators)

	filteredVals := make([]types.PoCValidationV2, 0, len(vals))
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

// pocValidated checks if the participant passed validation by majority vote.
// Uses validated_weight semantics:
// - validated_weight > 0 -> valid vote (passed validation)
// - validated_weight <= 0 -> invalid vote (fraud/failure detected)
func (wc *WeightCalculator) pocValidated(vals []types.PoCValidationV2, participantAddress string) bool {
	totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
	halfWeight := int64(totalWeight / 2)
	shouldContinue := false

	if len(wc.CurrentValidatorWeights) > 0 {
		valOutcome := calculateValidationOutcome(wc.CurrentValidatorWeights, vals)
		votedWeight := valOutcome.ValidWeight + valOutcome.InvalidWeight
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
			guardianValidCount := 0
			guardianInvalidCount := 0

			if wc.GuardianEnabled && len(wc.GuardianAddresses) > 0 {
				for _, v := range vals {
					if wc.GuardianAddresses[v.ValidatorParticipantAddress] {
						if v.ValidatedWeight > 0 {
							guardianValidCount++
						} else {
							guardianInvalidCount++
						}
					}
				}

				// Guardian tiebreaker: all voting guardians must agree
				if guardianValidCount > 0 && guardianInvalidCount == 0 {
					shouldContinue = true
					wc.Logger.LogInfo("Calculate: Guardian tiebreaker - unanimous valid. Accepting.",
						types.PoC, "participant", participantAddress,
						"validWeight", valOutcome.ValidWeight,
						"invalidWeight", valOutcome.InvalidWeight,
						"votedWeight", votedWeight,
						"totalWeight", totalWeight,
						"halfWeight", halfWeight,
						"guardianValidCount", guardianValidCount,
						"guardianInvalidCount", guardianInvalidCount,
					)
				} else if guardianInvalidCount > 0 && guardianValidCount == 0 {
					wc.Logger.LogWarn("Calculate: Guardian tiebreaker - unanimous invalid. Rejecting.",
						types.PoC, "participant", participantAddress,
						"validWeight", valOutcome.ValidWeight,
						"invalidWeight", valOutcome.InvalidWeight,
						"votedWeight", votedWeight,
						"totalWeight", totalWeight,
						"halfWeight", halfWeight,
						"guardianValidCount", guardianValidCount,
						"guardianInvalidCount", guardianInvalidCount,
					)
				} else {
					wc.Logger.LogWarn("Calculate: No majority and guardians did not reach consensus. Rejecting.",
						types.PoC, "participant", participantAddress,
						"validWeight", valOutcome.ValidWeight,
						"invalidWeight", valOutcome.InvalidWeight,
						"votedWeight", votedWeight,
						"totalWeight", totalWeight,
						"halfWeight", halfWeight,
						"guardianValidCount", guardianValidCount,
						"guardianInvalidCount", guardianInvalidCount,
					)
				}
			} else {
				wc.Logger.LogWarn("Calculate: Participant did not receive a majority of either valid or invalid validations. Rejecting.",
					types.PoC, "participant", participantAddress,
					"validWeight", valOutcome.ValidWeight,
					"invalidWeight", valOutcome.InvalidWeight,
					"votedWeight", votedWeight,
					"totalWeight", totalWeight,
					"halfWeight", halfWeight,
				)
			}
		}
	} else {
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

// calculateParticipantWeight computes the claimed weight from store commit and weight distribution.
// Total weight comes from StoreCommit.Count (scaled by weightScaleFactor).
// Per-node weights come from MLNodeWeightDistribution.
func (wc *WeightCalculator) calculateParticipantWeight(participantAddress string) ([]nodeWeight, int64) {
	commit, hasCommit := wc.StoreCommits[participantAddress]
	if !hasCommit || commit.Count == 0 {
		return nil, 0
	}

	// Calculate total weight from commit count
	totalWeight := mathsdk.LegacyNewDec(int64(commit.Count)).Mul(wc.WeightScaleFactor).TruncateInt64()

	// Get per-node weights from distribution
	distribution, hasDistribution := wc.NodeWeightDistributions[participantAddress]
	if !hasDistribution || len(distribution.Weights) == 0 {
		// No distribution - create a single "unknown" node with all weight
		wc.Logger.LogWarn("Calculate: No weight distribution for participant, using single node", types.PoC,
			"participant", participantAddress, "totalWeight", totalWeight)
		return []nodeWeight{{nodeId: "unknown", weight: totalWeight}}, totalWeight
	}

	// Build per-node weights from distribution
	nodeWeightsSlice := make([]nodeWeight, 0, len(distribution.Weights))
	for _, w := range distribution.Weights {
		scaledWeight := mathsdk.LegacyNewDec(int64(w.Weight)).Mul(wc.WeightScaleFactor).TruncateInt64()
		nodeWeightsSlice = append(nodeWeightsSlice, nodeWeight{nodeId: w.NodeId, weight: scaledWeight})
	}
	sort.Slice(nodeWeightsSlice, func(i, j int) bool {
		return nodeWeightsSlice[i].nodeId < nodeWeightsSlice[j].nodeId
	})

	return nodeWeightsSlice, totalWeight
}

type validationOutcome struct {
	ValidWeight   int64
	InvalidWeight int64
}

// calculateValidationOutcome computes valid/invalid weights from validations.
// Uses validated_weight semantics:
// - validated_weight == -1 -> invalid vote
// - validated_weight > 0 -> valid vote
func calculateValidationOutcome(currentValidatorsSet map[string]int64, validations []types.PoCValidationV2) validationOutcome {
	validWeight := int64(0)
	invalidWeight := int64(0)
	for _, v := range validations {
		if weight, ok := currentValidatorsSet[v.ValidatorParticipantAddress]; ok {
			if v.ValidatedWeight > 0 {
				validWeight += weight
			} else {
				// validated_weight <= 0 is treated as invalid (fraud/failure detected)
				invalidWeight += weight
			}
		}
	}
	return validationOutcome{
		ValidWeight:   validWeight,
		InvalidWeight: invalidWeight,
	}
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

// ComputeNewWeights computes new weights for active participants using off-chain store commits.
func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	epochStartBlockHeight := upcomingEpoch.PocStartBlockHeight
	am.LogInfo("ComputeNewWeights: computing new weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)

	// Get preserved weights from inference-serving MLNodes
	preservedParticipants := am.GetPreviousEpochMLNodesWithInferenceAllocation(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeights: Retrieved preserved participants", types.PoC,
		"numPreservedParticipants", len(preservedParticipants))

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

	// Get off-chain store commits (replaces on-chain batches)
	allStoreCommits, err := am.keeper.GetAllPoCV2StoreCommitsForStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting store commits by PoC stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// Get weight distributions for per-node weights
	allWeightDistributions, err := am.keeper.GetAllMLNodeWeightDistributionsForStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting weight distributions by PoC stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		// Continue without distributions - will use single "unknown" node
	}

	// Build inference-serving node IDs for filtering
	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeights: Found inference-serving nodes", types.PoC,
		"inferenceServingNodeIds", inferenceServingNodeIds)

	// Filter out store commits with distributions that only have inference-serving nodes
	storeCommits, weightDistributions := am.filterStoreCommitsFromInferenceNodes(allStoreCommits, allWeightDistributions, inferenceServingNodeIds)

	am.LogInfo("ComputeNewWeights: Filtered store commits", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"originalCommitsCount", len(allStoreCommits),
		"filteredCommitsCount", len(storeCommits))

	// Get PoC validations
	validations, err := am.keeper.GetPoCValidationsV2ByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting PoC validations by stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
	}

	validators := make([]string, len(validations))
	var i = 0
	for address := range validations {
		validators[i] = address
		i++
	}
	am.LogInfo("ComputeNewWeights: Retrieved PoC validations", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(validations)", len(validations),
		"validators", validators)

	// Collect participants and seeds
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)
	allowedCommits := make(map[string]types.PoCV2StoreCommit)
	allowedDistributions := make(map[string]types.MLNodeWeightDistribution)

	var sortedCommitKeys []string
	for key := range storeCommits {
		sortedCommitKeys = append(sortedCommitKeys, key)
	}
	sort.Strings(sortedCommitKeys)

	for _, participantAddress := range sortedCommitKeys {
		// Check participant allowlist
		if !am.keeper.IsParticipantAllowed(ctx, epochStartBlockHeight, participantAddress) {
			am.LogInfo("ComputeNewWeights: Participant not in allowlist, skipping", types.PoC,
				"address", participantAddress,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
			continue
		}

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
		allowedCommits[participantAddress] = storeCommits[participantAddress]
		if dist, ok := weightDistributions[participantAddress]; ok {
			allowedDistributions[participantAddress] = dist
		}
	}

	// Add seeds for preserved participants
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

	// Create weight calculator and calculate
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting params", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}
	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	// Build guardian address set for tiebreaker logic
	guardianEnabled := am.keeper.GetGenesisGuardianEnabled(ctx)
	guardianAddrs := am.keeper.GetGenesisGuardianAddresses(ctx)
	guardianSet := make(map[string]bool, len(guardianAddrs))
	for _, addr := range guardianAddrs {
		guardianSet[addr] = true
	}

	calculator := NewWeightCalculator(
		currentValidatorWeights,
		allowedCommits,
		allowedDistributions,
		validations,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
		weightScaleFactor,
		guardianEnabled,
		guardianSet,
	)
	pocMiningParticipants := calculator.Calculate()

	// Merge preserved participants with PoC mining participants
	var allActiveParticipants []*types.ActiveParticipant

	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index

		if pocParticipant := findParticipantByAddress(pocMiningParticipants, participantAddress); pocParticipant != nil {
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
				Seed:         pocParticipant.Seed,
				Models:       make([]string, 0),
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

	for _, pocParticipant := range pocMiningParticipants {
		if _, alreadyPreserved := preservedParticipantsSet[pocParticipant.Index]; alreadyPreserved {
			continue
		}
		allActiveParticipants = append(allActiveParticipants, pocParticipant)

		am.LogInfo("ComputeNewWeights: Added PoC-only participant", types.PoC,
			"participantAddress", pocParticipant.Index,
			"pocWeight", pocParticipant.Weight)
	}

	am.LogInfo("ComputeNewWeights: Final summary", types.PoC,
		"preservedParticipants", len(preservedParticipants),
		"pocMiningParticipants", len(pocMiningParticipants),
		"totalActiveParticipants", len(allActiveParticipants))

	return allActiveParticipants
}

// filterStoreCommitsFromInferenceNodes filters store commits and their weight distributions
// to exclude weight from inference-serving nodes. Returns filtered commits and distributions.
func (am AppModule) filterStoreCommitsFromInferenceNodes(
	allCommits map[string]types.PoCV2StoreCommit,
	allDistributions map[string]types.MLNodeWeightDistribution,
	inferenceServingNodeIds map[string]bool,
) (map[string]types.PoCV2StoreCommit, map[string]types.MLNodeWeightDistribution) {
	filteredCommits := make(map[string]types.PoCV2StoreCommit)
	filteredDistributions := make(map[string]types.MLNodeWeightDistribution)
	excludedNodeCount := 0

	for participantAddress, commit := range allCommits {
		distribution, hasDistribution := allDistributions[participantAddress]

		if !hasDistribution || len(distribution.Weights) == 0 {
			// No distribution - keep the commit as-is
			filteredCommits[participantAddress] = commit
			continue
		}

		// Filter out inference-serving nodes from distribution
		var filteredWeights []*types.MLNodeWeight
		filteredCount := uint32(0)
		for _, w := range distribution.Weights {
			if inferenceServingNodeIds[w.NodeId] {
				excludedNodeCount++
				am.LogWarn("filterStoreCommitsFromInferenceNodes: Excluding weight from inference-serving node", types.PoC,
					"participantAddress", participantAddress,
					"nodeId", w.NodeId,
					"weight", w.Weight)
			} else {
				filteredWeights = append(filteredWeights, w)
				filteredCount += w.Weight
			}
		}

		if filteredCount == 0 {
			// All nodes were inference-serving - skip this participant
			am.LogWarn("filterStoreCommitsFromInferenceNodes: All nodes inference-serving, skipping participant", types.PoC,
				"participantAddress", participantAddress)
			continue
		}

		// Create filtered commit with adjusted count
		filteredCommit := commit
		filteredCommit.Count = filteredCount
		filteredCommits[participantAddress] = filteredCommit

		// Create filtered distribution
		filteredDistribution := distribution
		filteredDistribution.Weights = filteredWeights
		filteredDistributions[participantAddress] = filteredDistribution
	}

	am.LogInfo("filterStoreCommitsFromInferenceNodes: Summary", types.PoC,
		"excludedNodeCount", excludedNodeCount,
		"originalParticipants", len(allCommits),
		"filteredParticipants", len(filteredCommits))

	return filteredCommits, filteredDistributions
}
