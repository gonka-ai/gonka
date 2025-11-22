package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type (
	Keeper struct {
		cdc           codec.BinaryCodec
		storeService  store.KVStoreService
		logger        log.Logger
		BankKeeper    types.BookkeepingBankKeeper
		BankView      types.BankKeeper
		validatorSet  types.ValidatorSet
		group         types.GroupMessageKeeper
		Staking       types.StakingKeeper
		BlsKeeper     types.BlsKeeper
		UpgradeKeeper types.UpgradeKeeper
		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority     string
		AccountKeeper types.AccountKeeper
		AuthzKeeper   types.AuthzKeeper
		getWasmKeeper func() wasmkeeper.Keeper `optional:"true"`

		collateralKeeper    types.CollateralKeeper
		streamvestingKeeper types.StreamVestingKeeper
		// Collections schema and stores
		Schema         collections.Schema
		Participants   collections.Map[sdk.AccAddress, types.Participant]
		RandomSeeds    collections.Map[collections.Pair[uint64, sdk.AccAddress], types.RandomSeed]
		PoCBatches     collections.Map[collections.Triple[int64, sdk.AccAddress, string], types.PoCBatch]
		PoCValidations collections.Map[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation]
		// Dynamic pricing collections
		ModelCurrentPriceMap collections.Map[string, uint64]
		ModelCapacityMap     collections.Map[string, uint64]
		// Governance models
		Models                        collections.Map[string, types.Model]
		Inferences                    collections.Map[string, types.Inference]
		InferenceTimeouts             collections.Map[collections.Pair[uint64, string], types.InferenceTimeout]
		InferenceValidationDetailsMap collections.Map[collections.Pair[uint64, string], types.InferenceValidationDetails]
		UnitOfComputePriceProposals   collections.Map[string, types.UnitOfComputePriceProposal]
		EpochGroupDataMap             collections.Map[collections.Pair[uint64, string], types.EpochGroupData]
		// Epoch collections
		Epochs                         collections.Map[uint64, types.Epoch]
		EffectiveEpochIndex            collections.Item[uint64]
		EpochGroupValidationsMap       collections.Map[collections.Pair[uint64, string], types.EpochGroupValidations]
		SettleAmounts                  collections.Map[sdk.AccAddress, types.SettleAmount]
		TopMiners                      collections.Map[sdk.AccAddress, types.TopMiner]
		PartialUpgrades                collections.Map[uint64, types.PartialUpgrade]
		EpochPerformanceSummaries      collections.Map[collections.Pair[sdk.AccAddress, uint64], types.EpochPerformanceSummary]
		TrainingExecAllowListSet       collections.KeySet[sdk.AccAddress]
		TrainingStartAllowListSet      collections.KeySet[sdk.AccAddress]
		PruningState                   collections.Item[types.PruningState]
		InferencesToPrune              collections.Map[collections.Pair[int64, string], collections.NoValue]
		ActiveInvalidations            collections.KeySet[collections.Pair[sdk.AccAddress, string]]
		ExcludedParticipantsMap        collections.Map[collections.Pair[uint64, sdk.AccAddress], types.ExcludedParticipant]
		// Confirmation PoC collections
		ConfirmationPoCEvents          collections.Map[collections.Pair[uint64, uint64], types.ConfirmationPoCEvent]
		ActiveConfirmationPoCEventItem collections.Item[types.ConfirmationPoCEvent]
		LastUpgradeHeight              collections.Item[int64]
		// Bridge & Wrapped Token collections
		BridgeContractAddresses        collections.Map[collections.Pair[string, string], types.BridgeContractAddress]
		BridgeTransactionsMap          collections.Map[collections.Triple[string, string, string], types.BridgeTransaction]
		WrappedTokenCodeIDItem         collections.Item[uint64]
		WrappedTokenMetadataMap        collections.Map[collections.Pair[string, string], types.BridgeTokenMetadata]
		WrappedTokenContractsMap       collections.Map[collections.Pair[string, string], types.BridgeWrappedTokenContract]
		WrappedContractReverseIndex    collections.Map[string, types.BridgeTokenReference]
		LiquidityPoolItem              collections.Item[types.LiquidityPool]
		LiquidityPoolApprovedTokensMap collections.Map[collections.Pair[string, string], types.BridgeTokenReference]
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,
	bank types.BookkeepingBankKeeper,
	bankView types.BankKeeper,
	group types.GroupMessageKeeper,
	validatorSet types.ValidatorSet,
	staking types.StakingKeeper,
	accountKeeper types.AccountKeeper,
	blsKeeper types.BlsKeeper,
	collateralKeeper types.CollateralKeeper,
	streamvestingKeeper types.StreamVestingKeeper,
	authzKeeper types.AuthzKeeper,
	getWasmKeeper func() wasmkeeper.Keeper,
	upgradeKeeper types.UpgradeKeeper,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		cdc:                 cdc,
		storeService:        storeService,
		authority:           authority,
		logger:              logger,
		BankKeeper:          bank,
		BankView:            bankView,
		group:               group,
		validatorSet:        validatorSet,
		Staking:             staking,
		AccountKeeper:       accountKeeper,
		AuthzKeeper:         authzKeeper,
		BlsKeeper:           blsKeeper,
		collateralKeeper:    collateralKeeper,
		streamvestingKeeper: streamvestingKeeper,
		getWasmKeeper:       getWasmKeeper,
		UpgradeKeeper:       upgradeKeeper,
		// collection init
		Participants: collections.NewMap(
			sb,
			types.ParticipantsPrefix,
			"participant",
			sdk.AccAddressKey,
			codec.CollValue[types.Participant](cdc),
		),
		RandomSeeds: collections.NewMap(
			sb,
			types.RandomSeedPrefix,
			"random_seed",
			collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
			codec.CollValue[types.RandomSeed](cdc),
		),
		PoCBatches: collections.NewMap(
			sb,
			types.PoCBatchPrefix,
			"poc_batch",
			collections.TripleKeyCodec(collections.Int64Key, sdk.AccAddressKey, collections.StringKey),
			codec.CollValue[types.PoCBatch](cdc),
		),
		PoCValidations: collections.NewMap(
			sb,
			types.PoCValidationPref,
			"poc_validation",
			collections.TripleKeyCodec(collections.Int64Key, sdk.AccAddressKey, sdk.AccAddressKey),
			codec.CollValue[types.PoCValidation](cdc),
		),
		// dynamic pricing collections
		ModelCurrentPriceMap: collections.NewMap(
			sb,
			types.DynamicPricingCurrentPrefix,
			"model_current_price",
			collections.StringKey,
			collections.Uint64Value,
		),
		ModelCapacityMap: collections.NewMap(
			sb,
			types.DynamicPricingCapacityPrefix,
			"model_capacity",
			collections.StringKey,
			collections.Uint64Value,
		),
		// governance models map
		Models: collections.NewMap(
			sb,
			types.ModelsPrefix,
			"models",
			collections.StringKey,
			codec.CollValue[types.Model](cdc),
		),
		// inferences map
		Inferences: collections.NewMap(
			sb,
			types.InferencesPrefix,
			"inferences",
			collections.StringKey,
			codec.CollValue[types.Inference](cdc),
		),
		// unit of compute price proposals map
		UnitOfComputePriceProposals: collections.NewMap(
			sb,
			types.UnitOfComputePriceProposalPrefix,
			"unit_of_compute_price_proposals",
			collections.StringKey,
			codec.CollValue[types.UnitOfComputePriceProposal](cdc),
		),
		InferenceValidationDetailsMap: collections.NewMap(
			sb,
			types.InferenceValidationDetailsPrefix,
			"inference_validation_details",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey),
			codec.CollValue[types.InferenceValidationDetails](cdc),
		),
		InferenceTimeouts: collections.NewMap(
			sb,
			types.InferenceTimeoutPrefix,
			"inference_timeout",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey),
			codec.CollValue[types.InferenceTimeout](cdc),
		),
		EpochGroupDataMap: collections.NewMap(
			sb,
			types.EpochGroupDataPrefix,
			"epoch_group_data",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey),
			codec.CollValue[types.EpochGroupData](cdc),
		),
		// Epoch collections wiring
		Epochs: collections.NewMap(
			sb,
			types.EpochsPrefix,
			"epochs",
			collections.Uint64Key,
			codec.CollValue[types.Epoch](cdc),
		),
		EffectiveEpochIndex: collections.NewItem(
			sb,
			types.EffectiveEpochIndexPrefix,
			"effective_epoch_index",
			collections.Uint64Value,
		),
		EpochGroupValidationsMap: collections.NewMap(
			sb,
			types.EpochGroupValidationsPrefix,
			"epoch_group_validations",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey),
			codec.CollValue[types.EpochGroupValidations](cdc),
		),
		SettleAmounts: collections.NewMap(
			sb,
			types.SettleAmountPrefix,
			"settle_amount",
			sdk.AccAddressKey,
			codec.CollValue[types.SettleAmount](cdc),
		),
		TopMiners: collections.NewMap(
			sb,
			types.TopMinerPrefix,
			"top_miner",
			sdk.AccAddressKey,
			codec.CollValue[types.TopMiner](cdc),
		),
		PartialUpgrades: collections.NewMap(
			sb,
			types.PartialUpgradePrefix,
			"partial_upgrade",
			collections.Uint64Key,
			codec.CollValue[types.PartialUpgrade](cdc),
		),
		EpochPerformanceSummaries: collections.NewMap(
			sb,
			types.EpochPerformanceSummaryPrefix,
			"epoch_performance_summary",
			collections.PairKeyCodec(sdk.AccAddressKey, collections.Uint64Key),
			codec.CollValue[types.EpochPerformanceSummary](cdc),
		),
		TrainingExecAllowListSet: collections.NewKeySet(
			sb,
			types.TrainingExecAllowListPrefix,
			"training_exec_allow_list",
			sdk.AccAddressKey,
		),
		TrainingStartAllowListSet: collections.NewKeySet(
			sb,
			types.TrainingStartAllowListPrefix,
			"training_start_allow_list",
			sdk.AccAddressKey,
		),
		PruningState: collections.NewItem(
			sb,
			types.PruningStatePrefix,
			"pruning_state",
			codec.CollValue[types.PruningState](cdc),
		),
		InferencesToPrune: collections.NewMap(
			sb,
			types.InferencesToPrunePrefix,
			"inferences_to_prune",
			collections.PairKeyCodec(collections.Int64Key, collections.StringKey),
			collections.NoValue{},
		),
		ActiveInvalidations: collections.NewKeySet(
			sb,
			types.ActiveInvalidationsPrefix,
			"active_invalidations",
			collections.PairKeyCodec(sdk.AccAddressKey, collections.StringKey),
		),
		ExcludedParticipantsMap: collections.NewMap(
			sb,
			types.ExcludedParticipantsPrefix,
			"excluded_participants",
			collections.PairKeyCodec(collections.Uint64Key, sdk.AccAddressKey),
			codec.CollValue[types.ExcludedParticipant](cdc),
		),
		ConfirmationPoCEvents: collections.NewMap(
			sb,
			types.ConfirmationPoCEventsPrefix,
			"confirmation_poc_events",
			collections.PairKeyCodec(collections.Uint64Key, collections.Uint64Key),
			codec.CollValue[types.ConfirmationPoCEvent](cdc),
		),
		ActiveConfirmationPoCEventItem: collections.NewItem(
			sb,
			types.ActiveConfirmationPoCEventPrefix,
			"active_confirmation_poc_event",
			codec.CollValue[types.ConfirmationPoCEvent](cdc),
		),
		LastUpgradeHeight: collections.NewItem(
			sb,
			types.LastUpgradeHeightPrefix,
			"last_upgrade_height",
			collections.Int64Value,
		),
		BridgeContractAddresses: collections.NewMap(
			sb,
			types.BridgeContractAddressesPrefix,
			"bridge_contract_addresses",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey),
			codec.CollValue[types.BridgeContractAddress](cdc),
		),
		BridgeTransactionsMap: collections.NewMap(
			sb,
			types.BridgeTransactionsPrefix,
			"bridge_transactions",
			collections.TripleKeyCodec(collections.StringKey, collections.StringKey, collections.StringKey),
			codec.CollValue[types.BridgeTransaction](cdc),
		),
		WrappedTokenMetadataMap: collections.NewMap(
			sb,
			types.WrappedTokenMetadataPrefix,
			"bridge_token_metadata",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey),
			codec.CollValue[types.BridgeTokenMetadata](cdc),
		),
		WrappedTokenContractsMap: collections.NewMap(
			sb,
			types.WrappedTokenContractsPrefix,
			"bridge_wrapped_token_contracts",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey),
			codec.CollValue[types.BridgeWrappedTokenContract](cdc),
		),
		WrappedContractReverseIndex: collections.NewMap(
			sb,
			types.WrappedContractReverseIndexPrefix,
			"wrapped_contract_reverse_index",
			collections.StringKey,
			codec.CollValue[types.BridgeTokenReference](cdc),
		),
		LiquidityPoolApprovedTokensMap: collections.NewMap(
			sb,
			types.LiquidityPoolApprovedTokensPrefix,
			"bridge_trade_approved_tokens",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey),
			codec.CollValue[types.BridgeTokenReference](cdc),
		),
		WrappedTokenCodeIDItem: collections.NewItem(
			sb,
			types.WrappedTokenCodeIDPrefix,
			"wrapped_token_code_id",
			collections.Uint64Value,
		),
		LiquidityPoolItem: collections.NewItem(
			sb,
			types.LiquidityPoolPrefix,
			"liquidity_pool",
			codec.CollValue[types.LiquidityPool](cdc),
		),
	}
	// Build the collections schema
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// GetWasmKeeper returns the WASM keeper
func (k Keeper) GetWasmKeeper() wasmkeeper.Keeper {
	return k.getWasmKeeper()
}

// GetCollateralKeeper returns the collateral keeper.
func (k Keeper) GetCollateralKeeper() types.CollateralKeeper {
	return k.collateralKeeper
}

// GetStreamVestingKeeper returns the streamvesting keeper.
func (k Keeper) GetStreamVestingKeeper() types.StreamVestingKeeper {
	return k.streamvestingKeeper
}

// Logger returns a module-specific logger.
func (k Keeper) Logger() log.Logger {
	return k.logger.With("module", fmt.Sprintf("x/%s", types.ModuleName))
}

func (k Keeper) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Info(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Error(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Warn(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogDebug(msg string, subSystem types.SubSystem, keyVals ...interface{}) {
	k.Logger().Debug(msg, append(keyVals, "subsystem", subSystem.String())...)
}

// Codec returns the binary codec used by the keeper.
func (k Keeper) Codec() codec.BinaryCodec {
	return k.cdc
}

type EntryType int

const (
	Debit EntryType = iota
	Credit
)

func (e EntryType) String() string {
	switch e {
	case Debit:
		return "debit"
	case Credit:
		return "credit"
	default:
		return "unknown"
	}
}
