//go:build upgraded

package app

import (
	"github.com/productscience/inference/app/upgrades/v2test"
)

func (app *App) setupUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(v2test.UpgradeName, v2test.CreateUpgradeHandler(app.ModuleManager, app.Configurator()))
}
