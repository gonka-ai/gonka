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
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger
		BankKeeper   types.BookkeepingBankKeeper
		BankView     types.BankKeeper
		validatorSet types.ValidatorSet
		group        types.GroupMessageKeeper
		Staking      types.StakingKeeper
		BlsKeeper    types.BlsKeeper
		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority     string
		AccountKeeper types.AccountKeeper
		AuthzKeeper   types.AuthzKeeper
		getWasmKeeper func() wasmkeeper.Keeper `optional:"true"`

		collateralKeeper    types.CollateralKeeper
		streamvestingKeeper types.StreamVestingKeeper
		Participants        collections.Map[sdk.AccAddress, types.Participant]
		RandomSeeds         collections.Map[collections.Pair[uint64, sdk.AccAddress], types.RandomSeed]
		PoCBatches          collections.Map[collections.Triple[int64, sdk.AccAddress, string], types.PoCBatch]
		PoCValidations      collections.Map[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidation]
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
		Epochs                   collections.Map[uint64, types.Epoch]
		EffectiveEpochIndex      collections.Item[uint64]
		EpochGroupValidationsMap collections.Map[collections.Pair[uint64, string], types.EpochGroupValidations]
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
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	sb := collections.NewSchemaBuilder(storeService)

	return Keeper{
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
	}
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
