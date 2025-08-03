package bls

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		panic(err)
	}

	// Set the active epoch ID from genesis
	k.SetActiveEpochID(ctx, genState.ActiveEpochId)
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	genesis.Params = k.GetParams(ctx)

	// Export the current active epoch ID
	activeEpochID, found := k.GetActiveEpochID(ctx)
	if found {
		genesis.ActiveEpochId = activeEpochID
	}

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
