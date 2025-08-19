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
	ParticipantsPrefix           = collections.NewPrefix(0)
	RandomSeedPrefix             = collections.NewPrefix(1)
	PoCBatchPrefix               = collections.NewPrefix(2)
	PoCValidationPref            = collections.NewPrefix(3)
	DynamicPricingCurrentPrefix  = collections.NewPrefix(4)
	DynamicPricingCapacityPrefix = collections.NewPrefix(5)
	ParamsKey                    = []byte("p_inference")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

const (
	TokenomicsDataKey  = "TokenomicsData/value/"
	GenesisOnlyDataKey = "GenesisOnlyData/value/"
)
