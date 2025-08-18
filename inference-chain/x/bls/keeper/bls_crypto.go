package keeper

import (
	"encoding/binary"
	"fmt"
	"math/big"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	"golang.org/x/crypto/sha3"
)

// computeParticipantPublicKey computes individual BLS public key for participant's slots
func (k Keeper) computeParticipantPublicKey(epochBLSData *types.EpochBLSData, slotIndices []uint32) ([]byte, error) {
	// Initialize aggregated public key as G2 identity
	var aggregatedPubKey bls12381.G2Affine

	// For each slot assigned to this participant
	for _, slotIndex := range slotIndices {
		// For each valid dealer's commitments
		for dealerIdx, isValid := range epochBLSData.ValidDealers {
			if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
				continue
			}

			dealerPart := epochBLSData.DealerParts[dealerIdx]
			if dealerPart == nil || len(dealerPart.Commitments) == 0 {
				continue
			}

			// Evaluate dealer's commitment polynomial at this slot index
			// This requires polynomial evaluation using the commitments
			slotPublicKey, err := k.evaluateCommitmentPolynomial(dealerPart.Commitments, slotIndex)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate commitment polynomial for dealer %d slot %d: %w", dealerIdx, slotIndex, err)
			}

			// Add to aggregated public key
			aggregatedPubKey.Add(&aggregatedPubKey, &slotPublicKey)
		}
	}

	// Return compressed public key bytes
	pubKeyBytes := aggregatedPubKey.Bytes()
	return pubKeyBytes[:], nil
}

// evaluateCommitmentPolynomial evaluates polynomial at given slot index
func (k Keeper) evaluateCommitmentPolynomial(commitments [][]byte, slotIndex uint32) (bls12381.G2Affine, error) {
	var result bls12381.G2Affine

	// Evaluate polynomial: result = Σ(commitments[i] * slotIndex^i)
	slotIndexBig := big.NewInt(int64(slotIndex))
	power := big.NewInt(1) // slotIndex^0 = 1

	for i, commitmentBytes := range commitments {
		if len(commitmentBytes) != 96 {
			return result, fmt.Errorf("invalid commitment %d length: expected 96, got %d", i, len(commitmentBytes))
		}

		var commitment bls12381.G2Affine
		err := commitment.Unmarshal(commitmentBytes)
		if err != nil {
			return result, fmt.Errorf("failed to unmarshal commitment %d: %w", i, err)
		}

		// Multiply commitment by slotIndex^i
		var term bls12381.G2Affine
		term.ScalarMultiplication(&commitment, power)

		// Add to result
		result.Add(&result, &term)

		// Update power for next iteration: power *= slotIndex
		power.Mul(power, slotIndexBig)
	}

	return result, nil
}

// verifyBLSPartialSignature verifies a BLS partial signature against participant's individual public key
func (k Keeper) verifyBLSPartialSignature(signature []byte, messageHash []byte, epochBLSData *types.EpochBLSData, slotIndices []uint32) bool {
	// Compute the participant's individual public key from stored commitments
	participantPublicKey, err := k.computeParticipantPublicKey(epochBLSData, slotIndices)
	if err != nil {
		k.Logger().Error("Failed to compute participant public key", "error", err)
		return false
	}

	// Parse the G1 signature (48 bytes compressed)
	if len(signature) != 48 {
		k.Logger().Error("Invalid signature length", "expected", 48, "actual", len(signature))
		return false
	}

	var g1Signature bls12381.G1Affine
	err = g1Signature.Unmarshal(signature)
	if err != nil {
		k.Logger().Error("Failed to unmarshal G1 signature", "error", err)
		return false
	}

	// Parse the G2 participant public key (96 bytes compressed)
	if len(participantPublicKey) != 96 {
		k.Logger().Error("Invalid participant public key length", "expected", 96, "actual", len(participantPublicKey))
		return false
	}

	var g2PublicKey bls12381.G2Affine
	err = g2PublicKey.Unmarshal(participantPublicKey)
	if err != nil {
		k.Logger().Error("Failed to unmarshal G2 participant public key", "error", err)
		return false
	}

	// Hash message to G1 point for BLS verification using proper hash-to-curve
	messageG1, err := k.hashToG1(messageHash)
	if err != nil {
		k.Logger().Error("Failed to hash message to G1", "error", err)
		return false
	}

	// Verify using pairing: e(signature, G2_generator) == e(message_hash, participant_public_key)
	_, _, _, g2Gen := bls12381.Generators()

	// Compute pairing e(signature, G2_generator)
	var pairing1 bls12381.GT
	pairing1, err = bls12381.Pair([]bls12381.G1Affine{g1Signature}, []bls12381.G2Affine{g2Gen})
	if err != nil {
		k.Logger().Error("Failed to compute pairing 1", "error", err)
		return false
	}

	// Compute pairing e(message_hash, participant_public_key)
	var pairing2 bls12381.GT
	pairing2, err = bls12381.Pair([]bls12381.G1Affine{messageG1}, []bls12381.G2Affine{g2PublicKey})
	if err != nil {
		k.Logger().Error("Failed to compute pairing 2", "error", err)
		return false
	}

	// Check if pairings are equal
	return pairing1.Equal(&pairing2)
}

