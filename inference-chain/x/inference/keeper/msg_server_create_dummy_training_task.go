package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateDummyTrainingTask(goCtx context.Context, msg *types.MsgCreateDummyTrainingTask) (*types.MsgCreateDummyTrainingTaskResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	msg.Task.CreatedAtBlockHeight = uint64(ctx.BlockHeight())
	if msg.Task.Epoch == nil {
		msg.Task.Epoch = training.NewEmptyEpochInfo()
	}

	k.SetTrainingTask(ctx, msg.Task)

	return &types.MsgCreateDummyTrainingTaskResponse{
		Task: msg.Task,
	}, nil
}
