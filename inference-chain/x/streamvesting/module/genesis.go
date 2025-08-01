package streamvesting

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/streamvesting/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// Set all the vesting schedules
	for _, elem := range genState.VestingScheduleList {
		k.SetVestingSchedule(ctx, elem)
	}

	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		panic(err)
	}
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	genesis.Params = k.GetParams(ctx)

	// Export all vesting schedules
	vestingSchedules := k.GetAllVestingSchedules(ctx)
	genesis.VestingScheduleList = vestingSchedules

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
