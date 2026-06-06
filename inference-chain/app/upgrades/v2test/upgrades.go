package v2test

import (
	"context"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		_ = mm
		_ = configurator
		_ = ctx
		_ = plan
		// Literally do nothing for the test upgrade
		//return mm.RunMigrations(ctx, configurator, vm)
		return vm, nil
	}
}
