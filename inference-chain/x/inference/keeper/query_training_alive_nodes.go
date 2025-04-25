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

	runStore := NewKeeperTrainingRunStore(k)
	runManager := training.NewRunManager(
		req.Req.RunId,
		runStore,
		10,
		10,
	)

	return &types.QueryTrainingAliveNodesResponse{}, nil
}
