package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
)

type TrainingRunStore struct {
	keeper Keeper
}

func NewKeeperTrainingRunStore(keeper Keeper) *TrainingRunStore {
	return &TrainingRunStore{
		keeper: keeper,
	}
}

func (k *TrainingRunStore) GetRunState(ctx context.Context, runId string) (*training.RunState, error) {
	//TODO implement me
	panic("implement me")
}

func (k *TrainingRunStore) SaveRunState(ctx context.Context, runId string, state *training.RunState) error {
	//TODO implement me
	panic("implement me")
}

func (k *TrainingRunStore) GetEpochState(ctx context.Context, runId string, epoch int) (*training.EpochState, error) {
	//TODO implement me
	panic("implement me")
}

func (k *TrainingRunStore) SaveEpochState(ctx context.Context, runId string, epoch int, state *training.EpochState) error {
	//TODO implement me
	panic("implement me")
}
