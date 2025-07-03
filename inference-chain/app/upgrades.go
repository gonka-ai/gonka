//go:build !upgraded

package app

import (
	storetypes "cosmossdk.io/store/types"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	v0_1_4 "github.com/productscience/inference/app/upgrades/v0.1.4"
	"github.com/productscience/inference/app/upgrades/v1_1"
	"github.com/productscience/inference/app/upgrades/v1_10"
	"github.com/productscience/inference/app/upgrades/v1_8"
	"github.com/productscience/inference/app/upgrades/v1_9"
	v2 "github.com/productscience/inference/app/upgrades/v2"
)

func (app *App) setupUpgradeHandlers() {
	app.Logger().Info("Setting up upgrade handlers")
	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		app.Logger().Error("Failed to read upgrade info from disk", "error", err)
		return
	}
	if upgradeInfo.Name == v1_10.UpgradeName {
		storeUpgrades := storetypes.StoreUpgrades{
			Added: []string{
				"wasm",
			},
		}
		app.SetStoreLoader(upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &storeUpgrades))
	}
	app.UpgradeKeeper.SetUpgradeHandler(v2.UpgradeName, v2.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v1_1.UpgradeName, v1_1.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v0_1_4.UpgradeName, v0_1_4.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v1_8.UpgradeName, v1_8.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v1_8.UpgradeNameRestart, v1_8.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v1_9.UpgradeName, v1_9.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v1_10.UpgradeName, v1_10.CreateUpgradeHandler(app.InferenceKeeper))
}
