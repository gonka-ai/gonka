package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// TopMinerKeyPrefix is the prefix to retrieve all TopMiner
	TopMinerKeyPrefix = "TopMiner/value/"
)

// TopMinerKey returns the store key to retrieve a TopMiner from the index fields
func TopMinerKey(
	address string,
) []byte {
	var key []byte

	addressBytes := []byte(address)
	key = append(key, addressBytes...)
	key = append(key, []byte("/")...)

	return key
}
