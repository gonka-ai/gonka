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

	// CollateralKey is the prefix to store collateral for participants
	CollateralKey = []byte("collateral/")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

// GetCollateralKey returns the store key for a participant's collateral
func GetCollateralKey(participantAddress string) []byte {
	return append(CollateralKey, []byte(participantAddress)...)
}
