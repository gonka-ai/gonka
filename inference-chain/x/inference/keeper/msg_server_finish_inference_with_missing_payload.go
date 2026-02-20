package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
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
		resp := &types.MsgFinishInferenceResponse{
			InferenceIndex: "",
			ErrorMessage:   err.Error(),
		}
		return failedFinishWithMissingPayload(resp, err)
	}
	if msg.MsgFinishInference == nil {
		var inferenceIndex string
		if msg.VotingResult != nil {
			inferenceIndex = msg.VotingResult.InferenceId
		}

		err := types.ErrEmptyMessage
		resp := &types.MsgFinishInferenceResponse{
			InferenceIndex: inferenceIndex,
			ErrorMessage:   err.Error(),
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

	hasPositiveVote, err := k.validateVotingResult(goCtx, msg.VotingResult)
	if err != nil {
		k.LogError("FinishInferenceWithMissingPayload: voting validation failed", types.Inferences,
			"inferenceId", msg.MsgFinishInference.InferenceId, "error", err)
		return failedFinishWithMissingPayload(failedFinish(ctx, err, msg.MsgFinishInference), err)
	}

	if !hasPositiveVote {
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

		return &types.MsgFinishInferenceWithMissingPayloadResponse{
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
	return &types.MsgFinishInferenceWithMissingPayloadResponse{
		InferenceIndex: resp.InferenceIndex,
	}, nil
}

// validateVotingResult validates the VotingResult attached to a FinishInferenceWithMissingPayload.
// Returns whether a positive vote was found and any validation error.
func (k msgServer) validateVotingResult(
	goCtx context.Context,
	result *types.VotingResult,
) (bool, error) {
	if len(result.Votes) == 0 {
		return false, sdkerrors.Wrap(types.ErrInvalidVoteCount, "no votes provided")
	}

	// Validate requester signature on the voting result.
	// The requester (executor) signs: inference_id + sha256(votes)
	// Use grantee-aware lookup because the executor may sign with a warm/grantee key.
	requesterPubKeys, err := k.GetAccountPubKeysWithGrantees(goCtx, result.RequesterAddress)
	if err != nil {
		return false, sdkerrors.Wrap(types.ErrParticipantNotFound, result.RequesterAddress)
	}

	voteFields := make([]calculations.VoteFields, len(result.Votes))
	for i, v := range result.Votes {
		voteFields[i] = calculations.VoteFields{
			InferenceId:        v.InferenceId,
			VoterAddress:       v.VoterAddress,
			VoteType:           int32(v.VoteType),
			RespondentDataHash: v.RespondentDataHash,
			Timestamp:          v.Timestamp,
			VoterSignature:     v.VoterSignature,
		}
	}
	resultPayload := calculations.VotingResultBytesToSign(result.InferenceId, voteFields)
	components := calculations.SignatureComponents{
		Payload:         string(resultPayload),
		EpochId:         0,
		Timestamp:       result.CompletedAt,
		TransferAddress: result.RequesterAddress,
		ExecutorAddress: "",
	}
	if sigErr := calculations.ValidateSignatureWithGrantees(components, calculations.Developer, requesterPubKeys, result.RequesterSignature); sigErr != nil {
		k.LogError("Invalid voting result requester signature", types.Inferences,
			"requesterAddress", result.RequesterAddress, "error", sigErr)
		return false, sdkerrors.Wrap(types.ErrInvalidVotingResultSignature, sigErr.Error())
	}

	hasPositiveVote := false
	for i, vote := range result.Votes {
		if vote.InferenceId != result.InferenceId {
			return false, sdkerrors.Wrapf(types.ErrVoteInferenceIdMismatch,
				"vote[%d] inference_id %s != %s", i, vote.InferenceId, result.InferenceId)
		}

		_, voterFound := k.GetParticipant(goCtx, vote.VoterAddress)
		if !voterFound {
			return false, sdkerrors.Wrapf(types.ErrVoterNotFound,
				"vote[%d] voter %s", i, vote.VoterAddress)
		}

		// Validate voter signature on their vote.
		if vote.VoterSignature == "" {
			return false, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
				"vote[%d] missing voter signature", i)
		}
		voterPubKey, err := k.GetAccountPubKey(goCtx, vote.VoterAddress)
		if err != nil {
			return false, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
				"vote[%d] cannot get voter pubkey: %v", i, err)
		}
		if sigErr := calculations.ValidateVoteSignature(
			vote.InferenceId,
			vote.VoterAddress,
			int32(vote.VoteType),
			vote.RespondentDataHash,
			vote.Timestamp,
			voterPubKey,
			vote.VoterSignature,
		); sigErr != nil {
			k.LogError("Invalid voter signature", types.Inferences,
				"inferenceId", result.InferenceId, "voteIndex", i, "voterAddress", vote.VoterAddress, "error", sigErr)
			return false, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
				"vote[%d] invalid voter signature: %v", i, sigErr)
		}

		switch vote.VoteType {
		case types.VoteType_VotePositive:
			if hasPositiveVote {
				return false, sdkerrors.Wrapf(types.ErrDuplicatePositiveVote,
					"vote[%d] second positive vote", i)
			}
			hasPositiveVote = true
		case types.VoteType_VoteNegative:
			if hasPositiveVote {
				return false, sdkerrors.Wrapf(types.ErrNegativeVoteAfterPositive,
					"vote[%d] negative vote after positive", i)
			}
		default:
			return false, sdkerrors.Wrapf(types.ErrInvalidVoteType,
				"vote[%d] type %d", i, vote.VoteType)
		}

	}

	return hasPositiveVote, nil
}

func failedFinishWithMissingPayload(
	resp *types.MsgFinishInferenceResponse,
	err error,
) (*types.MsgFinishInferenceWithMissingPayloadResponse, error) {
	return &types.MsgFinishInferenceWithMissingPayloadResponse{
		InferenceIndex: resp.InferenceIndex,
		ErrorMessage:   err.Error(),
	}, err
}
