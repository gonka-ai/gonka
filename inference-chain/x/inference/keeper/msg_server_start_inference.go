package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference deprecated", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator)
	return failedStart(ctx, classicInferenceDeprecatedError(), msg), nil
}

func failedStart(ctx sdk.Context, err error, msg *types.MsgStartInference) *types.MsgStartInferenceResponse {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent("start_inference",
			sdk.NewAttribute("result", "failed")))
	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
		ErrorMessage:   err.Error(),
	}
}
