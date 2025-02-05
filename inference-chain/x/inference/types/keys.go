package types

const (
	// ModuleName defines the module name
	ModuleName = "inference"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_inference"

	StandardRewardPoolAccName = "standard_reward"
	TopRewardPoolAccName      = "top_reward"
	PreProgrammedSaleAccName  = "pre_programmed_sale"
)

var (
	ParamsKey = []byte("p_inference")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
