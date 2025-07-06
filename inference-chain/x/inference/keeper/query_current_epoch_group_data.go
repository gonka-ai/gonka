package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CurrentEpochGroupData(goCtx context.Context, req *types.QueryCurrentEpochGroupDataRequest) (*types.QueryCurrentEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: *epochGroup.GroupData,
	}, nil
}

func (k Keeper) PreviousEpochGroupData(goCtx context.Context, req *types.QueryPreviousEpochGroupDataRequest) (*types.QueryPreviousEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	epochGroup, err := k.GetPreviousEpochGroup(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryPreviousEpochGroupDataResponse{
		EpochGroupData: *epochGroup.GroupData,
	}, nil
}
