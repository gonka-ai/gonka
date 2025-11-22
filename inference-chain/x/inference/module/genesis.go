package inference

import (
	"log"
	"strings"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/productscience/inference/x/inference/epochgroup"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// IgnoreDuplicateDenomRegistration can be toggled by tests to suppress the
// "denom already registered" error that arises from the Cosmos-SDK's global
// denom registry when multiple tests within the same process call InitGenesis.
//
// In production code this flag MUST remain false so that duplicate
// registrations still panic.
var IgnoreDuplicateDenomRegistration bool

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// Observability: start of InitGenesis
	k.LogInfo("InitGenesis: starting module genesis", types.System)
	InitGenesisEpoch(ctx, k)

	InitHoldingAccounts(ctx, k, genState)

	// Init empty TokenomicsData
	k.SetTokenomicsData(ctx, types.TokenomicsData{})
	err := k.PruningState.Set(ctx, types.PruningState{})
	if err != nil {
		panic(err)
	}

	// Set MLNode version with default if not defined
	if genState.MlnodeVersion != nil {
		k.SetMLNodeVersion(ctx, *genState.MlnodeVersion)
	} else {
		// Set default MLNode version
		k.SetMLNodeVersion(ctx, types.MLNodeVersion{CurrentVersion: "v3.0.8"})
	}

	// Set genesis only params from configuration
	genesisOnlyParams := genState.GenesisOnlyParams
	if len(genesisOnlyParams.GenesisGuardianAddresses) > 0 {
		k.LogInfo("Using configured genesis guardian addresses", types.System, "addresses", genesisOnlyParams.GenesisGuardianAddresses, "count", len(genesisOnlyParams.GenesisGuardianAddresses))
	} else {
		k.LogInfo("No genesis guardian addresses configured - genesis guardian enhancement will be disabled", types.System)
	}

	k.SetGenesisOnlyParams(ctx, &genesisOnlyParams)

	// Import participants provided in genesis
	for _, p := range genState.ParticipantList {
		err := k.SetParticipant(ctx, p)
		if err != nil {
			k.LogWarn("Error importing participant", types.System, "error", err, "participant", p)
		}
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

	// Set all bridge contract addresses from genesis
	if genState.Bridge != nil {
		for _, elem := range genState.Bridge.ContractAddresses {
			k.SetBridgeContractAddress(ctx, *elem)
		}

		// Set all bridge token metadata from genesis
		for _, elem := range genState.Bridge.TokenMetadata {
			k.SetTokenMetadata(ctx, elem.ChainId, elem.ContractAddress, keeper.TokenMetadata{
				Name:     elem.Name,
				Symbol:   elem.Symbol,
				Decimals: uint8(elem.Decimals),
			})
		}

		// Set all bridge trade approved tokens from genesis
		for _, elem := range genState.Bridge.TradeApprovedTokens {
			k.SetBridgeTradeApprovedToken(ctx, *elem)
		}
	}

	// Observability: end of InitGenesis
	k.LogInfo("InitGenesis: completed", types.System)

}

func InitGenesisEpoch(ctx sdk.Context, k keeper.Keeper) {
	genesisEpoch := &types.Epoch{
		Index:               0,
		PocStartBlockHeight: 0,
	}
	k.SetEpoch(ctx, genesisEpoch)
	k.SetEffectiveEpochIndex(ctx, genesisEpoch.Index)

	InitGenesisEpochGroup(ctx, k, uint64(genesisEpoch.PocStartBlockHeight))
}

func InitGenesisEpochGroup(ctx sdk.Context, k keeper.Keeper, pocStartBlockHeight uint64) {
	// Observability: creating initial epoch group
	k.LogInfo("[InitGenesisEpoch]: creating epoch group", types.EpochGroup, "pocStartBlockHeight", pocStartBlockHeight)

	epochGroup, err := k.CreateEpochGroup(ctx, pocStartBlockHeight, 0)
	if err != nil {
		log.Panicf("[InitGenesisEpoch] CreateEpochGroup failed. err = %v", err)
	}
	err = epochGroup.CreateGroup(ctx)
	if err != nil {
		log.Panicf("[InitGenesisEpoch] epochGroup.CreateGroup failed. err = %v", err)
	}

	// Fetch staking validators for logging and member addition
	k.LogInfo("[InitGenesisEpoch]: retrieving staking validators", types.EpochGroup)

	stakingValidators, err := k.Staking.GetAllValidators(ctx)
	if err != nil {
		log.Panicf("[InitGenesisEpoch] Staking.GetAllValidators failed. err = %v", err)
	}

	k.LogInfo("[InitGenesisEpoch]: staking validators retrieved", types.EpochGroup, "count", len(stakingValidators))

	if len(stakingValidators) == 0 {
		k.LogWarn("[InitGenesisEpoch]: no staking validators found", types.EpochGroup)
	}

	// Log the operator addresses of all validators to be added
	{
		addresses := make([]string, len(stakingValidators))
		for i, v := range stakingValidators {
			addresses[i] = v.OperatorAddress
		}
		if len(addresses) > 0 {
			k.LogInfo("[InitGenesisEpoch]: validator addresses", types.EpochGroup, "addresses", addresses)
		}
	}

	for _, validator := range stakingValidators {
		member, err := epochgroup.NewEpochMemberFromStakingValidator(validator)
		if err != nil || member == nil {
			log.Panicf("[InitGenesisEpoch] NewEpochMemberFromStakingValidator failed. err = %v", err)
		}
		k.LogInfo("[InitGenesisEpoch]: adding member to epoch group", types.EpochGroup,
			"member.Address", member.Address,
			"member.Weight", member.Weight,
			"member.Pubkey", member.Pubkey,
			"member.SeedSignature", member.SeedSignature,
			"member.Reputation", member.Reputation,
			"member.Models", member.Models)

		err = epochGroup.AddMember(ctx, *member)
		if err != nil {
			log.Panicf("[InitGenesisEpoch] epochGroup.AddMember failed. err = %v", err)
		}
	}

	err = epochGroup.MarkUnchanged(ctx)
	if err != nil {
		log.Panicf("[InitGenesisEpoch] epochGroup.MarkUnchanged failed. err = %v", err)
	}
}

func InitHoldingAccounts(ctx sdk.Context, k keeper.Keeper, state types.GenesisState) {

	supplyDenom := state.GenesisOnlyParams.SupplyDenom
	denomMetadata, found := k.BankView.GetDenomMetaData(ctx, types.BaseCoin)
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
	k.AccountKeeper.GetModuleAccount(ctx, types.BridgeEscrowAccName)

	topRewardCoin := sdk.NormalizeCoin(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.TopRewardAmount))
	preProgrammedCoin := sdk.NormalizeCoin(sdk.NewInt64Coin(supplyDenom, state.GenesisOnlyParams.PreProgrammedSaleAmount))

	if err := k.BankKeeper.MintCoins(ctx, types.TopRewardPoolAccName, sdk.NewCoins(topRewardCoin), "top_reward_pool init"); err != nil {
		panic(err)
	}
	if err := k.BankKeeper.MintCoins(ctx, types.PreProgrammedSaleAccName, sdk.NewCoins(preProgrammedCoin), "pre_programmed_coin_init"); err != nil {
		panic(err)
	}
}

