package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetTrainingTaskNodeActivityAtEpoch(ctx sdk.Context, taskId uint64, epoch int32) ([]*types.TrainingTaskNodeEpochActivity, error) {
	return GetAllValues(ctx, &k, types.TrainingTaskNodeEpochActivityEpochPrefix(taskId, epoch), func() *types.TrainingTaskNodeEpochActivity {
		return &types.TrainingTaskNodeEpochActivity{}
	})
}

func (k Keeper) SetTrainingTaskNodeEpochActivity(ctx sdk.Context, activity *types.TrainingTaskNodeEpochActivity) {
	SetValue(k, ctx, activity, []byte{}, types.TrainingTaskNodeEpochActivityKey(activity.TaskId, activity.Heartbeat.OuterStep, activity.Participant, activity.NodeId))
}

func (k Keeper) GetTrainingTaskNodeEpochActivity(ctx sdk.Context, taskId uint64, epoch int32, participant string, nodeId string) (*types.TrainingTaskNodeEpochActivity, bool) {
	activity := types.TrainingTaskNodeEpochActivity{}
	return GetValue(&k, ctx, &activity, []byte{}, types.TrainingTaskNodeEpochActivityKey(taskId, epoch, participant, nodeId))
}
