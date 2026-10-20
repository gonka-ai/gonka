// Package v0_2_17 holds the upgrade handler scaffold for the v0.2.17 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
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
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.17 migration steps land below this line.
		if err := migratePoCMinCommitCount(ctx, k); err != nil {
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

const pocMinCommitCount uint32 = 200

func migratePoCMinCommitCount(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	minCount := pocMinCommitCount
	if sampleSize := params.PocParams.EffectiveValidationSampleSize(); sampleSize > 0 && sampleSize < int64(minCount) {
		minCount = uint32(sampleSize)
	}
	updated := 0
	for _, model := range params.PocParams.GetModelConfigs() {
		if model == nil || model.GetStatTest().GetMinCount() != 0 {
			continue
		}
		if model.StatTest == nil {
			model.StatTest = &types.PoCStatTestParams{}
		}
		model.StatTest.MinCount = minCount
		updated++
	}
	if updated == 0 {
		return nil
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("min commit count migration produced invalid params: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated PoC min commit count", types.Upgrades,
		"min_count", minCount,
		"models", updated)
	return nil
}
