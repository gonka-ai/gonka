package v1_14

import (
	"context"
	"fmt"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		{
			for moduleName, version := range vm {
				fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
			}
			fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)

			// Set defaults for new parameters
			params := k.GetParams(ctx)
			params.ValidationParams.TimestampAdvance = 30
			params.ValidationParams.TimestampExpiration = 60
			err := k.SetParams(ctx, params)
			if err != nil {
				k.LogError("Failed to set params during upgrade: %v", types.Upgrades, "error", err)
			}

			// For some reason, the capability module doesn't have a version set, but it DOES exist, causing
			// the `InitGenesis` to panic.
			if _, ok := vm["capability"]; !ok {
				vm["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
			}
			return mm.RunMigrations(ctx, configurator, vm)
		}
	}
}
