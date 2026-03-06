package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevalidateInference(ctx context.Context, msg *types.MsgRevalidateInference) (*types.MsgRevalidateInferenceResponse, error) {
	// Revalidate is a special case for permissions!
	// The Creator needs to be the policy-id of the GROUP that the revalidation vote happened in.
	if err := k.CheckPermission(ctx, msg, NoPermission); err != nil {
		return nil, err
	}

	inference, executor, err := k.validateDecisionMessage(ctx, msg)
	if err != nil {
		return nil, err
	}

	if inference.Status == types.InferenceStatus_VALIDATED {
		k.LogDebug("Inference already validated", types.Validation, "inferenceId", msg.InferenceId)
		return nil, nil
	}

	previousStatus := inference.Status
	inference.Status = types.InferenceStatus_VALIDATED
	executor.ConsecutiveInvalidInferences = 0
	executor.CurrentEpochStats.ValidatedInferences++

	err = k.SetParticipant(ctx, *executor)
	if err != nil {
		return nil, err
	}

	k.LogInfo("Saving inference", types.Validation, "inferenceId", inference.InferenceId, "status", inference.Status, "authority", inference.ProposalDetails.PolicyAddress)
	err = k.SetInference(ctx, *inference)
	if err != nil {
		return nil, err
	}
	if inference.Status != previousStatus {
		emitInferenceStatusUpdatedEvent(sdk.UnwrapSDKContext(ctx), inference.InferenceId, inference.Status)
	}

	return &types.MsgRevalidateInferenceResponse{}, nil
}
