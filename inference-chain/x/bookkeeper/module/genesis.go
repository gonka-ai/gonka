package bookkeeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bookkeeper/keeper"
	"github.com/productscience/inference/x/bookkeeper/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	params, err := k.GetParams(ctx)
	if err != nil {
		//nolint:forbidigo // Genesis/Export code
		panic(err)
	}
	genesis.Params = params

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
