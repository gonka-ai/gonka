package inference

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingGroupData *types.EpochGroupData) []*types.ActiveParticipant {
	epochStartBlockHeight := int64(upcomingGroupData.PocStartBlockHeight)
	am.LogInfo("ComputeNewWeights: computing new weights", "epochStartBlockHeight", epochStartBlockHeight)

	// FIXME: Figure out something here:
	//  1. Either get current validators by using staking keeper or smth
	//  2. Or alter InitGenesis or set validator logic so there's always active participants
	var currentActiveParticipants *types.ActiveParticipants = nil
	if upcomingGroupData.EpochGroupId > 1 {
		val, found := am.keeper.GetActiveParticipants(ctx, upcomingGroupData.EpochGroupId-1)
		currentActiveParticipants = &val
		if !found {
			am.LogError("ComputeNewWeights: No active participants found.")
			return nil
		}
	}
	currentValidatorsAddressSet := getActiveAddressSet(currentActiveParticipants)

	originalBatches, err := am.keeper.GetPoCBatchesByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting batches by PoC stage", "epochStartBlockHeight", epochStartBlockHeight, "error", err)
		return nil
	}

	am.LogInfo("ComputeNewWeights: Retrieved original batches", "epochStartBlockHeight", epochStartBlockHeight, "len(batches)", len(originalBatches))

	validations, err := am.keeper.GetPoCValidationByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeights: Error getting PoC validations by stage", "epochStartBlockHeight", epochStartBlockHeight, "error", err)
	}

	am.LogInfo("ComputeNewWeights: Retrieved PoC validations", "epochStartBlockHeight", epochStartBlockHeight, "len(validations)", len(validations))

	var activeParticipants []*types.ActiveParticipant

	for participantAddress, batches := range originalBatches {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("ComputeNewWeights: Error getting participant", "address", participantAddress)
			continue
		}

		vals := validations[participantAddress]
		if vals == nil || len(vals) == 0 {
			am.LogError("ComputeNewWeights: No validations for participant found", "participant", participantAddress)
			continue
		}

		claimedWeight := getParticipantWeight(batches)
		if claimedWeight < 1 {
			am.LogWarn("ComputeNewWeights: Participant has non-positive claimedWeight.", "participant", participantAddress, "claimedWeight", claimedWeight)
			continue
		}

		if participant.ValidatorKey == "" {
			am.LogError("ComputeNewWeights: Participant hasn't provided their validator key.", "participant", participantAddress)
			continue
		}

		if currentActiveParticipants != nil {
			requiredValidators := (len(currentActiveParticipants.Participants) * 2) / 3
			if len(vals) < requiredValidators {
				am.LogWarn("ComputeNewWeights: Participant didn't receive enough validations. Defaulting to accepting",
					"participant", participantAddress,
					"validations", len(vals),
					"required", requiredValidators)
			} else {
				validatorCount := getValidatorIntersectionCount(currentValidatorsAddressSet, vals)

				if validatorCount < requiredValidators {
					am.LogWarn("ComputeNewWeights: Participant didn't receive enough validations",
						"participant", participantAddress,
						"validations", validatorCount,
						"required", requiredValidators)
					continue
				}
			}
		}

		seed, found := am.keeper.GetRandomSeed(ctx, epochStartBlockHeight, participantAddress)
		if !found {
			am.LogError("ComputeNewWeights: Participant didn't submit the seed for the upcoming epoch", "blockHeight", epochStartBlockHeight, "participant", participantAddress)
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
		am.LogInfo("ComputeNewWeights: Setting compute validator.", "activeParticipant", activeParticipant)
	}

	return activeParticipants
}

func getParticipantWeight(batches []types.PoCBatch) int64 {
	var weight int64
	for _, b := range batches {
		weight += int64(len(b.Nonces))
	}
	return weight
}

func getActiveAddressSet(activeParticipants *types.ActiveParticipants) *map[string]struct{} {
	if activeParticipants == nil {
		return nil
	}

	set := make(map[string]struct{})
	for _, ap := range activeParticipants.Participants {
		set[ap.Index] = struct{}{}
	}
	return &set
}

func getValidatorIntersectionCount(currentValidatorsSet *map[string]struct{}, validations []types.PoCValidation) int {
	count := 0
	for _, v := range validations {
		if _, ok := (*currentValidatorsSet)[v.ValidatorParticipantAddress]; ok && !v.FraudDetected {
			count++
		}
	}
	return count
}
