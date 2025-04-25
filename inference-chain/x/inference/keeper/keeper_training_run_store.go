package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type TrainingRunStore struct {
	keeper Keeper
}

func NewKeeperTrainingRunStore(keeper Keeper) *TrainingRunStore {
	return &TrainingRunStore{
		keeper: keeper,
	}
}

func (k *TrainingRunStore) GetRunState(ctx context.Context, runId uint64) (*types.TrainingTask, bool) {
	return k.keeper.GetTrainingTask(sdk.UnwrapSDKContext(ctx), runId)
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

func (k *TrainingRunStore) SaveEpochState(ctx context.Context, state []*types.TrainingTaskNodeEpochActivity) {
	if len(state) == 0 {
		return
	}

	epochId := state[0].Epoch
	runId := state[0].TaskId

	for _, activity := range state {
		if activity.Epoch != epochId {
			panic("Epoch ID mismatch")
		}
		if activity.TaskId != runId {
			panic("Run ID mismatch")
		}
		k.keeper.SetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), activity)
	}
}

func (k *TrainingRunStore) GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, participant string, nodeId string) (*types.TrainingTaskNodeEpochActivity, bool) {
	return k.keeper.GetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), runId, epoch, participant, nodeId)
}

func (k *TrainingRunStore) SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity) {
	k.keeper.SetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), activity)
}

func (k *TrainingRunStore) SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier) {
	k.keeper.SetTrainingBarrier(sdk.UnwrapSDKContext(ctx), barrier)
}
