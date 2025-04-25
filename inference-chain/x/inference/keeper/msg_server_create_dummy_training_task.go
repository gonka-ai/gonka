package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateDummyTrainingTask(goCtx context.Context, msg *types.MsgCreateDummyTrainingTask) (*types.MsgCreateDummyTrainingTaskResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetTrainingTask(ctx, msg.Task)

	return &types.MsgCreateDummyTrainingTaskResponse{
		Task: msg.Task,
	}, nil
}
