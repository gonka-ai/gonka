package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrInferenceNotFound, msg.InferenceId)
	}

	existingInference.Status = "FINISHED"
	existingInference.ResponseHash = msg.ResponseHash
	existingInference.ResponsePayload = msg.ResponsePayload
	existingInference.PromptTokenCount = msg.PromptTokenCount
	existingInference.CompletionTokenCount = msg.CompletionTokenCount
	existingInference.ExecutedBy = msg.ExecutedBy
	k.SetInference(ctx, existingInference)

	return &types.MsgFinishInferenceResponse{}, nil
}
