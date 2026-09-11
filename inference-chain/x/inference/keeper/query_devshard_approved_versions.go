package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) DevshardApprovedVersions(goCtx context.Context, req *types.QueryDevshardApprovedVersionsRequest) (*types.QueryDevshardApprovedVersionsResponse, error) {
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
	return &types.QueryDevshardApprovedVersionsResponse{Versions: out}, nil
}
