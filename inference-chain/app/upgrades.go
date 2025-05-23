//go:build !upgraded

package app

import (
	v1_1 "github.com/productscience/inference/app/upgrades/v1_1"
	v2 "github.com/productscience/inference/app/upgrades/v2"
)

func (app *App) setupUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(v2.UpgradeName, v2.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v1_1.UpgradeName, v1_1.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
}
