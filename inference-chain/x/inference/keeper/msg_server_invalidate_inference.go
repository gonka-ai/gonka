package keeper

import (
	"context"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) InvalidateInference(goCtx context.Context, msg *types.MsgInvalidateInference) (*types.MsgInvalidateInferenceResponse, error) {
	inference, found := k.GetInference(goCtx, msg.InferenceId)
	if found != true {
		k.LogError("Inference not found", types.Validation, "inferenceId", msg.InferenceId)
		return nil, errorsmod.Wrapf(types.ErrInferenceNotFound, "inference with id %s not found", msg.InferenceId)
	}

	if msg.Creator != inference.ProposalDetails.PolicyAddress {
		k.LogError("Invalid authority", types.Validation, "expected", inference.ProposalDetails.PolicyAddress, "got", msg.Creator)
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", inference.ProposalDetails.PolicyAddress, msg.Creator)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if found != true {
		k.LogError("Participant not found", types.Validation, "address", inference.ExecutedBy)
		return nil, errorsmod.Wrapf(types.ErrParticipantNotFound, "participant with address %s not found", inference.ExecutedBy)
	}

	// Idempotent, so no error
	if inference.Status == types.InferenceStatus_INVALIDATED {
		k.LogDebug("Inference already invalidated", types.Validation, "inferenceId", msg.InferenceId)
		return nil, nil
	}

	err := k.markInferenceAsInvalid(&executor, &inference, ctx)
	if err != nil {
		return nil, err
	}
	executor.Status = calculateStatus(k.Keeper.GetParams(goCtx).ValidationParams, executor)

	k.SetInference(ctx, inference)
	err = k.DevelopersStatsSet(ctx, inference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy)
	}

	k.SetParticipant(ctx, executor)
	return &types.MsgInvalidateInferenceResponse{}, nil
}

func (k msgServer) markInferenceAsInvalid(executor *types.Participant, inference *types.Inference, ctx sdk.Context) error {
	inference.Status = types.InferenceStatus_INVALIDATED
	executor.CurrentEpochStats.InvalidatedInferences++
	executor.ConsecutiveInvalidInferences++
	executor.CoinBalance -= inference.ActualCost
	k.LogBalance(executor.Address, inference.ActualCost, executor.CoinBalance, "inference_invalidated:"+inference.InferenceId)
	k.LogInfo("Invalid Inference subtracted from Executor CoinBalance ", types.Balances, "inferenceId", inference.InferenceId, "executor", executor.Address, "actualCost", inference.ActualCost, "coinBalance", executor.CoinBalance)
	// We need to refund the cost, so we have to lookup the person who paid
	payer, found := k.GetParticipant(ctx, inference.RequestedBy)
	if !found {
		k.LogError("Payer not found", types.Validation, "address", inference.RequestedBy)
		return types.ErrParticipantNotFound
	}
	err := k.IssueRefund(ctx, uint64(inference.ActualCost), payer.Address, "invalidated_inference:"+inference.InferenceId)
	if err != nil {
		k.LogError("Refund failed", types.Validation, "error", err)
	}
	k.LogInfo("Inference invalidated", types.Inferences, "inferenceId", inference.InferenceId, "executor", executor.Address, "actualCost", inference.ActualCost)
	return nil
}
