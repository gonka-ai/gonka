package inference

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

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

	// Set if defined
	if genState.TokenomicsData != nil {
		k.SetTokenomicsData(ctx, *genState.TokenomicsData)
	}

	k.SetGenesisOnlyParams(ctx, &genState.GenesisOnlyParams)

	// Set all the topMiner
	for _, elem := range genState.TopMinerList {
		k.SetTopMiner(ctx, elem)
	}
	// Set all the inferenceTimeout
	for _, elem := range genState.InferenceTimeoutList {
		k.SetInferenceTimeout(ctx, elem)
	}
	// Set all the inferenceValidationDetails
	for _, elem := range genState.InferenceValidationDetailsList {
		k.SetInferenceValidationDetails(ctx, elem)
	}
	// Set all the epochPerformanceSummary
	for _, elem := range genState.EpochPerformanceSummaryList {
		k.SetEpochPerformanceSummary(ctx, elem)
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

func InitHoldingAccounts(ctx sdk.Context, k keeper.Keeper, state types.GenesisState) {

	supplyDenom := state.GenesisOnlyParams.SupplyDenom
	denomMetadata, found := k.BankKeeper.GetDenomMetaData(ctx, types.BaseCoin)
	if !found {
		panic("BaseCoin denom not found")
	}

	err := LoadMetadataToSdk(denomMetadata)
	if err != nil {
		panic(err)
	}

	// Ensures creation if not already existing
	k.AccountKeeper.GetModuleAccount(ctx, types.TopRewardPoolAccName)
	k.AccountKeeper.GetModuleAccount(ctx, types.PreProgrammedSaleAccName)

	topRewardCoin := sdk.NormalizeCoin(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.TopRewardAmount))
	preProgrammedCoin := sdk.NormalizeCoin(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.PreProgrammedSaleAmount))

	if err := k.BankKeeper.MintCoins(ctx, types.TopRewardPoolAccName, sdk.NewCoins(topRewardCoin)); err != nil {
		panic(err)
	}
	if err := k.BankKeeper.MintCoins(ctx, types.PreProgrammedSaleAccName, sdk.NewCoins(preProgrammedCoin)); err != nil {
		panic(err)
	}
}

func LoadMetadataToSdk(metadata banktypes.Metadata) error {
	for _, denom := range metadata.DenomUnits {
		err := sdk.RegisterDenom(denom.Denom, math.LegacyNewDec(10).Power(uint64(denom.Exponent)))
		if err != nil {
			return err
		}
	}
	err := sdk.SetBaseDenom(metadata.Base)
	if err != nil {
		return err
	}
	return nil
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
	// Get all tokenomicsData
	tokenomicsData, found := k.GetTokenomicsData(ctx)
	if found {
		genesis.TokenomicsData = &tokenomicsData
	}
	genesisOnlyParams, found := k.GetGenesisOnlyParams(ctx)
	if found {
		genesis.GenesisOnlyParams = genesisOnlyParams
	}
	genesis.ModelList = getModels(&ctx, &k)
	genesis.TopMinerList = k.GetAllTopMiner(ctx)
	genesis.InferenceTimeoutList = k.GetAllInferenceTimeout(ctx)
	genesis.InferenceValidationDetailsList = k.GetAllInferenceValidationDetails(ctx)
	genesis.EpochPerformanceSummaryList = k.GetAllEpochPerformanceSummary(ctx)
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
