package keeper

import (
	"context"
	"log/slog"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingTaskAll(goCtx context.Context, req *types.QueryTrainingTaskAllRequest) (*types.QueryTrainingTaskAllResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	tasks, err := k.GetAllTrainingTasks(ctx)
	if err != nil {
		slog.Error("Error getting all training tasks", "err", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryTrainingTaskAllResponse{
		Tasks: tasks,
	}, nil
}
