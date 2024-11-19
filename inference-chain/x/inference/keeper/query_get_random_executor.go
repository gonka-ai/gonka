package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetRandomExecutor(goCtx context.Context, req *types.QueryGetRandomExecutorRequest) (*types.QueryGetRandomExecutorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	participant, err := epochGroup.GetRandomMember(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}
