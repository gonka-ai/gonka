package keeper

import (
	"context"

	"github.com/cometbft/cometbft/libs/bytes"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/validation"
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

	backend := NewVotingResultBackend(k)
	hasPositiveVote, requesterSigPassed, err := validation.ValidateVotingResult(goCtx, backend, msg.VotingResult)
	if err != nil {
		k.LogError("FinishInferenceWithMissingPayload: voting validation failed", types.Inferences,
			"inferenceId", msg.MsgFinishInference.InferenceId, "error", err)
		resp := failedFinish(ctx, err, msg.MsgFinishInference)
		if requesterSigPassed {
			params, paramsErr := k.GetParams(ctx)
			if paramsErr == nil {
				requesterAddr, addrErr := sdk.AccAddressFromBech32(msg.VotingResult.RequesterAddress)
				if addrErr == nil {
					k.SlashForForgedVotingResult(goCtx, requesterAddr, params)
				}
			}
			// Return success so the slash is committed; client gets error via ErrorMessage.
			return &types.MsgFinishInferenceWithMissingPayloadResponse{
				InferenceIndex: resp.InferenceIndex,
				ErrorMessage:   err.Error(),
			}, nil
		}
		return failedFinishWithMissingPayload(resp, err)
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

// sampleAllowedVoters replays the voter sampling algorithm for the given inference
// and returns the set of addresses that would have been sampled (excluding TA and executor).
// Used to validate that VotingResult signers are legitimate sampled voters.
func (k msgServer) sampleAllowedVoters(
	goCtx context.Context,
	inf *types.Inference,
	maxVoters int,
) (map[string]bool, error) {
	eg, err := k.GetEpochGroup(goCtx, inf.EpochId, "")
	if err != nil {
		return nil, err
	}

	blockHash := bytes.HexBytes(inf.StartBlockHash)
	nextMember := eg.MakeRandomMemberReplayableFn(goCtx, blockHash)

	exclude := map[string]bool{
		inf.TransferredBy: true,
		inf.AssignedTo:    true,
	}

	allowed := make(map[string]bool)
	for len(allowed) < maxVoters {
		participant, err := nextMember()
		if err != nil {
			break
		}
		if exclude[participant.Address] {
			continue
		}
		if participant.InferenceUrl == "" {
			continue
		}
		allowed[participant.Address] = true
	}
	return allowed, nil
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
