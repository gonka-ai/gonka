package keeper

import (
	"context"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevalidateInference(goCtx context.Context, msg *types.MsgRevalidateInference) (*types.MsgRevalidateInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	inference, found := k.GetInference(goCtx, msg.InferenceId)
	if found != true {
		k.LogError("Validation: Inference not found", "inferenceId", msg.InferenceId)
		return nil, errorsmod.Wrapf(types.ErrInferenceNotFound, "inference with id %s not found", msg.InferenceId)
	}

	if msg.Creator != inference.ProposalDetails.PolicyAddress {
		k.LogError("Validation: Invalid authority", "expected", inference.ProposalDetails.PolicyAddress, "got", msg.Creator)
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", inference.ProposalDetails.PolicyAddress, msg.Creator)
	}

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if found != true {
		k.LogError("Validation: Participant not found", "address", inference.ExecutedBy)
		return nil, errorsmod.Wrapf(types.ErrParticipantNotFound, "participant with address %s not found", inference.ExecutedBy)
	}

	if inference.Status == types.InferenceStatus_VALIDATED {
		k.LogDebug("Validation: Inference already validated", "inferenceId", msg.InferenceId)
		return nil, nil
	}

	inference.Status = types.InferenceStatus_VALIDATED
	executor.ConsecutiveInvalidInferences = 0
	executor.ValidatedInferences++

	executor.Status = calculateStatus(FalsePositiveRate, executor)
	k.SetParticipant(ctx, executor)

	k.LogInfo("Validation: Saving inference", "inferenceId", inference.InferenceId, "status", inference.Status, "authority", inference.ProposalDetails.PolicyAddress)
	k.SetInference(ctx, inference)

	return &types.MsgRevalidateInferenceResponse{}, nil
}
