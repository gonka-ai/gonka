package inference

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"github.com/shopspring/decimal"
)

// expectedBlockDurationSec is the expected duration of a block in seconds (5.41).
var expectedBlockDurationSec = decimal.New(541, -2)

func CalculateTimeNormalizationFactor(
	genStartTimestamp, exchangeEndTimestamp int64,
	pocStageDuration, pocExchangeDuration int64,
) mathsdk.LegacyDec {
	if genStartTimestamp == 0 || exchangeEndTimestamp == 0 {
		return mathsdk.LegacyOneDec()
	}

	actualDurationSec := exchangeEndTimestamp - genStartTimestamp
	if actualDurationSec <= 0 {
		return mathsdk.LegacyOneDec()
	}

	expectedBlocks := pocStageDuration + pocExchangeDuration
	expectedDurationSec := decimal.NewFromInt(expectedBlocks).Mul(expectedBlockDurationSec)
	actualDurationDecimal := decimal.NewFromInt(actualDurationSec)

	factor, err := decimalToLegacyDec(expectedDurationSec.Div(actualDurationDecimal))
	if err != nil {
		return mathsdk.LegacyOneDec()
	}
	return factor
}

// PoCWeightCalculator encapsulates all the data needed to calculate new weights for participants.
// Uses off-chain store commits and weight distributions instead of on-chain batches.
type PoCWeightCalculator struct {
	ModelVotingPowers       map[string]map[string]int64 // model -> (participant -> votingPower)
	TotalNetworkWeight      int64
	StoreCommits            map[types.PoCParticipantModelKey]types.PoCV2StoreCommit
	NodeWeightDistributions map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution
	Validations             map[types.PoCParticipantModelKey][]types.PoCValidationV2
	PocParams               *types.PocParams
	Participants            map[string]types.Participant
	Seeds                   map[string]types.RandomSeed
	EpochStartBlockHeight   int64
	Logger                  types.InferenceLogger
	TimeNormalizationFactor mathsdk.LegacyDec
	GuardianEnabled         bool
	GuardianAddresses       map[string]bool
	AppHash                 string
	ValidationSlots         int

	// sortedVotingPowers caches PrepareSortedEntries output per model.
	// Avoids re-sorting the same voting power map for every participant on the same model.
	sortedVotingPowers map[string]sortedModelVP
}

// sortedModelVP holds pre-computed sorted entries and total weight for a model.
type sortedModelVP struct {
	entries     []calculations.WeightEntry
	totalWeight int64
}

// NewPoCWeightCalculator creates a new PoCWeightCalculator instance.
func NewPoCWeightCalculator(
	modelVotingPowers map[string]map[string]int64,
	totalNetworkWeight int64,
	storeCommits map[types.PoCParticipantModelKey]types.PoCV2StoreCommit,
	nodeWeightDistributions map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution,
	validations map[types.PoCParticipantModelKey][]types.PoCValidationV2,
	pocParams *types.PocParams,
	participants map[string]types.Participant,
	seeds map[string]types.RandomSeed,
	epochStartBlockHeight int64,
	logger types.InferenceLogger,
	timeNormalizationFactor mathsdk.LegacyDec,
	guardianEnabled bool,
	guardianAddresses map[string]bool,
	appHash string,
	validationSlots int,
) *PoCWeightCalculator {
	// Pre-compute sorted voting power entries per model to avoid re-sorting
	// for every participant in pocValidated.
	sortedVP := make(map[string]sortedModelVP, len(modelVotingPowers))
	if validationSlots > 0 {
		for _, modelID := range sortedKeys(modelVotingPowers) {
			vp := modelVotingPowers[modelID]
			entries, total := calculations.PrepareSortedEntries(vp)
			sortedVP[modelID] = sortedModelVP{entries: entries, totalWeight: total}
		}
	}

	return &PoCWeightCalculator{
		ModelVotingPowers:       modelVotingPowers,
		TotalNetworkWeight:      totalNetworkWeight,
		StoreCommits:            storeCommits,
		NodeWeightDistributions: nodeWeightDistributions,
		Validations:             validations,
		PocParams:               pocParams,
		Participants:            participants,
		Seeds:                   seeds,
		EpochStartBlockHeight:   epochStartBlockHeight,
		Logger:                  logger,
		TimeNormalizationFactor: timeNormalizationFactor,
		GuardianEnabled:         guardianEnabled,
		GuardianAddresses:       guardianAddresses,
		AppHash:                 appHash,
		ValidationSlots:         validationSlots,
		sortedVotingPowers:      sortedVP,
	}
}

