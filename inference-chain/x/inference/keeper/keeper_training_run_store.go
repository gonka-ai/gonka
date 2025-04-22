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

func NewKeeperTrainingRunStore(keeper Keeper) *TrainingRunStore {
	return &TrainingRunStore{
		keeper: keeper,
	}
}

func (k *TrainingRunStore) GetRunState(ctx context.Context, runId uint64) (*types.TrainingTask, error) {
	trainingTask, found := k.keeper.GetTrainingTask(sdk.UnwrapSDKContext(ctx), runId)
	if !found {
		return nil, nil
	}
	return trainingTask, nil
}

func (k *TrainingRunStore) SaveRunState(ctx context.Context, state *types.TrainingTask) error {
	k.keeper.SetTrainingTask(sdk.UnwrapSDKContext(ctx), state)
	return nil
}

func (k *TrainingRunStore) GetEpochState(ctx context.Context, runId uint64, epoch int32) (*training.EpochState, error) {
	hehe, err := k.keeper.GetTrainingTaskNodeActivityAtEpoch(sdk.UnwrapSDKContext(ctx), runId, epoch)
	if err != nil {
		return nil, err
	}

	epochState := &training.EpochState{
		Epoch:    epoch,
		Activity: hehe,
	}
	return epochState, nil
}

func (k *TrainingRunStore) SaveEpochState(ctx context.Context, runId uint64, epoch int32, state *training.EpochState) error {
	//TODO implement me
	panic("implement me")
}

func (k *TrainingRunStore) GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, participant string, nodeId string) (*types.TrainingTaskNodeEpochActivity, error) {
	activity, found := k.keeper.GetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), runId, epoch, participant, nodeId)
	if !found {
		return nil, fmt.Errorf("activity not found for runId: %d, epoch: %d, participant: %s, nodeId: %s", runId, epoch, participant, nodeId)
	}
	return activity, nil
}

func (k *TrainingRunStore) SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity) {
	k.keeper.SetTrainingTaskNodeEpochActivity(sdk.UnwrapSDKContext(ctx), activity)
}
