// Package v0_2_16 holds the upgrade handler scaffold for the v0.2.16 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_16

import (
	"context"
	"fmt"
	"slices"

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
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.16 migration steps land below this line.
		if err := migrateDynamicCoefficientParams(ctx, k); err != nil {
			return fromVM, err
		}
		if err := migrateCurrentEffectiveCoefficients(ctx, k); err != nil {
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

func migrateDynamicCoefficientParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return nil
	}
	if params.PocParams.DynamicCoefficientParams != nil {
		changed := params.PocParams.WeightScaleFactor != nil
		params.PocParams.WeightScaleFactor = nil
		for _, model := range params.PocParams.Models {
			if model != nil && model.WeightScaleFactor != nil {
				model.WeightScaleFactor = nil
				changed = true
			}
		}
		if changed {
			return k.SetParams(ctx, params)
		}
		return nil
	}

	enabled := make([]string, 0, len(params.PocParams.Models))
	modelByID := make(map[string]*types.PoCModelConfig)
	for _, model := range params.PocParams.Models {
		if model == nil || model.ModelId == "" {
			continue
		}
		canonical, err := canonicalMigrationDecimal(model.WeightScaleFactor)
		if err != nil {
			return fmt.Errorf("dynamic coefficient migration model %q: %w", model.ModelId, err)
		}
		legacy, err := canonical.ToLegacyDec()
		if err != nil {
			return fmt.Errorf("dynamic coefficient migration model %q: %w", model.ModelId, err)
		}
		if !legacy.IsPositive() {
			model.DynamicCoefficient = nil
			model.WeightScaleFactor = nil
			continue
		}
		model.WeightScaleFactor = canonical
		enabled = append(enabled, model.ModelId)
		modelByID[model.ModelId] = model
	}
	if len(enabled) == 0 || len(enabled) > 5000 {
		return nil
	}
	slices.Sort(enabled)

	baseTarget := uint32(10000 / len(enabled))
	if baseTarget < 2 {
		return nil
	}
	remainder := uint32(10000 % len(enabled))
	remainderModel := enabled[0]
	if params.DelegationParams != nil {
		initial := params.DelegationParams.InitialModelId
		if _, ok := modelByID[initial]; ok {
			remainderModel = initial
		}
	}

	smallestTarget := baseTarget
	for _, modelID := range enabled {
		target := baseTarget
		if modelID == remainderModel {
			target += remainder
		}
		model := modelByID[modelID]
		legacyScale := cloneMigrationDecimal(model.WeightScaleFactor)
		model.DynamicCoefficient = &types.DynamicCoefficientModelConfig{
			CoeffMin:           cloneMigrationDecimal(legacyScale),
			CoeffMax:           cloneMigrationDecimal(legacyScale),
			RelativeDifficulty: &types.Decimal{Value: 1, Exponent: 0},
			TargetShareBps:     target,
		}
		model.WeightScaleFactor = nil
	}
	targetZone := uint32(500)
	if smallestTarget <= targetZone {
		targetZone = smallestTarget - 1
	}
	params.PocParams.DynamicCoefficientParams = &types.DynamicCoefficientParams{
		TargetZoneBps:     targetZone,
		StepMin:           &types.Decimal{Value: 5, Exponent: -3},
		StepMax:           &types.Decimal{Value: 5, Exponent: -2},
		BootstrapStepMax:  &types.Decimal{Value: 25, Exponent: -2},
		BootstrapShareBps: 100,
	}
	params.PocParams.WeightScaleFactor = nil
	if err := params.Validate(); err != nil {
		return fmt.Errorf("dynamic coefficient migration produced invalid params: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated dynamic coefficient params", types.Upgrades,
		"enabled_models", len(enabled),
		"target_zone_bps", targetZone)
	return nil
}

func canonicalMigrationDecimal(value *types.Decimal) (*types.Decimal, error) {
	if value == nil {
		return &types.Decimal{Value: 1, Exponent: 0}, nil
	}
	coefficient := value.Value
	exponent := value.Exponent
	for coefficient != 0 && coefficient%10 == 0 {
		coefficient /= 10
		exponent++
	}
	if exponent < -12 {
		return nil, fmt.Errorf("weight_scale_factor must be exactly representable with at most 12 fractional decimal places")
	}
	return &types.Decimal{Value: coefficient, Exponent: exponent}, nil
}

func cloneMigrationDecimal(value *types.Decimal) *types.Decimal {
	if value == nil {
		return nil
	}
	return &types.Decimal{Value: value.Value, Exponent: value.Exponent}
}

func migrateCurrentEffectiveCoefficients(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}
	data, found, err := k.GetEpochGroupDataWithError(ctx, epochIndex, "")
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	changed := false
	for _, scale := range data.ConfirmationWeightScales {
		if scale == nil || scale.EffectiveCoefficient != nil || scale.WeightScaleFactor == nil {
			continue
		}
		scale.EffectiveCoefficient = cloneMigrationDecimal(scale.WeightScaleFactor)
		scale.WeightScaleFactor = nil
		changed = true
	}
	if changed {
		k.SetEpochGroupData(ctx, data)
	}
	return nil
}
