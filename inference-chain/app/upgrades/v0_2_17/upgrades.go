// Package v0_2_17 moves each model onto a PREFILL scheme block.
//
// v0.2.16 has already run. It left seq_len and stat_test on the model, wrote
// dynamic_coefficient on the model, and cleared weight_scale_factor. This
// upgrade copies that recipe and that coefficient onto schemes[PREFILL], then
// clears the flat fields. It does not add a DECODE block and does not change
// poc_scheme, confirmation_poc_scheme, or confirmation_scheme_events.
//
// A chain that has not run v0.2.16 still has the flat recipe and the flat
// weight_scale_factor. The same handler copies the recipe and leaves that
// static factor on the model.
package v0_2_17

import (
	"context"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	gogoproto "github.com/cosmos/gogoproto/proto"

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

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

// migratePocSchemeBlocks copies the flat prefill recipe, and the v0.2.16
// model coefficient when one is present, onto schemes[PREFILL]. Flat seq_len,
// stat_test, and the model coefficient are cleared after the copy. A model
// that cannot be converted fails the upgrade.
func migratePocSchemeBlocks(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return nil
	}
	changed := false
	for _, model := range params.PocParams.Models {
		if model == nil {
			return fmt.Errorf("poc scheme migration: models contains a nil entry")
		}
		modelChanged, err := migrateModelScheme(model)
		if err != nil {
			return err
		}
		changed = changed || modelChanged
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("poc scheme migration produced invalid params: %w", err)
	}
	if !changed {
		return nil
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated prefill scheme blocks", types.Upgrades, "models", len(params.PocParams.Models))
	return nil
}

func migrateModelScheme(model *types.PoCModelConfig) (bool, error) {
	changed := false
	block := prefillBlock(model)
	if block == nil {
		if model.SeqLen <= 0 {
			return false, fmt.Errorf("model %q has no PREFILL scheme block and seq_len %d", model.ModelId, model.SeqLen)
		}
		block = &types.PocSchemeParams{
			Scheme:    types.PocScheme_POC_SCHEME_PREFILL,
			SeqLen:    model.SeqLen,
			MaxTokens: 0,
			StatTest:  cloneStatTest(model.StatTest),
		}
		model.Schemes = append([]*types.PocSchemeParams{block}, model.Schemes...)
		changed = true
	}
	if block.DynamicCoefficient != nil && model.DynamicCoefficient != nil &&
		!gogoproto.Equal(block.DynamicCoefficient, model.DynamicCoefficient) {
		return false, fmt.Errorf("model %q PREFILL dynamic_coefficient differs from the model field", model.ModelId)
	}
	if block.DynamicCoefficient == nil && model.DynamicCoefficient != nil {
		block.DynamicCoefficient = cloneDynamicCoefficient(model.DynamicCoefficient)
		changed = true
	}
	if model.SeqLen != 0 || model.StatTest != nil {
		model.SeqLen = 0
		model.StatTest = nil
		changed = true
	}
	if model.DynamicCoefficient != nil && block.DynamicCoefficient != nil {
		model.DynamicCoefficient = nil
		changed = true
	}
	return changed, nil
}

func prefillBlock(model *types.PoCModelConfig) *types.PocSchemeParams {
	for _, block := range model.Schemes {
		if block != nil && block.Scheme == types.PocScheme_POC_SCHEME_PREFILL {
			return block
		}
	}
	return nil
}

func cloneStatTest(stat *types.PoCStatTestParams) *types.PoCStatTestParams {
	if stat == nil {
		return nil
	}
	cloned := gogoproto.Clone(stat)
	return cloned.(*types.PoCStatTestParams)
}

func cloneDynamicCoefficient(config *types.DynamicCoefficientModelConfig) *types.DynamicCoefficientModelConfig {
	if config == nil {
		return nil
	}
	cloned := gogoproto.Clone(config)
	return cloned.(*types.DynamicCoefficientModelConfig)
}
