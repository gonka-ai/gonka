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
	pocStartBlockHeight uint64,
) []byte {
	var key []byte

	participantBytes := []byte(participant)
	key = append(key, participantBytes...)
	key = append(key, []byte("/")...)

	pocStartBlockHeightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(pocStartBlockHeightBytes, pocStartBlockHeight)
	key = append(key, pocStartBlockHeightBytes...)
	key = append(key, []byte("/")...)

	return key
}
