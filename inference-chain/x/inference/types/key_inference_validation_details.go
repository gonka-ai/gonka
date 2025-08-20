package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// InferenceValidationDetailsKeyPrefix is the prefix to retrieve all InferenceValidationDetails
	InferenceValidationDetailsKeyPrefix = "InferenceValidationDetails/value/"
)

func InferenceValidationDetailsEpochKey(
	epochId uint64,
) []byte {
	var key []byte
	epochIdBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochIdBytes, epochId)
	key = append(key, epochIdBytes...)
	key = append(key, []byte("/")...)

	return key
}

// InferenceValidationDetailsKey returns the store key to retrieve a InferenceValidationDetails from the index fields
func InferenceValidationDetailsKey(
	epochId uint64,
	inferenceId string,
) []byte {
	var key []byte

	epochIdBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochIdBytes, epochId)
	key = append(key, epochIdBytes...)
	key = append(key, []byte("/")...)

	inferenceIdBytes := []byte(inferenceId)
	key = append(key, inferenceIdBytes...)
	key = append(key, []byte("/")...)

	return key
}
