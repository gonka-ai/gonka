package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)

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
		k.LogError("GetCurrentEpochGroup", types.EpochGroup, err)
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
	k.LogInfo("Executor CoinBalance credited for inference", types.Payments, "executor", executor.Address, "coin_balance", executor.CoinBalance, "actual_cost", existingInference.ActualCost)
	executor.CurrentEpochStats.InferenceCount++

	refundAmount := existingInference.EscrowAmount - existingInference.ActualCost
	if refundAmount > 0 {
		err = k.IssueRefund(ctx, uint64(refundAmount), requester.Address)
		if err != nil {
			k.LogError("Unable to Issue Refund for finished inference", types.Payments, err)
		}
	}

	k.SetParticipant(ctx, executor)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", msg.InferenceId),
		),
	)
	executorPower := uint64(0)
	executorReputation := int32(0)
	for _, weight := range currentEpochGroup.GroupData.ValidationWeights {
		if weight.MemberAddress == executor.Address {
			executorPower = uint64(weight.Weight)
			executorReputation = weight.Reputation
			break
		}
	}

	inferenceDetails := types.InferenceValidationDetails{
		InferenceId:        existingInference.InferenceId,
		ExecutorId:         existingInference.ExecutedBy,
		ExecutorReputation: executorReputation,
		TrafficBasis:       math.Max(currentEpochGroup.GroupData.NumberOfRequests, currentEpochGroup.GroupData.PreviousEpochRequests),
		ExecutorPower:      executorPower,
		EpochId:            currentEpochGroup.GroupData.EpochGroupId,
	}
	k.LogDebug(
		"Adding Inference Validation Details",
		types.Validation,
		"inference_id", inferenceDetails.InferenceId,
		"epoch_id", inferenceDetails.EpochId,
		"executor_id", inferenceDetails.ExecutorId,
		"executor_power", inferenceDetails.ExecutorPower,
		"executor_reputation", inferenceDetails.ExecutorReputation,
		"traffic_basis", inferenceDetails.TrafficBasis,
	)
	k.SetInferenceValidationDetails(ctx, inferenceDetails)
	k.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)

	return &types.MsgFinishInferenceResponse{}, nil
}
