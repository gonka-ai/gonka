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

	TopRewardPoolAccName     = "top_reward"
	PreProgrammedSaleAccName = "pre_programmed_sale"
	BridgeEscrowAccName      = "bridge_escrow"
)

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
