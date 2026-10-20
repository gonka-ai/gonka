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
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type AuthzMigrationKeeper interface {
	IterateGrants(ctx context.Context, handler func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool)
	GetAuthorization(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time)
	SaveGrant(ctx context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	authzKeeper AuthzMigrationKeeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.17 migration steps land below this line.
		if err := backfillTrainingParamDefaults(ctx, k); err != nil {
			return fromVM, err
		}
		if err := backfillTrainshardNodeStatuses(ctx, k); err != nil {
			return fromVM, err
		}
		if err := repairTrainshardReservations(ctx, k); err != nil {
			return fromVM, err
		}
		if err := backfillTrainshardWeightEpochs(ctx, k); err != nil {
			return fromVM, err
		}
		if err := clearTrainshardAutokickRequests(ctx, k); err != nil {
			return fromVM, err
		}
		if err := clearStaleTrainingOptIns(ctx, k); err != nil {
			return fromVM, err
		}
		if err := grantTrainingWarmKeyAuthz(ctx, authzKeeper, k); err != nil {
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
