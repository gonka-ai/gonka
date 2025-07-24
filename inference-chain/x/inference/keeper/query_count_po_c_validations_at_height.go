package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCValidationsAtHeight(goCtx context.Context, req *types.QueryCountPoCValidationsAtHeightRequest) (*types.QueryCountPoCValidationsAtHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Get all PoCValidations at the specified height
	pocValidations, err := k.GetPoCValidationByStage(ctx, req.BlockHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Count the total number of PoCValidations
	var count uint64 = 0
	for _, validations := range pocValidations {
		count += uint64(len(validations))
	}

	return &types.QueryCountPoCValidationsAtHeightResponse{
		Count: count,
	}, nil
}