package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetModel(ctx context.Context, model *types.Model) {
	SetValue(k, ctx, model, types.KeyPrefix(types.ModelKeyPrefix), types.ModelKey(model.Id))
}

func (k Keeper) GetAllModels(ctx context.Context) ([]*types.Model, error) {
	return GetAllValues(ctx, &k, types.KeyPrefix(types.ModelKeyPrefix), func() *types.Model { return &types.Model{} })
}
