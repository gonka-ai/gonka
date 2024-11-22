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
		k.LogError("Validation: Inference not found", "inferenceId", msg.InferenceId)
		return nil, errorsmod.Wrapf(types.ErrInferenceNotFound, "inference with id %s not found", msg.InferenceId)
	}

	if msg.Creator != inference.ProposalDetails.PolicyAddress {
		k.LogError("Validation: Invalid authority", "expected", inference.ProposalDetails.PolicyAddress, "got", msg.Creator)
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", inference.ProposalDetails.PolicyAddress, msg.Creator)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if found != true {
		k.LogError("Validation: Participant not found", "address", inference.ExecutedBy)
		return nil, errorsmod.Wrapf(types.ErrParticipantNotFound, "participant with address %s not found", inference.ExecutedBy)
	}

	// Idempotent, so no error
	if inference.Status == types.InferenceStatus_INVALIDATED {
		k.LogDebug("Validation: Inference already invalidated", "inferenceId", msg.InferenceId)
		return nil, nil
	}

	err := k.markInferenceAsInvalid(&executor, &inference, ctx)
	if err != nil {
		return nil, err
	}
	executor.Status = calculateStatus(FalsePositiveRate, executor)

	k.SetInference(ctx, inference)
	k.SetParticipant(ctx, executor)
	return &types.MsgInvalidateInferenceResponse{}, nil
}

func (k msgServer) markInferenceAsInvalid(executor *types.Participant, inference *types.Inference, ctx sdk.Context) error {
	inference.Status = types.InferenceStatus_INVALIDATED
	executor.InvalidatedInferences++
	executor.ConsecutiveInvalidInferences++
	executor.CoinBalance -= inference.ActualCost
	// We need to refund the cost, so we have to lookup the person who paid
	payer, found := k.GetParticipant(ctx, inference.RequestedBy)
	if !found {
		k.Logger().Error("Validation: Payer not found", "address", inference.RequestedBy)
		return types.ErrParticipantNotFound
	}
	if payer.Address == executor.Address {
		// It is possible that a participant returns an invalid
		// inference for it's own self-inference
		executor.RefundBalance += inference.ActualCost
	} else {
		payer.RefundBalance += inference.ActualCost
		k.SetParticipant(ctx, payer)
	}
	k.Logger().Info("Validation: Inference invalidated", "inferenceId", inference.InferenceId, "executor", executor.Address, "actualCost", inference.ActualCost)
	return nil
}
