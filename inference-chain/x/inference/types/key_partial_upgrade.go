package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// PartialUpgradeKeyPrefix is the prefix to retrieve all PartialUpgrade
	PartialUpgradeKeyPrefix = "PartialUpgrade/value/"
)

// PartialUpgradeKey returns the store key to retrieve a PartialUpgrade from the index fields
func PartialUpgradeKey(
	height uint64,
) []byte {
	var key []byte

	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, height)
	key = append(key, heightBytes...)
	key = append(key, []byte("/")...)

	return key
}
