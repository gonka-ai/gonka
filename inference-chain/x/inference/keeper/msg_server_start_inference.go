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

	_, pFound := k.GetParticipant(ctx, msg.Creator)
	if !pFound {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator)
	}

	k.SetInference(ctx, types.Inference{
		Index:               msg.InferenceId,
		InferenceId:         msg.InferenceId,
		PromptHash:          msg.PromptHash,
		PromptPayload:       msg.PromptPayload,
		ReceivedBy:          msg.ReceivedBy,
		Status:              "STARTED",
		StartBlockHeight:    ctx.BlockHeight(),
		StartBlockTimestamp: ctx.BlockTime().UnixMilli(),
	})

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}
