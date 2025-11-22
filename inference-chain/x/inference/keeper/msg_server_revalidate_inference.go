package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevalidateInference(ctx context.Context, msg *types.MsgRevalidateInference) (*types.MsgRevalidateInferenceResponse, error) {
	inference, executor, err := k.validateDecisionMessage(ctx, msg)
	if err != nil {
		return nil, err
	}

	if inference.Status == types.InferenceStatus_VALIDATED {
		k.LogDebug("Inference already validated", types.Validation, "inferenceId", msg.InferenceId)
		return nil, nil
	}

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

	return &types.MsgRevalidateInferenceResponse{}, nil
}
