package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// EpochGroupValidationsKeyPrefix is the prefix to retrieve all EpochGroupValidations
	EpochGroupValidationsKeyPrefix = "EpochGroupValidations/value/"
)

// EpochGroupValidationsKey returns the store key to retrieve a EpochGroupValidations from the index fields
func EpochGroupValidationsKey(
	participant string,
	epochIndex uint64,
) []byte {
	var key []byte

	participantBytes := []byte(participant)
	key = append(key, participantBytes...)
	key = append(key, []byte("/")...)

	epochIndexBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochIndexBytes, epochIndex)
	key = append(key, epochIndexBytes...)
	key = append(key, []byte("/")...)

	return key
}
