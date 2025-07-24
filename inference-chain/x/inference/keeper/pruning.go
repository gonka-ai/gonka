package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// PruneInferences removes old inference records based on threshold and status
func (k Keeper) PruneInferences(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	// Get all inferences
	inferences := k.GetAllInference(ctx)

	// Count of pruned inferences for logging
	prunedCount := 0

	k.LogDebug("Starting inference pruning iteration", types.Pruning,
		"total_inferences", len(inferences),
		"current_epoch", currentEpochIndex,
		"threshold", pruningThreshold)

	// Iterate through all inferences
	for _, inference := range inferences {
		// Check if the inference is eligible for pruning
		if isInferenceEligibleForPruning(inference, currentEpochIndex, pruningThreshold) {
			k.LogDebug("Pruning inference", types.Pruning,
				"inference_index", inference.Index,
				"inference_epoch", inference.EpochId,
				"current_epoch", currentEpochIndex)
			// Remove the inference
			k.RemoveInference(ctx, inference.Index)
			prunedCount++
		}
	}
	k.LogInfo("Pruned inferences", types.Pruning, "count", prunedCount, "current_epoch", currentEpochIndex, "threshold", pruningThreshold)

	return nil
}

// isInferenceEligibleForPruning checks if inference can be pruned based on age
func isInferenceEligibleForPruning(inference types.Inference, currentEpochIndex uint64, pruningThreshold uint64) bool {
	cutoff := currentEpochIndex - pruningThreshold
	return inference.EpochId <= cutoff
}

// PrunePoCData removes old PoC data within limited range for performance
func (k Keeper) PrunePoCData(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	_, found := k.GetEpoch(ctx, currentEpochIndex)
	if !found {
		k.LogError("Failed to get current epoch", types.Pruning, "epoch_index", currentEpochIndex)
		return types.ErrEffectiveEpochNotFound
	}

	// Calculate the maximum number of epochs to check (5 times the pruning threshold)
	// This limits how far back we look to avoid performance issues on deep chains
	maxEpochsToCheck := pruningThreshold * 5
	k.LogInfo("Starting PoC data pruning", types.Pruning,
		"max_epochs_to_check", maxEpochsToCheck,
		"current_epoch", currentEpochIndex,
		"threshold", pruningThreshold)

	// Calculate the starting epoch index (the oldest epoch we'll check)
	// We want to start from the oldest eligible epoch and work backwards
	var startEpochIndex uint64

	// Handle edge cases for different chain depths:
	if currentEpochIndex <= pruningThreshold {
		// Case 1: Chain is too young - no epochs are old enough to prune
		// If current epoch is less than or equal to the threshold, there's nothing to prune
		k.LogDebug("No epochs old enough to prune", types.Pruning, "current_epoch", currentEpochIndex, "threshold", pruningThreshold)
		return nil
	} else if currentEpochIndex <= maxEpochsToCheck+pruningThreshold {
		// Case 2: Young chain - we don't have enough epochs to go back 5*threshold
		// Start from the beginning (epoch 0)
		startEpochIndex = 0
	} else {
		// Case 3: Mature chain - we have enough epochs to apply the full optimization
		// Start from (currentEpochIndex - maxEpochsToCheck)
		// This ensures we only check the oldest eligible epochs within our limit
		startEpochIndex = currentEpochIndex - maxEpochsToCheck
	}

	// Collect epochs that are eligible for pruning, limited by maxEpochsToCheck
	// We'll only collect epochs that are older than the pruning threshold
	var epochsToCheck []types.Epoch
	epochsChecked := uint64(0)
	k.LogDebug("Starting epoch collection", types.Pruning,
		"start_epoch_index", startEpochIndex,
		"current_epoch", currentEpochIndex,
		"max_epochs_to_check", maxEpochsToCheck)

	// Iterate from the starting epoch index up to the current epoch
	// but limit the number of epochs we check to maxEpochsToCheck for performance
	for i := startEpochIndex; i < currentEpochIndex && epochsChecked < maxEpochsToCheck; i++ {
		// Skip epochs that are not old enough to be pruned
		// (currentEpochIndex - i) gives us the age of the epoch in epochs
		epochAge := currentEpochIndex - i
		if epochAge < pruningThreshold {
			k.LogDebug("Skipping epoch - not old enough", types.Pruning,
				"epoch_index", i,
				"epoch_age", epochAge,
				"threshold", pruningThreshold)
			continue
		}
		k.LogDebug("Checking epoch for pruning", types.Pruning,
			"epoch_index", i,
			"epoch_age", epochAge,
			"threshold", pruningThreshold)

		// Get the epoch data - skip if not found
		epoch, found := k.GetEpoch(ctx, i)
		if !found {
			k.LogInfo("Epoch not found - skipping", types.Pruning, "epoch_index", i)
			continue
		}
		k.LogDebug("Found epoch to process", types.Pruning,
			"epoch_index", i,
			"poc_start_block_height", epoch.PocStartBlockHeight)

		// Add this epoch to our list of epochs to prune
		epochsToCheck = append(epochsToCheck, *epoch)
		epochsChecked++
	}

	// Count of pruned PoC batches and validations for logging
	prunedBatchCount := 0
	prunedValidationCount := 0

	// Prune PoCBatch and PoCValidation records for each epoch
	k.LogDebug("Starting pruning process", types.Pruning,
		"epochs_to_process", len(epochsToCheck),
		"current_epoch", currentEpochIndex)
	for _, epoch := range epochsToCheck {
		k.LogInfo("Pruning epoch", types.Pruning,
			"poc_start_block_height", epoch.PocStartBlockHeight)
		// Prune PoCBatch records
		prunedBatchCount += k.prunePoCBatchesForEpoch(ctx, epoch.PocStartBlockHeight)

		// Prune PoCValidation records
		prunedValidationCount += k.prunePoCValidationsForEpoch(ctx, epoch.PocStartBlockHeight)
	}

	k.LogInfo("Pruned PoC data", types.Pruning,
		"batch_count", prunedBatchCount,
		"validation_count", prunedValidationCount,
		"current_epoch", currentEpochIndex,
		"threshold", pruningThreshold)

	return nil
}

