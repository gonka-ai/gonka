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
	executor, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy)
	}
	requester, found := k.GetParticipant(ctx, existingInference.ReceivedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, existingInference.ReceivedBy)
	}

	existingInference.Status = types.InferenceStatus_FINISHED
	existingInference.ResponseHash = msg.ResponseHash
	existingInference.ResponsePayload = msg.ResponsePayload
	existingInference.PromptTokenCount = msg.PromptTokenCount
	existingInference.CompletionTokenCount = msg.CompletionTokenCount
	existingInference.ExecutedBy = msg.ExecutedBy
	existingInference.EndBlockHeight = ctx.BlockHeight()
	existingInference.EndBlockTimestamp = ctx.BlockTime().UnixMilli()
	existingInference.ActualCost = CalculateCost(existingInference)
	k.SetInference(ctx, existingInference)

	executor.LastInferenceTime = existingInference.EndBlockTimestamp
	executor.PromptTokenCount[existingInference.Model] += existingInference.PromptTokenCount
	executor.CompletionTokenCount[existingInference.Model] += existingInference.CompletionTokenCount
	executor.CoinBalance += existingInference.ActualCost
	executor.InferenceCount++

	refundAmount := existingInference.EscrowAmount - existingInference.ActualCost
	if refundAmount > 0 {
		if requester.Address == executor.Address {
			executor.CoinBalance += refundAmount
		} else {
			requester.CoinBalance += refundAmount
			k.SetParticipant(ctx, requester)
		}
	}

	k.SetParticipant(ctx, executor)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", msg.InferenceId),
		),
	)

	return &types.MsgFinishInferenceResponse{}, nil
}
