//go:build !upgraded

package app

import (
	"context"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	districutiontypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/app/upgrades/v0_2_10"
	v0_2_2 "github.com/productscience/inference/app/upgrades/v0_2_2"
	v0_2_3 "github.com/productscience/inference/app/upgrades/v0_2_3"
	"github.com/productscience/inference/app/upgrades/v0_2_4"
	"github.com/productscience/inference/app/upgrades/v0_2_5"
	"github.com/productscience/inference/app/upgrades/v0_2_6"
	"github.com/productscience/inference/app/upgrades/v0_2_7"
	"github.com/productscience/inference/app/upgrades/v0_2_8"
	"github.com/productscience/inference/app/upgrades/v0_2_9"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func CreateEmptyUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {

		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)

		// For some reason, the capability module doesn't have a version set, but it DOES exist, causing
		// the `InitGenesis` to panic.
		if _, ok := vm["capability"]; !ok {
			vm["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}
		return mm.RunMigrations(ctx, configurator, vm)
	}
}

func (app *App) setupUpgradeHandlers() {
	app.Logger().Info("Setting up upgrade handlers")
	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		app.Logger().Error("Failed to read upgrade info from disk", "error", err)
		return
	}
	app.Logger().Info("Applying upgrade", "upgradeInfo", upgradeInfo)

	app.UpgradeKeeper.SetUpgradeHandler(v0_2_2.UpgradeName, v0_2_2.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_3.UpgradeName, v0_2_3.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_4.UpgradeName, v0_2_4.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_5.UpgradeName, v0_2_5.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper, app.BlsKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_6.UpgradeName, v0_2_6.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper, app.DistrKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_7.UpgradeName, v0_2_7.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper, app.DistrKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_8.UpgradeName, v0_2_8.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper, app.BlsKeeper, app.DistrKeeper, app.AuthzKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_9.UpgradeName, v0_2_9.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_2_10.UpgradeName, v0_2_10.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
}

func (app *App) registerMigrations() {
	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 4, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 5, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 6, func(ctx sdk.Context) error {
		return nil
	})

	// v0.2.5 upgrade migrations
	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 7, func(ctx sdk.Context) error {
		if err := app.InferenceKeeper.MigrateLegacyBridgeState(ctx); err != nil {
			return err
		}
		return app.InferenceKeeper.MigrateConfirmationWeights(ctx)
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 8, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 9, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 10, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 11, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(districutiontypes.ModuleName, 3, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(slashingtypes.ModuleName, 4, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(stakingtypes.ModuleName, 5, func(ctx sdk.Context) error {
		return nil
	})

	app.Configurator().RegisterMigration(inferencetypes.ModuleName, 11, func(ctx sdk.Context) error {
		return nil
	})
}
