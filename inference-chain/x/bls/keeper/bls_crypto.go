package keeper

import (
	"fmt"
	"math/big"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/productscience/inference/x/bls/types"
)

// computeParticipantPublicKey computes individual BLS public key for participant's slots
func (k Keeper) computeParticipantPublicKey(epochBLSData *types.EpochBLSData, slotIndices []uint32) ([]byte, error) {
	// Initialize aggregated public key as G2 identity
	var aggregatedPubKey bls12381.G2Affine
	aggregatedPubKey.SetInfinity()

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
	result.SetInfinity()

	// Evaluate polynomial at x = slotIndex+1 using Fr arithmetic:
	// result = Σ(commitments[i] * x^i)
	var x fr.Element
	x.SetUint64(uint64(slotIndex + 1))
	var power fr.Element
	power.SetOne() // x^0 = 1

	for i, commitmentBytes := range commitments {
		if len(commitmentBytes) != 96 {
			return result, fmt.Errorf("invalid commitment %d length: expected 96, got %d", i, len(commitmentBytes))
		}

		var commitment bls12381.G2Affine
		err := commitment.Unmarshal(commitmentBytes)
		if err != nil {
			return result, fmt.Errorf("failed to unmarshal commitment %d: %w", i, err)
		}

		// Multiply commitment by x^i
		var term bls12381.G2Affine
		term.ScalarMultiplication(&commitment, power.BigInt(new(big.Int)))

		// Add to result
		result.Add(&result, &term)

		// Update power for next iteration: power *= x
		power.Mul(&power, &x)
	}

	return result, nil
}

// verifyBLSPartialSignature verifies BLS partial signatures per-slot.
// The signature payload may contain N concatenated 48-byte compressed G1 signatures,
// and SlotIndices must have the same length N (1:1 mapping). Each (slot, sig)
// is verified against the aggregated slot public key computed from commitments.
func (k Keeper) verifyBLSPartialSignature(signature []byte, messageHash []byte, epochBLSData *types.EpochBLSData, slotIndices []uint32) bool {
	// Sanity: signature must be multiple of 48 and match slots length
	if len(signature)%48 != 0 {
		k.Logger().Error("Invalid signature payload length", "length", len(signature))
		return false
	}
	sigCount := len(signature) / 48
	if sigCount != len(slotIndices) {
		k.Logger().Error("Signature count mismatch", "sigCount", sigCount, "slots", len(slotIndices))
		return false
	}

	// Hash message to G1 once
	messageG1, err := k.hashToG1(messageHash)
	if err != nil {
		k.Logger().Error("Failed to hash message to G1", "error", err)
		return false
	}

	// Verify using pairing: e(signature, G2_generator) == e(message_hash, participant_public_key)
	_, _, _, g2Gen := bls12381.Generators()

	// Verify each (slot, sig) pair independently
	for i, slotIndex := range slotIndices {
		start := i * 48
		end := start + 48
		sigBytes := signature[start:end]

		// Parse G1 signature
		var g1Signature bls12381.G1Affine
		if err := g1Signature.Unmarshal(sigBytes); err != nil {
			k.Logger().Error("Failed to unmarshal per-slot G1 signature", "slot", slotIndex, "error", err)
			return false
		}

		// Compute aggregated slot public key across valid dealers
		var slotPubKey bls12381.G2Affine
		slotPubKey.SetInfinity()
		for dealerIdx, isValid := range epochBLSData.ValidDealers {
			if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
				continue
			}
			dealerPart := epochBLSData.DealerParts[dealerIdx]
			if dealerPart == nil || len(dealerPart.Commitments) == 0 {
				continue
			}
			eval, err := k.evaluateCommitmentPolynomial(dealerPart.Commitments, slotIndex)
			if err != nil {
				k.Logger().Error("Failed to evaluate commitment polynomial", "dealerIdx", dealerIdx, "slot", slotIndex, "error", err)
				return false
			}
			slotPubKey.Add(&slotPubKey, &eval)
		}

		// Pairing checks
		p1, err := bls12381.Pair([]bls12381.G1Affine{g1Signature}, []bls12381.G2Affine{g2Gen})
		if err != nil {
			k.Logger().Error("Failed to compute pairing 1", "slot", slotIndex, "error", err)
			return false
		}
		p2, err := bls12381.Pair([]bls12381.G1Affine{messageG1}, []bls12381.G2Affine{slotPubKey})
		if err != nil {
			k.Logger().Error("Failed to compute pairing 2", "slot", slotIndex, "error", err)
			return false
		}
		if !p1.Equal(&p2) {
			k.Logger().Error("Per-slot signature verification failed", "slot", slotIndex)
			return false
		}
	}
	return true
}

