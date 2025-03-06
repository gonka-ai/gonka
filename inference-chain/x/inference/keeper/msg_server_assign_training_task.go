package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AssignTrainingTask(goCtx context.Context, msg *types.MsgAssignTrainingTask) (*types.MsgAssignTrainingTaskResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	err := k.StartTask(ctx, msg.TaskId, msg.Assignees)
	if err != nil {
		return nil, err
	}

	return &types.MsgAssignTrainingTaskResponse{}, nil
}
