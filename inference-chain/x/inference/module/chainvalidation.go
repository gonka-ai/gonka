package inference

import (
	"context"
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
func (am AppModule) getCurrentValidatorWeights(ctx context.Context, epochGroupId uint64) (map[string]int64, error) {
	if epochGroupId <= 1 {
		return nil, nil
	}
	if epochGroupId <= 1 {
		currentValidators, err := am.keeper.Staking.GetAllValidators(ctx)
		if err != nil {
			am.LogError("getCurrentValidatorWeights: Error getting current validators in first epoch group", types.PoC, "error", err)
			return nil, err
		}
		weights := make(map[string]int64)
		for _, validator := range currentValidators {
			weights[validator.OperatorAddress] = validator.Tokens.Int64()
		}
		return weights, nil
	}

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

func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingGroupData *types.EpochGroupData) []*types.ActiveParticipant {
	epochStartBlockHeight := int64(upcomingGroupData.PocStartBlockHeight)
	am.LogInfo("ComputeNewWeights: computing new weights", types.PoC, "epochStartBlockHeight", epochStartBlockHeight)

	// Get current active participants weights
	currentValidatorWeights, err := am.getCurrentValidatorWeights(ctx, upcomingGroupData.EpochGroupId)
	am.LogInfo("ComputeNewWeights: Retrieved current validator weights", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "weights", currentValidatorWeights)

	if err != nil {
		am.LogError("ComputeNewWeights: Error getting current validator weights", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "error", err)
		return nil
	}

	originalBatches, err := am.keeper.GetPoCBatchesByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting batches by PoC stage", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "error", err)
		return nil
	}

	am.LogInfo("ComputeNewWeights: Retrieved original batches", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "len(batches)", len(originalBatches))

	validations, err := am.keeper.GetPoCValidationByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting PoC validations by stage", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "error", err)
	}

	validators := make([]string, len(validations))
	var i = 0
	for address, _ := range validations {
		validators[i] = address
		i += 1
	}
	am.LogInfo("ComputeNewWeights: Retrieved PoC validations", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "len(validations)", len(validations), "validators", validators)

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
			am.LogError("ComputeNewWeights: Error getting participant", types.PoC, "address", participantAddress)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, epochStartBlockHeight, participantAddress)
		if !found {
			am.LogError("ComputeNewWeights: Participant didn't submit the seed for the upcoming epoch", types.PoC, "blockHeight", epochStartBlockHeight, "participant", participantAddress)
			continue
		}
		seeds[participantAddress] = seed
	}

	// Create a WeightCalculator and use it to calculate the new weights
	calculator := NewWeightCalculator(
		currentValidatorWeights,
		originalBatches,
		validations,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
	)
	return calculator.Calculate()
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

	vals := wc.Validations[participantAddress]
	if vals == nil || len(vals) == 0 {
		wc.Logger.LogError("Calculate: No validations for participant found", types.PoC, "participant", participantAddress)
		return nil
	}

	claimedWeight := calculateParticipantWeight(wc.OriginalBatches[participantAddress])
	if claimedWeight < 1 {
		wc.Logger.LogWarn("Calculate: Participant has non-positive claimedWeight.", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)
		return nil
	}

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

	activeParticipant := &types.ActiveParticipant{
		Index:        participantAddress,
		ValidatorKey: participant.ValidatorKey,
		Weight:       claimedWeight,
		InferenceUrl: participant.InferenceUrl,
		Seed:         &seed,
		Models:       make([]string, 0),
	}
	return activeParticipant
}

func (wc *WeightCalculator) pocValidated(vals []types.PoCValidation, participantAddress string) bool {
	totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
	halfWeight := int64(totalWeight / 2)
	shouldContinue := false

	if wc.CurrentValidatorWeights != nil && len(wc.CurrentValidatorWeights) > 0 {
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
		shouldContinue = true
		wc.Logger.LogError("Calculate: No current validator weights found. Accepting the participant.", types.PoC, "participant", participantAddress)
	}

	return shouldContinue
}

func calculateParticipantWeight(batches []types.PoCBatch) int64 {
	uniqueNonces := make(map[int64]struct{})

	for _, b := range batches {
		for _, nonce := range b.Nonces {
			uniqueNonces[nonce] = struct{}{}
		}
	}

	return int64(len(uniqueNonces))
}

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