func LoadMetadataToSdk(metadata banktypes.Metadata) error {
	// NOTE: sdk.RegisterDenom stores the mapping in a process-global registry.
	// This function is called in two places:
	// 1. During genesis initialization (InitHoldingAccounts)
	// 2. During app startup (app.initializeDenomMetadata in app.go)
	//
	// When several tests initialise the app within the same "go test" process
	// the same denom (nicoin/icoin/…) can be registered more than once and the
	// second attempt returns an error.  In production this situation should be
	// considered fatal, therefore we gate the duplicate-tolerant behaviour
	// behind a flag that tests can enable explicitly.

	for _, denom := range metadata.DenomUnits {
		err := sdk.RegisterDenom(denom.Denom, math.LegacyNewDec(10).Power(uint64(denom.Exponent)))
		if err != nil {
			if IgnoreDuplicateDenomRegistration && strings.Contains(err.Error(), "already registered") {
				// Skip duplicate error in test runs.
				continue
			}
			return err
		}
	}

	if err := sdk.SetBaseDenom(metadata.Base); err != nil {
		if IgnoreDuplicateDenomRegistration && strings.Contains(err.Error(), "already registered") {
			return nil
		}
		return err
	}
	return nil
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := &types.GenesisState{}
	genesis.Params = k.GetParams(ctx)

	genesisOnlyParams, found := k.GetGenesisOnlyParams(ctx)
	if found {
		genesis.GenesisOnlyParams = genesisOnlyParams
	}

	mlnodeVersion, found := k.GetMLNodeVersion(ctx)
	if found {
		genesis.MlnodeVersion = &mlnodeVersion
	}
	genesis.ModelList = getModels(&ctx, &k)
	// Export participants
	participants := k.GetAllParticipant(ctx)
	genesis.ParticipantList = participants

	// Export bridge data
	contractAddresses := k.GetAllBridgeContractAddresses(ctx)
	contractAddressPtrs := make([]*types.BridgeContractAddress, len(contractAddresses))
	for i := range contractAddresses {
		contractAddressPtrs[i] = &contractAddresses[i]
	}

	tokenMetadata := k.GetAllBridgeTokenMetadata(ctx)
	tokenMetadataPtrs := make([]*types.BridgeTokenMetadata, len(tokenMetadata))
	for i := range tokenMetadata {
		tokenMetadataPtrs[i] = &tokenMetadata[i]
	}

	tradeApprovedTokens := k.GetAllBridgeTradeApprovedTokens(ctx)
	tradeApprovedTokenPtrs := make([]*types.BridgeTokenReference, len(tradeApprovedTokens))
	for i := range tradeApprovedTokens {
		tradeApprovedTokenPtrs[i] = &tradeApprovedTokens[i]
	}

	genesis.Bridge = &types.Bridge{
		ContractAddresses:   contractAddressPtrs,
		TokenMetadata:       tokenMetadataPtrs,
		TradeApprovedTokens: tradeApprovedTokenPtrs,
	}
	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}

func getModels(ctx *sdk.Context, k *keeper.Keeper) []types.Model {
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		panic(err)
	}
	models2, err := keeper.PointersToValues(models)
	if err != nil {
		panic(err)
	}
	return models2
}
