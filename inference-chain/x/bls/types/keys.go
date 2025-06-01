package types

const (
	// ModuleName defines the module name
	ModuleName = "bls"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_bls"
)

var (
	ParamsKey = []byte("p_bls")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
