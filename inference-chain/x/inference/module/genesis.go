package inference

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// Set all the inference
	for _, elem := range genState.InferenceList {
		k.SetInference(ctx, elem)
	}
	// Set all the participant
	for _, elem := range genState.ParticipantList {
		k.SetParticipant(ctx, elem)
	}
	// PRTODO: set active participants here, but how?
	// Set all the epochGroupData
	for _, elem := range genState.EpochGroupDataList {
		k.SetEpochGroupData(ctx, elem)
	}
	// Set all the settleAmount
	for _, elem := range genState.SettleAmountList {
		k.SetSettleAmount(ctx, elem)
	}
	// Set all the epochGroupValidations
	for _, elem := range genState.EpochGroupValidationsList {
		k.SetEpochGroupValidations(ctx, elem)
	}
	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		panic(err)
	}
	for _, elem := range genState.ModelList {
		if elem.ProposedBy != "genesis" {
			panic("At genesis all model.ProposedBy are expected to be \"genesis\".")
		}

		elem.ProposedBy = k.GetAuthority()
		k.SetModel(ctx, &elem)
	}
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	genesis.Params = k.GetParams(ctx)

	genesis.InferenceList = k.GetAllInference(ctx)
	genesis.ParticipantList = k.GetAllParticipant(ctx)
	genesis.EpochGroupDataList = k.GetAllEpochGroupData(ctx)
	genesis.SettleAmountList = k.GetAllSettleAmount(ctx)
	genesis.EpochGroupValidationsList = k.GetAllEpochGroupValidations(ctx)
	genesis.ModelList = getModels(&ctx, &k)
	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}

func getModels(ctx *sdk.Context, k *keeper.Keeper) []types.Model {
	models, err := k.GetAllModels(ctx)
	if err != nil {
		panic(err)
	}
	models2, err := keeper.PointersToValues(models)
	if err != nil {
		panic(err)
	}
	return models2
}
