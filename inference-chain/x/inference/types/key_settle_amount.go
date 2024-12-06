package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// SettleAmountKeyPrefix is the prefix to retrieve all SettleAmount
	SettleAmountKeyPrefix = "SettleAmount/value/"
)

// SettleAmountKey returns the store key to retrieve a SettleAmount from the index fields
func SettleAmountKey(
	participant string,
) []byte {
	var key []byte

	participantBytes := []byte(participant)
	key = append(key, participantBytes...)
	key = append(key, []byte("/")...)

	return key
}
