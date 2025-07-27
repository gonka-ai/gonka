package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// PruneInferences removes old inference records based on threshold and status
func (k Keeper) PruneInferences(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	inferences := k.GetAllInference(ctx)
	prunedCount := 0

	k.LogDebug("Starting inference pruning iteration", types.Pruning,
		"total_inferences", len(inferences),
		"current_epoch", currentEpochIndex,
		"threshold", pruningThreshold)

	for _, inference := range inferences {
		if isInferenceEligibleForPruning(inference, currentEpochIndex, pruningThreshold) {
			k.LogDebug("Pruning inference", types.Pruning,
				"inference_index", inference.Index,
				"inference_epoch", inference.EpochId,
				"current_epoch", currentEpochIndex)
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

var lookbackMultiplier = uint64(5)

// PrunePoCData removes old PoC data within limited range for performance
func (k Keeper) PrunePoCData(ctx context.Context, currentEpochIndex uint64, pruningThreshold uint64) error {
	_, found := k.GetEpoch(ctx, currentEpochIndex)
	if !found {
		k.LogError("Failed to get current epoch", types.Pruning, "epoch_index", currentEpochIndex)
		return types.ErrEffectiveEpochNotFound
	}

	// Limit how far back we look to avoid performance issues on deep chains
	maxEpochsToCheck := pruningThreshold * lookbackMultiplier
	k.LogInfo("Starting PoC data pruning", types.Pruning,
		"max_epochs_to_check", maxEpochsToCheck,
		"current_epoch", currentEpochIndex,
		"threshold", pruningThreshold)

	var startEpochIndex uint64

	if currentEpochIndex <= pruningThreshold {
		// Chain too young - nothing to prune
		k.LogDebug("No epochs old enough to prune", types.Pruning, "current_epoch", currentEpochIndex, "threshold", pruningThreshold)
		return nil
	} else if currentEpochIndex <= maxEpochsToCheck+pruningThreshold {
		// Young chain - start from beginning
		startEpochIndex = 0
	} else {
		// Mature chain - apply optimization limit
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

	for i := startEpochIndex; i < currentEpochIndex && epochsChecked < maxEpochsToCheck; i++ {
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

		epoch, found := k.GetEpoch(ctx, i)
		if !found {
			k.LogInfo("Epoch not found - skipping", types.Pruning, "epoch_index", i)
			continue
		}
		k.LogDebug("Found epoch to process", types.Pruning,
			"epoch_index", i,
			"poc_start_block_height", epoch.PocStartBlockHeight)

		epochsToCheck = append(epochsToCheck, *epoch)
		epochsChecked++
	}

	prunedBatchCount := 0
	prunedValidationCount := 0

	k.LogDebug("Starting pruning process", types.Pruning,
		"epochs_to_process", len(epochsToCheck),
		"current_epoch", currentEpochIndex)
	for _, epoch := range epochsToCheck {
		k.LogInfo("Pruning epoch", types.Pruning,
			"poc_start_block_height", epoch.PocStartBlockHeight)
		prunedBatchCount += k.prunePoCBatchesForEpoch(ctx, epoch.PocStartBlockHeight)
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
	batches, err := k.GetPoCBatchesByStage(ctx, pocStageStartBlockHeight)
	if err != nil {
		k.LogError("Failed to get PoCBatches by stage", types.Pruning, "error", err, "poc_stage_start_block_height", pocStageStartBlockHeight)
		return 0
	}

	prunedCount := 0

	for participantAddr, batchSlice := range batches {
		for _, batch := range batchSlice {
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
	validations, err := k.GetPoCValidationByStage(ctx, pocStageStartBlockHeight)
	if err != nil {
		k.LogError("Failed to get PoCValidations by stage", types.Pruning, "error", err, "poc_stage_start_block_height", pocStageStartBlockHeight)
		return 0
	}

	prunedCount := 0

	for participantAddr, validationSlice := range validations {
		for _, validation := range validationSlice {
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
