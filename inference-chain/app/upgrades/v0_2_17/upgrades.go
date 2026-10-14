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

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.17 migration steps land below this line.
		if err := migrateLegacyPruningLimits(ctx, k); err != nil {
			return fromVM, err
		}
		if err := cleanupLeftoverState(ctx, k); err != nil {
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
// InferencePruningMax / PocPruningMax, so chains could reach v0.2.17 with the
// values stored as 0 — in which case every pruner is a no-op and pruning never
// drains. Positive custom values are preserved, so the migration is idempotent.
//
// This lived in the v0.2.16 handler while the pruning fix targeted that
// release (#1773). v0.2.16 has since executed, so the step moves here to
// actually run.
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

// cleanupLeftoverState removes state that is no longer read by any code path:
// the legacy EpochGroupValidations map (migrated to per-inference entries, then
// dropped for epochs older than the previous one), TopMiners, the abandoned
// training subsystem, and the pre-v2 PoC prefixes.
//
// Originally written for the v0.2.14 handler, then moved to the v0.2.16 handler
// (#1287) because mainnet upgraded past v0.2.14 before it landed. v0.2.16 has
// since executed, so it moves here to actually run.
//
// Every step is a bounded Clear/Delete over prefixes measured at ~0 bytes on
// mainnet by `inferenced state-stats --legacy-only`, so this stays cheap enough
// for an upgrade handler. The large prefixes (InferenceValidationDetails,
// developer-stats) are deliberately not touched here: deletion is expensive in
// Cosmos SDK 0.53.3. They are handled by gradual per-block pruning instead
// (#1499 and #1793, both on top of #1773).
func cleanupLeftoverState(ctx context.Context, k keeper.Keeper) error {
	k.LogInfo("cleaning up leftover state", types.Upgrades, "version", UpgradeName)

	if err := k.MigrateEpochGroupValidationsToEntries(ctx); err != nil {
		return err
	}
	if err := k.TopMiners.Clear(ctx, nil); err != nil {
		return err
	}
	if err := k.ClearTrainingState(ctx); err != nil {
		return err
	}
	if err := k.ClearLegacyPoCv2Data(ctx); err != nil {
		return err
	}

	k.LogInfo("finished cleaning up leftover state", types.Upgrades, "version", UpgradeName)
	return nil
}
