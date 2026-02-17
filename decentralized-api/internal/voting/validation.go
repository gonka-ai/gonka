// Package voting provides types and services for the node voting mechanism.
package voting

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"time"
	"log/slog"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "cosmossdk.io/errors"

	"github.com/cometbft/cometbft/libs/bytes"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

const (
	ModuleName = "votes"
)

var errorRegisterCounter uint32 = 1100
func registerError(description string) error {
	err := sdkerrors.Register(ModuleName, errorRegisterCounter, description)
	errorRegisterCounter++
	return err
}

// TODO: Inferencetypes has error codes used for validations, should we use those instead?
// NOTE: These errors are sorted alphabetically.
var (
	ErrAssigneeMismatch             = registerError("assignee does not match challenger")
	ErrCreatorMismatch              = registerError("creator does not match challenger")
	ErrDuplicatePositiveVote        = registerError("more than one positive vote was collected")
	ErrEmptyTransferAddress         = registerError("transfer address is empty")
	ErrEmptyTransferSignature       = registerError("transfer signature is empty")
	ErrExceededMaximumNegativeVotes = registerError("too many negative votes were collected")
	ErrInferenceIdMismatch          = registerError("inference IDs do not match")
	ErrInferenceNotFound            = registerError("inference not found on-chain and no assignment proof provided")
	ErrInvalidChallengerRole        = registerError("invalid challenger role")
	ErrInvalidNegativeOutcome       = registerError("negative outcome with invalid number of votes")
	ErrInvalidTransferSignature     = registerError("invalid transfer signature")
	ErrInvalidVoteType              = registerError("invalid vote type")
	ErrNegativeVoteAfterPositive    = registerError("a negative vote was collected after a positive vote")
	ErrNilAssignmentProof           = registerError("assignment proof is nil")
	ErrSeedMismatch                 = registerError("initial seeds do not match")
	ErrTooManySkips                 = registerError("too many voters were skipped")
	ErrTransferAgentNotFound        = registerError("transfer agent not found or has no public key")
)

// ChainVerifier queries the chain for inference state and validates challenger claims.
// Used by the voting service at dispute initiation.
type ChainVerifier struct {
	Recorder   cosmosclient.CosmosMessageClient
	EpochGroup *epochgroup.EpochGroup
}

// NewChainVerifier creates a new ChainVerifier with the given cosmos client and epoch group.
func NewChainVerifier(recorder cosmosclient.CosmosMessageClient, eg *epochgroup.EpochGroup) *ChainVerifier {
	return &ChainVerifier{
		Recorder:   recorder,
		EpochGroup: eg,
	}
}

// QueryInferenceState queries the chain for MsgStartInference and MsgFinishInference data.
// Returns an universal OnChainProof containing all relevant on-chain data for the inference.
func (cv *ChainVerifier) QueryInferenceState(ctx context.Context, inferenceId string) (*OnChainProof, error) {
	queryClient := cv.Recorder.NewInferenceQueryClient()

	response, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		return &OnChainProof{InferenceExists: false}, nil
	}

	inference := response.Inference
	finishExists := inference.ResponseHash != ""

	return &OnChainProof{
		InferenceExists:      true,
		AssignedTo:           inference.AssignedTo,
		CreatedBy:            inference.TransferredBy,
		FinishExists:         finishExists,
		ExpectedPromptHash:   inference.PromptHash,
		ExpectedResponseHash: inference.ResponseHash,
		RequestTimestamp:     inference.RequestTimestamp,
		TransferSignature:    inference.TransferSignature,
	}, nil
}

// ValidateAssignmentProof verifies the TransferSignature cryptographically.
func (cv *ChainVerifier) ValidateAssignmentProof(ctx context.Context, proof *AssignmentProof) error {
	if proof == nil {
		slog.Error("assignment proof is nil")
		return ErrNilAssignmentProof
	}
	if proof.TransferSignature == "" {
		slog.Error("transfer signature is empty")
		return ErrEmptyTransferSignature
	}
	if proof.TransferAddress == "" {
		slog.Error("transfer address is empty")
		return ErrEmptyTransferAddress
	}

	queryClient := cv.Recorder.NewInferenceQueryClient()
	participant, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{
		Address: proof.TransferAddress,
	})
	if err != nil || participant.Pubkey == "" {
		slog.Error("failed to get transfer agent participant and its public key", "error", err, "transferAddress", proof.TransferAddress)
		return ErrTransferAgentNotFound
	}

	components := calculations.SignatureComponents{
		Payload:         proof.PromptHash,
		Timestamp:       proof.Timestamp,
		TransferAddress: proof.TransferAddress,
		ExecutorAddress: proof.ExecutorAddress,
	}

	if err := calculations.ValidateSignatureWithGrantees(
		components,
		calculations.TransferAgent,
		[]string{participant.Pubkey},
		proof.TransferSignature,
	); err != nil {
		slog.Error("invalid transfer signature", "error", err)
		return ErrInvalidTransferSignature
	}

	return nil
}

