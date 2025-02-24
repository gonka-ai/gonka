package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) HardwareNodes(goCtx context.Context, req *types.QueryHardwareNodesRequest) (*types.QueryHardwareNodesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	nodes, found := k.GetHardwareNodes(ctx, req.Participant)
	if !found {
		return &types.QueryHardwareNodesResponse{
			Nodes: &types.HardwareNodes{
				HardwareNodes: []*types.HardwareNode{},
			},
		}, nil
	}

	return &types.QueryHardwareNodesResponse{
		Nodes: nodes,
	}, nil
}
