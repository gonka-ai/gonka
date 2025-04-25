package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingBarrier(goCtx context.Context, req *types.QueryTrainingBarrierRequest) (*types.QueryTrainingBarrierResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	store := NewKeeperTrainingRunStore(k)
	runManager := training.NewRunManager(
		req.Req.RunId,
		store,
		10,
		10,
	)

	_ = runManager
	_ = ctx
	// PRTODO: query!!!

	return &types.QueryTrainingBarrierResponse{}, nil
}
