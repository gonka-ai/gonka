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
func (k Keeper) FreezePocStageRecipe(ctx context.Context, stageHeight int64) (*types.PocStageRecipe, error) {
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
	recipe, err := types.SnapshotPocStageRecipe(params.PocParams, stageHeight)
	if err != nil {
		return nil, err
	}
	if err := k.PocStageRecipes.Set(ctx, stageHeight, *recipe); err != nil {
		return nil, err
	}
	return recipe, nil
}

// GetEpochPocRecipe is the regular recipe behind an epoch's measured weights.
func (k Keeper) GetEpochPocRecipe(ctx context.Context, epochIndex uint64) (types.PocStageRecipe, bool, error) {
	epoch, found := k.GetEpoch(ctx, epochIndex)
	if !found || epoch == nil {
		return types.PocStageRecipe{}, false, nil
	}
	height := epoch.PocStartBlockHeight
	// A liveness fallback retains the measurement height of the carried weights.
	if participants, ok := k.GetActiveParticipants(ctx, epochIndex); ok {
		height = participants.PocStartBlockHeight
	}
	return k.GetPocStageRecipe(ctx, height)
}
