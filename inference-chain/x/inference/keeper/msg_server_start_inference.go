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

	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	// If the inference already exists, it might be because FinishInference came before StartInference
	// In that case, we need to update the existing inference record with the start information
	if found && existingInference.Status != types.InferenceStatus_FINISHED {
		// then it's a duplicate StartInference, which is an error
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

	var inference types.Inference
	if existingInference.Status == types.InferenceStatus_FINISHED {
		// If the inference exists and is in FINISHED state, it means FinishInference came first
		// Update the existing inference with the start information, but keep the finish information
		k.LogInfo("StartInference received after FinishInference", types.Inferences, "inferenceId", msg.InferenceId)
		inference = existingInference
		inference.PromptHash = msg.PromptHash
		inference.PromptPayload = msg.PromptPayload
		inference.RequestedBy = msg.RequestedBy
		inference.Model = msg.Model
		inference.StartBlockHeight = ctx.BlockHeight()
		inference.StartBlockTimestamp = ctx.BlockTime().UnixMilli()
		inference.MaxTokens = DefaultMaxTokens
		inference.AssignedTo = msg.AssignedTo
		inference.NodeVersion = msg.NodeVersion
	} else {
		// Normal case: StartInference comes first
		inference = types.Inference{
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
			MaxTokens:   DefaultMaxTokens,
			AssignedTo:  msg.AssignedTo,
			NodeVersion: msg.NodeVersion,
		}
	}

	// Only put payment in escrow if it hasn't been done already
	// (if FinishInference came first, we'll need to do this now)
	if inference.EscrowAmount == 0 {
		// If StartInference happens after FinishInference, we need to use the actual token counts
		// for the escrow amount calculation, not the MaxTokens amount
		if inference.Status == types.InferenceStatus_FINISHED {
			// We already have the actual token counts from FinishInference
			// Make sure ActualCost is calculated based on those token counts
			if inference.ActualCost == 0 {
				inference.ActualCost = CalculateCost(inference)
			}
			// Use the actual cost for escrow
			escrowAmount, err := k.PutPaymentInEscrow(ctx, &inference)
			if err != nil {
				return nil, err
			}
			inference.EscrowAmount = escrowAmount
		} else {
			// Normal case: StartInference comes first
			escrowAmount, err := k.PutPaymentInEscrow(ctx, &inference)
			if err != nil {
				return nil, err
			}
			inference.EscrowAmount = escrowAmount
		}
	}

	k.SetInference(ctx, inference)
	// TODO epochId пустой, потому что заполняется только на Finish inference: fix!!!
	err := k.DevelopersStatsSet(goCtx, inference.RequestedBy, inference.InferenceId, inference.Status, inference.EpochGroupId, inference.PromptTokenCount+inference.CompletionTokenCount)
	if err != nil {
		k.LogError("DevelopersStatsSet", types.Inferences, err)
	}

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

	if inference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, currentEpochGroup, inference)
		if err != nil {
			return nil, err
		}
	}

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}
