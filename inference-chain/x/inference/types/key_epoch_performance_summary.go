package types

import "encoding/binary"

var _ binary.ByteOrder

const (
	// EpochPerformanceSummaryKeyPrefix is the prefix to retrieve all EpochPerformanceSummary
	EpochPerformanceSummaryKeyPrefix = "EpochPerformanceSummary/value/"
)

// EpochPerformanceSummaryKey returns the store key to retrieve a EpochPerformanceSummary from the index fields
func EpochPerformanceSummaryKey(
	participantId string,
	epochStartHeight uint64,
) []byte {
	var key []byte

	participantIdBytes := []byte(participantId)
	key = append(key, participantIdBytes...)
	key = append(key, []byte("/")...)

	epochStartHeightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochStartHeightBytes, epochStartHeight)
	key = append(key, epochStartHeightBytes...)
	key = append(key, []byte("/")...)

	return key
}

func EpochPerformanceSummaryKeyParticipantPrefix(participantId string) []byte {
	var key []byte

	participantIdBytes := []byte(participantId)
	key = append(key, participantIdBytes...)
	key = append(key, []byte("/")...)

	return key
}
