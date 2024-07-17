package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	_, found := k.GetInference(ctx, msg.InferenceId)
	if found {
		return nil, sdkerrors.Wrap(types.ErrInferenceIdExists, msg.InferenceId)
	}

	k.SetInference(ctx, types.Inference{
		Index:         msg.InferenceId,
		InferenceId:   msg.InferenceId,
		PromptHash:    msg.PromptHash,
		PromptPayload: msg.PromptPayload,
		ReceivedBy:    msg.ReceivedBy,
		Status:        "STARTED",
		BlockHeight:   ctx.BlockHeight(),
	})

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}
