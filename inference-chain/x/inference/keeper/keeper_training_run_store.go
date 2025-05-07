package keeper

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
)

type TrainingRunStore struct {
	keeper Keeper
}

var _ training.RunStore = (*TrainingRunStore)(nil)

func NewKeeperTrainingRunStore(keeper Keeper) *TrainingRunStore {
	return &TrainingRunStore{
		keeper: keeper,
	}
}

func (k *TrainingRunStore) GetRunState(ctx context.Context, runId uint64) *types.TrainingTask {
	task, found := k.keeper.GetTrainingTask(sdk.UnwrapSDKContext(ctx), runId)
	if !found {
		return nil
	} else {
		if task == nil {
			panic("keeper.GetTrainingTask: task = nil. found = true")
		}

		return task
	}
}

func (k *TrainingRunStore) SaveRunState(ctx context.Context, state *types.TrainingTask) error {
	k.keeper.SetTrainingTask(sdk.UnwrapSDKContext(ctx), state)
	return nil
}

func (k *TrainingRunStore) GetEpochState(ctx context.Context, runId uint64, epoch int32) ([]*types.TrainingTaskNodeEpochActivity, error) {
	activity, err := k.keeper.GetTrainingTaskNodeActivityAtEpoch(sdk.UnwrapSDKContext(ctx), runId, epoch)
	if err != nil {
		return nil, err
	}

	return activity, nil
}

func (k *TrainingRunStore) SaveEpochState(ctx context.Context, state []*types.TrainingTaskNodeEpochActivity) error {
	if len(state) == 0 {
		return nil
	}

	outerStep := state[0].Heartbeat.OuterStep
	runId := state[0].TaskId

	for _, activity := range state {
		if activity.Heartbeat.OuterStep != outerStep {
			return fmt.Errorf("invalid OuterStep %d, expected %d", activity.Heartbeat.OuterStep, outerStep)
		}
		if activity.TaskId != runId {
			return fmt.Errorf("invalid run id %d, expected %d", activity.TaskId, runId)
		}
		k.keeper.SetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), activity)
	}
	return nil
}

func (k *TrainingRunStore) GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, nodeId training.GlobalNodeId) *types.TrainingTaskNodeEpochActivity {
	activity, found := k.keeper.GetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), runId, epoch, nodeId.Participant, nodeId.LocalNodeId)
	if !found {
		return nil
	} else {
		if activity == nil {
			panic("GetParticipantActivity: nil training task node epoch activity")
		}

		return activity
	}
}

func (k *TrainingRunStore) SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity) {
	k.keeper.SetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), activity)
}

func (k *TrainingRunStore) SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier) {
	k.keeper.SetTrainingBarrier(sdk.UnwrapSDKContext(ctx), barrier)
}

func (k *TrainingRunStore) GetBarrierEpochStatus(ctx context.Context, key types.TrainingTaskBarrierEpochKey) ([]*types.TrainingTaskBarrier, error) {
	return k.keeper.GetTrainingBarrierForEpoch(sdk.UnwrapSDKContext(ctx), key)
}
