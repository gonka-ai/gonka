package types

const (
	// ModuleName defines the module name
	ModuleName = "genesistransfer"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_genesistransfer"
)

var (
	ParamsKey = []byte("p_genesistransfer")
)

const (
	TransferRecordKeyPrefix = "transfer_record/"
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
