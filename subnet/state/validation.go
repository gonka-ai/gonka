package state

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"

	"subnet/types"
)

// DeriveSeed extracts a deterministic non-zero int64 seed from a signature.
// Takes first 8 bytes, masks to positive, ensures non-zero.
func DeriveSeed(signature []byte) (int64, error) {
	if len(signature) < 8 {
		return 0, types.ErrSeedTooShort
	}
	raw := binary.BigEndian.Uint64(signature[:8])
	seed := int64(raw & ((1 << 63) - 1))
	if seed == 0 {
		seed = 1
	}
	return seed, nil
}

// DeterministicFloat generates a deterministic float64 in [0,1) from seed and inferenceID.
// Uses sha256("%d:%d") -> first 8 bytes -> uint64 / MaxUint64.
func DeterministicFloat(seed int64, inferenceID uint64) float64 {
	input := fmt.Sprintf("%d:%d", seed, inferenceID)
	sum := sha256.Sum256([]byte(input))
	hashInt := binary.BigEndian.Uint64(sum[:8])
	return float64(hashInt) / float64(math.MaxUint64)
}

// ShouldValidate returns true if this validator should validate the given inference.
// probability = (rateBasisPoints/10000) * validatorSlotCount / (totalSlots - executorSlotCount)
// Returns DeterministicFloat(seed, inferenceID) < probability.
func ShouldValidate(seed int64, inferenceID uint64, validatorSlotCount, executorSlotCount, totalSlots, rateBasisPoints uint32) bool {
	if totalSlots <= executorSlotCount {
		return false
	}
	rate := float64(rateBasisPoints) / 10000.0
	probability := rate * float64(validatorSlotCount) / float64(totalSlots-executorSlotCount)
	if probability > 1.0 {
		probability = 1.0
	}
	return DeterministicFloat(seed, inferenceID) < probability
}
