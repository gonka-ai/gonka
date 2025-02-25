package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) HardwareNodesAll(goCtx context.Context, req *types.QueryHardwareNodesAllRequest) (*types.QueryHardwareNodesAllResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	k.GetHardwareNodes()
	// TODO: Process the query
	_ = ctx

	return &types.QueryHardwareNodesAllResponse{}, nil
}
