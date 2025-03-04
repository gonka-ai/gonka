package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AssignTrainingTask(goCtx context.Context, msg *types.MsgAssignTrainingTask) (*types.MsgAssignTrainingTaskResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	task, found := k.GetTrainingTask(ctx, msg.TaskId)
	if !found {
		return nil, types.ErrTrainingTaskNotFound
	}

	task.Assignees = msg.Assignees

	k.SetTrainingTask(ctx, task)

	return &types.MsgAssignTrainingTaskResponse{}, nil
}
