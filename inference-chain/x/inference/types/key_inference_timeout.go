package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// InferenceTimeoutKeyPrefix is the prefix to retrieve all InferenceTimeout
	InferenceTimeoutKeyPrefix = "InferenceTimeout/value/"
)

func InferenceTimeoutHeightKey(
	expirationHeight uint64) []byte {
	var key []byte

	expirationHeightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expirationHeightBytes, expirationHeight)
	key = append(key, expirationHeightBytes...)
	key = append(key, []byte("/")...)
	return key
}

// InferenceTimeoutKey returns the store key to retrieve a InferenceTimeout from the index fields
func InferenceTimeoutKey(
	expirationHeight uint64,
	inferenceId string,
) []byte {
	var key []byte

	expirationHeightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expirationHeightBytes, expirationHeight)
	key = append(key, expirationHeightBytes...)
	key = append(key, []byte("/")...)

	inferenceIdBytes := []byte(inferenceId)
	key = append(key, inferenceIdBytes...)
	key = append(key, []byte("/")...)

	return key
}
