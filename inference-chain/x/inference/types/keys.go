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
	// prefixes
	EpochGroupDataPrefix = collections.NewPrefix(0)
	ParamsKey            = []byte("p_inference")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

const (
	TokenomicsDataKey  = "TokenomicsData/value/"
	GenesisOnlyDataKey = "GenesisOnlyData/value/"
)
