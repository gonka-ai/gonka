package types

import "strconv"

const (
	UnitOfComputePriceKeyPrefix    = "UnitOfCompute/price/value/"
	UnitOfComputeProposalKeyPrefix = "UnitOfCompute/proposal/value/"
)

func UnitOfComputePriceKey(
	epochId uint64,
) []byte {
	var key []byte

	epochIdBytes := []byte(strconv.FormatUint(epochId, 10))
	key = append(key, epochIdBytes...)
	key = append(key, []byte("/")...)

	return key
}

func UnitOfComputeProposalKey(
	participant string,
) []byte {
	var key []byte

	participantBytes := []byte(participant)
	key = append(key, participantBytes...)
	key = append(key, []byte("/")...)

	return key
}
