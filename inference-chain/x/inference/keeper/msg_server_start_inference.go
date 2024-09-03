package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	staketypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

const DefaultMaxTokens = 2048

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	log.Println("Iterating validators")
	err := k.validatorSet.IterateValidators(goCtx, func(index int64, val staketypes.ValidatorI) (stop bool) {
		log.Println("ValidatorI", "index", index, "val", val)
		return false
	})
	if err != nil {
		log.Println("Error iterating validators", "error", err)
	}
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
		ReceivedBy:          msg.ReceivedBy,
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
