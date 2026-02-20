// Package voting provides types and services for the node voting mechanism.
package voting

import (
	"context"
	"fmt"

	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/validation"
)

// ChainVerifier queries the chain for inference state and validates challenger claims.
// Used by the voting service at dispute initiation.
type ChainVerifier struct {
	Recorder   cosmosclient.CosmosMessageClient
}

// NewChainVerifier creates a new ChainVerifier with the given cosmos client.
func NewChainVerifier(recorder cosmosclient.CosmosMessageClient) *ChainVerifier {
	return &ChainVerifier{
		Recorder: recorder,
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

	inf := response.Inference
	finishExists := inf.ResponseHash != ""

	return &OnChainProof{
		InferenceExists:      true,
		AssignedTo:           inf.AssignedTo,
		CreatedBy:            inf.TransferredBy,
		FinishExists:         finishExists,
		ExpectedPromptHash:   inf.PromptHash,
		ExpectedResponseHash: inf.ResponseHash,
		RequestTimestamp:     inf.RequestTimestamp,
		TransferSignature:    inf.TransferSignature,
	}, nil
}

// ValidateVotingResultSignature verifies the requester's signature on a VotingResult.
// Used as an off-chain fail-fast pre-check before submitting to the chain.
func ValidateVotingResultSignature(result *inference.VotingResult, requesterPubkey string) error {
	voteFields := make([]calculations.VoteFields, len(result.Votes))
	for i, vote := range result.Votes {
		voteFields[i] = calculations.VoteFields{
			InferenceId:        vote.InferenceId,
			VoterAddress:       vote.VoterAddress,
			VoteType:           int32(vote.VoteType),
			RespondentDataHash: vote.RespondentDataHash,
			Timestamp:          vote.Timestamp,
			VoterSignature:     vote.VoterSignature,
		}
	}
	payloadBytes := calculations.VotingResultBytesToSign(result.InferenceId, voteFields)

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

// ValidateVotingResultFull validates a VotingResult using the shared inference-chain validation.
// Call this before submitting MsgFinishInferenceWithMissingPayload.
func ValidateVotingResultFull(
	ctx context.Context,
	queryClient types.QueryClient,
	cosmosClient cosmosclient.CosmosMessageClient,
	_ *types.Inference,
	result *inference.VotingResult,
	requesterPubkey string,
) error {
	backend := NewQueryBackend(queryClient, cosmosClient, []string{requesterPubkey})
	typesResult := toTypesVotingResult(result)
	_, _, err := validation.ValidateVotingResult(ctx, backend, typesResult)
	if err != nil {
		logging.Error("Voting result validation failed", types.Voting,
			"inferenceId", result.InferenceId, "error", err)
		return fmt.Errorf("invalid voting result: %w", err)
	}
	return nil
}
