package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// InferenceStatsStorageKeyPrefix is the prefix to retrieve all InferenceStatsStorage
	InferenceStatsStorageKeyPrefix = "InferenceStatsStorage/value/"
)

// InferenceStatsStorageKey returns the store key to retrieve an InferenceStatsStorage from the index fields
func InferenceStatsStorageKey(
	index string,
) []byte {
	var key []byte

	indexBytes := []byte(index)
	key = append(key, indexBytes...)
	key = append(key, []byte("/")...)

	return key
}
