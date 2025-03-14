package inference

import (
	"context"
	"log/slog"
	"sort"

	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingGroupData *types.EpochGroupData) []*types.ActiveParticipant {
	epochStartBlockHeight := int64(upcomingGroupData.PocStartBlockHeight)
	am.LogInfo("ComputeNewWeights: computing new weights", types.PoC, "epochStartBlockHeight", epochStartBlockHeight)

	// FIXME: Figure out something here:
	//  1. Either get current validators by using staking keeper or smth
	//  2. Or alter InitGenesis or set validator logic so there's always active participants
	var currentActiveParticipants *types.ActiveParticipants = nil
	if upcomingGroupData.EpochGroupId > 1 {
		val, found := am.keeper.GetActiveParticipants(ctx, upcomingGroupData.EpochGroupId-1)
		currentActiveParticipants = &val
		if !found {
			am.LogError("ComputeNewWeights: No active participants found.", types.PoC)
			return nil
		}
	}
	currentValidatorWeights := getActiveParticipantsWeights(currentActiveParticipants)
	totalWeight := getTotalWeight(currentValidatorWeights)
	requiredValidWeight := (totalWeight * 2) / 3

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

	am.LogInfo("ComputeNewWeights: Retrieved PoC validations", types.PoC, "epochStartBlockHeight", epochStartBlockHeight, "len(validations)", len(validations))

	var activeParticipants []*types.ActiveParticipant

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

		vals := validations[participantAddress]
		if vals == nil || len(vals) == 0 {
			am.LogError("ComputeNewWeights: No validations for participant found", types.PoC, "participant", participantAddress)
			continue
		}

		claimedWeight := getParticipantWeight(originalBatches[participantAddress])
		if claimedWeight < 1 {
			am.LogWarn("ComputeNewWeights: Participant has non-positive claimedWeight.", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)
			continue
		}

		if participant.ValidatorKey == "" {
			am.LogError("ComputeNewWeights: Participant hasn't provided their validator key.", types.PoC, "participant", participantAddress)
			continue
		}

		if currentActiveParticipants != nil {
			valOutcome := getValidationOutcome(currentValidatorWeights, vals)
			votedWeight := uint64(valOutcome.InvalidWeight + valOutcome.ValidWeight)
			if votedWeight < requiredValidWeight {
				am.LogWarn("ComputeNewWeights: Participant didn't receive enough validations. Defaulting to accepting",
					types.PoC, "participant", participantAddress,
					"votedWeight", votedWeight,
					"requiredValidWeight", requiredValidWeight)
			} else {
				if uint64(valOutcome.ValidWeight) < requiredValidWeight {
					am.LogWarn("ComputeNewWeights: Participant didn't receive enough validations",
						types.PoC, "participant", participantAddress,
						"validWeight", valOutcome.ValidWeight,
						"requiredValidWeight", requiredValidWeight)
					continue
				}
			}
		}

		seed, found := am.keeper.GetRandomSeed(ctx, epochStartBlockHeight, participantAddress)
		if !found {
			am.LogError("ComputeNewWeights: Participant didn't submit the seed for the upcoming epoch", types.PoC, "blockHeight", epochStartBlockHeight, "participant", participantAddress)
			continue
		}

		activeParticipant := &types.ActiveParticipant{
			Index:        participantAddress,
			ValidatorKey: participant.ValidatorKey,
			Weight:       claimedWeight,
			InferenceUrl: participant.InferenceUrl,
			Models:       participant.Models,
			Seed:         &seed,
		}
		activeParticipants = append(activeParticipants, activeParticipant)
		am.LogInfo("ComputeNewWeights: Setting compute validator.", types.PoC, "activeParticipant", activeParticipant)
	}

	return activeParticipants
}

func getParticipantWeight(batches []types.PoCBatch) int64 {
	uniqueNonces := make(map[int64]struct{})

	for _, b := range batches {
		for _, nonce := range b.Nonces {
			uniqueNonces[nonce] = struct{}{}
		}
	}

	return int64(len(uniqueNonces))
}

func getActiveParticipantsWeights(activeParticipants *types.ActiveParticipants) map[string]int64 {
	if activeParticipants == nil {
		return nil
	}

	weights := make(map[string]int64)
	for _, ap := range activeParticipants.Participants {
		weights[ap.Index] = ap.Weight
	}
	return weights
}

func getTotalWeight(validatorWeights map[string]int64) uint64 {
	if validatorWeights == nil {
		return 0
	}

	totalWeight := uint64(0)
	for participant, weight := range validatorWeights {
		if weight < 0 {
			slog.Error("getTotalWeight: Negative weight found", "participant", participant, "weight", weight)
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

func getValidationOutcome(currentValidatorsSet map[string]int64, validations []types.PoCValidation) validationOutcome {
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
