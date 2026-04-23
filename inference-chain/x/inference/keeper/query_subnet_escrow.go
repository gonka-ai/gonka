package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) SubnetEscrow(ctx context.Context, req *types.QueryGetSubnetEscrowRequest) (*types.QueryGetSubnetEscrowResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	escrow, found := k.GetSubnetEscrow(ctx, req.Id)
	if !found {
		return &types.QueryGetSubnetEscrowResponse{Found: false}, nil
	}

	return &types.QueryGetSubnetEscrowResponse{
		Escrow: &escrow,
		Found:  true,
	}, nil
}
