package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) QueuedTrainingTasks(goCtx context.Context, req *types.QueryQueuedTrainingTasksRequest) (*types.QueryQueuedTrainingTasksResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	taskIds := k.ListQueuedTasks(ctx)
	tasks, err := k.GetTasks(ctx, taskIds)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryQueuedTrainingTasksResponse{
		Tasks: tasks,
	}, nil
}
