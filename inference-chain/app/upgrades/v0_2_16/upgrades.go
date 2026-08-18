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

		if err := cleanupLeftoverState(ctx, k); err != nil {
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

// cleanupLeftoverState removes state that is no longer read by any code path:
// the legacy EpochGroupValidations map (migrated to per-inference entries, then
// dropped for epochs older than the previous one), TopMiners, the abandoned
// training subsystem, and the pre-v2 PoC prefixes.
//
// Originally written for the v0.2.14 handler. Mainnet upgraded past v0.2.14
// before this landed, so that handler can no longer run there — it moved here to
// ship with v0.2.16 (see #1287).
//
// Every step is a bounded Clear/Delete over prefixes measured at ~0 bytes on
// mainnet by `inferenced state-stats --legacy-only`, so this stays cheap enough
// for an upgrade handler. The large prefixes (InferenceValidationDetails,
// developer-stats) are deliberately not touched here: deletion is expensive in
// Cosmos SDK 0.53.3, so they are bled off gradually by the per-block pruner
// instead (#1499).
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
