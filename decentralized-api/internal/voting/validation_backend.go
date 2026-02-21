package voting

import (
	"context"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/validation"
)

// queryBackend implements validation.VotingResultBackend using chain queries.
type queryBackend struct {
	queryClient   types.QueryClient
	cosmosClient  cosmosclient.CosmosMessageClient
	requesterKeys []string
}

// NewQueryBackend creates a validation backend for off-chain use.
// requesterKeys is the executor's pubkey.
func NewQueryBackend(
	queryClient types.QueryClient,
	cosmosClient cosmosclient.CosmosMessageClient,
	requesterKeys []string,
) validation.VotingResultBackend {
	return &queryBackend{
		queryClient:   queryClient,
		cosmosClient:  cosmosClient,
		requesterKeys: requesterKeys,
	}
}

func (b *queryBackend) GetRequesterPubKeys(_ context.Context, _ string) ([]string, error) {
	return b.requesterKeys, nil
}

func (b *queryBackend) GetInference(ctx context.Context, inferenceId string) (*types.Inference, error) {
	resp, err := b.queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil || resp == nil {
		return nil, types.ErrInferenceNotFound
	}
	return &resp.Inference, nil
}

func (b *queryBackend) GetAllowedVoters(ctx context.Context, inf *types.Inference) (map[string]bool, error) {
	allowed, err := SampleVotersForInference(ctx, b.cosmosClient, inf,
		inf.TransferredBy, inf.AssignedTo)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(allowed))
	for _, v := range allowed {
		m[v.Address] = true
	}
	return m, nil
}

func (b *queryBackend) GetVoterPubKey(ctx context.Context, voterAddress string) (string, error) {
	resp, err := b.queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{
		Address: voterAddress,
	})
	if err != nil || resp == nil || resp.GetPubkey() == "" {
		return "", err
	}
	return resp.GetPubkey(), nil
}

// toTypesVotingResult converts api VotingResult to types VotingResult for the shared validator.
func toTypesVotingResult(r *inference.VotingResult) *types.VotingResult {
	if r == nil {
		return nil
	}
	votes := make([]*types.SignedVote, len(r.Votes))
	for i, v := range r.Votes {
		if v == nil {
			continue
		}
		votes[i] = &types.SignedVote{
			InferenceId:        v.InferenceId,
			VoterAddress:       v.VoterAddress,
			VoteType:           types.VoteType(v.VoteType),
			RespondentDataHash: v.RespondentDataHash,
			Timestamp:          v.Timestamp,
			VoterSignature:     v.VoterSignature,
		}
	}
	return &types.VotingResult{
		InferenceId:        r.InferenceId,
		Votes:              votes,
		CompletedAt:        r.CompletedAt,
		RequesterAddress:   r.RequesterAddress,
		RequesterSignature: r.RequesterSignature,
	}
}
