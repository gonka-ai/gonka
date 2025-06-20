package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)

	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		// If the inference doesn't exist, it means FinishInference came before StartInference
		// Create a placeholder inference record that will be updated when StartInference comes
		k.LogInfo("FinishInference received before StartInference", types.Inferences, "inference_id", msg.InferenceId)
		existingInference = types.Inference{
			Index:                msg.InferenceId,
			InferenceId:          msg.InferenceId,
			Status:               types.InferenceStatus_FINISHED,
			ResponseHash:         msg.ResponseHash,
			ResponsePayload:      msg.ResponsePayload,
			PromptTokenCount:     msg.PromptTokenCount,
			CompletionTokenCount: msg.CompletionTokenCount,
			ExecutedBy:           msg.ExecutedBy,
			EndBlockHeight:       ctx.BlockHeight(),
			EndBlockTimestamp:    ctx.BlockTime().UnixMilli(),
			// These fields will be updated when StartInference comes
			MaxTokens: DefaultMaxTokens, // Use default for now
		}
	}
	executor, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy)
	}

	// If this is a placeholder inference (FinishInference came before StartInference),
	// we don't have the requester information yet, so we'll skip this check
	var requester types.Participant
	if existingInference.RequestedBy != "" {
		requester, found = k.GetParticipant(ctx, existingInference.RequestedBy)
		if !found {
			return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, existingInference.RequestedBy)
		}
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

	// Always save the inference, even if it's a placeholder
	k.SetInference(ctx, existingInference)
	err = k.SetDeveloperStats(ctx, existingInference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", existingInference.InferenceId, "inference_status", existingInference.Status.String(), "developer", existingInference.RequestedBy)
	}

	executor.LastInferenceTime = existingInference.EndBlockTimestamp
	executor.CoinBalance += existingInference.ActualCost
	k.LogBalance(executor.Address, existingInference.ActualCost, executor.CoinBalance, "inference_finished:"+existingInference.InferenceId)
	k.LogInfo("Executor CoinBalance credited for inference", types.Balances, "executor", executor.Address, "coin_balance", executor.CoinBalance, "actual_cost", existingInference.ActualCost)
	executor.CurrentEpochStats.InferenceCount++
	executor.CurrentEpochStats.EarnedCoins += uint64(existingInference.ActualCost)

	// Only issue a refund if we have requester information and an escrow amount
	// (if FinishInference came before StartInference, we won't have this information yet)
	if existingInference.IsCompleted() && existingInference.EscrowAmount > 0 {
		refundAmount := existingInference.EscrowAmount - existingInference.ActualCost
		if refundAmount > 0 {
			err = k.IssueRefund(ctx, uint64(refundAmount), requester.Address, "inference_refund:"+existingInference.InferenceId)
			if err != nil {
				k.LogError("Unable to Issue Refund for finished inference", types.Payments, err)
			}
		}
	}

	k.SetParticipant(ctx, executor)

	if existingInference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, currentEpochGroup, existingInference)
		if err != nil {
			return nil, err
		}
	}

	return &types.MsgFinishInferenceResponse{}, nil
}

func (k msgServer) handleInferenceCompleted(ctx sdk.Context, currentEpochGroup *epochgroup.EpochGroup, existingInference types.Inference) error {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", existingInference.InferenceId),
		),
	)
	executorPower := uint64(0)
	executorReputation := int32(0)
	for _, weight := range currentEpochGroup.GroupData.ValidationWeights {
		if weight.MemberAddress == existingInference.ExecutedBy {
			executorPower = uint64(weight.Weight)
			executorReputation = weight.Reputation
			break
		}
	}
	modelEpochGroup, err := k.GetEpochGroup(ctx, currentEpochGroup.GroupData.PocStartBlockHeight, existingInference.Model)
	if err != nil {
		k.LogError("Unable to get model Epoch Group", types.EpochGroup, err)
		return err
	}

	inferenceDetails := types.InferenceValidationDetails{
		InferenceId:        existingInference.InferenceId,
		ExecutorId:         existingInference.ExecutedBy,
		ExecutorReputation: executorReputation,
		TrafficBasis:       uint64(math.Max(currentEpochGroup.GroupData.NumberOfRequests, currentEpochGroup.GroupData.PreviousEpochRequests)),
		ExecutorPower:      executorPower,
		EpochId:            currentEpochGroup.GroupData.EpochGroupId,
		Model:              existingInference.Model,
		TotalPower:         uint64(modelEpochGroup.GroupData.TotalWeight),
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
	return nil
}
