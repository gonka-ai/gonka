package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const DefaultMaxTokens = 2048

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference", "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.ReceivedBy, "model", msg.Model)

	_, found := k.GetInference(ctx, msg.InferenceId)
	if found {
		return nil, sdkerrors.Wrap(types.ErrInferenceIdExists, msg.InferenceId)
	}

	_, pFound := k.GetParticipant(ctx, msg.Creator)
	if !pFound {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator)
	}

	_, found = k.GetParticipant(ctx, msg.ReceivedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ReceivedBy)
	}

	inference := types.Inference{
		Index:               msg.InferenceId,
		InferenceId:         msg.InferenceId,
		PromptHash:          msg.PromptHash,
		PromptPayload:       msg.PromptPayload,
		RequestedBy:         msg.ReceivedBy,
		Status:              types.InferenceStatus_STARTED,
		Model:               msg.Model,
		StartBlockHeight:    ctx.BlockHeight(),
		StartBlockTimestamp: ctx.BlockTime().UnixMilli(),
		// For now, use the default tokens. Long term, we'll need to add MaxTokens to the message.
		MaxTokens: DefaultMaxTokens,
	}
	escrowAmount, err := k.PutPaymentInEscrow(ctx, &inference)
	if err != nil {
		return nil, err
	}
	inference.EscrowAmount = escrowAmount
	k.SetInference(ctx, inference)

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}
