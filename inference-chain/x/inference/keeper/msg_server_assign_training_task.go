package keeper

import (
	"context"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AssignTrainingTask(goCtx context.Context, msg *types.MsgAssignTrainingTask) (*types.MsgAssignTrainingTaskResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	err := k.StartTask(ctx, msg.TaskId, msg.Assignees)
	if err != nil {
		k.LogError("MsgAssignTrainingTask: failed to StartTask", types.Training, "error", err)
		return nil, err
	}

	k.LogInfo("MsgAssignTrainingTask: task assigned and started, emitting training_task_assigned event", types.Training, "taskId", msg.TaskId, "assignees", msg.Assignees)
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"training_task_assigned",
			sdk.NewAttribute("task_id", strconv.FormatUint(msg.TaskId, 10)),
		),
	)

	return &types.MsgAssignTrainingTaskResponse{}, nil
}
