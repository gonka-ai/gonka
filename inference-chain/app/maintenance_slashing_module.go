package app

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/x/slashing"
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// maintenanceSlashingModule retains the SDK module's services, genesis,
// migrations and hooks, but uses the application's configured keeper for
// liveness accounting. The SDK wiring copies its keeper into AppModule before
// the application can install its maintenance checker after dependency injection.
type maintenanceSlashingModule struct {
	slashing.AppModule
	keeper *slashingkeeper.Keeper
}

func (m maintenanceSlashingModule) BeginBlock(ctx context.Context) error {
	return slashing.BeginBlocker(ctx, *m.keeper)
}

func (app *App) configureMaintenanceSlashingModule() error {
	sdkModule, ok := app.ModuleManager.Modules[slashingtypes.ModuleName].(slashing.AppModule)
	if !ok {
		return fmt.Errorf("unexpected slashing module type %T", app.ModuleManager.Modules[slashingtypes.ModuleName])
	}
	app.ModuleManager.Modules[slashingtypes.ModuleName] = maintenanceSlashingModule{
		AppModule: sdkModule,
		keeper:    &app.SlashingKeeper,
	}
	return nil
}
