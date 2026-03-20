package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) SubnetHostEpochStats(ctx context.Context, req *types.QueryGetSubnetHostEpochStatsRequest) (*types.QueryGetSubnetHostEpochStatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid participant address")
	}

	stats, found := k.GetSubnetHostEpochStats(ctx, req.EpochIndex, participantAddr)
	if !found {
		return &types.QueryGetSubnetHostEpochStatsResponse{Found: false}, nil
	}

	return &types.QueryGetSubnetHostEpochStatsResponse{
		Stats: &stats,
		Found: true,
	}, nil
}
