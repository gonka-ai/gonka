// Package v0_2_13 holds the upgrade handler for the v0.2.13 release.
//
// At branch-creation time this is a minimal scaffold: capability-version
// fix + RunMigrations. As state-affecting features land for v0.2.13,
// add small single-purpose migration steps below the capability fix and
// above RunMigrations, in dependency order. Each step should:
//
//   - Be a top-level function in this file (or a sibling _migrations file
//     for the larger multi-step ones, like v0.2.12 does).
//   - Read state, mutate, write state. Avoid stashing state in package
//     globals or carrying it across step boundaries.
//   - Return an error and let the handler bubble it up. A failed migration
//     halts the chain at the upgrade height; that's the correct behavior.
//   - Have its own unit test in upgrades_test.go.
//
// If the migration touches the InferenceModule's state in a way that needs
// a ConsensusVersion bump, also:
//   - Bump InferenceModule.ConsensusVersion() in x/inference/module/module.go.
//   - Register a corresponding migration in app/upgrades.go's
//     registerMigrations() (the pattern there is `RegisterMigration(name,
//     fromVersion, handler)` registering N → N+1).
//
// If a new keeper is needed (BlsKeeper, AuthzKeeper, FeeGrantKeeper, etc.),
// add it to CreateUpgradeHandler's signature and thread it through the
// SetUpgradeHandler call in app/upgrades.go.
package v0_2_13

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

		// Capability module's version is sometimes missing from the version
		// map. Set it explicitly so RunMigrations doesn't re-run InitGenesis
		// on a chain where capability state already exists.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// === migration steps land below this line as v0.2.13 features merge ===

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}
