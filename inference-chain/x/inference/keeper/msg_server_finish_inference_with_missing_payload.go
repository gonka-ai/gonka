package keeper

import (
	"context"
	"slices"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInferenceWithMissingPayload(
	goCtx context.Context,
	msg *types.MsgFinishInferenceWithMissingPayload,
) (*types.MsgFinishInferenceWithMissingPayloadResponse, error) {
	k.LogInfo("FinishInferenceWithMissingPayload", types.Inferences, "inferenceId", msg.MsgFinishInference.InferenceId)
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg == nil {
		err := types.ErrEmptyMessage
		resp := &types.MsgFinishInferenceResponse {
			InferenceIndex: "",
			ErrorMessage: err.Error(),
		}
		return failedFinishWithMissingPayload(resp, err)
	}
	if msg.MsgFinishInference == nil {
		var inferenceIndex string
		if msg.VotingResult != nil {
			inferenceIndex = msg.VotingResult.InferenceId
		}

		err := types.ErrEmptyMessage
		resp := &types.MsgFinishInferenceResponse {
			InferenceIndex: inferenceIndex,
			ErrorMessage: err.Error(),
		}
		return failedFinishWithMissingPayload(resp, err)
	}
	if msg.VotingResult == nil {
		err := types.ErrEmptyVotingResult
		return failedFinishWithMissingPayload(failedFinish(ctx, err, msg.MsgFinishInference), err)
	}
	if msg.Creator != msg.MsgFinishInference.Creator {
		err := types.ErrCreatorMismatch
		return failedFinishWithMissingPayload(failedFinish(ctx, err, msg.MsgFinishInference), err)
	}
	if msg.VotingResult.InferenceId != msg.MsgFinishInference.InferenceId {
		err := types.ErrInferenceIdMismatch
		return failedFinishWithMissingPayload(failedFinish(ctx, err, msg.MsgFinishInference), err)
	}

	// TODO: Perform voting validations here

	if !slices.ContainsFunc(
		msg.VotingResult.Votes,
		func(v *types.SignedVote) bool {
			return v.VoteType == types.VoteType_VotePositive
		},
	) {
		k.LogWarn(
			"FinishInferenceWithMissingPayload: Negative vote outcome, refunding user", types.Inferences,
			"inferenceId", msg.MsgFinishInference.InferenceId,
		)

		resp, err := k.finishInferenceImpl(goCtx, msg.MsgFinishInference, true)
		if err != nil {
			k.LogError(
				"FinishInferenceWithMissingPayload: failed to finish inference", types.Inferences,
				"inferenceId", msg.MsgFinishInference.InferenceId,
				"error", err,
			)
			return failedFinishWithMissingPayload(resp, err)
		}

		return &types.MsgFinishInferenceWithMissingPayloadResponse {
			InferenceIndex: resp.InferenceIndex,
		}, nil
	}

	resp, err := k.finishInferenceImpl(goCtx, msg.MsgFinishInference, false)
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
