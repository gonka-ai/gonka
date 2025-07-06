package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)

	_, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy)
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	blockContext := calculations.BlockContext{
		BlockHeight:    ctx.BlockHeight(),
		BlockTimestamp: ctx.BlockTime().UnixMilli(),
	}

	inference, payments := calculations.ProcessFinishInference(&existingInference, msg, blockContext, k)

	finalInference, err := k.processInferencePayments(ctx, inference, payments)
	if err != nil {
		return nil, err
	}
	k.SetInference(ctx, *finalInference)
	if existingInference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, &existingInference)
		if err != nil {
			return nil, err
		}
	}

	return &types.MsgFinishInferenceResponse{}, nil
}

func (k msgServer) handleInferenceCompleted(ctx sdk.Context, existingInference *types.Inference) error {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", existingInference.InferenceId),
		),
	)
	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		k.LogError("Effective Epoch Index not found", types.EpochGroup)
		return types.ErrEffectiveEpochNotFound.Wrapf("handleInferenceCompleted: Effective Epoch Index not found")
	}
	currentEpochGroup, err := k.GetEpochGroupForEpoch(ctx, *effectiveEpoch)
	if err != nil {
		k.LogError("Unable to get current Epoch Group", types.EpochGroup, "err", err)
		return err
	}

	existingInference.EpochPocStartBlockHeight = uint64(effectiveEpoch.PocStartBlockHeight)
	existingInference.EpochId = effectiveEpoch.Index
	currentEpochGroup.GroupData.NumberOfRequests++

	executorPower := uint64(0)
	executorReputation := int32(0)
	for _, weight := range currentEpochGroup.GroupData.ValidationWeights {
		if weight.MemberAddress == existingInference.ExecutedBy {
			executorPower = uint64(weight.Weight)
			executorReputation = weight.Reputation
			break
		}
	}

	modelEpochGroup, err := currentEpochGroup.GetSubGroup(ctx, existingInference.Model)
	if err != nil {
		k.LogError("Unable to get model Epoch Group", types.EpochGroup, "err", err)
		return err
	}

	inferenceDetails := types.InferenceValidationDetails{
		InferenceId:        existingInference.InferenceId,
		ExecutorId:         existingInference.ExecutedBy,
		ExecutorReputation: executorReputation,
		TrafficBasis:       uint64(math.Max(currentEpochGroup.GroupData.NumberOfRequests, currentEpochGroup.GroupData.PreviousEpochRequests)),
		ExecutorPower:      executorPower,
		// Can be deleted in next upgrade
		EpochId:      currentEpochGroup.GroupData.EpochGroupId,
		EpochGroupId: currentEpochGroup.GroupData.EpochGroupId,
		Model:        existingInference.Model,
		TotalPower:   uint64(modelEpochGroup.GroupData.TotalWeight),
	}
	if inferenceDetails.TotalPower == inferenceDetails.ExecutorPower {
		k.LogWarn("Executor Power equals Total Power", types.Validation,
			"model", existingInference.Model,
			"epoch_id", currentEpochGroup.GroupData.EpochGroupId,
			"epoch_start_block_height", currentEpochGroup.GroupData.PocStartBlockHeight,
			"group_id", modelEpochGroup.GroupData.EpochGroupId,
			"inference_id", existingInference.InferenceId,
			"executor_id", inferenceDetails.ExecutorId,
			"executor_power", inferenceDetails.ExecutorPower,
		)
	}
	k.LogDebug(
		"Adding Inference Validation Details",
		types.Validation,
		"inference_id", inferenceDetails.InferenceId,
		"epoch_group_id", inferenceDetails.EpochGroupId,
		"executor_id", inferenceDetails.ExecutorId,
		"executor_power", inferenceDetails.ExecutorPower,
		"executor_reputation", inferenceDetails.ExecutorReputation,
		"traffic_basis", inferenceDetails.TrafficBasis,
	)
	k.SetInferenceValidationDetails(ctx, inferenceDetails)
	k.SetInference(ctx, *existingInference)
	k.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)
	return nil
}
