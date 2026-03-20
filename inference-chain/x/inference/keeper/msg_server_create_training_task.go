package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/training"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// TODO: Once ready this needs to validate the message payload per specs (max number of hardware resources or string length.
func (k msgServer) CreateTrainingTask(goCtx context.Context, msg *types.MsgCreateTrainingTask) (*types.MsgCreateTrainingTaskResponse, error) {
	if err := k.CheckPermission(goCtx, msg, TrainingStartPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	taskId := k.GetNextTaskID(ctx)

	task := &types.TrainingTask{
		Id:                    taskId,
		RequestedBy:           msg.Creator,
		CreatedAtBlockHeight:  uint64(ctx.BlockHeight()),
		AssignedAtBlockHeight: 0,
		FinishedAtBlockHeight: 0,
		HardwareResources:     msg.HardwareResources,
		Config:                msg.Config,
		Epoch:                 training.NewEmptyEpochInfo(),
	}

	err := k.CreateTask(ctx, task)
	if err != nil {
		return nil, err
	}

	return &types.MsgCreateTrainingTaskResponse{
		Task: task,
	}, nil
}
