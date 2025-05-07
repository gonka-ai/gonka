package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingAliveNodes(goCtx context.Context, req *types.QueryTrainingAliveNodesRequest) (*types.QueryTrainingAliveNodesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	_ = ctx

	runStore := NewKeeperTrainingRunStore(k)
	runManager := training.NewRunManager(req.Req.RunId, runStore, k)

	nodeIds, err := runManager.GetEpochActiveNodes(goCtx, req.Req.OuterStep, training.NewBlockInfo(ctx))
	if err != nil {
		k.LogError("GetEpochActiveNodes failure", types.Training, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	nodeIdsString := make([]string, len(nodeIds))
	for i, nodeId := range nodeIds {
		nodeIdsString[i] = nodeId.ToString()
	}
	response := &types.QueryTrainingAliveNodesResponse{
		Resp: &types.GetAliveNodesResponse{
			AliveNodes: nodeIdsString,
		},
	}

	return response, nil
}