// ValidateChallengerRole verifies that the challenger is legitimately associated with the inference.
func (cv *ChainVerifier) ValidateChallengerRole(onChain *OnChainProof, req *VotingInitiateRequest) error {
	if !onChain.InferenceExists {
		if req.AssignmentProof == nil {
			slog.Error("inference not found on-chain and no assignment proof provided")
			return ErrInferenceNotFound
		}
		return nil
	}

	switch req.ChallengerRole {
	case RoleExecutor:
		if onChain.AssignedTo != req.ChallengerAddress {
			slog.Error("challenger is not the assigned executor", "challenger", req.ChallengerAddress, "expected", onChain.AssignedTo)
			return ErrAssigneeMismatch
		}
	case RoleTA:
		if onChain.CreatedBy != req.ChallengerAddress {
			slog.Error("challenger is not the transfer agent", "challenger", req.ChallengerAddress, "expected", onChain.CreatedBy)
			return ErrCreatorMismatch
		}
	default:
		slog.Error("invalid challenger role", "role", req.ChallengerRole)
		return ErrInvalidChallengerRole
	}

	return nil
}

// ValidateVotes performs full deterministic validation of the voting session.
func (cv *ChainVerifier) ValidateVotes(
	ctx context.Context,
	onChain *OnChainProof,
	cfg VotingConfig,
	req VotingInitiateRequest,
	res VotingResult,
	start *inference.MsgStartInference,
	blockHash bytes.HexBytes,
) error {
	randomCtx, err := cv.EpochGroup.NewReplayableRandomContext(ctx, blockHash)
	if err != nil {
		slog.Error("error creating replayable random context")
		return err // TODO: What to do here?
	}

	if err := cv.ValidateAssignmentProof(ctx, req.AssignmentProof); err != nil {
		return err
	}
	if randomCtx.Seed != req.AssignmentProof.Seed {
		slog.Error("seed does not match expected seed", "expected", randomCtx.Seed, "actual", req.AssignmentProof.Seed)
		return ErrSeedMismatch
	}

	if res.InferenceId != req.InferenceId || res.InferenceId != start.InferenceId {
		slog.Error("response inference ID does not match voting request and start inference request IDs", "response", res.InferenceId, "req", req.InferenceId, "start", start.InferenceId)
		return ErrInferenceIdMismatch
	}

	if err := cv.ValidateChallengerRole(onChain, &req); err != nil {
		return err
	}

	// Challenger signature validation
	msg := []byte{}
	msg = append(msg, req.InferenceId...)
	msg = append(msg, req.ChallengerAddress...)
	msg = append(msg, req.RespondentAddress...)
	binary.LittleEndian.AppendUint64(msg, uint64(req.Timestamp))

	if err := calculations.ValidateSignatureBytes(msg, req.ChallengerAddress, req.ChallengerSignature); err != nil {
		slog.Error("signature does not match for challenger")
		return err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	skippedMembersCount := uint8(0)
	negativeVotesCounter := 0
	hasPositiveVote := false
	voteIdx := 0

	// TODO: Validate upper bound
	// TODO: Check that hashes match
	// TODO: Check auth key
	for voteIdx < len(res.Votes) {
		participant, err := cv.EpochGroup.GetRandomMemberReplayable(ctx, randomCtx)
		if err != nil {
			slog.Error("error getting next participant")
			return err // TODO: What to do here?
		}

		vote := res.Votes[voteIdx]
		if vote.VoterAddress != participant.Address {
			skippedMembersCount++
			if skippedMembersCount > cfg.MaxNumSkips {
				slog.Error("too many vote skips")
				return ErrTooManySkips
			}
			continue
		}

		if vote.InferenceId != req.InferenceId {
			slog.Error("vote inference ID does not match expected inference ID", "vote", vote.InferenceId, "req", req.InferenceId)
			return ErrInferenceIdMismatch
		}

		msg := []byte{}
		msg = append(msg, vote.InferenceId...)
		msg = append(msg, vote.VoterAddress...)
		binary.LittleEndian.AppendUint16(msg, uint16(vote.VoteType))
		msg = append(msg, vote.RespondentDataHash...)
		binary.LittleEndian.AppendUint64(msg, uint64(vote.Timestamp))

		if err := calculations.ValidateSignatureBytes(msg, vote.VoterAddress, vote.VoterSignature); err != nil {
			slog.Error("signature does not match for voter")
			return err
		}

		calculations.ValidateTimestamp(
			vote.Timestamp,
			sdkCtx.BlockTime().UnixNano(),
			-1, // TODO: params.ValidationParams.TimestampExpiration,
			-1, // TODO: params.ValidationParams.TimestampAdvance,
			60*int64(time.Second),
		)

		switch vote.VoteType {
		case types.VoteType_VotePositive:
			if hasPositiveVote {
				slog.Error("got more than one positive vote")
				return ErrDuplicatePositiveVote
			}
			hasPositiveVote = true
		case types.VoteType_VoteNegative:
			negativeVotesCounter++
			if hasPositiveVote {
				slog.Error("got a negative vote after a positive vote was received")
				return ErrNegativeVoteAfterPositive
			}
			if negativeVotesCounter > cfg.MaxNumNodes {
				slog.Error("too many negative votes")
				return ErrExceededMaximumNegativeVotes
			}
		case types.VoteType_VoteInvalid:
			slog.Error("vote type is invalid")
			return ErrInvalidVoteType
		default:
			slog.Error("vote type is unknown")
			return ErrInvalidVoteType
		}

		voteIdx++
	}

	if !hasPositiveVote && negativeVotesCounter != cfg.MaxNumNodes {
		slog.Error("too few negative votes and no positive vote")
		return ErrInvalidNegativeOutcome
	}

	return nil
}

// DetermineVerificationOutcome checks the on-chain state and determines what the vote should be.
func (cv *ChainVerifier) DetermineVerificationOutcomeAndDeliverPayload(
	onChain *OnChainProof,
	verificationType VerificationType,
	actualDataHash string,
) VoteType {
	switch verificationType {
	case VerifyPayloadFromTA:
		if actualDataHash == "" {
			return types.VoteType_VoteNegative
		}
		if onChain.ExpectedPromptHash != "" && actualDataHash != onChain.ExpectedPromptHash {
			return types.VoteType_VoteNegative
		}
		if onChain.FinishExists {
			return types.VoteType_VoteNegative
		}
		return types.VoteType_VotePositive
	// TODO: Implement other verification types when needed
	// case VerifyMsgStartExists:
	// 	// Check if MsgStartInference exists
	// 	if onChain.InferenceExists {
	// 		return types.VoteType_VotePositive // TA posted the message
	// 	}
	// 	return types.VoteType_VoteNegative // TA didn't post
	//
	// case VerifyMsgFinishExists:
	// 	// Check if MsgFinishInference exists
	// 	if onChain.FinishExists {
	// 		return types.VoteType_VotePositive // Executor completed
	// 	}
	// 	return types.VoteType_VoteNegative // Executor didn't complete
	//
	// case VerifyPromptHashMatch:
	// 	// Compare actual payload hash with on-chain prompt_hash
	// 	if actualDataHash == onChain.ExpectedPromptHash {
	// 		return types.VoteType_VotePositive // Hash matches
	// 	}
	// 	return types.VoteType_VoteNegative // Hash mismatch
	//
	// case VerifyResponseHashMatch:
	// 	// Compare actual payload hash with on-chain response_hash
	// 	if actualDataHash == onChain.ExpectedResponseHash {
	// 		return types.VoteType_VotePositive // Hash matches
	// 	}
	// 	return VoteNegative // Hash mismatch
	//
	// case VerifyPayloadFromExecutor:
	// 	// TA challenges executor: verify executor has the payload
	// 	if actualDataHash != "" {
	// 		return types.VoteType_VotePositive // Got payload
	// 	}
	// 	return types.VoteType_VoteNegative // No payload
	//
	// case VerifyResponseDeliveryToTA:
	// 	// TA claims they didn't receive response from executor.
	// 	// We verify: Did executor complete their job?
	// 	if onChain.FinishExists && actualDataHash != "" {
	// 		return types.VoteType_VotePositive // Executor completed and has payload - not at fault
	// 	}
	// 	return types.VoteType_VoteNegative // Executor didn't complete or doesn't have payload
	default:
		return types.VoteType_VoteInvalid
	}
}

// Returns inference_id + hash256(votes) + requester_address + completed_at
func votingResultBytesToSign(inferenceId string, votes []*inference.SignedVote) []byte {
	votesHash := sha256.New()
	for _, vote := range votes {
		votesFields := [6]string{
			vote.InferenceId,
			vote.VoterAddress,
			strconv.FormatInt(int64(vote.VoteType), 10),
			vote.RespondentDataHash,
			strconv.FormatInt(vote.Timestamp, 10),
			vote.VoterSignature,
		}
		for _, field := range votesFields {
			// According to the docs for hash.Hash, Write never returns an error.
			votesHash.Write([]byte(field))
		}
	}

	payloadBytes := append([]byte(inferenceId), votesHash.Sum([]byte{})...)
	return payloadBytes
}

// ValidateVotingResultSignature validates the requester signature against inference_id
// Signs: inference_id + hash256(votes) + requester_address + completed_at
func ValidateVotingResultSignature(result *inference.VotingResult, requesterPubkey string) error {
	payloadBytes := votingResultBytesToSign(result.InferenceId, result.Votes)

	components := calculations.SignatureComponents{
		Payload:         string(payloadBytes),
		EpochId:         0,
		Timestamp:       result.CompletedAt,
		TransferAddress: result.RequesterAddress,
		ExecutorAddress: "",
	}
	return calculations.ValidateSignature(
		components,
		calculations.Developer,
		requesterPubkey,
		result.RequesterSignature,
	)
}
