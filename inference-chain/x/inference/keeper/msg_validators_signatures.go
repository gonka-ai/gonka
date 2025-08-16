package keeper

import (
	"context"
	"errors"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
)

var (
	ErrEmptyBlockHeight   = errors.New("empty block height")
	ErrSignaturesNotFound = errors.New("signatures not found")
)

func (k Keeper) GetValidatorsProofByHeight(ctx context.Context, req *types.QueryGetValidatorsProofRequest) (*types.QueryGetValidatorsProofResponse, error) {
	if req.GetProofHeight() == 0 {
		return nil, ErrEmptyBlockHeight
	}

	fmt.Printf("GetValidatorsProofByHeight: block_height %v\n", req.GetProofHeight())

	signatures, found := k.GetValidatorsSignatures(ctx, req.ProofHeight)
	if !found {
		return nil, ErrSignaturesNotFound
	}

	return &types.QueryGetValidatorsProofResponse{Proof: &signatures}, nil
}
