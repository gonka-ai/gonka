package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

func (app *App) withLastUpgradeHeight(next upgradetypes.UpgradeHandler) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		outVM, err := next(ctx, plan, vm)
		if err != nil {
			return outVM, err
		}

		if err := app.InferenceKeeper.SetLastUpgradeHeight(ctx, plan.Height); err != nil {
			app.Logger().Error("Failed to set last upgrade height from upgrade handler",
				"height", plan.Height,
				"name", plan.Name,
				"error", err,
			)
			return outVM, err
		}

		app.Logger().Info("Recorded last upgrade height from upgrade handler",
			"height", plan.Height,
			"name", plan.Name,
		)

		return outVM, nil
	}
}

func (app *App) setTrackedUpgradeHandler(name string, handler upgradetypes.UpgradeHandler) {
	app.UpgradeKeeper.SetUpgradeHandler(name, app.withLastUpgradeHeight(handler))
}
