package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// BatchStartInference executes multiple MsgStartInference messages within a single Cosmos
// transaction while isolating each item in its own CacheContext (sub-transaction).
//
// Motivation: when StartInference messages are batched by the BatchConsumer into one Cosmos
// TX, the atomic nature of the TX means a single failure rolls back ALL messages. The
// previous workaround was the failedStart soft-error pattern — returning (response, nil) with
// an ErrorMessage field — which prevents rollback but leaks internal errors as success
// responses, complicating observability.
//
// BatchStartInference solves this correctly: each inner message runs in a CacheContext. On
// success the sub-context writes are committed to the parent context; on failure only that
// item's changes are discarded and an ErrorMessage is recorded in the response. Other items
// in the same batch are unaffected.
//
// The outer handler always returns (response, nil) so the wrapping Cosmos TX always succeeds.
// Individual item failures are surfaced via the Results[i].ErrorMessage field.
func (k msgServer) BatchStartInference(goCtx context.Context, msg *types.MsgBatchStartInference) (*types.MsgBatchStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	results := make([]*types.MsgStartInferenceResponse, len(msg.Starts))

	for i, start := range msg.Starts {
		// Create an isolated sub-context for this item. If the item fails we discard its
		// writes by simply not calling writeCache(); the parent ctx remains clean.
		cacheCtx, writeCache := ctx.CacheContext()

		resp, err := k.StartInference(cacheCtx, start)
		if err != nil {
			// Item-level failure: log it, record error in results, continue with rest.
			k.LogError("BatchStartInference: item failed", types.Inferences,
				"index", i,
				"inferenceId", start.InferenceId,
				"error", err)
			results[i] = &types.MsgStartInferenceResponse{
				InferenceIndex: start.InferenceId,
				ErrorMessage:   err.Error(),
			}
			// writeCache is intentionally NOT called — sub-context changes are discarded.
		} else {
			// Item succeeded: commit its writes to the parent context.
			writeCache()
			results[i] = resp
		}
	}

	return &types.MsgBatchStartInferenceResponse{Results: results}, nil
}
