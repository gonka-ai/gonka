package types

const (
	UnitOfComputeProposalKeyPrefix = "UnitOfCompute/proposal/value/"
)

func UnitOfComputeProposalKey(
	participant string,
) []byte {
	var key []byte

	participantBytes := []byte(participant)
	key = append(key, participantBytes...)
	key = append(key, []byte("/")...)

	return key
}
