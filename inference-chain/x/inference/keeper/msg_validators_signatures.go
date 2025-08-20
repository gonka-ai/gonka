package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/types"
)

var (
	ErrEmptyBlockHeight   = errors.New("empty block height")
	ErrSignaturesNotFound = errors.New("signatures not found")
)

func (k Keeper) GetParticipantsProofByHeight(ctx context.Context, req *types.QueryGetParticipantsProofRequest) (*types.QueryGetParticipantsProofResponse, error) {
	if req.GetProofHeight() == 0 {
		return nil, ErrEmptyBlockHeight
	}

	signatures, found := k.GetValidatorsSignatures(ctx, req.ProofHeight)
	if !found {
		return nil, ErrSignaturesNotFound
	}

	proof, _ := k.GetActiveParticipantsProof(ctx, req.ProofHeight)
	return &types.QueryGetParticipantsProofResponse{ValidatorsProof: &signatures, MerkleProof: &proof}, nil
}

func (k Keeper) IfProofPending(ctx context.Context, req *types.QueryIsProofPendingRequest) (*types.QueryIsProofPendingResponse, error) {
	epochIdPendingProof, found := k.GetPendingProof(ctx, req.ProofHeight)
	return &types.QueryIsProofPendingResponse{
		Pending:         found,
		UpcomingEpochId: epochIdPendingProof,
	}, nil
}
