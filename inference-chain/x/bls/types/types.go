package types

import (
	"cosmossdk.io/math"
)

// ParticipantWithWeightAndKey represents a participant with their weight and secp256k1 key
// This type is passed from the inference module to the BLS module
type ParticipantWithWeightAndKey struct {
	Address            string
	PercentageWeight   math.LegacyDec
	Secp256k1PublicKey []byte
}
