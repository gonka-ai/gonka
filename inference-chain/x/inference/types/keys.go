package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "inference"

	SettleSubAccount = "settled"
	OwedSubAccount   = "owed"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_inference"
	// TransientStoreKey defines the transient store key
	TransientStoreKey = "transient_inference"

	TopRewardPoolAccName     = "top_reward"
	PreProgrammedSaleAccName = "pre_programmed_sale"
	BridgeEscrowAccName      = "bridge_escrow"
)

// These prefixes should NEVER change, only increase
var (
	ParticipantsPrefix                = collections.NewPrefix(0)
	RandomSeedPrefix                  = collections.NewPrefix(1)
	PoCBatchPrefix                    = collections.NewPrefix(2)
	PoCValidationPref                 = collections.NewPrefix(3)
	DynamicPricingCurrentPrefix       = collections.NewPrefix(4)
	DynamicPricingCapacityPrefix      = collections.NewPrefix(5)
	ModelsPrefix                      = collections.NewPrefix(6)
	InferenceTimeoutPrefix            = collections.NewPrefix(7)
	InferenceValidationDetailsPrefix  = collections.NewPrefix(8)
	UnitOfComputePriceProposalPrefix  = collections.NewPrefix(9)
	EpochGroupDataPrefix              = collections.NewPrefix(10)
	EpochsPrefix                      = collections.NewPrefix(11)
	EffectiveEpochIndexPrefix         = collections.NewPrefix(12)
	EpochGroupValidationsPrefix       = collections.NewPrefix(13)
	InferencesPrefix                  = collections.NewPrefix(14)
	SettleAmountPrefix                = collections.NewPrefix(15)
	TopMinerPrefix                    = collections.NewPrefix(16)
	PartialUpgradePrefix              = collections.NewPrefix(17)
	EpochPerformanceSummaryPrefix     = collections.NewPrefix(18)
	TrainingExecAllowListPrefix       = collections.NewPrefix(19)
	TrainingStartAllowListPrefix      = collections.NewPrefix(20)
	PruningStatePrefix                = collections.NewPrefix(21)
	InferencesToPrunePrefix           = collections.NewPrefix(22)
	ActiveInvalidationsPrefix         = collections.NewPrefix(23)
	ExcludedParticipantsPrefix        = collections.NewPrefix(24)
	ConfirmationPoCEventsPrefix       = collections.NewPrefix(25)
	ActiveConfirmationPoCEventPrefix  = collections.NewPrefix(26)
	LastUpgradeHeightPrefix           = collections.NewPrefix(27)
	BridgeContractAddressesPrefix     = collections.NewPrefix(28)
	BridgeTransactionsPrefix          = collections.NewPrefix(29)
	WrappedTokenCodeIDPrefix          = collections.NewPrefix(30)
	WrappedTokenMetadataPrefix        = collections.NewPrefix(31)
	WrappedTokenContractsPrefix       = collections.NewPrefix(32)
	WrappedContractReverseIndexPrefix = collections.NewPrefix(33)
	LiquidityPoolPrefix               = collections.NewPrefix(34)
	LiquidityPoolApprovedTokensPrefix = collections.NewPrefix(35)
	ParticipantAllowListPrefix        = collections.NewPrefix(36)
	// Legacy PoC v2 prefixes -- key codec changed in v0.2.12 (added model_id).
	// Cleared by the v0.2.12 upgrade handler. Never used by new code.
	LegacyPoCValidationV2Prefix            = collections.NewPrefix(38)
	LegacyPoCV2StoreCommitPrefix           = collections.NewPrefix(39)
	LegacyMLNodeWeightDistributionPrefix   = collections.NewPrefix(40)
	PocV2EnabledEpochPrefix                = collections.NewPrefix(41)
	PoCValidationSnapshotPrefix            = collections.NewPrefix(42)
	PunishmentGraceEpochsPrefix            = collections.NewPrefix(43)
	ActiveParticipantsCachePrefix          = collections.NewPrefix(44)
	ModelLoadRollingWindowPrefix           = collections.NewPrefix(45)
	ModelInferenceCountRollingWindowPrefix = collections.NewPrefix(46)
	EpochGroupValidationEntryPrefix        = collections.NewPrefix(47)
	DevshardEscrowsPrefix                  = collections.NewPrefix(48)
	DevshardEscrowCounterPrefix            = collections.NewPrefix(49)
	DevshardEscrowEpochCountPrefix         = collections.NewPrefix(50)
	DevshardHostEpochStatsPrefix           = collections.NewPrefix(51)
	DevshardEscrowsByEpochPrefix           = collections.NewPrefix(52)
	PoCDelegationPrefix                    = collections.NewPrefix(53)
	PoCRefusalPrefix                       = collections.NewPrefix(54)
	PoCDirectIntentPrefix                  = collections.NewPrefix(55)
	DelegationSnapshotPrefix               = collections.NewPrefix(56)
	BootstrapDelegationSnapshotPrefix      = collections.NewPrefix(57)
	// Replacement PoC v2 prefixes with model-aware key codecs.
	// Introduced in v0.2.12; see Legacy* prefixes 38/39/40 above for the
	// cleared predecessors.
	PoCValidationV2Prefix           = collections.NewPrefix(58)
	PoCV2StoreCommitPrefix          = collections.NewPrefix(59)
	MLNodeWeightDistributionPrefix  = collections.NewPrefix(60)
	BridgeMintRefundsPrefix         = collections.NewPrefix(61)
	BridgeWithdrawalRefundsPrefix   = collections.NewPrefix(62)
	BridgeWithdrawalTokenRefsPrefix = collections.NewPrefix(63)
	// BridgeTransactionValidatorsPrefix indexes per-validator confirmations
	// for a bridge transaction. Split off of BridgeTransaction.Validators
	// (an inline []string) so each validator's confirmation tx pays constant
	// gas regardless of how many other validators have already confirmed.
	// Keyed by (chainId, blockNumber, contentHashPart, validator) — the
	// first three match BridgeTransactionsPrefix's triple (content-addressed,
	// not receipt-addressed, so conflict transactions at the same receipt
	// location get separate validator sets), the fourth is the validator's
	// canonical bech32 address.
	BridgeTransactionValidatorsPrefix = collections.NewPrefix(64)
	PreservedNodesSnapshotPrefix      = collections.NewPrefix(65)
	ParamsKey                         = []byte("p_inference")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

const (
	TokenomicsDataKey  = "TokenomicsData/value/"
	GenesisOnlyDataKey = "GenesisOnlyData/value/"
	MLNodeVersionKey   = "MLNodeVersion/value/"
)

// TransientStore prefixes
var (
	FinishedInferenceQueueEntryPrefix = collections.NewPrefix(1)
	FinishedInferenceQueueNextSeqKey  = collections.NewPrefix(2)
	TransientSPRTValuesKey            = collections.NewPrefix(3)
	TransientEpochDataModelMetaKey    = collections.NewPrefix(4)
	TransientEpochDataModelWeightKey  = collections.NewPrefix(5)
)
