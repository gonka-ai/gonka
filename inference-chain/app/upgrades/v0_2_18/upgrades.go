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

		// v0.2.16 already moved approved names into the store and then ran, so
		// it cannot stamp policy rows. Names approved before pass_count
		// (mainnet v4.1 / v4.2) stay DERIVED.
		if err := stampUnpoliciedApprovedVersionsDerived(ctx, k); err != nil {
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

// stampUnpoliciedApprovedVersionsDerived writes a DERIVED policy for every
// approved name that has none. Names that already have a policy are left
// unchanged, including their approved-version row.
func stampUnpoliciedApprovedVersionsDerived(ctx context.Context, k keeper.Keeper) error {
	versions, err := k.GetApprovedVersions(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, v := range versions {
		_, ok, err := k.GetVersionPolicy(ctx, v.Name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		v.PassCount = types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED
		if err := k.SetVersionPolicy(ctx, types.DevshardVersionPolicy{Name: v.Name, PassCount: v.PassCount}); err != nil {
			return err
		}
		if err := k.SetApprovedVersion(ctx, v); err != nil {
			return err
		}
		n++
	}
	k.LogInfo("stamped unpolicied approved devshard versions as derived", types.Upgrades, "count", n)
	return nil
}