// Calculate computes the new weights for active participants.
func (wc *PoCWeightCalculator) Calculate() []*types.ActiveParticipant {
	sortedKeys := wc.getSortedParticipantModelKeys()
	activeParticipantsMap := make(map[string]*types.ActiveParticipant)

	for _, key := range sortedKeys {
		activeParticipant := wc.validatedParticipant(key)
		if activeParticipant == nil {
			continue
		}
		existing, found := activeParticipantsMap[activeParticipant.Index]
		if !found {
			activeParticipantsMap[activeParticipant.Index] = activeParticipant
			wc.Logger.LogInfo("Calculate: Setting compute validator.", types.PoC, "activeParticipant", activeParticipant, "modelId", key.ModelID)
			continue
		}
		existing.Weight += activeParticipant.Weight
		existing.Models = append(existing.Models, activeParticipant.Models...)
		existing.MlNodes = append(existing.MlNodes, activeParticipant.MlNodes...)
		wc.Logger.LogInfo("Calculate: Merging model contribution", types.PoC,
			"participant", activeParticipant.Index,
			"modelId", key.ModelID,
			"weight", activeParticipant.Weight)
	}

	var participantAddresses []string
	for participantAddress := range activeParticipantsMap {
		participantAddresses = append(participantAddresses, participantAddress)
	}
	slices.Sort(participantAddresses)

	activeParticipants := make([]*types.ActiveParticipant, 0, len(participantAddresses))
	for _, participantAddress := range participantAddresses {
		activeParticipants = append(activeParticipants, activeParticipantsMap[participantAddress])
	}
	return activeParticipants
}

func (wc *PoCWeightCalculator) getSortedParticipantModelKeys() []types.PoCParticipantModelKey {
	return sortedStoreCommitKeys(wc.StoreCommits)
}

func (wc *PoCWeightCalculator) validatedParticipant(key types.PoCParticipantModelKey) *types.ActiveParticipant {
	participant, ok := wc.Participants[key.ParticipantAddress]
	if !ok {
		wc.Logger.LogError("Calculate: Participant not found", types.PoC, "address", key.ParticipantAddress, "modelId", key.ModelID)
		return nil
	}

	vals := wc.getParticipantValidations(key)
	if len(vals) == 0 {
		wc.Logger.LogError("Calculate: No validations for participant found", types.PoC, "participant", key.ParticipantAddress, "modelId", key.ModelID)
		return nil
	}

	// Get claimed weight from store commit and per-node weights from distribution
	nodeWeights, claimedWeight := wc.calculateParticipantWeight(key)
	if claimedWeight < 1 {
		wc.Logger.LogWarn("Calculate: Participant has non-positive claimedWeight.", types.PoC, "participant", key.ParticipantAddress, "modelId", key.ModelID, "claimedWeight", claimedWeight)
		return nil
	}
	wc.Logger.LogInfo("Calculate: participant claims weight", types.PoC, "participant", key.ParticipantAddress, "modelId", key.ModelID, "claimedWeight", claimedWeight)

	if participant.ValidatorKey == "" {
		wc.Logger.LogError("Calculate: Participant hasn't provided their validator key.", types.PoC, "participant", key.ParticipantAddress, "modelId", key.ModelID)
		return nil
	}

	if !wc.pocValidated(vals, key) {
		return nil
	}

	seed, found := wc.Seeds[key.ParticipantAddress]
	if !found {
		wc.Logger.LogError("Calculate: Seed not found", types.PoC, "blockHeight", wc.EpochStartBlockHeight, "participant", key.ParticipantAddress, "modelId", key.ModelID)
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
		Models:       []string{key.ModelID},
		MlNodes:      modelMLNodesArray,
	}
	return activeParticipant
}

