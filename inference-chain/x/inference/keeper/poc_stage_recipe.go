package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetPocStageRecipe(ctx context.Context, stageHeight int64) (types.PocStageRecipe, bool, error) {
	recipe, err := k.PocStageRecipes.Get(ctx, stageHeight)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PocStageRecipe{}, false, nil
		}
		return types.PocStageRecipe{}, false, err
	}
	return recipe, true, nil
}

// FreezePocStageRecipe writes the live params snapshot for this stage height once.
// A later MsgUpdateParams must not change an in-flight stage.
func (k Keeper) FreezePocStageRecipe(ctx context.Context, stageHeight int64, event *types.ConfirmationPoCEvent) (*types.PocStageRecipe, error) {
	existing, found, err := k.GetPocStageRecipe(ctx, stageHeight)
	if err != nil {
		return nil, err
	}
	if found {
		return &existing, nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	epoch, _ := k.GetEffectiveEpochIndex(ctx)
	graceEpoch, graceFound := k.GetPocSchemeEnabledEpoch(ctx)
	recipe := types.SnapshotPocStageRecipe(params.PocParams, event, stageHeight, epoch, graceEpoch, graceFound)
	if err := k.PocStageRecipes.Set(ctx, stageHeight, *recipe); err != nil {
		return nil, err
	}
	return recipe, nil
}
