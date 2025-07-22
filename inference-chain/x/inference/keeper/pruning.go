package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// PruneInferences prunes inferences that are older than the specified threshold
// and have a terminal status (FINISHED, VALIDATED, INVALIDATED, or EXPIRED).
// It returns an error if any operation fails.
func (k Keeper) PruneInferences(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	if pruningThreshold == 0 {
		k.LogInfo("Inference pruning disabled (threshold is 0)", types.Inferences)
		return nil
	}

	k.LogInfo("Starting inference pruning", types.Inferences,
		"currentEpochIndex", currentEpochIndex,
		"pruningThreshold", pruningThreshold)

	// Get all inferences
	inferences := k.GetAllInference(ctx)
	prunedCount := 0

	for _, inference := range inferences {
		// Skip inferences that don't have an epoch ID (should be rare)
		if inference.EpochId == 0 {
			continue
		}

		// Check if the inference is eligible for pruning
		if isInferenceEligibleForPruning(inference, currentEpochIndex, pruningThreshold) {
			// Remove the inference
			k.RemoveInference(ctx, inference.Index)
			prunedCount++

			k.LogDebug("Pruned inference", types.Inferences,
				"inferenceId", inference.InferenceId,
				"status", inference.Status.String(),
				"epochId", inference.EpochId)
		}
	}

	k.LogInfo("Completed inference pruning", types.Inferences,
		"prunedCount", prunedCount,
		"totalCount", len(inferences))

	return nil
}

// isInferenceEligibleForPruning determines if an inference is eligible for pruning
// based on its status and age.
func isInferenceEligibleForPruning(inference types.Inference, currentEpochIndex uint64, pruningThreshold uint64) bool {
	// Check if the inference is old enough to be pruned
	return isOldEnoughToPrune(inference.EpochId, currentEpochIndex, pruningThreshold)
}

// isOldEnoughToPrune checks if the inference is old enough to be pruned
// based on the epoch difference and pruning threshold.
func isOldEnoughToPrune(inferenceEpochId uint64, currentEpochIndex uint64, pruningThreshold uint64) bool {
	// If the current epoch is less than the inference epoch (should never happen),
	// don't prune to avoid potential issues
	if currentEpochIndex < inferenceEpochId {
		return false
	}

	// Calculate the epoch difference
	epochDiff := currentEpochIndex - inferenceEpochId

	// Check if the epoch difference is greater than or equal to the pruning threshold
	return epochDiff >= pruningThreshold
}

// RemovePoCBatch removes a PoCBatch from the store
func (k Keeper) RemovePoCBatch(
	ctx context.Context,
	pocStageStartBlockHeight int64,
	participantAddress string,
	batchId string,
) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocBatchKeyPrefix))
	store.Delete(types.PoCBatchKey(
		pocStageStartBlockHeight,
		participantAddress,
		batchId,
	))
}

// RemovePoCValidation removes a PoCValidation from the store
func (k Keeper) RemovePoCValidation(
	ctx context.Context,
	pocStageStartBlockHeight int64,
	participantAddress string,
	validatorParticipantAddress string,
) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocValidationPrefix))
	store.Delete(types.PoCValidationKey(
		pocStageStartBlockHeight,
		participantAddress,
		validatorParticipantAddress,
	))
}

// PrunePoCData prunes PoC data (PoCBatch and PoCValidation) that is older than the specified threshold.
// It returns an error if any operation fails.
func (k Keeper) PrunePoCData(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	if pruningThreshold == 0 {
		k.LogInfo("PoC data pruning disabled (threshold is 0)", types.PoC)
		return nil
	}

	k.LogInfo("Starting PoC data pruning", types.PoC,
		"currentEpochIndex", currentEpochIndex,
		"pruningThreshold", pruningThreshold)

	// Get all previous epochs that are eligible for pruning
	prunedBatchCount := 0
	prunedValidationCount := 0

	// Prune PoC batches
	for epochIndex := uint64(1); epochIndex < currentEpochIndex; epochIndex++ {
		// Skip epochs that are not old enough to be pruned
		if !isOldEnoughToPrune(epochIndex, currentEpochIndex, pruningThreshold) {
			continue
		}

		// Get the epoch to determine its PoC stage start block height
		epoch, found := k.GetEpoch(ctx, epochIndex)
		if !found {
			continue
		}

		// Get all PoC batches for this epoch
		pocBatches, err := k.GetPoCBatchesByStage(ctx, epoch.PocStartBlockHeight)
		if err != nil {
			k.LogError("Error getting PoC batches", types.PoC, "error", err.Error())
			continue
		}

		// Prune all PoC batches for this epoch
		for participantAddress, batches := range pocBatches {
			for _, batch := range batches {
				k.RemovePoCBatch(ctx, batch.PocStageStartBlockHeight, participantAddress, batch.BatchId)
				prunedBatchCount++
			}
		}

		// Get all PoC validations for this epoch
		pocValidations, err := k.GetPoCValidationByStage(ctx, epoch.PocStartBlockHeight)
		if err != nil {
			k.LogError("Error getting PoC validations", types.PoC, "error", err.Error())
			continue
		}

		// Prune all PoC validations for this epoch
		for participantAddress, validations := range pocValidations {
			for _, validation := range validations {
				k.RemovePoCValidation(ctx, validation.PocStageStartBlockHeight, participantAddress, validation.ValidatorParticipantAddress)
				prunedValidationCount++
			}
		}

		k.LogInfo("Pruned PoC data for epoch", types.PoC,
			"epochIndex", epochIndex,
			"pocStageStartBlockHeight", epoch.PocStartBlockHeight)
	}

	k.LogInfo("Completed PoC data pruning", types.PoC,
		"prunedBatchCount", prunedBatchCount,
		"prunedValidationCount", prunedValidationCount)

	return nil
}
