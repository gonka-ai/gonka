package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// PowerKeyPrefix is the prefix to retrieve all Participant
	PowerKeyPrefix   = "Power/value/"
	EpochGroupPrefix = "Epoch/group/"
)

// PowerKey returns the store key to retrieve a Power from the index fields
func PowerKey(
	index string,
) []byte {
	var key []byte

	indexBytes := []byte(index)
	key = append(key, indexBytes...)
	key = append(key, []byte("/")...)

	return key
}
