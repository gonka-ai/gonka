package keeper

import (
	"fmt"

	"github.com/productscience/inference/x/bls/types"
)

func expectedEncryptedSharesCount(participant types.BLSParticipantInfo) (int, error) {
	if participant.SlotEndIndex < participant.SlotStartIndex {
		return 0, fmt.Errorf(
			"invalid slot range for participant %s: start=%d end=%d",
			participant.Address,
			participant.SlotStartIndex,
			participant.SlotEndIndex,
		)
	}

	slotCount := uint64(participant.SlotEndIndex-participant.SlotStartIndex) + 1
	keysPerSlot := uint64(1 + len(participant.AllowedSecp256K1PublicKeys))
	expected := slotCount * keysPerSlot
	maxInt := int(^uint(0) >> 1)
	if expected > uint64(maxInt) {
		return 0, fmt.Errorf("expected encrypted shares count overflow for participant %s", participant.Address)
	}
	if expected > uint64(types.MaxEncryptedSharesPerParticipantCount) {
		return 0, fmt.Errorf("expected encrypted shares count %d exceeds maximum %d for participant %s", expected, types.MaxEncryptedSharesPerParticipantCount, participant.Address)
	}

	return int(expected), nil
}

func validateEncryptedSharesShape(participant types.BLSParticipantInfo, encryptedShares [][]byte) error {
	expected, err := expectedEncryptedSharesCount(participant)
	if err != nil {
		return err
	}
	if len(encryptedShares) != expected {
		return fmt.Errorf("invalid encrypted shares count: expected %d, got %d", expected, len(encryptedShares))
	}
	return nil
}

func hasValidEncryptedSharesShape(participant types.BLSParticipantInfo, encryptedShares [][]byte) bool {
	return validateEncryptedSharesShape(participant, encryptedShares) == nil
}
