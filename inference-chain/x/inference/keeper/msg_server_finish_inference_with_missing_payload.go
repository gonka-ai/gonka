package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInferenceWithMissingPayload(
	goCtx context.Context,
	msg *types.MsgFinishInferenceWithMissingPayload,
) (*types.MsgFinishInferenceWithMissingPayloadResponse, error) {
	k.LogInfo("FinishInferenceWithMissingPayload", types.Inferences, "inferenceId", msg.MsgFinishInference.InferenceId)

	// TODO: Perform voting validations here
	resp, err := k.FinishInference(goCtx, msg.MsgFinishInference)
	if err != nil {
		k.LogError(
			"FinishInferenceWithMissingPayload: failed to finish inference", types.Inferences,
			"inferenceId", msg.MsgFinishInference.InferenceId,
			"error", err,
		)
		return failedFinishWithMissingPayload(resp, err)
	}

	k.LogInfo(
		"FinishInferenceWithMissingPayload: done handling message", types.Inferences,
		"inferenceId", msg.VotingResult.InferenceId,
	)
	return &types.MsgFinishInferenceWithMissingPayloadResponse {
		InferenceIndex: resp.InferenceIndex,
	}, nil
}

func failedFinishWithMissingPayload(
	resp *types.MsgFinishInferenceResponse,
	err error,
) (*types.MsgFinishInferenceWithMissingPayloadResponse, error) {
	return &types.MsgFinishInferenceWithMissingPayloadResponse {
		InferenceIndex: resp.InferenceIndex,
		ErrorMessage:   err.Error(),
	}, err
}
