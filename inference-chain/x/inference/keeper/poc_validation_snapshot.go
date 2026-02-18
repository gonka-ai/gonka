package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPoCValidationSnapshot(ctx context.Context, snapshot types.PoCValidationSnapshot) error {
	return k.PoCValidationSnapshots.Set(ctx, snapshot.PocStageStartHeight, snapshot)
}

func (k Keeper) GetPoCValidationSnapshot(ctx context.Context, pocStageStartHeight int64) (types.PoCValidationSnapshot, bool, error) {
	snapshot, err := k.PoCValidationSnapshots.Get(ctx, pocStageStartHeight)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PoCValidationSnapshot{}, false, nil
		}
		return types.PoCValidationSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (k Keeper) DeletePoCValidationSnapshot(ctx context.Context, pocStageStartHeight int64) error {
	return k.PoCValidationSnapshots.Remove(ctx, pocStageStartHeight)
}
