package inference

import (
	"context"
	"encoding/base64"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) ComputeNewWeights(ctx context.Context, blockHeight int64) ([]keeper.ComputeResult, []*types.ActiveParticipant) {
	// FIXME: Figure out something here:
	//  1. Either get current validators by using staking keeper or smth
	//  2. Or alter InitGenesis or set validator logic so there's always active participants
	var currentActiveParticipants *types.ActiveParticipants = nil
	if !proofofcompute.IsZeroEpoch(blockHeight) {
		val, found := am.keeper.GetActiveParticipants(ctx)
		currentActiveParticipants = &val
		if !found {
			am.LogError("No active participants found.")
			return nil, nil
		}
	}
	currentValidatorsAddressSet := getActiveAddressSet(currentActiveParticipants)

	epochStartBlockHeight := proofofcompute.GetStartBlockHeightFromSetNewValidatorsStage(blockHeight)
	am.LogInfo("Epoch start block height", "blockHeight", blockHeight, "epochStartBlockHeight", epochStartBlockHeight)

	originalBatches, err := am.keeper.GetPoCBatchesByStage(ctx, blockHeight)
	if err != nil {
		am.LogError("Error getting batches by PoC stage", "epochStartBlockHeight", epochStartBlockHeight, "error", err)
		return nil, nil
	}

	am.LogInfo("Retrieved original batches", "epochStartBlockHeight", epochStartBlockHeight, "len(batches)", len(originalBatches))

	validations, err := am.keeper.GetPoCValidationByStage(ctx, blockHeight)
	if err != nil {
		am.LogError("Error getting PoC validations by stage", "epochStartBlockHeight", epochStartBlockHeight, "error", err)
	}

	am.LogInfo("Retrieved PoC validations", "epochStartBlockHeight", epochStartBlockHeight, "len(validations)", len(validations))

	var activeParticipants []*types.ActiveParticipant
	var computeResults []keeper.ComputeResult

	for participantAddress, batches := range originalBatches {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("Error getting participant", "address", participantAddress)
			continue
		}

		vals := validations[participantAddress]
		if vals == nil || len(vals) == 0 {
			am.LogError("No validations for participant found", "participant", participantAddress)
			continue
		}

		claimedWeight := getParticipantWeight(batches)
		if claimedWeight < 1 {
			am.LogWarn("Participant has non-positive claimedWeight.", "participant", participantAddress, "claimedWeight", claimedWeight)
			continue
		}

		if participant.ValidatorKey == "" {
			am.LogError("Participant hasn't provided their validator key.", "participant", participantAddress)
			continue
		}

		pubKeyBytes, err := base64.StdEncoding.DecodeString(participant.ValidatorKey)
		if err != nil {
			am.LogError("Error decoding pubkey", "error", err)
			continue
		}

		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		if currentActiveParticipants != nil {
			requiredValidators := (len(currentActiveParticipants.Participants) * 2) / 3
			if len(vals) < requiredValidators {
				am.LogWarn("Participant didn't receive enough validations. Defaulting to accepting",
					"participant", participantAddress,
					"validations", len(vals),
					"required", requiredValidators)
			} else {
				validatorCount := getValidatorIntersectionCount(currentValidatorsAddressSet, vals)

				if validatorCount < requiredValidators {
					am.LogWarn("Participant didn't receive enough validations",
						"participant", participantAddress,
						"validations", validatorCount,
						"required", requiredValidators)
					continue
				}
			}
		}

		r := keeper.ComputeResult{
			Power:           claimedWeight,
			ValidatorPubKey: &pubKey,
			OperatorAddress: participantAddress,
		}
		am.LogInfo("Setting compute validator.", "computeResult", r)
		computeResults = append(computeResults, r)

		activeParticipant := &types.ActiveParticipant{
			Index:        participantAddress,
			ValidatorKey: participant.ValidatorKey,
			Weight:       claimedWeight,
			InferenceUrl: participant.InferenceUrl,
			Models:       participant.Models,
		}
		activeParticipants = append(activeParticipants, activeParticipant)
	}

	return computeResults, activeParticipants
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
