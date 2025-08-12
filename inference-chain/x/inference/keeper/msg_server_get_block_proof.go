package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetBlockProofByHeight(ctx context.Context, req *types.QueryBlockProofRequest) (*types.QueryBlockProofResponse, error) {
	proof, _ := k.GetBlockProof(ctx, req.GetProofHeight())
	return &types.QueryBlockProofResponse{&proof}, nil
}
