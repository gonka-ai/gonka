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
)

var (
	ParticipantsPrefix               = collections.NewPrefix(0)
	RandomSeedPrefix                 = collections.NewPrefix(1)
	PoCBatchPrefix                   = collections.NewPrefix(2)
	PoCValidationPref                = collections.NewPrefix(3)
	DynamicPricingCurrentPrefix      = collections.NewPrefix(4)
	DynamicPricingCapacityPrefix     = collections.NewPrefix(5)
	ModelsPrefix                     = collections.NewPrefix(6)
	InferenceTimeoutPrefix           = collections.NewPrefix(7)
	InferenceValidationDetailsPrefix = collections.NewPrefix(8)
	UnitOfComputePriceProposalPrefix = collections.NewPrefix(9)
	EpochGroupDataPrefix             = collections.NewPrefix(10)
	EpochsPrefix                     = collections.NewPrefix(11)
	EffectiveEpochIndexPrefix        = collections.NewPrefix(12)
	EpochGroupValidationsPrefix      = collections.NewPrefix(13)
	InferencesPrefix                 = collections.NewPrefix(14)
	SettleAmountPrefix               = collections.NewPrefix(15)
	TopMinerPrefix                   = collections.NewPrefix(16)
	PartialUpgradePrefix             = collections.NewPrefix(17)
	EpochPerformanceSummaryPrefix    = collections.NewPrefix(18)
	ParamsKey                        = []byte("p_inference")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

const (
	TokenomicsDataKey  = "TokenomicsData/value/"
	GenesisOnlyDataKey = "GenesisOnlyData/value/"
)
