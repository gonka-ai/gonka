package keeper

import (
	"context"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetMinimumValidationAverage(goCtx context.Context, req *types.QueryGetMinimumValidationAverageRequest) (*types.QueryGetMinimumValidationAverageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	currentEpochData, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("failed to get current epoch data", types.Validation, "error", err)
		return nil, status.Error(codes.Internal, "failed to get current epoch data")
	}
	trafficBasis := math.Max(currentEpochData.GroupData.NumberOfRequests, currentEpochData.GroupData.PreviousEpochRequests)

	return &types.QueryGetMinimumValidationAverageResponse{
		TrafficBasis:             uint64(trafficBasis),
		BlockHeight:              uint64(ctx.BlockHeight()),
		MinimumValidationAverage: calculations.CalculateMinimumValidationAverage(int64(trafficBasis), currentEpochData.GroupData.ValidationParams).String(),
	}, nil
}
