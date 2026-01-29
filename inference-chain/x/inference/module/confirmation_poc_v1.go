package inference

import (
	"context"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// UpdateConfirmationWeightsV1 calculates confirmation weights using on-chain PoCBatch (V1 flow).
func (am AppModule) UpdateConfirmationWeightsV1(
	ctx context.Context,
	event *types.ConfirmationPoCEvent,
	currentValidatorWeights map[string]int64,
	weightScaleFactor mathsdk.LegacyDec,
) []*types.ActiveParticipant {
	// Get PoC batches and validations using trigger_height as key
	allBatches, err := am.keeper.GetPoCBatchesByStage(ctx, event.TriggerHeight)
	if err != nil {
		am.LogError("updateConfirmationWeightsV1: failed to get PoC batches for confirmation", types.PoC, "error", err)
		return nil
	}

	validations, err := am.keeper.GetPoCValidationByStage(ctx, event.TriggerHeight)
	if err != nil {
		am.LogError("updateConfirmationWeightsV1: failed to get PoC validations for confirmation", types.PoC, "error", err)
		return nil
	}

	// Collect participants and seeds for WeightCalculatorV1
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)

	for participantAddress := range allBatches {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogWarn("updateConfirmationWeightsV1: Participant not found", types.PoC,
				"address", participantAddress)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, event.EpochIndex, participantAddress)
		if found {
			seeds[participantAddress] = seed
		}
	}

	// Create WeightCalculatorV1 (reuse regular V1 PoC logic)
	calculator := NewWeightCalculatorV1(
		currentValidatorWeights,
		allBatches,
		validations,
		participants,
		seeds,
		event.TriggerHeight,
		am,
		weightScaleFactor,
	)

	// Calculate confirmation weights
	return calculator.Calculate()
}
