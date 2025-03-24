package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const DefaultMaxTokens = 5000

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.RequestedBy, "model", msg.Model)
	_, found := k.GetInference(ctx, msg.InferenceId)
	if found {
		return nil, sdkerrors.Wrap(types.ErrInferenceIdExists, msg.InferenceId)
	}

	_, pFound := k.GetParticipant(ctx, msg.Creator)
	if !pFound {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator)
	}

	_, found = k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy)
	}

	inference := types.Inference{
		Index:               msg.InferenceId,
		InferenceId:         msg.InferenceId,
		PromptHash:          msg.PromptHash,
		PromptPayload:       msg.PromptPayload,
		RequestedBy:         msg.RequestedBy,
		Status:              types.InferenceStatus_STARTED,
		Model:               msg.Model,
		StartBlockHeight:    ctx.BlockHeight(),
		StartBlockTimestamp: ctx.BlockTime().UnixMilli(),
		// For now, use the default tokens. Long term, we'll need to add MaxTokens to the message.
		MaxTokens:  DefaultMaxTokens,
		AssignedTo: msg.AssignedTo,
	}
	escrowAmount, err := k.PutPaymentInEscrow(ctx, &inference)
	if err != nil {
		return nil, err
	}
	inference.EscrowAmount = escrowAmount
	k.SetInference(ctx, inference)
	expirationBlocks := k.GetParams(ctx).ValidationParams.ExpirationBlocks
	k.SetInferenceTimeout(ctx, types.InferenceTimeout{
		ExpirationHeight: uint64(inference.StartBlockHeight + expirationBlocks),
		InferenceId:      inference.InferenceId,
	})
	k.LogInfo("Inference Timeout Set:", types.Inferences, "InferenceId", inference.InferenceId, "ExpirationHeight", inference.StartBlockHeight+10)

	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetCurrentEpochGroup", types.EpochGroup, err)
	} else {
		currentEpochGroup.GroupData.NumberOfRequests++
		k.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)
	}

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}
