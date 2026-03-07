package keeper

import (
	"context"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetNodeDiagnostics returns diagnostic information about the node's state for operators.
// This is a lightweight read-only function that helps operators diagnose performance issues.
func (k *Keeper) GetNodeDiagnostics(ctx context.Context) map[string]interface{} {
	diagnostics := make(map[string]interface{})

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	diagnostics["block_height"] = sdkCtx.BlockHeight()
	diagnostics["block_time"] = sdkCtx.BlockTime().String()

	// Epoch info
	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if found && effectiveEpoch != nil {
		diagnostics["effective_epoch_index"] = effectiveEpoch.Index
		diagnostics["poc_start_block_height"] = effectiveEpoch.PocStartBlockHeight
	}

	// Participant count (uses optimized counting)
	countStart := time.Now()
	participantCount := k.CountAllParticipants(ctx)
	countDuration := time.Since(countStart)
	diagnostics["participant_count"] = participantCount
	diagnostics["participant_count_ms"] = countDuration.Milliseconds()

	// Pruning state
	pruningState, err := k.PruningState.Get(ctx)
	if err == nil {
		diagnostics["pruning_inference_epoch"] = pruningState.InferencePrunedEpoch
		diagnostics["pruning_poc_batches_epoch"] = pruningState.PocBatchesPrunedEpoch
		diagnostics["pruning_poc_validations_epoch"] = pruningState.PocValidationsPrunedEpoch
	}

	// Count active model prices
	priceStart := time.Now()
	prices, err := k.GetAllModelCurrentPrices(ctx)
	pricesDuration := time.Since(priceStart)
	if err == nil {
		diagnostics["active_model_count"] = len(prices)
	}
	diagnostics["prices_query_ms"] = pricesDuration.Milliseconds()

	return diagnostics
}
