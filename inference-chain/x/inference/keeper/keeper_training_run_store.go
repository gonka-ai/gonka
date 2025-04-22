package keeper

import (
	"context"
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
	//TODO implement me
	panic("implement me")
}

func (k *TrainingRunStore) SaveEpochState(ctx context.Context, runId uint64, epoch int32, state *training.EpochState) error {
	//TODO implement me
	panic("implement me")
}
