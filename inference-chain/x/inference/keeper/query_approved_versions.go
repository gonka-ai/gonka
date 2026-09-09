package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ApprovedVersions(goCtx context.Context, req *types.QueryApprovedVersionsRequest) (*types.QueryApprovedVersionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	versions, err := k.GetApprovedVersions(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	out := make([]*types.DevshardApprovedVersion, 0, len(versions))
	for i := range versions {
		v := versions[i]
		out = append(out, &v)
	}
	return &types.QueryApprovedVersionsResponse{Versions: out}, nil
}
