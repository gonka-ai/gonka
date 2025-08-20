package keeper

import (
	"context"
	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// PruneInferences removes old inference records based on threshold and status
func (k Keeper) PruneInferences(ctx context.Context, upcomingEpochIndex uint64, pruningThreshold uint64) error {
	inferences := k.GetAllInference(ctx)
	prunedCount := 0

	k.LogInfo("Starting inference pruning iteration", types.Pruning,
		"total_inferences", len(inferences),
		"upcoming_epoch_index", upcomingEpochIndex,
		"threshold", pruningThreshold)

	for _, inference := range inferences {
		if isInferenceEligibleForPruning(inference, upcomingEpochIndex, pruningThreshold) {
			k.LogInfo("Pruning inference", types.Pruning,
				"inference_index", inference.Index,
				"inference_epoch", inference.EpochId,
				"upcoming_epoch_index", upcomingEpochIndex)
			k.RemoveInference(ctx, inference.Index)
			prunedCount++
		}
	}
	k.LogInfo("Pruned inferences", types.Pruning, "count", prunedCount, "upcoming_epoch_index", upcomingEpochIndex, "threshold", pruningThreshold)

	return nil
}

// isInferenceEligibleForPruning checks if inference can be pruned based on age
func isInferenceEligibleForPruning(inference types.Inference, upcomingEpochIndex uint64, pruningThreshold uint64) bool {
	if inference.Status == types.InferenceStatus_STARTED || inference.Status == types.InferenceStatus_VOTING {
		// pending activity
		return false
	}
	if pruningThreshold > upcomingEpochIndex {
		return false
	}

	cutoff := upcomingEpochIndex - pruningThreshold
	return inference.EpochId <= cutoff
}

var lookbackMultiplier = uint64(5)

// PrunePoCData removes old PoC data within limited range for performance
func (k Keeper) PrunePoCData(ctx context.Context, upcomingEpochIndex uint64, pruningThreshold uint64) error {
	_, found := k.GetEpoch(ctx, upcomingEpochIndex)
	if !found {
		k.LogError("Failed to get upcoming epoch", types.Pruning, "upcoming_epoch_index", upcomingEpochIndex)
		return types.ErrEffectiveEpochNotFound
	}

	// Limit how far back we look to avoid performance issues on deep chains
	maxEpochsToCheck := pruningThreshold * lookbackMultiplier
	k.LogInfo("Starting PoC data pruning", types.Pruning,
		"max_epochs_to_check", maxEpochsToCheck,
		"upcoming_epoch_index", upcomingEpochIndex,
		"threshold", pruningThreshold)

	var startEpochIndex uint64

	if upcomingEpochIndex <= pruningThreshold {
		// Chain too young - nothing to prune
		k.LogInfo("No epochs old enough to prune", types.Pruning, "upcoming_epoch_index", upcomingEpochIndex, "threshold", pruningThreshold)
		return nil
	} else if upcomingEpochIndex <= maxEpochsToCheck+pruningThreshold {
		// Young chain - start from beginning
		startEpochIndex = 0
	} else {
		// Mature chain - apply optimization limit
		startEpochIndex = upcomingEpochIndex - maxEpochsToCheck
	}

	// Collect epochs that are eligible for pruning, limited by maxEpochsToCheck
	// We'll only collect epochs that are older than the pruning threshold
	var epochsToCheck []types.Epoch
	epochsChecked := uint64(0)
	k.LogInfo("Starting epoch collection", types.Pruning,
		"start_epoch_index", startEpochIndex,
		"upcoming_epoch_index", upcomingEpochIndex,
		"max_epochs_to_check", maxEpochsToCheck)

	for i := startEpochIndex; i < upcomingEpochIndex && epochsChecked < maxEpochsToCheck; i++ {
		epochAge := upcomingEpochIndex - i
		if epochAge < pruningThreshold {
			k.LogInfo("Skipping epoch - not old enough", types.Pruning,
				"epoch_index", i,
				"epoch_age", epochAge,
				"threshold", pruningThreshold)
			continue
		}
		k.LogInfo("Checking epoch for pruning", types.Pruning,
			"epoch_index", i,
			"epoch_age", epochAge,
			"threshold", pruningThreshold)

		epoch, found := k.GetEpoch(ctx, i)
		if !found {
			k.LogInfo("Epoch not found - skipping", types.Pruning, "epoch_index", i)
			continue
		}
		k.LogInfo("Found epoch to process", types.Pruning,
			"epoch_index", i,
			"poc_start_block_height", epoch.PocStartBlockHeight)

		epochsToCheck = append(epochsToCheck, *epoch)
		epochsChecked++
	}

	prunedBatchCount := 0
	prunedValidationCount := 0

	k.LogInfo("Starting pruning process", types.Pruning,
		"epochs_to_process", len(epochsToCheck),
		"upcoming_epoch_index", upcomingEpochIndex)
	for _, epoch := range epochsToCheck {
		k.LogInfo("Pruning epoch", types.Pruning,
			"epoch_index", epoch.Index,
			"poc_start_block_height", epoch.PocStartBlockHeight)

		prunedBatchCount += k.prunePoCBatchesForEpoch(ctx, epoch.PocStartBlockHeight)
		prunedValidationCount += k.prunePoCValidationsForEpoch(ctx, epoch.PocStartBlockHeight)
	}

	k.LogInfo("Pruned PoC data", types.Pruning,
		"batch_count", prunedBatchCount,
		"validation_count", prunedValidationCount,
		"upcoming_epoch_index", upcomingEpochIndex,
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
			pAddr, err := sdk.AccAddressFromBech32(batch.ParticipantAddress)
			if err != nil {
				continue
			}
			pk := collections.Join3(batch.PocStageStartBlockHeight, pAddr, batch.BatchId)
			_ = k.PoCBatches.Remove(ctx, pk)
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
			pAddr, err := sdk.AccAddressFromBech32(validation.ParticipantAddress)
			if err != nil {
				continue
			}
			vAddr, err := sdk.AccAddressFromBech32(validation.ValidatorParticipantAddress)
			if err != nil {
				continue
			}
			pk := collections.Join3(validation.PocStageStartBlockHeight, pAddr, vAddr)
			_ = k.PoCValidations.Remove(ctx, pk)
			prunedCount++
		}

		k.LogInfo("Pruned PoCValidations for participant", types.Pruning,
			"participant", participantAddr,
			"count", len(validationSlice),
			"poc_stage_start_block_height", pocStageStartBlockHeight)
	}

	return prunedCount
}
