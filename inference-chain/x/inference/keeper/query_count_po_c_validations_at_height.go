package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCvalidationsAtHeight(goCtx context.Context, req *types.QueryCountPoCvalidationsAtHeightRequest) (*types.QueryCountPoCvalidationsAtHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	count, err := k.GetPocValidationCountByStage(ctx, int64(req.BlockHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get PoC validation count: %v", err)
	}
	return &types.QueryCountPoCvalidationsAtHeightResponse{
		Count: count,
	}, nil
}
