package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevalidateInference(goCtx context.Context, msg *types.MsgRevalidateInference) (*types.MsgRevalidateInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	inference, executor, err := k.validateDecisionMessage(ctx, msg)
	if err != nil {
		return nil, err
	}

	if inference.Status == types.InferenceStatus_VALIDATED {
		k.LogDebug("Inference already validated", types.Validation, "inferenceId", msg.InferenceId)
		return nil, nil
	}

	inference.Status = types.InferenceStatus_VALIDATED
	executor.ConsecutiveInvalidInferences = 0
	executor.CurrentEpochStats.ValidatedInferences++

	cacheCtx, writeFn := ctx.CacheContext()

	err = k.SetParticipant(cacheCtx, *executor)
	if err != nil {
		return nil, err
	}

	k.LogInfo("Saving inference", types.Validation, "inferenceId", inference.InferenceId, "status", inference.Status, "authority", inference.ProposalDetails.PolicyAddress)
	err = k.SetInference(cacheCtx, *inference)
	if err != nil {
		return nil, err
	}

	writeFn()
	return &types.MsgRevalidateInferenceResponse{}, nil
}
