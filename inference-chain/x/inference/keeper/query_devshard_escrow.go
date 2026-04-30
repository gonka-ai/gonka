package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) DevshardEscrow(ctx context.Context, req *types.QueryGetDevshardEscrowRequest) (*types.QueryGetDevshardEscrowResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	escrow, found := k.GetDevshardEscrow(ctx, req.Id)
	if !found {
		return &types.QueryGetDevshardEscrowResponse{Found: false}, nil
	}

	return &types.QueryGetDevshardEscrowResponse{
		Escrow: &escrow,
		Found:  true,
	}, nil
}
