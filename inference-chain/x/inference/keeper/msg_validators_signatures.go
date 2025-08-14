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

func (k Keeper) SetValidatorsProofWithHeight(ctx context.Context, req *types.QuerySetValidatorsProofRequest) (*types.QuerySetValidatorsProofResponse, error) {
	if req.Proof.BlockHeight == 0 {
		return nil, ErrEmptyBlockHeight
	}

	err := k.SetValidatorsSignatures(ctx, *req.Proof)
	if err != nil {
		return nil, err
	}
	return &types.QuerySetValidatorsProofResponse{}, nil
}

func (k Keeper) GetValidatorsProofByHeight(ctx context.Context, req *types.QueryGetValidatorsProofRequest) (*types.QueryGetValidatorsProofResponse, error) {
	if req.GetProofHeight() == 0 {
		return nil, ErrEmptyBlockHeight
	}

	signatures, found := k.GetValidatorsSignatures(ctx, req.ProofHeight)
	if !found {
		return nil, ErrSignaturesNotFound
	}

	return &types.QueryGetValidatorsProofResponse{Proof: &signatures}, nil
}
