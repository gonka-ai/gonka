package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// EpochGroupDataKeyPrefix is the prefix to retrieve all EpochGroupData
	EpochGroupDataKeyPrefix = "EpochGroupData/value/"
)

// EpochGroupDataKey returns the store key to retrieve a EpochGroupData from the index fields
func EpochGroupDataKey(
	pocStartBlockHeight uint64,
) []byte {
	var key []byte

	pocStartBlockHeightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(pocStartBlockHeightBytes, pocStartBlockHeight)
	key = append(key, pocStartBlockHeightBytes...)
	key = append(key, []byte("/")...)

	return key
}