// aggregateBLSPartialSignatures aggregates per-slot signatures into a single signature using Lagrange weights.
func (k Keeper) aggregateBLSPartialSignatures(partialSignatures []types.PartialSignature) ([]byte, error) {
	if len(partialSignatures) == 0 {
		return nil, fmt.Errorf("no partial signatures to aggregate")
	}

	// Flatten per-slot signatures
	type slotSig struct {
		slot uint32
		sig  bls12381.G1Affine
	}
	var slotSigs []slotSig
	slotSeen := make(map[uint32]struct{})
	var slots []uint32
	for i, ps := range partialSignatures {
		if len(ps.Signature)%48 != 0 {
			return nil, fmt.Errorf("invalid signature payload at index %d: length=%d", i, len(ps.Signature))
		}
		count := len(ps.Signature) / 48
		if count != len(ps.SlotIndices) {
			return nil, fmt.Errorf("signature count mismatch at index %d: sigs=%d slots=%d", i, count, len(ps.SlotIndices))
		}
		for j := 0; j < count; j++ {
			slot := ps.SlotIndices[j]
			start := j * 48
			end := start + 48
			var g1 bls12381.G1Affine
			if err := g1.Unmarshal(ps.Signature[start:end]); err != nil {
				return nil, fmt.Errorf("failed to unmarshal signature at batch %d item %d: %w", i, j, err)
			}
			slotSigs = append(slotSigs, slotSig{slot: slot, sig: g1})
			if _, ok := slotSeen[slot]; !ok {
				slotSeen[slot] = struct{}{}
				slots = append(slots, slot)
			}
		}
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("no slot indices present in partial signatures")
	}

	// Precompute field elements for each slot index.
	xElems := make([]fr.Element, len(slots))
	for i, idx := range slots {
		// Use x-domain as slotIndex+1 to avoid x=0
		xElems[i].SetUint64(uint64(idx + 1))
	}

	// Compute Lagrange coefficients λ_i(0) for each slot index at evaluation point 0.
	// λ_i(0) = Π_{j≠i} (0 - x_j) / (x_i - x_j) in the BLS12-381 scalar field.
	type lambdaVal = fr.Element
	lambdaBySlot := make(map[uint32]lambdaVal, len(slots))
	for i := range slots {
		// numerator = Π_{j≠i} (-x_j)
		var numerator fr.Element
		numerator.SetOne()
		for j := range slots {
			if j == i {
				continue
			}
			var term fr.Element
			term.Neg(&xElems[j]) // -x_j
			numerator.Mul(&numerator, &term)
		}

		// denominator = Π_{j≠i} (x_i - x_j)
		var denominator fr.Element
		denominator.SetOne()
		for j := range slots {
			if j == i {
				continue
			}
			var diff fr.Element
			diff.Sub(&xElems[i], &xElems[j]) // x_i - x_j
			denominator.Mul(&denominator, &diff)
		}

		// lam = numerator * inverse(denominator)
		var denInv fr.Element
		denInv.Inverse(&denominator)
		var lam fr.Element
		lam.Mul(&numerator, &denInv)
		lambdaBySlot[slots[i]] = lam
	}

	// Initialize aggregated signature as G1 identity (zero point)
	var aggregatedSignature bls12381.G1Affine
	aggregatedSignature.SetInfinity()

	for _, ss := range slotSigs {
		lam, ok := lambdaBySlot[ss.slot]
		if !ok {
			return nil, fmt.Errorf("missing Lagrange coefficient for slot index %d", ss.slot)
		}
		var scaledSig bls12381.G1Affine
		scaledSig.ScalarMultiplication(&ss.sig, lam.BigInt(new(big.Int)))
		aggregatedSignature.Add(&aggregatedSignature, &scaledSig)
	}

	// Return compressed bytes
	signatureBytes := aggregatedSignature.Bytes()
	return signatureBytes[:], nil
}

// hashToG1 maps a 32-byte message hash (interpreted as an Fp element) to a G1 point.
// This mirrors the EIP-2537 MAP_FP_TO_G1: single-field-element SWU map + isogeny, then cofactor clear.
func (k Keeper) hashToG1(hash []byte) (bls12381.G1Affine, error) {
	var out bls12381.G1Affine
	if len(hash) != 32 {
		return out, fmt.Errorf("message hash must be 32 bytes, got %d", len(hash))
	}
	// Build 48-byte big-endian Fp element from 32-byte hash (left-pad with zeros)
	var be [48]byte
	copy(be[48-32:], hash)
	var u fp.Element
	u.SetBytes(be[:])
	// Map to curve using single-field SWU, then apply isogeny to the curve
	p := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&p.X, &p.Y)
	// Clear cofactor to ensure point is in G1 subgroup
	out.ClearCofactor(&p)
	return out, nil
}

// trySetFromHash removed; mapping now uses single-field SWU map aligned with EIP-2537.
