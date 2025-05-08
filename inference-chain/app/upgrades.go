//go:build !upgraded

package app

import (
	v2 "github.com/productscience/inference/app/upgrades/v2"
)

func (app *App) setupUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(v2.UpgradeName, v2.CreateUpgradeHandler(app.ModuleManager, app.Configurator(), app.InferenceKeeper))
}
