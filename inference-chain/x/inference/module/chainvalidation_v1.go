package inference

import (
	"context"
	"sort"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// WeightCalculatorV1 encapsulates all the data needed to calculate new weights for participants.
// Uses on-chain PoCBatch and PoCValidation (V1 flow).
type WeightCalculatorV1 struct {
	CurrentValidatorWeights map[string]int64
	OriginalBatches         map[string][]types.PoCBatch
	Validations             map[string][]types.PoCValidation
	Participants            map[string]types.Participant
	Seeds                   map[string]types.RandomSeed
	EpochStartBlockHeight   int64
	Logger                  types.InferenceLogger
	WeightScaleFactor       mathsdk.LegacyDec
}

// NewWeightCalculatorV1 creates a new WeightCalculatorV1 instance for V1 on-chain batches.
func NewWeightCalculatorV1(
	currentValidatorWeights map[string]int64,
	originalBatches map[string][]types.PoCBatch,
	validations map[string][]types.PoCValidation,
	participants map[string]types.Participant,
	seeds map[string]types.RandomSeed,
	epochStartBlockHeight int64,
	logger types.InferenceLogger,
	weightScaleFactor mathsdk.LegacyDec,
) *WeightCalculatorV1 {
	return &WeightCalculatorV1{
		CurrentValidatorWeights: currentValidatorWeights,
		OriginalBatches:         originalBatches,
		Validations:             validations,
		Participants:            participants,
		Seeds:                   seeds,
		EpochStartBlockHeight:   epochStartBlockHeight,
		Logger:                  logger,
		WeightScaleFactor:       weightScaleFactor,
	}
}

// Calculate computes the new weights for active participants based on the data in the WeightCalculatorV1
func (wc *WeightCalculatorV1) Calculate() []*types.ActiveParticipant {
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

func (wc *WeightCalculatorV1) getSortedBatchKeys() []string {
	var sortedBatchKeys []string
	for key := range wc.OriginalBatches {
		sortedBatchKeys = append(sortedBatchKeys, key)
	}
	sort.Strings(sortedBatchKeys)
	return sortedBatchKeys
}

func (wc *WeightCalculatorV1) validatedParticipant(participantAddress string) *types.ActiveParticipant {
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

	nodeWeights, claimedWeight := wc.calculateParticipantWeight(wc.OriginalBatches[participantAddress])
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

func (wc *WeightCalculatorV1) getParticipantValidations(participantAddress string) []types.PoCValidation {
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

func (wc *WeightCalculatorV1) pocValidated(vals []types.PoCValidation, participantAddress string) bool {
	totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
	halfWeight := int64(totalWeight / 2)
	shouldContinue := false

	if len(wc.CurrentValidatorWeights) > 0 {
		valOutcome := calculateValidationOutcomeV1(wc.CurrentValidatorWeights, vals)
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

func (wc *WeightCalculatorV1) calculateParticipantWeight(batches []types.PoCBatch) ([]nodeWeight, int64) {
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

		weight = mathsdk.LegacyNewDec(weight).Mul(wc.WeightScaleFactor).TruncateInt64()
		nodeId := batch.NodeId
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

// calculateValidationOutcomeV1 computes valid/invalid weights from V1 PoCValidation.
// Uses FraudDetected field for V1 validations.
func calculateValidationOutcomeV1(currentValidatorsSet map[string]int64, validations []types.PoCValidation) validationOutcome {
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

// ComputeNewWeightsV1 computes new weights for active participants using on-chain PoCBatch (V1 flow).
func (am AppModule) ComputeNewWeightsV1(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	epochStartBlockHeight := upcomingEpoch.PocStartBlockHeight
	am.LogInfo("ComputeNewWeightsV1: computing new weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
	// STEP 1: Get preserved weights from inference-serving MLNodes in current epoch
	preservedParticipants := am.GetPreviousEpochMLNodesWithInferenceAllocation(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeightsV1: Retrieved preserved participants", types.PoC,
		"numPreservedParticipants", len(preservedParticipants))

	// Get current active participants weights
	currentValidatorWeights, err := am.getCurrentValidatorWeights(ctx)
	am.LogInfo("ComputeNewWeightsV1: Retrieved current validator weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"weights", currentValidatorWeights)

	if err != nil {
		am.LogError("ComputeNewWeightsV1: Error getting current validator weights", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// STEP 2: Get PoC batches and filter out batches from inference-serving nodes
	allOriginalBatches, err := am.keeper.GetPoCBatchesByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeightsV1: Error getting batches by PoC stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// Build a set of inference-serving node IDs that should be excluded from PoC mining
	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeightsV1: Found inference-serving nodes", types.PoC,
		"inferenceServingNodeIds", inferenceServingNodeIds)

	// Filter out PoC batches from inference-serving nodes
	originalBatches := am.filterPoCBatchesFromInferenceNodesV1(allOriginalBatches, inferenceServingNodeIds)

	am.LogInfo("ComputeNewWeightsV1: Filtered PoC batches", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"originalBatchesCount", len(allOriginalBatches),
		"filteredBatchesCount", len(originalBatches))

	validations, err := am.keeper.GetPoCValidationByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeightsV1: Error getting PoC validations by stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
	}

	validators := make([]string, len(validations))
	var i = 0
	for address := range validations {
		validators[i] = address
		i += 1
	}
	am.LogInfo("ComputeNewWeightsV1: Retrieved PoC validations", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(validations)", len(validations),
		"validators", validators)

	// Collect all participants, seeds, and filter batches by allowlist
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)
	allowedBatches := make(map[string][]types.PoCBatch)

	var sortedBatchKeys []string
	for key := range originalBatches {
		sortedBatchKeys = append(sortedBatchKeys, key)
	}
	sort.Strings(sortedBatchKeys)

	currentHeight := sdk.UnwrapSDKContext(ctx).BlockHeight()
	allowlistActive := am.keeper.IsParticipantAllowlistActive(ctx, currentHeight)

	isAllowed := func(addr, label string) bool {
		if !allowlistActive {
			return true
		}
		allowed, err := am.keeper.IsAllowlistedParticipant(ctx, addr)
		if err != nil {
			am.LogError("ComputeNewWeightsV1: Invalid "+label+" address format", types.PoC,
				"address", addr, "error", err)
			return false
		}
		if !allowed {
			am.LogInfo("ComputeNewWeightsV1: Skipping non-allowlisted "+label, types.PoC,
				"address", addr)
			return false
		}
		return true
	}

	for _, participantAddress := range sortedBatchKeys {
		if !isAllowed(participantAddress, "participant") {
			continue
		}

		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("ComputeNewWeightsV1: Error getting participant", types.PoC,
				"address", participantAddress,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress)
		if !found {
			am.LogError("ComputeNewWeightsV1: Participant didn't submit the seed for the upcoming epoch", types.PoC,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
				"participant", participantAddress)
			continue
		}
		seeds[participantAddress] = seed
		allowedBatches[participantAddress] = originalBatches[participantAddress]
	}

	// STEP 3: Add seeds for preserved participants if they have submitted seeds
	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index
		if seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress); found {
			preservedParticipant.Seed = &seed
			seeds[participantAddress] = seed
			am.LogInfo("ComputeNewWeightsV1: Added seed for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		} else {
			am.LogWarn("ComputeNewWeightsV1: No seed found for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		}
	}

	// STEP 4: Create WeightCalculatorV1 and calculate PoC mining participants (excluding inference-serving nodes)
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("ComputeNewWeightsV1: Error getting params", types.PoC, "error", err.Error())
		return nil
	}
	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()
	calculator := NewWeightCalculatorV1(
		currentValidatorWeights,
		allowedBatches,
		validations,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
		weightScaleFactor,
	)
	pocMiningParticipants := calculator.Calculate()

	// STEP 4: Merge preserved participants with PoC mining participants
	var allActiveParticipants []*types.ActiveParticipant

	// Add preserved participants first
	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index

		if !isAllowed(participantAddress, "preserved participant") {
			continue
		}

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

			am.LogInfo("ComputeNewWeightsV1: Merged preserved and PoC participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight,
				"pocWeight", pocParticipant.Weight,
				"combinedWeight", combinedWeight,
				"combinedMLNodes", combinedMLNodes)
		} else {
			// Only preserved participant (no PoC mining activity)
			allActiveParticipants = append(allActiveParticipants, preservedParticipant)

			am.LogInfo("ComputeNewWeightsV1: Added preserved-only participant", types.PoC,
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
		if _, alreadyPreserved := preservedParticipantsSet[pocParticipant.Index]; alreadyPreserved {
			continue
		}
		// Defensive allowlist check (should already be filtered via allowedBatches, but explicit is safer)
		if !isAllowed(pocParticipant.Index, "PoC-only participant") {
			continue
		}
		allActiveParticipants = append(allActiveParticipants, pocParticipant)

		am.LogInfo("ComputeNewWeightsV1: Added PoC-only participant", types.PoC,
			"participantAddress", pocParticipant.Index,
			"pocWeight", pocParticipant.Weight)
	}

	am.LogInfo("ComputeNewWeightsV1: Final summary", types.PoC,
		"preservedParticipants", len(preservedParticipants),
		"pocMiningParticipants", len(pocMiningParticipants),
		"totalActiveParticipants", len(allActiveParticipants))

	return allActiveParticipants
}

// filterPoCBatchesFromInferenceNodesV1 removes PoC batches from nodes that should be serving inference
func (am AppModule) filterPoCBatchesFromInferenceNodesV1(allBatches map[string][]types.PoCBatch, inferenceServingNodeIds map[string]bool) map[string][]types.PoCBatch {
	filteredBatches := make(map[string][]types.PoCBatch)
	excludedBatchCount := 0

	for participantAddress, batches := range allBatches {
		var validBatches []types.PoCBatch

		for _, batch := range batches {
			// Check if this batch is from an inference-serving node
			if inferenceServingNodeIds[batch.NodeId] {
				// Exclude this batch - the node should have been serving inference, not mining PoC
				excludedBatchCount++
				am.LogWarn("filterPoCBatchesFromInferenceNodesV1: Excluding PoC batch from inference-serving node", types.PoC,
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

	am.LogInfo("filterPoCBatchesFromInferenceNodesV1: Summary", types.PoC,
		"excludedBatchCount", excludedBatchCount,
		"originalParticipants", len(allBatches),
		"filteredParticipants", len(filteredBatches))

	return filteredBatches
}
