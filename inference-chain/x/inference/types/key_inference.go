package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// InferenceKeyPrefix is the prefix to retrieve all Inference
	InferenceKeyPrefix = "Inference/value/"
)

// InferenceKey returns the store key to retrieve a Inference from the index fields
func InferenceKey(
	index string,
) []byte {
	var key []byte

	indexBytes := []byte(index)
	key = append(key, indexBytes...)
	key = append(key, []byte("/")...)

	return key
}
