package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// ParticipantKeyPrefix is the prefix to retrieve all Participant
	ParticipantKeyPrefix = "Participant/value/"
)

// ParticipantKey returns the store key to retrieve a Participant from the index fields
func ParticipantKey(
	index string,
) []byte {
	var key []byte

	indexBytes := []byte(index)
	key = append(key, indexBytes...)
	key = append(key, []byte("/")...)

	return key
}
