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

	InitHoldingAccounts(ctx, k, genState)

	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		panic(err)
	}

}

func InitHoldingAccounts(ctx sdk.Context, k keeper.Keeper, state types.GenesisState) {

	supplyDenom := state.GenesisOnlyParams.SupplyDenom

	// Ensures creation if not already existing
	k.AccountKeeper.GetModuleAccount(ctx, types.StandardRewardPoolAccName)
	k.AccountKeeper.GetModuleAccount(ctx, types.TopRewardPoolAccName)
	k.AccountKeeper.GetModuleAccount(ctx, types.PreProgrammedSaleAccName)

	standardRewardCoins := sdk.NewCoins(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.StandardRewardAmount))
	topRewardCoins := sdk.NewCoins(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.TopRewardAmount))
	preProgrammedSaleCoins := sdk.NewCoins(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.PreProgrammedSaleAmount))

	if err := k.BankKeeper.MintCoins(ctx, types.StandardRewardPoolAccName, standardRewardCoins); err != nil {
		panic(err)
	}
	if err := k.BankKeeper.MintCoins(ctx, types.TopRewardPoolAccName, topRewardCoins); err != nil {
		panic(err)
	}
	if err := k.BankKeeper.MintCoins(ctx, types.PreProgrammedSaleAccName, preProgrammedSaleCoins); err != nil {
		panic(err)
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
	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
