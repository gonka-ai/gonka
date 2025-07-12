package types

const (
	// ModuleName defines the module name
	ModuleName = "collateral"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_collateral"
)

var (
	ParamsKey = []byte("p_collateral")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
