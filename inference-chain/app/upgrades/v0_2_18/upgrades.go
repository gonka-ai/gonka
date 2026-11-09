// Package v0_2_18 holds the upgrade handler for the v0.2.18 release.
//
// v0.2.18 schedules CometBFT vote extensions for the block BLS beacon (Step 1
// of devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
// Release precondition: validators holding well over 2/3 of voting power run
// tmkms 0.14.0 with sign_extensions = true (not 0.15.0, see tmkms#1174); any
// validator that does not panics from VoteExtensionsEnableHeight on.
//
// Add further migration steps below the capability fix and above
// RunMigrations. If later work bumps a module ConsensusVersion, it must also
// register the corresponding migration in app/upgrades.go's registerMigrations().
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
	consensusParams ConsensusParamsStore,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		enableHeight, changed, err := EnableVoteExtensions(ctx, consensusParams, VoteExtensionsEnableDelay)
		if err != nil {
			return fromVM, err
		}
		if changed {
			k.LogInfo("scheduled vote extensions", types.Upgrades, "enable_height", enableHeight)
		} else {
			k.LogInfo("vote extensions already scheduled; leaving consensus params unchanged", types.Upgrades, "enable_height", enableHeight)
		}

		// Future v0.2.18 migration steps land below this line.

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}
