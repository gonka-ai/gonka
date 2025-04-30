package v0_0_2_alpha6

import (
	"context"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		// This was needed only as a trigger to upgrade the API binaries
		//return mm.RunMigrations(ctx, configurator, vm)
		return vm, nil
	}
}
