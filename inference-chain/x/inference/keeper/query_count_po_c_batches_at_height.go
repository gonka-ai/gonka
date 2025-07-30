package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCbatchesAtHeight(goCtx context.Context, req *types.QueryCountPoCbatchesAtHeightRequest) (*types.QueryCountPoCbatchesAtHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	count, err := k.GetPoCBatchesCountByStage(ctx, int64(req.BlockHeight))
	if err != nil {
		return nil, err
	}
	return &types.QueryCountPoCbatchesAtHeightResponse{
		Count: count,
	}, nil
}
