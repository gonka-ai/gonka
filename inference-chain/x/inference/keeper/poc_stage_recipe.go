package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetPocStageRecipe(ctx context.Context, stageHeight int64) (types.PocStageRecipe, bool, error) {
	recipe, found, err := k.getRegularPocStageRecipe(ctx, stageHeight)
	if err != nil || found {
		return recipe, found, err
	}
	event, active, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return types.PocStageRecipe{}, false, err
	}
	if active && event.Recipe != nil && event.Recipe.StageHeight == stageHeight {
		return *event.Recipe, true, nil
	}
	return types.PocStageRecipe{}, false, nil
}

func (k Keeper) getRegularPocStageRecipe(ctx context.Context, stageHeight int64) (types.PocStageRecipe, bool, error) {
	for _, slot := range []collections.Item[types.PocStageRecipe]{k.LatestPocStageRecipe, k.RetainedPocStageRecipe} {
		recipe, err := slot.Get(ctx)
		if errors.Is(err, collections.ErrNotFound) {
			continue
		}
		if err != nil {
			return types.PocStageRecipe{}, false, err
		}
		if recipe.StageHeight == stageHeight {
			return recipe, true, nil
		}
	}
	return types.PocStageRecipe{}, false, nil
}

// FreezePocStageRecipe freezes the parameters for an in-flight regular stage.
// A later MsgUpdateParams must not change an in-flight stage.
func (k Keeper) FreezePocStageRecipe(ctx context.Context, stageHeight int64) (*types.PocStageRecipe, error) {
	existing, found, err := k.getRegularPocStageRecipe(ctx, stageHeight)
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
	// Preserve the effective measurement before replacing the latest attempt.
	// Failed rounds may keep that same measurement alive for many epochs.
	hasLatest, err := k.LatestPocStageRecipe.Has(ctx)
	if err != nil {
		return nil, err
	}
	if hasLatest {
		epoch, ok := k.GetEffectiveEpoch(ctx)
		if !ok || epoch == nil {
			return nil, fmt.Errorf("cannot retain PoC recipe: effective epoch not found")
		}
		active, found, err := k.GetEpochPocRecipe(ctx, epoch.Index)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("cannot retain PoC recipe for effective epoch %d", epoch.Index)
		}
		if err := k.RetainedPocStageRecipe.Set(ctx, active); err != nil {
			return nil, err
		}
	}
	if err := k.LatestPocStageRecipe.Set(ctx, *recipe); err != nil {
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
	return k.getRegularPocStageRecipe(ctx, height)
}
