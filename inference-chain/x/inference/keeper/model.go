package keeper

import (
	"context"
	"sort"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetModel(ctx context.Context, model *types.Model) {
	// store value via collections map (keyed by model.Id)
	_ = k.Models.Set(ctx, model.Id, *model)
}

func (k Keeper) DeleteGovernanceModel(ctx context.Context, id string) {
	_ = k.Models.Remove(ctx, id)
}

func (k Keeper) GetGovernanceModel(ctx context.Context, id string) (*types.Model, bool) {
	v, err := k.Models.Get(ctx, id)
	if err != nil {
		return nil, false
	}
	return &v, true
}

func (k Keeper) GetGovernanceModels(ctx context.Context) ([]*types.Model, error) {
	iter, err := k.Models.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	out := make([]*types.Model, 0, len(vals))
	for i := range vals {
		m := vals[i] // ensure distinct address
		out = append(out, &m)
	}
	return out, nil
}

func (k Keeper) GetGovernanceModelsSorted(ctx context.Context) ([]*types.Model, error) {
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		return nil, err
	}
	// iteration over collections.StringKey is already sorted by key, but keep explicit sort for safety
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].Id < models[j].Id
	})
	return models, nil
}

func (k Keeper) IsValidGovernanceModel(ctx context.Context, id string) bool {
	ok, err := k.Models.Has(ctx, id)
	return err == nil && ok
}
