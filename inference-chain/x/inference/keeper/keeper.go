package keeper

import (
	"context"
	"fmt"
	"sync"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// epochGroupCacheKey keys the hot cache by (epochIndex, modelId).
type epochGroupCacheKey struct {
	Epoch   uint64
	ModelId string
}

// RevalidationEventInfo holds inference_id and validator for an inference_validation event with needs_revalidation=true.
type RevalidationEventInfo struct {
	InferenceId string
	Validator   string
}

// BlockRevalidationEventsProvider returns all inference_validation events with needs_revalidation=true
// for a given block height (e.g. from block results). The app can implement this by reading block
// results for the height and parsing events. When set on the keeper, ProcessPendingRevalidationEvents
// uses it in BeginBlock to get all events from the previous block (not only "locally initiated").
type BlockRevalidationEventsProvider interface {
	GetInferenceValidationRevalidationEvents(ctx context.Context, height int64) ([]RevalidationEventInfo, error)
}

// epochGroupCache holds EpochGroupData for current and previous effective epoch only.
// Cache is invalidated in PrepareForBlock each block.
type epochGroupCache struct {
	mu           sync.RWMutex
	inited       bool
	current      uint64
	previous     uint64
	currentDirty bool
	m            map[epochGroupCacheKey]types.EpochGroupData
}

// randomSeedCacheKey keys the warm cache by (epochIndex, participant). Participant is Bech32 string so the key is comparable.
type randomSeedCacheKey struct {
	Epoch       uint64
	Participant string
}

// randomSeedCache holds RandomSeed for current effective epoch only.
type randomSeedCache struct {
	mu      sync.RWMutex
	inited  bool
	current uint64
	m       map[randomSeedCacheKey]types.RandomSeed
}

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
		// PoC v2 collections
		PoCValidationsV2          collections.Map[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidationV2]
		PoCV2StoreCommits         collections.Map[collections.Pair[int64, sdk.AccAddress], types.PoCV2StoreCommit]
		MLNodeWeightDistributions collections.Map[collections.Pair[int64, sdk.AccAddress], types.MLNodeWeightDistribution]
		// Dynamic pricing collections
		ModelCurrentPriceMap collections.Map[string, uint64]
		ModelCapacityMap     collections.Map[string, uint64]
		// Governance models
		Models                                   collections.Map[string, types.Model]
		Inferences                               collections.Map[string, types.Inference]
		InferenceTimeouts                        collections.Map[collections.Pair[uint64, string], types.InferenceTimeout]
		InferenceValidationDetailsMap            collections.Map[collections.Pair[uint64, string], types.InferenceValidationDetails]
		InferenceRevalidations                   collections.Map[collections.Pair[string, string], types.RevalidationVoteRecord]
		InferenceRevalidationTotalEligibleWeight collections.Map[string, int64]
		UnitOfComputePriceProposals              collections.Map[string, types.UnitOfComputePriceProposal]
		EpochGroupDataMap                        collections.Map[collections.Pair[uint64, string], types.EpochGroupData]
		// EpochGroupData hot cache: current and previous effective epoch only; inited on first Get, refreshed on SetEffectiveEpochIndex.
		epochGroupCache *epochGroupCache
		// RandomSeed warm cache: current effective epoch only; inited on first Get, refreshed on SetEffectiveEpochIndex.
		randomSeedCache *randomSeedCache
		// Normalized weighted participants per block: blockHash -> BTree(cumulative weight -> address). Last NormalizedParticipantsCacheBlocks blocks.
		normalizedWeightedParticipants *normalizedWeightedParticipantsCache
		// Selected-to-vote participants per (blockHeight, inferenceId) with capped vote weights; evicted after NormalizedParticipantsCacheBlocks blocks.
		revalidationVoteParticipants *revalidationVoteParticipantsCache
		// When true, revalidation votes are stored in InferenceRevalidations (keeper); when false, in ephemeralRevalidationVotes (cleared after 300 blocks).
		storeRevalidationVotes bool
		// Ephemeral revalidation votes when storeRevalidationVotes is false; evicted after NormalizedParticipantsCacheBlocks blocks.
		ephemeralRevalidationVotes *ephemeralRevalidationVoteCache
		// Optional: provides all inference_validation events with needs_revalidation=true for a block (e.g. from block results).
		// When set, used in BeginBlock to get all events from the previous block; when nil, no revalidation hook is run.
		blockRevalidationEventsProvider BlockRevalidationEventsProvider
		// Epoch collections
		Epochs                    collections.Map[uint64, types.Epoch]
		EffectiveEpochIndex       collections.Item[uint64]
		EpochGroupValidationsMap  collections.Map[collections.Pair[uint64, string], types.EpochGroupValidations]
		SettleAmounts             collections.Map[sdk.AccAddress, types.SettleAmount]
		TopMiners                 collections.Map[sdk.AccAddress, types.TopMiner]
		PartialUpgrades           collections.Map[uint64, types.PartialUpgrade]
		EpochPerformanceSummaries collections.Map[collections.Pair[sdk.AccAddress, uint64], types.EpochPerformanceSummary]
		TrainingExecAllowListSet  collections.KeySet[sdk.AccAddress]
		TrainingStartAllowListSet collections.KeySet[sdk.AccAddress]
		ParticipantAllowListSet   collections.KeySet[sdk.AccAddress]
		PruningState              collections.Item[types.PruningState]
		InferencesToPrune         collections.Map[collections.Pair[int64, string], collections.NoValue]
		ActiveInvalidations       collections.KeySet[collections.Pair[sdk.AccAddress, string]]
		ExcludedParticipantsMap   collections.Map[collections.Pair[uint64, sdk.AccAddress], types.ExcludedParticipant]
		// Confirmation PoC collections
		ConfirmationPoCEvents          collections.Map[collections.Pair[uint64, uint64], types.ConfirmationPoCEvent]
		ActiveConfirmationPoCEventItem collections.Item[types.ConfirmationPoCEvent]
		LastUpgradeHeight              collections.Item[int64]
		PocV2EnabledEpoch              collections.Item[uint64]
		// Bridge & Wrapped Token collections
		BridgeContractAddresses        collections.Map[collections.Pair[string, string], types.BridgeContractAddress]
		BridgeTransactionsMap          collections.Map[collections.Triple[string, string, string], types.BridgeTransaction]
		WrappedTokenCodeIDItem         collections.Item[uint64]
		WrappedTokenMetadataMap        collections.Map[collections.Pair[string, string], types.BridgeTokenMetadata]
		WrappedTokenContractsMap       collections.Map[collections.Pair[string, string], types.BridgeWrappedTokenContract]
		WrappedContractReverseIndex    collections.Map[string, types.BridgeTokenReference]
		LiquidityPoolItem              collections.Item[types.LiquidityPool]
		LiquidityPoolApprovedTokensMap collections.Map[collections.Pair[string, string], types.BridgeTokenReference]
		// PoC validation sampling snapshots
		PoCValidationSnapshots collections.Map[int64, types.PoCValidationSnapshot]
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
		//nolint:forbidigo // init code
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
		// PoC v2 collections
		PoCValidationsV2: collections.NewMap(
			sb,
			types.PoCValidationV2Prefix,
			"poc_validation_v2",
			collections.TripleKeyCodec(collections.Int64Key, sdk.AccAddressKey, sdk.AccAddressKey),
			codec.CollValue[types.PoCValidationV2](cdc),
		),
		PoCV2StoreCommits: collections.NewMap(
			sb,
			types.PoCV2StoreCommitPrefix,
			"poc_v2_store_commit",
			collections.PairKeyCodec(collections.Int64Key, sdk.AccAddressKey),
			codec.CollValue[types.PoCV2StoreCommit](cdc),
		),
		MLNodeWeightDistributions: collections.NewMap(
			sb,
			types.MLNodeWeightDistributionPrefix,
			"mlnode_weight_distribution",
			collections.PairKeyCodec(collections.Int64Key, sdk.AccAddressKey),
			codec.CollValue[types.MLNodeWeightDistribution](cdc),
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
		InferenceRevalidations: collections.NewMap(
			sb,
			types.InferenceRevalidationsPrefix,
			"inference_revalidations",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey),
			codec.CollValue[types.RevalidationVoteRecord](cdc),
		),
		InferenceRevalidationTotalEligibleWeight: collections.NewMap(
			sb,
			types.InferenceRevalidationTotalWeightPrefix,
			"inference_revalidation_total_eligible_weight",
			collections.StringKey,
			collections.Int64Value,
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
		// EpochGroupcache is pointed by a keeper to avoid copying when keeper is passed by value
		epochGroupCache:                &epochGroupCache{m: make(map[epochGroupCacheKey]types.EpochGroupData)},
		randomSeedCache:                &randomSeedCache{m: make(map[randomSeedCacheKey]types.RandomSeed)},
		normalizedWeightedParticipants: newNormalizedWeightedParticipantsCache(),
		revalidationVoteParticipants:   newRevalidationVoteParticipantsCache(),
		storeRevalidationVotes:         false,
		ephemeralRevalidationVotes:     newEphemeralRevalidationVoteCache(),
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
		ParticipantAllowListSet: collections.NewKeySet(
			sb,
			types.ParticipantAllowListPrefix,
			"participant_allow_list",
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
		PocV2EnabledEpoch: collections.NewItem(
			sb,
			types.PocV2EnabledEpochPrefix,
			"poc_v2_enabled_epoch",
			collections.Uint64Value,
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
		PoCValidationSnapshots: collections.NewMap(
			sb,
			types.PoCValidationSnapshotPrefix,
			"poc_validation_snapshot",
			collections.Int64Key,
			codec.CollValue[types.PoCValidationSnapshot](cdc),
		),
	}
	// Build the collections schema
	schema, err := sb.Build()
	if err != nil {
		//nolint:forbidigo // init code
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

// SetBlockRevalidationEventsProvider sets the optional provider used to get all inference_validation
// events with needs_revalidation=true from a block (e.g. from block results). Call from the app
// after creating the keeper to enable the revalidation hook with all events from the previous block.
func (k *Keeper) SetBlockRevalidationEventsProvider(p BlockRevalidationEventsProvider) {
	k.blockRevalidationEventsProvider = p
}

// SetStoreRevalidationVotes sets whether revalidation votes are persisted to keeper storage (true)
// or only kept in the ephemeral cache cleared after NormalizedParticipantsCacheBlocks blocks (false).
func (k *Keeper) SetStoreRevalidationVotes(store bool) {
	k.storeRevalidationVotes = store
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
