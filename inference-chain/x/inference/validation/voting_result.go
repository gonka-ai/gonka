package validation

import (
	"context"

	sdkerrors "cosmossdk.io/errors"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// VotingResultBackend provides chain/backend access for validating a VotingResult.
// Both the keeper (on-chain) and decentralized-api (off-chain) implement this.
type VotingResultBackend interface {
	GetRequesterPubKeys(ctx context.Context, requesterAddress string) ([]string, error)
	GetInference(ctx context.Context, inferenceId string) (*types.Inference, error)
	GetAllowedVoters(ctx context.Context, inf *types.Inference) (map[string]bool, error)
	GetVoterPubKey(ctx context.Context, voterAddress string) (string, error)
}

// ValidateVotingResult validates a VotingResult using the provided backend.
// Returns: hasPositiveVote, requesterSigPassed (true if requester signature validated before any failure), error.
func ValidateVotingResult(
	ctx context.Context,
	backend VotingResultBackend,
	result *types.VotingResult,
) (hasPositiveVote bool, requesterSigPassed bool, err error) {
	if len(result.Votes) == 0 {
		return false, false, sdkerrors.Wrap(types.ErrInvalidVoteCount, "no votes provided")
	}

	requesterPubKeys, err := backend.GetRequesterPubKeys(ctx, result.RequesterAddress)
	if err != nil {
		return false, false, sdkerrors.Wrap(types.ErrParticipantNotFound, result.RequesterAddress)
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
		return false, false, sdkerrors.Wrap(types.ErrInvalidVotingResultSignature, sigErr.Error())
	}
	requesterSigPassed = true

	inf, err := backend.GetInference(ctx, result.InferenceId)
	if err != nil || inf == nil {
		return false, requesterSigPassed, sdkerrors.Wrap(types.ErrInferenceNotFound, result.InferenceId)
	}

	if len(inf.StartBlockHash) > 0 {
		allowedVoters, err := backend.GetAllowedVoters(ctx, inf)
		if err != nil {
			return false, requesterSigPassed, sdkerrors.Wrap(types.ErrInvalidVotingResult, err.Error())
		}
		for i, vote := range result.Votes {
			if !allowedVoters[vote.VoterAddress] {
				return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
					"vote[%d] voter %s not in replayable sampled set", i, vote.VoterAddress)
			}
		}
	}

	hasPositiveVote = false
	for i, vote := range result.Votes {
		if vote.InferenceId != result.InferenceId {
			return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrVoteInferenceIdMismatch,
				"vote[%d] inference_id %s != %s", i, vote.InferenceId, result.InferenceId)
		}

		voterPubKey, err := backend.GetVoterPubKey(ctx, vote.VoterAddress)
		if err != nil || voterPubKey == "" {
			return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrVoterNotFound,
				"vote[%d] voter %s", i, vote.VoterAddress)
		}

		if vote.VoterSignature == "" {
			return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
				"vote[%d] missing voter signature", i)
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
			return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrInvalidVotingResult,
				"vote[%d] invalid voter signature: %v", i, sigErr)
		}

		switch vote.VoteType {
		case types.VoteType_VotePositive:
			if hasPositiveVote {
				return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrDuplicatePositiveVote,
					"vote[%d] second positive vote", i)
			}
			hasPositiveVote = true
		case types.VoteType_VoteNegative:
			if hasPositiveVote {
				return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrNegativeVoteAfterPositive,
					"vote[%d] negative vote after positive", i)
			}
		default:
			return false, requesterSigPassed, sdkerrors.Wrapf(types.ErrInvalidVoteType,
				"vote[%d] type %d", i, vote.VoteType)
		}
	}

	return hasPositiveVote, requesterSigPassed, nil
}
