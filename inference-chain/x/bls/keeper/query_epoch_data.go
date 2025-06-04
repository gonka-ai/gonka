package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/bls/types"
)

// EpochBLSData returns complete BLS data for a given epoch
func (k Keeper) EpochBLSData(ctx context.Context, req *types.QueryEpochBLSDataRequest) (*types.QueryEpochBLSDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.EpochId == 0 {
		return nil, status.Error(codes.InvalidArgument, "epoch_id cannot be zero")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Retrieve EpochBLSData for the requested epoch
	epochBLSData, found := k.GetEpochBLSData(sdkCtx, req.EpochId)
	if !found {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("no DKG data found for epoch %d", req.EpochId))
	}

	// Return complete epoch data
	return &types.QueryEpochBLSDataResponse{
		EpochData: epochBLSData,
	}, nil
}
