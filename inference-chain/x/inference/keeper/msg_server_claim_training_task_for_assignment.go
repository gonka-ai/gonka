package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// FIXME: move to chain params?
// Number of blocks an assinger has to finish the assignment process
const TrainingTaskAssignmentDeadline = 100

// TODO: ideally this should check that one participant can't claim more than 1 task at a time
func (k msgServer) ClaimTrainingTaskForAssignment(goCtx context.Context, msg *types.MsgClaimTrainingTaskForAssignment) (*types.MsgClaimTrainingTaskForAssignmentResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	task, found := k.GetTrainingTask(ctx, msg.TaskId)
	if !found {
		return nil, types.ErrTrainingTaskNotFound
	}

	if task.AssignedAtBlockHeight != 0 {
		return nil, types.ErrTrainingTaskAlreadyAssigned
	}

	blockHeight := uint64(ctx.BlockHeight())
	blocksSinceAssignment := task.ClaimedByAssignerAtBlockHeight - blockHeight
	if task.Assigner != "" && blocksSinceAssignment < TrainingTaskAssignmentDeadline {
		return nil, types.ErrTrainingTaskAlreadyAssigned
	}

	task.Assigner = msg.Creator
	task.ClaimedByAssignerAtBlockHeight = blockHeight
	k.SetTrainingTask(ctx, task)

	return &types.MsgClaimTrainingTaskForAssignmentResponse{}, nil
}
