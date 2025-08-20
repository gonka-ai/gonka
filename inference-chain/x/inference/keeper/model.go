package keeper

import (
	"context"
	"sort"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetModel(ctx context.Context, model *types.Model) {
	SetValue(k, ctx, model, types.KeyPrefix(types.ModelKeyPrefix), types.ModelKey(model.Id))
}

func (k Keeper) DeleteGovernanceModel(ctx context.Context, id string) {
	DeleteValue(&k, ctx, types.KeyPrefix(types.ModelKeyPrefix), types.ModelKey(id))
}

func (k Keeper) GetGovernanceModel(ctx context.Context, id string) (*types.Model, bool) {
	return GetValue(&k, ctx, &types.Model{}, types.KeyPrefix(types.ModelKeyPrefix), types.ModelKey(id))
}

func (k Keeper) GetGovernanceModels(ctx context.Context) ([]*types.Model, error) {
	return GetAllValues(ctx, &k, types.KeyPrefix(types.ModelKeyPrefix), func() *types.Model { return &types.Model{} })
}

func (k Keeper) GetGovernanceModelsSorted(ctx context.Context) ([]*types.Model, error) {
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].Id < models[j].Id
	})
	return models, nil
}

func (k Keeper) IsValidGovernanceModel(ctx context.Context, id string) bool {
	_, found := k.GetGovernanceModel(ctx, id)
	return found
}
