// Package v0_2_18 holds the upgrade handler scaffold for the v0.2.18 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_18

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

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.18 migration steps land below this line.
		if err := migrateLegacyPruningLimits(ctx, k); err != nil {
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

// migrateLegacyPruningLimits backfills non-positive stored pruning limits with
// their defaults. DefaultEpochParams originally carried no values for
// InferencePruningMax / PocPruningMax, so chains could reach v0.2.18 with the
// values stored as 0 — in which case every pruner is a no-op and pruning never
// drains. Positive custom values are preserved, so the migration is idempotent.
//
// This lived in the v0.2.16 handler, then the v0.2.17 one, while the pruning
// fix targeted those releases. It now ships with v0.2.18, so the step lives here.
func migrateLegacyPruningLimits(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	defaults := types.DefaultEpochParams()
	if params.EpochParams == nil {
		params.EpochParams = defaults
		return k.SetParams(ctx, params)
	}

	changed := false
	if params.EpochParams.InferencePruningMax <= 0 {
		params.EpochParams.InferencePruningMax = defaults.InferencePruningMax
		changed = true
	}
	if params.EpochParams.PocPruningMax <= 0 {
		params.EpochParams.PocPruningMax = defaults.PocPruningMax
		changed = true
	}
	if !changed {
		return nil
	}
	return k.SetParams(ctx, params)
}
