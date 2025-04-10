package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InProgressTrainingTasks(goCtx context.Context, req *types.QueryInProgressTrainingTasksRequest) (*types.QueryInProgressTrainingTasksResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	taskIds, err := k.ListInProgressTasks(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	tasks, err := k.GetTasks(ctx, taskIds)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryInProgressTrainingTasksResponse{
		Tasks: tasks,
	}, nil
}
