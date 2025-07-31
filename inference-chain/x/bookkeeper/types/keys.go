package types

const (
	// ModuleName defines the module name
	ModuleName = "bookkeeper"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_bookkeeper"
)

var (
	ParamsKey = []byte("p_bookkeeper")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