// prunePoCBatchesForEpoch prunes all PoCBatch records for the specified epoch.
// It returns the number of records pruned.
func (k Keeper) prunePoCBatchesForEpoch(ctx context.Context, pocStageStartBlockHeight int64) int {
	// Get all PoCBatch records for the epoch
	batches, err := k.GetPoCBatchesByStage(ctx, pocStageStartBlockHeight)
	if err != nil {
		k.LogError("Failed to get PoCBatches by stage", types.Pruning, "error", err, "poc_stage_start_block_height", pocStageStartBlockHeight)
		return 0
	}

	prunedCount := 0

	// Iterate through all batches and remove them
	for participantAddr, batchSlice := range batches {
		for _, batch := range batchSlice {
			// Remove the batch
			storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
			store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocBatchKeyPrefix))
			key := types.PoCBatchKey(batch.PocStageStartBlockHeight, batch.ParticipantAddress, batch.BatchId)
			store.Delete(key)
			prunedCount++
		}

		k.LogInfo("Pruned PoCBatches for participant", types.Pruning,
			"participant", participantAddr,
			"count", len(batchSlice),
			"poc_stage_start_block_height", pocStageStartBlockHeight)
	}

	return prunedCount
}

// prunePoCValidationsForEpoch prunes all PoCValidation records for the specified epoch.
// It returns the number of records pruned.
func (k Keeper) prunePoCValidationsForEpoch(ctx context.Context, pocStageStartBlockHeight int64) int {
	// Get all PoCValidation records for the epoch
	validations, err := k.GetPoCValidationByStage(ctx, pocStageStartBlockHeight)
	if err != nil {
		k.LogError("Failed to get PoCValidations by stage", types.Pruning, "error", err, "poc_stage_start_block_height", pocStageStartBlockHeight)
		return 0
	}

	prunedCount := 0

	// Iterate through all validations and remove them
	for participantAddr, validationSlice := range validations {
		for _, validation := range validationSlice {
			// Remove the validation
			storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
			store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocValidationPrefix))
			key := types.PoCValidationKey(validation.PocStageStartBlockHeight, validation.ParticipantAddress, validation.ValidatorParticipantAddress)
			store.Delete(key)
			prunedCount++
		}

		k.LogInfo("Pruned PoCValidations for participant", types.Pruning,
			"participant", participantAddr,
			"count", len(validationSlice),
			"poc_stage_start_block_height", pocStageStartBlockHeight)
	}

	return prunedCount
}
