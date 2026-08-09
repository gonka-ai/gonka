package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) DynamicCoefficients(
	goCtx context.Context,
	req *types.QueryDynamicCoefficientsRequest,
) (*types.QueryDynamicCoefficientsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	epochIndex := req.EpochIndex
	if epochIndex == 0 {
		var found bool
		epochIndex, found = k.GetEffectiveEpochIndex(ctx)
		if !found {
			return nil, status.Error(codes.NotFound, "effective epoch not found")
		}
	}

	data, found, err := k.GetEpochGroupDataWithError(ctx, epochIndex, "")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return nil, status.Error(codes.NotFound, "epoch group data not found")
	}
	return &types.QueryDynamicCoefficientsResponse{
		EpochIndex:   epochIndex,
		Params:       data.DynamicCoefficientParams,
		Coefficients: data.ConfirmationWeightScales,
	}, nil
}
