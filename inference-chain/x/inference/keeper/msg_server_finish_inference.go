package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/calculations"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("FinishInference", "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)

	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrInferenceNotFound, msg.InferenceId)
	}
	executor, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy)
	}
	requester, found := k.GetParticipant(ctx, existingInference.RequestedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, existingInference.RequestedBy)
	}
	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetCurrentEpochGroup", err)
		return nil, err
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
	existingInference.EpochGroupId = currentEpochGroup.GroupData.PocStartBlockHeight
	k.SetInference(ctx, existingInference)

	executor.LastInferenceTime = existingInference.EndBlockTimestamp
	executor.CoinBalance += existingInference.ActualCost
	executor.InferenceCount++

	refundAmount := existingInference.EscrowAmount - existingInference.ActualCost
	if refundAmount > 0 {
		err = k.IssueRefund(ctx, uint64(refundAmount), requester.Address)
		if err != nil {
			k.LogError("Unable to Issue Refund for finished inference", err)
		}
	}

	k.SetParticipant(ctx, executor)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", msg.InferenceId),
		),
	)
	currentEpochGroup.GroupData.NumberOfRequests++
	currentEpochGroup.GroupData.FinishedInferences = append(currentEpochGroup.GroupData.FinishedInferences,
		&types.InferenceDetail{
			InferenceId: existingInference.InferenceId,
			Executor:    existingInference.ExecutedBy,
			ExecutorReputation: calculations.CalculateReputation(calculations.ReputationContext{
				EpochCount:       int64(executor.EpochsCompleted),
				ValidationParams: currentEpochGroup.GroupData.ValidationParams,
			}).InexactFloat64(),
			TrafficBasis: math.Max(currentEpochGroup.GroupData.NumberOfRequests, currentEpochGroup.GroupData.PreviousEpochRequests),
		})
	k.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)

	return &types.MsgFinishInferenceResponse{}, nil
}