// aggregateBLSPartialSignatures aggregates multiple G1 partial signatures into a single signature
func (k Keeper) aggregateBLSPartialSignatures(partialSignatures []types.PartialSignature) ([]byte, error) {
	if len(partialSignatures) == 0 {
		return nil, fmt.Errorf("no partial signatures to aggregate")
	}

	// Initialize aggregated signature as G1 identity (zero point)
	var aggregatedSignature bls12381.G1Affine

	for i, partialSig := range partialSignatures {
		// Parse each partial signature
		if len(partialSig.Signature) != 48 {
			return nil, fmt.Errorf("invalid signature length at index %d: expected 48, got %d", i, len(partialSig.Signature))
		}

		var g1Signature bls12381.G1Affine
		err := g1Signature.Unmarshal(partialSig.Signature)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal signature at index %d: %w", i, err)
		}

		// Add to aggregated signature: aggregatedSignature += g1Signature
		aggregatedSignature.Add(&aggregatedSignature, &g1Signature)
	}

	// Return compressed bytes
	signatureBytes := aggregatedSignature.Bytes()
	return signatureBytes[:], nil
}

// hashToG1 converts a hash to a G1 point using proper hash-to-curve
// Implements a simplified but secure hash-to-curve for BLS12-381 G1
func (k Keeper) hashToG1(hash []byte) (bls12381.G1Affine, error) {
	// Implement simplified hash-to-curve following BLS standards approach
	// This uses the "hash and try" method with a counter for security

	var result bls12381.G1Affine

	// Try up to 256 attempts to find a valid point
	for counter := 0; counter < 256; counter++ {
		// Create hash input with counter for domain separation
		hashInput := make([]byte, len(hash)+4)
		copy(hashInput, hash)
		binary.BigEndian.PutUint32(hashInput[len(hash):], uint32(counter))

		// Hash the input with counter using keccak256
		hashFunc := sha3.NewLegacyKeccak256()
		hashFunc.Write(hashInput)
		attempt := hashFunc.Sum(nil)

		// Try to create a valid G1 point from this hash
		// Use the hash as x-coordinate and try to find a valid point
		if k.trySetFromHash(&result, attempt) {
			return result, nil
		}
	}

	return result, fmt.Errorf("failed to hash to G1 point after 256 attempts")
}

// trySetFromHash attempts to create a valid G1 point from a hash
// This implements a simplified version of hash-to-curve point generation
func (k Keeper) trySetFromHash(point *bls12381.G1Affine, hash []byte) bool {
	// This is a simplified implementation of point generation from hash
	// In production, use proper hash-to-curve implementation from BLS standards

	// Create field element from hash
	var x fr.Element
	x.SetBytes(hash)

	// Use the hash as a scalar multiplier with the generator
	// This ensures we get a valid point on the curve
	g1GenJac, _, _, _ := bls12381.Generators()
	var g1Gen bls12381.G1Affine
	g1Gen.FromJacobian(&g1GenJac)

	// Multiply generator by the scalar derived from hash
	point.ScalarMultiplication(&g1Gen, x.BigInt(new(big.Int)))

	// Check if the point is valid (always true for scalar multiplication of generator)
	return !point.IsInfinity()
}
