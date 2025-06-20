package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// PowerKeyPrefix is the prefix to retrieve all Participant
	EpochGroupPrefix = "Epoch/group/"
)
