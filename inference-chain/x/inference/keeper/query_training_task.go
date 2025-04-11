package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingTask(goCtx context.Context, req *types.QueryTrainingTaskRequest) (*types.QueryTrainingTaskResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	task, found := k.GetTrainingTask(ctx, req.Id)
	if !found {
		return nil, status.Error(codes.NotFound, "task not found")
	}

	return &types.QueryTrainingTaskResponse{
		Task: task,
	}, nil
}
