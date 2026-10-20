// Package v0_2_17 prepares the transitional PREFILL and permanent DECODE recipes.
//
// Moves flat recipes and dynamic coefficients into PREFILL blocks, adds DECODE
// templates, and preserves poc_scheme. Pre-v0.2.16 static weight factors remain
// on the model.
package v0_2_17

import (
	"context"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := migratePocSchemeBlocks(ctx, k); err != nil {
			return fromVM, err
		}
		if err := freezeActiveEpochRecipe(ctx, k); err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func freezeActiveEpochRecipe(ctx context.Context, k keeper.Keeper) error {
	epoch, found := k.GetEffectiveEpoch(ctx)
	if !found || epoch == nil {
		return fmt.Errorf("cannot freeze upgrade recipe: effective epoch not found")
	}
	height := epoch.PocStartBlockHeight
	if participants, ok := k.GetActiveParticipants(ctx, epoch.Index); ok {
		height = participants.PocStartBlockHeight
	}
	_, err := k.FreezePocStageRecipe(ctx, height)
	return err
}

// migratePocSchemeBlocks moves flat fields into PREFILL blocks and clears them.
// The upgrade fails if any model cannot be converted.
func migratePocSchemeBlocks(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return nil
	}
	for _, model := range params.PocParams.Models {
		if model == nil {
			return fmt.Errorf("poc scheme migration: models contains a nil entry")
		}
		if model.SeqLen <= 0 {
			return fmt.Errorf("model %q has invalid prefill seq_len %d", model.ModelId, model.SeqLen)
		}
		model.Schemes = []*types.PocSchemeParams{
			{
				Scheme:             types.PocScheme_POC_SCHEME_PREFILL,
				SeqLen:             model.SeqLen,
				StatTest:           model.StatTest,
				DynamicCoefficient: model.DynamicCoefficient,
			},
			decodeScheme(model.ModelId),
		}
		model.SeqLen = 0
		model.StatTest = nil
		model.DynamicCoefficient = nil
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("poc scheme migration produced invalid params: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated prefill scheme blocks", types.Upgrades, "models", len(params.PocParams.Models))
	return nil
}
