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
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}

	// Set the active epoch ID from genesis
	k.SetActiveEpochID(ctx, genState.ActiveEpochId)

	// Set the current signing epoch ID from genesis if it's set
	if genState.CurrentSigningEpochId > 0 {
		k.SetCurrentSigningEpochID(ctx, genState.CurrentSigningEpochId)
	}

	// Set all the loaded entity models
	k.SetAllEpochBLSData(ctx, genState.BlsDataList)
	k.SetAllThresholdSigningRequests(ctx, genState.SigningRequests)
	k.SetAllGroupKeyValidationStates(ctx, genState.GroupValidationStates)
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

	// Export the current active epoch ID
	activeEpochID, found := k.GetActiveEpochID(ctx)
	if found {
		genesis.ActiveEpochId = activeEpochID
	}

	// Export the current signing epoch ID
	currentSigningEpochID, foundSigning := k.GetCurrentSigningEpochID(ctx)
	if foundSigning {
		genesis.CurrentSigningEpochId = currentSigningEpochID
	}

	// Export all entity models
	genesis.BlsDataList = k.GetAllEpochBLSData(ctx)
	genesis.SigningRequests = k.GetAllThresholdSigningRequests(ctx)
	genesis.GroupValidationStates = k.GetAllGroupKeyValidationStates(ctx)

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
