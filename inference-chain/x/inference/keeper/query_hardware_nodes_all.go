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

	nodes, err := k.GetAllHardwareNodes(ctx)
	if err != nil {
		k.LogError("HardwareNodesAll query: error getting all hardware nodes", types.Nodes, "err", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryHardwareNodesAllResponse{
		Nodes: nodes,
	}, nil
}
