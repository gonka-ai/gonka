package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateTrainingTask(goCtx context.Context, msg *types.MsgCreateTrainingTask) (*types.MsgCreateTrainingTaskResponse, error) {
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
	}

	err := k.CreateTask(ctx, task)
	if err != nil {
		return nil, err
	}

	return &types.MsgCreateTrainingTaskResponse{
		Task: task,
	}, nil
}
