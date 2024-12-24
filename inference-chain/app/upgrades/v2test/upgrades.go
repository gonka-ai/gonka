package v2test

import (
	"context"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"fmt"
	"github.com/cosmos/cosmos-sdk/types/module"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {

		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)
		// Literally do nothing for the test upgrade
		//return mm.RunMigrations(ctx, configurator, vm)
		return vm, nil
	}
}
