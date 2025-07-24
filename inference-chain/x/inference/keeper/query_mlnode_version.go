package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) MLNodeVersion(goCtx context.Context, req *types.QueryGetMLNodeVersionRequest) (*types.QueryGetMLNodeVersionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	val, found := k.GetMLNodeVersion(ctx)
	if !found {
		// Return default version if not found
		val = types.MLNodeVersion{CurrentVersion: "v3.0.8"}
	}

	return &types.QueryGetMLNodeVersionResponse{MlnodeVersion: val}, nil
}
