package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/validation"
)

// votingResultBackendAdapter adapts msgServer to validation.VotingResultBackend.
type votingResultBackendAdapter struct {
	msgServer
}

// NewVotingResultBackend returns a validation.VotingResultBackend for the keeper.
func NewVotingResultBackend(ms msgServer) validation.VotingResultBackend {
	return &votingResultBackendAdapter{msgServer: ms}
}

func (a *votingResultBackendAdapter) GetInference(ctx context.Context, inferenceId string) (*types.Inference, error) {
	inf, found := a.Keeper.GetInference(ctx, inferenceId)
	if !found {
		return nil, types.ErrInferenceNotFound
	}
	return &inf, nil
}

func (a *votingResultBackendAdapter) GetAllowedVoters(ctx context.Context, inf *types.Inference, maxVoters int) (map[string]bool, error) {
	return a.sampleAllowedVoters(ctx, inf, maxVoters)
}

func (a *votingResultBackendAdapter) GetRequesterPubKeys(ctx context.Context, requesterAddress string) ([]string, error) {
	return a.GetAccountPubKeysWithGrantees(ctx, requesterAddress)
}

func (a *votingResultBackendAdapter) GetVoterPubKey(ctx context.Context, voterAddress string) (string, error) {
	return a.GetAccountPubKey(ctx, voterAddress)
}
