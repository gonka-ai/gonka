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

		// Existing chains have no epoch_validation_params field. Write the
		// admission defaults before the audit so this block reads the governed caps.
		if err := migrateEpochValidationParams(ctx, k); err != nil {
			return nil, err
		}

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Canonical-account validation finishes before any audit write. Distribution
		// and epoch-group rewrites then run before this block's epoch stage.
		if err := k.RepairEpochValidationState(ctx); err != nil {
			return nil, err
		}
		// Votes already stored have no signer counter. Seed counters only for
		// stages that can still accept votes. Finished confirmation triggers
		// are not scanned or deleted here; the bounded confirmation cleanup
		// walks them on later blocks.
		if err := k.RebuildPocValidationV2Counts(ctx); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

// migrateEpochValidationParams writes the admission caps when the stored
// params do not have them yet. A later governance value is left in place.
func migrateEpochValidationParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.EpochValidationParams != nil {
		return nil
	}
	params.EpochValidationParams = types.DefaultEpochValidationParams()
	return k.SetParams(ctx, params)
}