func (wc *PoCWeightCalculator) getParticipantValidations(key types.PoCParticipantModelKey) []types.PoCValidationV2 {
	vals := wc.Validations[key]

	validators := make([]string, len(vals))
	for i, v := range vals {
		validators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("Calculate: Found ALL submitted validations for participant", types.PoC,
		"participant", key.ParticipantAddress, "modelId", key.ModelID, "len(vals)", len(vals), "validators", validators)

	// Filter to validations from participants with voting power for this model.
	// When no voting-power snapshot exists for the model yet, keep the original
	// validations list for logging and guardian handling. pocValidated() still
	// rejects later if the model has no voting-power data.
	modelVP := wc.ModelVotingPowers[key.ModelID]
	filteredVals := make([]types.PoCValidationV2, 0, len(vals))
	if len(modelVP) == 0 {
		filteredVals = vals
	} else {
		for _, v := range vals {
			if _, ok := modelVP[v.ValidatorParticipantAddress]; ok {
				filteredVals = append(filteredVals, v)
			}
		}
	}

	filteredValidators := make([]string, len(filteredVals))
	for i, v := range filteredVals {
		filteredValidators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("Calculate: filtered validations to model validators with voting power", types.PoC,
		"participant", key.ParticipantAddress, "modelId", key.ModelID, "len(vals)", len(filteredVals), "validators", filteredValidators)

	return filteredVals
}

// pocValidated checks if the participant passed validation by majority vote.
// Uses per-model voting powers (delegation-resolved) for both slot sampling and threshold.
// Only DIRECT members (participants who submitted PoC for this model) have voting power.
// Delegated consensus weight flows into DIRECT members' voting power.
func (wc *PoCWeightCalculator) pocValidated(vals []types.PoCValidationV2, key types.PoCParticipantModelKey) bool {
	votingPowers := wc.ModelVotingPowers[key.ModelID]
	if len(votingPowers) == 0 {
		wc.Logger.LogWarn("Calculate: No voting powers for model. Rejecting.", types.PoC,
			"participant", key.ParticipantAddress, "modelId", key.ModelID)
		return false
	}

	if wc.ValidationSlots > 0 {
		// Slot-based: sample validators, count per-slot (each slot = 1 weight).
		// Preserves duplicates -- a validator with 2 slots gets their vote counted twice.
		// Only the model-local share of total network weight is sampled; the
		// unsampled remainder behaves like abstention against the full slot count.
		cached := wc.sortedVotingPowers[key.ModelID]
		sampledSlots := calculations.ComputeSampledSlotCount(cached.totalWeight, wc.TotalNetworkWeight, wc.ValidationSlots)
		assigned := calculations.GetSlotsFromSorted(
			wc.AppHash, key.ParticipantAddress, key.ModelID,
			cached.entries, cached.totalWeight, sampledSlots,
		)
		voteMap := make(map[string]int64)
		for _, v := range vals {
			voteMap[v.ValidatorParticipantAddress] = v.ValidatedWeight
		}
		totalSlots := int64(wc.ValidationSlots)
		var validSlots, invalidSlots int64
		for _, slotValidator := range assigned {
			vote, hasVote := voteMap[slotValidator]
			if !hasVote {
				continue
			}
			if vote > 0 {
				validSlots++
			} else {
				invalidSlots++
			}
		}
		twoThirdsSlots := totalSlots * 2 / 3
		if validSlots > twoThirdsSlots {
			wc.Logger.LogInfo("Calculate: Valid majority (slot-sampled). Accepting.", types.PoC,
				"participant", key.ParticipantAddress, "modelId", key.ModelID,
				"validSlots", validSlots, "totalSlots", totalSlots)
			return true
		}
		if invalidSlots > twoThirdsSlots {
			wc.Logger.LogWarn("Calculate: Invalid majority (slot-sampled). Rejecting.", types.PoC,
				"participant", key.ParticipantAddress, "modelId", key.ModelID,
				"invalidSlots", invalidSlots, "totalSlots", totalSlots)
			return false
		}
		return wc.guardianProtection(vals, key, ValidationOutcome{
			TotalWeight:   totalSlots,
			ValidWeight:   validSlots,
			InvalidWeight: invalidSlots,
		})
	}

	// Non-slot: weight approvals by votingPower, threshold against totalNetworkWeight.
	outcome := calculateValidationOutcome(votingPowers, vals)
	outcome.TotalWeight = wc.TotalNetworkWeight
	twoThirds := wc.TotalNetworkWeight * 2 / 3
	if outcome.ValidWeight > twoThirds {
		wc.Logger.LogInfo("Calculate: Valid majority. Accepting.", types.PoC,
			"participant", key.ParticipantAddress, "modelId", key.ModelID,
			"validWeight", outcome.ValidWeight, "totalNetworkWeight", wc.TotalNetworkWeight)
		return true
	}
	if outcome.InvalidWeight > twoThirds {
		wc.Logger.LogWarn("Calculate: Invalid majority. Rejecting.", types.PoC,
			"participant", key.ParticipantAddress, "modelId", key.ModelID,
			"invalidWeight", outcome.InvalidWeight, "totalNetworkWeight", wc.TotalNetworkWeight)
		return false
	}
	return wc.guardianProtection(vals, key, outcome)
}

// ValidationOutcome holds aggregated vote weight sums.
type ValidationOutcome struct {
	TotalWeight   int64
	ValidWeight   int64
	InvalidWeight int64
}

// guardianProtection handles tie-breaking when no clear majority exists.
// All voting guardians must agree unanimously for the decision to pass.
func (wc *PoCWeightCalculator) guardianProtection(vals []types.PoCValidationV2, key types.PoCParticipantModelKey, outcome ValidationOutcome) bool {
	if !wc.GuardianEnabled || len(wc.GuardianAddresses) == 0 {
		wc.Logger.LogWarn("Calculate: No majority and no guardians. Rejecting.", types.PoC,
			"participant", key.ParticipantAddress,
			"modelId", key.ModelID,
			"validWeight", outcome.ValidWeight,
			"invalidWeight", outcome.InvalidWeight,
			"totalWeight", outcome.TotalWeight,
		)
		return false
	}

	guardianValidCount, guardianInvalidCount := 0, 0
	for _, v := range vals {
		if wc.GuardianAddresses[v.ValidatorParticipantAddress] {
			if v.ValidatedWeight > 0 {
				guardianValidCount++
			} else {
				guardianInvalidCount++
			}
		}
	}

	if guardianValidCount > 0 && guardianInvalidCount == 0 {
		wc.Logger.LogInfo("Calculate: Guardian tiebreaker - unanimous valid. Accepting.", types.PoC,
			"participant", key.ParticipantAddress,
			"modelId", key.ModelID,
			"guardianValidCount", guardianValidCount,
		)
		return true
	}

	if guardianInvalidCount > 0 && guardianValidCount == 0 {
		wc.Logger.LogWarn("Calculate: Guardian tiebreaker - unanimous invalid. Rejecting.", types.PoC,
			"participant", key.ParticipantAddress,
			"modelId", key.ModelID,
			"guardianInvalidCount", guardianInvalidCount,
		)
		return false
	}

	wc.Logger.LogWarn("Calculate: No majority and guardians split. Rejecting.", types.PoC,
		"participant", key.ParticipantAddress,
		"modelId", key.ModelID,
		"guardianValidCount", guardianValidCount,
		"guardianInvalidCount", guardianInvalidCount,
	)
	return false
}

type nodeWeight struct {
	nodeId string
	weight int64
}

// calculateParticipantWeight computes the claimed weight from store commit and weight distribution.
// Total weight comes from StoreCommit.Count (scaled by weightScaleFactor and timeNormalizationFactor).
// Per-node weights come from MLNodeWeightDistribution.
// PocWeight is raw proven compute (timeNormalizationFactor only, no model coefficient).
// Model coefficients are applied at the aggregation step in AggregateConsensusWeight.
func (wc *PoCWeightCalculator) calculateParticipantWeight(key types.PoCParticipantModelKey) ([]nodeWeight, int64) {
	commit, hasCommit := wc.StoreCommits[key]
	if !hasCommit || commit.Count == 0 {
		return nil, 0
	}

	normFactor := mathsdk.LegacyOneDec()
	if wc.TimeNormalizationFactor.IsPositive() {
		normFactor = wc.TimeNormalizationFactor
	}

	totalWeight := mathsdk.LegacyNewDec(int64(commit.Count)).Mul(normFactor).TruncateInt64()

	distribution, hasDistribution := wc.NodeWeightDistributions[key]
	if !hasDistribution || len(distribution.Weights) == 0 {
		wc.Logger.LogWarn("Calculate: No weight distribution for participant, skipping PoC weight", types.PoC,
			"participant", key.ParticipantAddress, "modelId", key.ModelID, "totalWeight", totalWeight)
		return nil, 0
	}

	nodeWeightsSlice := make([]nodeWeight, 0, len(distribution.Weights))
	for _, w := range distribution.Weights {
		scaledWeight := mathsdk.LegacyNewDec(int64(w.Weight)).Mul(normFactor).TruncateInt64()
		nodeWeightsSlice = append(nodeWeightsSlice, nodeWeight{nodeId: w.NodeId, weight: scaledWeight})
	}
	slices.SortFunc(nodeWeightsSlice, func(a, b nodeWeight) int {
		return strings.Compare(a.nodeId, b.nodeId)
	})
	wc.Logger.LogInfo("Calculate: Calculating participant raw weight", types.PoC,
		"participant", key.ParticipantAddress,
		"modelId", key.ModelID,
		"timeNormalizationFactor", wc.TimeNormalizationFactor,
		"count", commit.Count,
		"totalWeight", totalWeight,
	)

	return nodeWeightsSlice, totalWeight
}

// calculateValidationOutcome computes valid/invalid weights from validations.
// validated_weight > 0 is a valid vote, validated_weight <= 0 is invalid.
func calculateValidationOutcome(currentValidatorsSet map[string]int64, validations []types.PoCValidationV2) ValidationOutcome {
	var validWeight, invalidWeight int64
	for _, v := range validations {
		if weight, ok := currentValidatorsSet[v.ValidatorParticipantAddress]; ok {
			if v.ValidatedWeight > 0 {
				validWeight += weight
			} else {
				invalidWeight += weight
			}
		}
	}
	return ValidationOutcome{
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

// PreservedParticipantsFromCurrentEpoch reads the preserved-nodes snapshot for the
// currently-active epoch (about to be replaced by upcomingEpoch) and returns the
// corresponding ActiveParticipant records. Used by ComputeNewWeights to carry preserved
// weight into the next epoch.
func (am AppModule) PreservedParticipantsFromCurrentEpoch(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	preservedParticipants := make(map[string]*types.ActiveParticipant)

	// Skip for first epoch or if we can't get current epoch (which is about to end)
	if upcomingEpoch.Index <= 1 {
		am.LogInfo("PreservedParticipantsFromCurrentEpoch: Skipping for first epoch", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	// Get current epoch group data (the epoch that's about to end)
	// At this point in the flow, we're still in the current epoch - the transition happens later in onSetNewValidatorsStage
	currentEpochGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil {
		am.LogError("PreservedParticipantsFromCurrentEpoch: Unable to get current epoch group", types.PoC, "error", err.Error())
		return nil
	}
	if currentEpochGroup.GroupData.EpochIndex != upcomingEpoch.Index-1 {
		am.LogError("PreservedParticipantsFromCurrentEpoch: Current epoch group does not match upcoming epoch", types.PoC,
			"currentEpochGroup.EpochIndex", currentEpochGroup.GroupData.EpochIndex,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	am.LogInfo("PreservedParticipantsFromCurrentEpoch: Processing current epoch group (about to end)", types.PoC,
		"currentEpochGroup.EpochIndex", currentEpochGroup.GroupData.EpochIndex,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"pocStartBlockHeight", currentEpochGroup.GroupData.PocStartBlockHeight,
		"len(validationWeight)", len(currentEpochGroup.GroupData.ValidationWeights))

	preservedSnapshot, found, err := am.keeper.GetPreservedNodesSnapshot(ctx)
	if err != nil {
		am.LogError("PreservedParticipantsFromCurrentEpoch: Error getting preserved nodes snapshot", types.PoC,
			"epochIndex", currentEpochGroup.GroupData.EpochIndex,
			"error", err)
		return nil
	}
	if !found {
		am.LogWarn("PreservedParticipantsFromCurrentEpoch: Preserved nodes snapshot not found", types.PoC,
			"epochIndex", currentEpochGroup.GroupData.EpochIndex)
		return nil
	}

	preservedNodesByParticipant, err := am.GetPreservedNodesByParticipant(ctx, currentEpochGroup.GroupData.EpochIndex, &preservedSnapshot)
	if err != nil {
		am.LogError("PreservedParticipantsFromCurrentEpoch: Error getting preserved nodes by participant", types.PoC, "error", err)
		return nil
	}

	// Iterate through all validation weights in current epoch to find inference-serving MLNodes
	for _, validationWeight := range currentEpochGroup.GroupData.ValidationWeights {
		participantAddress := validationWeight.MemberAddress

		modelBuckets, ok := preservedNodesByParticipant[participantAddress]
		if !ok || len(modelBuckets) == 0 {
			continue
		}

		participant, found := am.keeper.GetParticipant(ctx, participantAddress)
		if !found {
			am.LogError("PreservedParticipantsFromCurrentEpoch: Participant not found", types.PoC,
				"participantAddress", participantAddress)
			continue
		}

		// Build per-model MlNodes arrays with Models populated
		var models []string
		var mlNodeArrays []*types.ModelMLNodes

		// Sort model IDs for deterministic order
		sortedModelIds := make([]string, 0, len(modelBuckets))
		for modelId := range modelBuckets {
			sortedModelIds = append(sortedModelIds, modelId)
		}
		slices.Sort(sortedModelIds)

		for _, modelId := range sortedModelIds {
			nodes := modelBuckets[modelId]
			filtered := make([]*types.MLNodeInfo, 0, len(nodes))
			for _, node := range nodes {
				if node.NodeId != "" {
					filtered = append(filtered, node)
				}
			}
			if len(filtered) > 0 {
				models = append(models, modelId)
				mlNodeArrays = append(mlNodeArrays, &types.ModelMLNodes{MlNodes: filtered})
			}
		}

		if len(mlNodeArrays) == 0 {
			continue
		}

		activeParticipant := &types.ActiveParticipant{
			Index:        participantAddress,
			ValidatorKey: participant.ValidatorKey,
			InferenceUrl: participant.InferenceUrl,
			Seed:         nil,
			Models:       models,
			MlNodes:      mlNodeArrays,
		}
		activeParticipant.Weight = RecalculateWeight(activeParticipant)

		preservedParticipants[participantAddress] = activeParticipant

		am.LogInfo("PreservedParticipantsFromCurrentEpoch: Created preserved participant", types.PoC,
			"participantAddress", participantAddress,
			"totalWeight", activeParticipant.Weight,
			"models", models)
	}

	am.LogInfo("PreservedParticipantsFromCurrentEpoch: Summary", types.PoC,
		"totalPreservedParticipants", len(preservedParticipants))

	participantsSlice := make([]*types.ActiveParticipant, 0, len(preservedParticipants))
	for _, participant := range preservedParticipants {
		participantsSlice = append(participantsSlice, participant)
	}
	// Sort participants by address for consistent order
	slices.SortFunc(participantsSlice, func(a, b *types.ActiveParticipant) int {
		if a.Index < b.Index {
			return -1
		}
		if a.Index > b.Index {
			return 1
		}
		return 0
	})

	return participantsSlice
}

// GetPreservedNodesByParticipant returns per-model preserved node buckets.
// Result: participant address -> model ID -> preserved nodes for that model.
func (am AppModule) GetPreservedNodesByParticipant(
	ctx context.Context,
	epochId uint64,
	preservedSnapshot *types.PreservedNodesSnapshot,
) (map[string]map[string][]*types.MLNodeInfo, error) {
	result := make(map[string]map[string][]*types.MLNodeInfo)
	if preservedSnapshot == nil {
		return result, nil
	}

	for _, modelNodes := range preservedSnapshot.ModelPreservedNodes {
		if len(modelNodes.Participants) == 0 {
			continue
		}

		subgroupData, found := am.keeper.GetEpochGroupData(ctx, epochId, modelNodes.ModelId)
		if !found {
			am.LogWarn("GetPreservedNodesByParticipant: Model subgroup not found for preserved snapshot", types.PoC,
				"epochId", epochId,
				"modelId", modelNodes.ModelId)
			continue
		}

		preservedNodeSet := make(map[string]map[string]struct{}, len(modelNodes.Participants))
		for _, p := range modelNodes.Participants {
			if p == nil {
				continue
			}
			nodeSet := make(map[string]struct{}, len(p.NodeIds))
			for _, nodeID := range p.NodeIds {
				nodeSet[nodeID] = struct{}{}
			}
			preservedNodeSet[p.ParticipantId] = nodeSet
		}

		for _, validationWeight := range subgroupData.ValidationWeights {
			participantNodes, ok := preservedNodeSet[validationWeight.MemberAddress]
			if !ok {
				continue
			}
			for _, node := range validationWeight.MlNodes {
				if node == nil {
					continue
				}
				if _, ok := participantNodes[node.NodeId]; !ok {
					continue
				}
				if _, ok := result[validationWeight.MemberAddress]; !ok {
					result[validationWeight.MemberAddress] = make(map[string][]*types.MLNodeInfo)
				}
				copyNode := *node
				copyNode.TimeslotAllocation = append([]bool(nil), node.TimeslotAllocation...)
				result[validationWeight.MemberAddress][modelNodes.ModelId] = append(result[validationWeight.MemberAddress][modelNodes.ModelId], &copyNode)
			}
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

// mergeByModel merges per-model MLNode arrays from two participants (preserved + PoC).
// For each model present in either side, nodes are combined and deduped by NodeId.
// Returns sorted model list and corresponding MlNodes arrays.
func mergeByModel(
	preservedModels []string, preservedMLNodes []*types.ModelMLNodes,
	pocModels []string, pocMLNodes []*types.ModelMLNodes,
) ([]string, []*types.ModelMLNodes) {
	modelNodes := make(map[string][]*types.MLNodeInfo)

	// Add preserved nodes per model
	for i, modelId := range preservedModels {
		if i < len(preservedMLNodes) && preservedMLNodes[i] != nil {
			for _, node := range preservedMLNodes[i].MlNodes {
				if node != nil && node.NodeId != "" {
					modelNodes[modelId] = append(modelNodes[modelId], node)
				}
			}
		}
	}

	// Add PoC nodes per model, dedup by NodeId
	for i, modelId := range pocModels {
		if i < len(pocMLNodes) && pocMLNodes[i] != nil {
			existing := make(map[string]bool)
			for _, node := range modelNodes[modelId] {
				existing[node.NodeId] = true
			}
			for _, node := range pocMLNodes[i].MlNodes {
				if node != nil && node.NodeId != "" && !existing[node.NodeId] {
					modelNodes[modelId] = append(modelNodes[modelId], node)
				}
			}
		}
	}

	// Build sorted output
	sortedModels := make([]string, 0, len(modelNodes))
	for modelId := range modelNodes {
		sortedModels = append(sortedModels, modelId)
	}
	slices.Sort(sortedModels)

	result := make([]*types.ModelMLNodes, 0, len(sortedModels))
	for _, modelId := range sortedModels {
		result = append(result, &types.ModelMLNodes{MlNodes: modelNodes[modelId]})
	}
	return sortedModels, result
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

// mergeMLNodeArrays is a legacy helper for the V1 path.
// It flattens all nodes into MlNodes[0] and deduplicates by NodeId.
func mergeMLNodeArrays(preservedMLNodes, pocMLNodes []*types.ModelMLNodes) []*types.ModelMLNodes {
	_, merged := mergeByModel(nil, preservedMLNodes, nil, pocMLNodes)
	// Flatten all model buckets into a single array (V1 has one model)
	var allNodes []*types.MLNodeInfo
	for _, m := range merged {
		if m != nil {
			allNodes = append(allNodes, m.MlNodes...)
		}
	}
	if len(allNodes) == 0 {
		return nil
	}
	return []*types.ModelMLNodes{{MlNodes: allNodes}}
}

// getInferenceServingNodeIds returns preserved node IDs for the current episode snapshot,
// keyed by participant_id -> node_id set. HardwareNode.LocalId is unique per
// participant only, so callers must consult by (participantAddress, nodeId).
func (am AppModule) getInferenceServingNodeIds(ctx context.Context, upcomingEpoch types.Epoch) map[string]map[string]struct{} {
	inferenceServingNodeIds := make(map[string]map[string]struct{})

	if upcomingEpoch.Index <= 1 {
		return inferenceServingNodeIds
	}

	preservedSnapshot, found, err := am.keeper.GetPreservedNodesSnapshot(ctx)
	if err != nil {
		am.LogError("getInferenceServingNodeIds: Unable to get preserved nodes snapshot", types.PoC, "error", err.Error())
		return inferenceServingNodeIds
	}
	if !found {
		return inferenceServingNodeIds
	}

	totalNodes := 0
	for _, modelNodes := range preservedSnapshot.ModelPreservedNodes {
		for _, p := range modelNodes.Participants {
			if p == nil {
				continue
			}
			nodeSet, ok := inferenceServingNodeIds[p.ParticipantId]
			if !ok {
				nodeSet = make(map[string]struct{})
				inferenceServingNodeIds[p.ParticipantId] = nodeSet
			}
			for _, nodeID := range p.NodeIds {
				if _, exists := nodeSet[nodeID]; !exists {
					nodeSet[nodeID] = struct{}{}
					totalNodes++
				}
			}
		}
	}
	am.LogInfo("getInferenceServingNodeIds: preserved snapshot loaded", types.PoC,
		"epoch", upcomingEpoch.Index,
		"participantCount", len(inferenceServingNodeIds),
		"nodeCount", totalNodes)

	return inferenceServingNodeIds
}

// ComputeNewWeights computes new weights for active participants using off-chain store commits.
func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	epochStartBlockHeight := upcomingEpoch.PocStartBlockHeight
	am.LogInfo("ComputeNewWeights: computing new weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)

	// Get preserved weights from inference-serving MLNodes
	preservedParticipants := am.PreservedParticipantsFromCurrentEpoch(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeights: Retrieved preserved participants", types.PoC,
		"numPreservedParticipants", len(preservedParticipants))

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
	for key := range validations {
		validators[i] = key.ParticipantAddress + ":" + key.ModelID
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
	allowedCommits := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)
	allowedDistributions := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)

	sortedCommitKeys := sortedStoreCommitKeys(storeCommits)

	for _, commitKey := range sortedCommitKeys {
		participantAddress := commitKey.ParticipantAddress
		// Check participant allowlist
		if !am.keeper.IsParticipantAllowed(ctx, epochStartBlockHeight, participantAddress) {
			am.LogInfo("ComputeNewWeights: Participant not in allowlist, skipping", types.PoC,
				"address", participantAddress,
				"modelId", commitKey.ModelID,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
			continue
		}

		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("ComputeNewWeights: Error getting participant", types.PoC,
				"address", participantAddress,
				"modelId", commitKey.ModelID,
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
				"participant", participantAddress,
				"modelId", commitKey.ModelID)
			continue
		}
		seeds[participantAddress] = seed
		allowedCommits[commitKey] = storeCommits[commitKey]
		if dist, ok := weightDistributions[commitKey]; ok {
			allowedDistributions[commitKey] = dist
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
	guardianEnabled := am.keeper.GetGenesisGuardianEnabled(ctx)
	guardianAddrs := am.keeper.GetGenesisGuardianAddresses(ctx)
	guardianSet := make(map[string]bool, len(guardianAddrs))
	for _, addr := range guardianAddrs {
		accAddr, err := utils.OperatorAddressToAccAddress(addr)
		if err != nil {
			am.LogWarn("ComputeNewWeights: Failed to convert guardian address", types.PoC,
				"operatorAddress", addr, "error", err)
			continue
		}
		guardianSet[accAddr] = true
	}

	guardianAccAddrs := make([]string, 0, len(guardianSet))
	for addr := range guardianSet {
		guardianAccAddrs = append(guardianAccAddrs, addr)
	}
	am.LogInfo("ComputeNewWeights: Resolved guardian addresses", types.PoC,
		"guardianEnabled", guardianEnabled,
		"guardianAccAddrs", guardianAccAddrs)

	var appHash string
	var validationSlots int
	timeNormalizationFactor := mathsdk.LegacyOneDec()
	modelVotingPowers := make(map[string]map[string]int64)
	totalNetworkWeight := int64(0)

	snapshot, snapshotFound, _ := am.keeper.GetPoCValidationSnapshot(ctx, epochStartBlockHeight)
	if snapshotFound {
		if params.PocParams.ValidationSlots > 0 {
			appHash = snapshot.AppHash
			validationSlots = int(params.PocParams.ValidationSlots)
		}
		if params.PocParams.PocNormalizationEnabled {
			timeNormalizationFactor = CalculateTimeNormalizationFactor(
				snapshot.GenerationStartTimestamp,
				snapshot.ExchangeEndTimestamp,
				params.EpochParams.PocStageDuration,
				params.EpochParams.PocExchangeDuration,
			)
		}
		// Load per-model voting powers from snapshot
		for _, mvw := range snapshot.ModelVotingPowers {
			modelVotingPowers[mvw.ModelId] = types.VotingPowerSliceToMap(mvw.VotingPowers)
		}
		totalNetworkWeight = snapshot.TotalNetworkWeight
		am.LogInfo("ComputeNewWeights: Using validation snapshot", types.PoC,
			"appHash", appHash,
			"validationSlots", validationSlots,
			"numModels", len(modelVotingPowers),
			"totalNetworkWeight", totalNetworkWeight,
			"timeNormalizationFactor", timeNormalizationFactor.String(),
			"pocNormalizationEnabled", params.PocParams.PocNormalizationEnabled,
		)
	} else {
		am.LogWarn("ComputeNewWeights: Validation snapshot not found", types.PoC,
			"epochStartBlockHeight", epochStartBlockHeight,
		)
	}

	calculator := NewPoCWeightCalculator(
		modelVotingPowers,
		totalNetworkWeight,
		allowedCommits,
		allowedDistributions,
		validations,
		params.PocParams,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
		timeNormalizationFactor,
		guardianEnabled,
		guardianSet,
		appHash,
		validationSlots,
	)
	pocMiningParticipants := calculator.Calculate()

	// Merge preserved participants with PoC mining participants (per-model)
	var allActiveParticipants []*types.ActiveParticipant

	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index

		if pocParticipant := findParticipantByAddress(pocMiningParticipants, participantAddress); pocParticipant != nil {
			mergedModels, mergedMLNodes := mergeByModel(
				preservedParticipant.Models, preservedParticipant.MlNodes,
				pocParticipant.Models, pocParticipant.MlNodes,
			)

			mergedParticipant := &types.ActiveParticipant{
				Index:        participantAddress,
				ValidatorKey: preservedParticipant.ValidatorKey,
				Weight:       0, // Will be set by aggregation
				InferenceUrl: preservedParticipant.InferenceUrl,
				Seed:         pocParticipant.Seed,
				Models:       mergedModels,
				MlNodes:      mergedMLNodes,
			}
			mergedParticipant.Weight = RecalculateWeight(mergedParticipant)

			allActiveParticipants = append(allActiveParticipants, mergedParticipant)

			am.LogInfo("ComputeNewWeights: Merged preserved and PoC participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight,
				"pocWeight", pocParticipant.Weight,
				"combinedWeight", mergedParticipant.Weight,
				"models", mergedModels)
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
	allCommits map[types.PoCParticipantModelKey]types.PoCV2StoreCommit,
	allDistributions map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution,
	inferenceServingNodeIds map[string]map[string]struct{},
) (map[types.PoCParticipantModelKey]types.PoCV2StoreCommit, map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution) {
	filteredCommits := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)
	filteredDistributions := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)
	excludedNodeCount := 0

	for key, commit := range allCommits {
		distribution, hasDistribution := allDistributions[key]

		if !hasDistribution || len(distribution.Weights) == 0 {
			am.LogWarn("filterStoreCommitsFromInferenceNodes: No distribution, cannot filter inference nodes, skipping", types.PoC,
				"participantAddress", key.ParticipantAddress,
				"modelId", key.ModelID,
				"commitCount", commit.Count)
			continue
		}

		participantNodes := inferenceServingNodeIds[key.ParticipantAddress]

		var filteredWeights []*types.MLNodeWeight
		filteredCount := uint32(0)
		for _, w := range distribution.Weights {
			if _, isServing := participantNodes[w.NodeId]; isServing {
				excludedNodeCount++
				am.LogWarn("filterStoreCommitsFromInferenceNodes: Excluding weight from inference-serving node", types.PoC,
					"participantAddress", key.ParticipantAddress,
					"modelId", key.ModelID,
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
				"participantAddress", key.ParticipantAddress,
				"modelId", key.ModelID)
			continue
		}

		// Create filtered commit with adjusted count
		filteredCommit := commit
		filteredCommit.Count = filteredCount
		filteredCommits[key] = filteredCommit

		// Create filtered distribution
		filteredDistribution := distribution
		filteredDistribution.Weights = filteredWeights
		filteredDistributions[key] = filteredDistribution
	}

	am.LogInfo("filterStoreCommitsFromInferenceNodes: Summary", types.PoC,
		"excludedNodeCount", excludedNodeCount,
		"originalParticipants", len(allCommits),
		"filteredParticipants", len(filteredCommits))

	return filteredCommits, filteredDistributions
}
