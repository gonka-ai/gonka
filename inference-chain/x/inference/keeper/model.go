package keeper

import (
	"context"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetModel(ctx context.Context, model *types.Model) {
	SetValue(k, ctx, model, types.KeyPrefix(types.ModelKeyPrefix), types.ModelKey(model.Id))
}

func (k Keeper) GetAllModels(ctx context.Context) ([]*types.Model, error) {
	store := PrefixStore(ctx, k, types.KeyPrefix(types.ModelKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	var models []*types.Model
	for ; iterator.Valid(); iterator.Next() {
		value := iterator.Value()

		var model types.Model
		if err := k.cdc.Unmarshal(value, &model); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Model: %w", err)
		}

		models = append(models, &model)
	}

	return models, nil
}
