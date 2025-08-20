//go:build upgraded

package app

import (
	v0_1_4 "github.com/productscience/inference/app/upgrades/v0.1.4"
	v2 "github.com/productscience/inference/app/upgrades/v2"
	"github.com/productscience/inference/app/upgrades/v2test"
)

func (app *App) setupUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(v2test.UpgradeName, v2test.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
	app.UpgradeKeeper.SetUpgradeHandler(v2.UpgradeName, v2.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
	app.UpgradeKeeper.SetUpgradeHandler(v0_1_4.UpgradeName, v0_1_4.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
}
