package keeper

import (
	"fmt"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
)

// Thanks for this optimizations inspiration to prof. Dan Boneh & Ash Vardanian
// from Alex Petrov aka sysman.

// computeParticipantPublicKey computes individual BLS public key for participant's slots.
//
// Deprecated: use computeParticipantPublicKeyBlst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
func (k Keeper) computeParticipantPublicKey(epochBLSData *types.EpochBLSData, slotIndices []uint32) ([]byte, error) {
	// Initialize aggregated public key as G2 identity
	var aggregatedPubKey bls12381.G2Affine
	aggregatedPubKey.SetInfinity()

	// For each slot assigned to this participant
	for _, slotIndex := range slotIndices {
		if len(epochBLSData.SlotPublicKeys) <= int(slotIndex) {
			return nil, fmt.Errorf("precomputed slot public key missing for slot %d", slotIndex)
		}

		// Use precomputed slot public key
		var slotPubKey bls12381.G2Affine
		if err := slotPubKey.Unmarshal(epochBLSData.SlotPublicKeys[slotIndex]); err != nil {
			return nil, fmt.Errorf("failed to unmarshal precomputed slot public key %d: %w", slotIndex, err)
		}
		aggregatedPubKey.Add(&aggregatedPubKey, &slotPubKey)
	}

	// Return compressed public key bytes
	pubKeyBytes := aggregatedPubKey.Bytes()
	return pubKeyBytes[:], nil
}

// computeParticipantPublicKeyBlst computes individual BLS public key for participant's slots using blst.
func (k Keeper) computeParticipantPublicKeyBlst(epochBLSData *types.EpochBLSData, slotIndices []uint32) ([]byte, error) {
	if len(slotIndices) == 0 {
		return new(blst.P2).ToAffine().Compress(), nil
	}

	points := make([]*blst.P2Affine, 0, len(slotIndices))
	for _, slotIndex := range slotIndices {
		if len(epochBLSData.SlotPublicKeys) <= int(slotIndex) {
			return nil, fmt.Errorf("precomputed slot public key missing for slot %d", slotIndex)
		}

		p := new(blst.P2Affine).Uncompress(epochBLSData.SlotPublicKeys[slotIndex])
		if p == nil {
			return nil, fmt.Errorf("failed to unmarshal precomputed slot public key %d with blst", slotIndex)
		}
		points = append(points, p)
	}

	// Efficiently add all points
	res := blst.P2AffinesAdd(points)
	return res.ToAffine().Compress(), nil
}

// PrecomputeSlotPublicKeys precomputes public keys for all slots in the epoch.
//
// Deprecated: use PrecomputeSlotPublicKeysBlst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
func (k Keeper) PrecomputeSlotPublicKeys(epochBLSData *types.EpochBLSData) ([][]byte, error) {
	totalSlots := epochBLSData.ITotalSlots
	slotPublicKeys := make([][]byte, totalSlots)

	// Pre-unmarshal all valid dealer commitments once
	type dealerCommitments struct {
		points []bls12381.G2Affine
	}
	activeDealers := make([]dealerCommitments, 0)
	for dealerIdx, isValid := range epochBLSData.ValidDealers {
		if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
			continue
		}
		dealerPart := epochBLSData.DealerParts[dealerIdx]
		if dealerPart == nil || len(dealerPart.Commitments) == 0 {
			continue
		}

		points := make([]bls12381.G2Affine, len(dealerPart.Commitments))
		for i, cb := range dealerPart.Commitments {
			if err := points[i].Unmarshal(cb); err != nil {
				return nil, fmt.Errorf("failed to unmarshal commitment %d for dealer %d: %w", i, dealerIdx, err)
			}
		}
		activeDealers = append(activeDealers, dealerCommitments{points: points})
	}

	// 1. Pre-aggregate commitments across all dealers: totalCommitments[i] = sum over all dealers of C_dealer,i
	// This reduces 100 MSM calls per slot down to 1 MSM call per slot.
	maxCoeffs := 0
	for _, dealer := range activeDealers {
		if len(dealer.points) > maxCoeffs {
			maxCoeffs = len(dealer.points)
		}
	}

	totalCommitments := make([]bls12381.G2Affine, maxCoeffs)
	for i := range totalCommitments {
		totalCommitments[i].SetInfinity()
	}

	for _, dealer := range activeDealers {
		for i, point := range dealer.points {
			totalCommitments[i].Add(&totalCommitments[i], &point)
		}
	}

	// 2. For each slot, compute aggregated public key using the total commitments
	for slotIndex := uint32(0); slotIndex < totalSlots; slotIndex++ {
		var x fr.Element
		x.SetUint64(uint64(slotIndex + 1))

		var power fr.Element
		power.SetOne()
		scalars := make([]fr.Element, len(totalCommitments))
		for i := range scalars {
			scalars[i] = power
			power.Mul(&power, &x)
		}

		var aggregatedPubKey bls12381.G2Affine
		_, err := aggregatedPubKey.MultiExp(totalCommitments, scalars, ecc.MultiExpConfig{})
		if err != nil {
			return nil, fmt.Errorf("failed to multiexp for slot %d: %w", slotIndex, err)
		}

		pkBytes := aggregatedPubKey.Bytes()
		slotPublicKeys[slotIndex] = pkBytes[:]
	}

	return slotPublicKeys, nil
}

// PrecomputeSlotPublicKeysBlst precomputes public keys for all slots in the epoch using the blst library.
// This is an identical implementation to PrecomputeSlotPublicKeys but uses blst for higher performance MSM.
func (k Keeper) PrecomputeSlotPublicKeysBlst(epochBLSData *types.EpochBLSData) ([][]byte, error) {
	totalSlots := epochBLSData.ITotalSlots
	slotPublicKeys := make([][]byte, totalSlots)

	// Pre-unmarshal all valid dealer commitments once using blst
	type dealerCommitments struct {
		points []*blst.P2Affine
	}
	activeDealers := make([]dealerCommitments, 0)
	for dealerIdx, isValid := range epochBLSData.ValidDealers {
		if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
			continue
		}
		dealerPart := epochBLSData.DealerParts[dealerIdx]
		if dealerPart == nil || len(dealerPart.Commitments) == 0 {
			continue
		}

		points := make([]*blst.P2Affine, len(dealerPart.Commitments))
		for i, cb := range dealerPart.Commitments {
			p := new(blst.P2Affine).Uncompress(cb)
			if p == nil {
				return nil, fmt.Errorf("failed to unmarshal commitment %d for dealer %d with blst", i, dealerIdx)
			}
			points[i] = p
		}
		activeDealers = append(activeDealers, dealerCommitments{points: points})
	}

	if len(activeDealers) == 0 {
		return slotPublicKeys, nil
	}

	maxCoeffs := 0
	for _, dealer := range activeDealers {
		if len(dealer.points) > maxCoeffs {
			maxCoeffs = len(dealer.points)
		}
	}

	// 1. Pre-aggregate commitments across all dealers
	totalCommitmentsAffine := make([]*blst.P2Affine, maxCoeffs)
	for i := 0; i < maxCoeffs; i++ {
		pointsToAggregate := make([]*blst.P2Affine, 0)
		for _, dealer := range activeDealers {
			if i < len(dealer.points) {
				pointsToAggregate = append(pointsToAggregate, dealer.points[i])
			}
		}
		if len(pointsToAggregate) == 0 {
			// Identity point in G2
			totalCommitmentsAffine[i] = new(blst.P2).ToAffine()
		} else {
			totalCommitmentsAffine[i] = blst.P2AffinesAdd(pointsToAggregate).ToAffine()
		}
	}

	// 2. For each slot, compute aggregated public key using the total commitments
	for slotIndex := uint32(0); slotIndex < totalSlots; slotIndex++ {
		// Use gnark's fr for easy field math (powers), then convert to blst bytes
		var x fr.Element
		x.SetUint64(uint64(slotIndex + 1))

		var power fr.Element
		power.SetOne()

		scalars := make([]byte, len(totalCommitmentsAffine)*32)
		for i := 0; i < len(totalCommitmentsAffine); i++ {
			pBytes := power.Bytes()
			// gnark-crypto uses big-endian, blst expects little-endian for scalars
			for j := 0; j < 16; j++ {
				pBytes[j], pBytes[31-j] = pBytes[31-j], pBytes[j]
			}
			copy(scalars[i*32:(i+1)*32], pBytes[:])
			power.Mul(&power, &x)
		}

		// Perform MSM with blst
		// 255 bits for BLS12-381 scalar field
		aggregatedPubKeyP2 := blst.P2AffinesMult(totalCommitmentsAffine, scalars, 255)

		pkBytes := aggregatedPubKeyP2.ToAffine().Compress()
		slotPublicKeys[slotIndex] = pkBytes
	}

	return slotPublicKeys, nil
}

// verifyBLSPartialSignatureBlst verifies BLS partial signatures per-slot using the blst library.
func (k Keeper) verifyBLSPartialSignatureBlst(signature []byte, messageHash []byte, epochBLSData *types.EpochBLSData, slotIndices []uint32) bool {
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

	// Hash message to G1 using gnark-crypto implementation for consistency
	messageG1Gnark, err := k.hashToG1(messageHash)
	if err != nil {
		k.Logger().Error("Failed to hash message to G1", "error", err)
		return false
	}
	msgG1Bytes := messageG1Gnark.Bytes()
	messageG1Blst := new(blst.P1Affine).Uncompress(msgG1Bytes[:])
	if messageG1Blst == nil {
		k.Logger().Error("Failed to uncompress message G1 with blst")
		return false
	}

	// G2 generator in blst
	g2Gen := blst.P2Generator().ToAffine()

	// Verify each (slot, sig) pair independently
	for i, slotIndex := range slotIndices {
		start := i * 48
		end := start + 48
		sigBytes := signature[start:end]

		// Parse G1 signature with blst
		g1Signature := new(blst.P1Affine).Uncompress(sigBytes)
		if g1Signature == nil {
			k.Logger().Error("Failed to unmarshal per-slot G1 signature with blst", "slot", slotIndex)
			return false
		}
		// Full signature validation (subgroup check + reject infinity).
		if !g1Signature.SigValidate(true) {
			k.Logger().Error("Invalid per-slot G1 signature with blst (SigValidate failed)", "slot", slotIndex)
			return false
		}

		// Parse precomputed slot public key with blst
		if len(epochBLSData.SlotPublicKeys) <= int(slotIndex) {
			k.Logger().Error("Precomputed slot public key missing", "slot", slotIndex)
			return false
		}
		slotPubKey := new(blst.P2Affine).Uncompress(epochBLSData.SlotPublicKeys[slotIndex])
		if slotPubKey == nil {
			k.Logger().Error("Failed to unmarshal precomputed slot public key with blst", "slot", slotIndex)
			return false
		}
		// Public key validation (subgroup check + reject identity).
		if !slotPubKey.KeyValidate() {
			k.Logger().Error("Invalid precomputed slot public key with blst (KeyValidate failed)", "slot", slotIndex)
			return false
		}

		// Pairing check: e(signature, G2_generator) == e(messageG1, slotPubKey)
		// Combined Miller loop: e(signature, G2_generator) * e(messageG1, -slotPubKey) == 1
		// To negate a point, we subtract it from the identity point.
		negSlotPubKey := new(blst.P2).Sub(slotPubKey).ToAffine()

		// Perform pairing check
		ml := blst.Fp12MillerLoopN([]blst.P2Affine{*g2Gen, *negSlotPubKey}, []blst.P1Affine{*g1Signature, *messageG1Blst})
		ml.FinalExp()
		one := blst.Fp12One()
		if !ml.Equals(&one) {
			k.Logger().Error("Per-slot signature verification failed with blst", "slot", slotIndex)
			return false
		}
	}
	return true
}

// verifyBLSPartialSignature verifies BLS partial signatures per-slot.
//
// Deprecated: use verifyBLSPartialSignatureBlst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
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

		// Compute aggregated slot public key
		var slotPubKey bls12381.G2Affine
		if len(epochBLSData.SlotPublicKeys) <= int(slotIndex) {
			k.Logger().Error("Precomputed slot public key missing", "slot", slotIndex)
			return false
		}

		// Use precomputed slot public key
		if err := slotPubKey.Unmarshal(epochBLSData.SlotPublicKeys[slotIndex]); err != nil {
			k.Logger().Error("Failed to unmarshal precomputed slot public key", "slot", slotIndex, "error", err)
			return false
		}

		// Pairing check: e(signature, G2_generator) == e(messageG1, slotPubKey)
		// Optimized using PairingCheck which combines Miller loops and does a single FinalExp.
		// e(signature, G2_generator) * e(messageG1, -slotPubKey) == 1
		var negSlotPubKey bls12381.G2Affine
		negSlotPubKey.Neg(&slotPubKey)

		isValid, err := bls12381.PairingCheck(
			[]bls12381.G1Affine{g1Signature, messageG1},
			[]bls12381.G2Affine{g2Gen, negSlotPubKey},
		)
		if err != nil {
			k.Logger().Error("Failed to compute pairing check", "slot", slotIndex, "error", err)
			return false
		}
		if !isValid {
			k.Logger().Error("Per-slot signature verification failed", "slot", slotIndex)
			return false
		}
	}
	return true
}

// aggregateBLSPartialSignaturesBlst aggregates per-slot signatures into a single signature using Lagrange weights and blst.
func (k Keeper) aggregateBLSPartialSignaturesBlst(partialSignatures []types.PartialSignature) ([]byte, error) {
	if len(partialSignatures) == 0 {
		return nil, fmt.Errorf("no partial signatures to aggregate")
	}

	// Flatten per-slot signatures and track unique slots
	type slotSig struct {
		slot uint32
		sig  *blst.P1Affine
	}
	var slotSigs []slotSig
	slotSeen := make(map[uint32]struct{})
	var uniqueSlots []uint32

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
			g1 := new(blst.P1Affine).Uncompress(ps.Signature[start:end])
			if g1 == nil {
				return nil, fmt.Errorf("failed to uncompress signature at batch %d item %d with blst", i, j)
			}
			// Full signature validation (subgroup check + reject infinity).
			if !g1.SigValidate(true) {
				return nil, fmt.Errorf("invalid signature at batch %d item %d (SigValidate failed)", i, j)
			}
			slotSigs = append(slotSigs, slotSig{slot: slot, sig: g1})
			if _, ok := slotSeen[slot]; !ok {
				slotSeen[slot] = struct{}{}
				uniqueSlots = append(uniqueSlots, slot)
			}
		}
	}

	if len(uniqueSlots) == 0 {
		return nil, fmt.Errorf("no slot indices present in partial signatures")
	}

	// Compute Lagrange coefficients λ_i(0) using gnark-crypto for field math
	xElems := make([]fr.Element, len(uniqueSlots))
	for i, idx := range uniqueSlots {
		xElems[i].SetUint64(uint64(idx + 1))
	}

	lambdaBySlot := make(map[uint32][]byte, len(uniqueSlots))
	for i := range uniqueSlots {
		var numerator fr.Element
		numerator.SetOne()
		for j := range uniqueSlots {
			if j == i {
				continue
			}
			var term fr.Element
			term.Neg(&xElems[j])
			numerator.Mul(&numerator, &term)
		}

		var denominator fr.Element
		denominator.SetOne()
		for j := range uniqueSlots {
			if j == i {
				continue
			}
			var diff fr.Element
			diff.Sub(&xElems[i], &xElems[j])
			denominator.Mul(&denominator, &diff)
		}

		var denInv fr.Element
		denInv.Inverse(&denominator)
		var lam fr.Element
		lam.Mul(&numerator, &denInv)

		// Convert to little-endian for blst
		lBytes := lam.Bytes()
		for j := 0; j < 16; j++ {
			lBytes[j], lBytes[31-j] = lBytes[31-j], lBytes[j]
		}
		lambdaBySlot[uniqueSlots[i]] = lBytes[:]
	}

	// Prepare data for blst MSM
	points := make([]*blst.P1Affine, len(slotSigs))
	scalars := make([]byte, len(slotSigs)*32)
	for i, ss := range slotSigs {
		points[i] = ss.sig
		copy(scalars[i*32:(i+1)*32], lambdaBySlot[ss.slot])
	}

	// Perform MSM with blst
	aggregatedSignature := blst.P1AffinesMult(points, scalars, 255)

	// Return compressed bytes
	return aggregatedSignature.ToAffine().Compress(), nil
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

// verifyFinalSignature verifies an aggregated signature against a group public key using gnark-crypto.
//
// Deprecated: use verifyFinalSignatureBlst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
// e(signature, G2_generator) == e(message_hash_to_g1, group_public_key)
func (k Keeper) verifyFinalSignature(signature []byte, messageHash []byte, groupPubKeyBytes []byte) bool {
	var sigAff bls12381.G1Affine
	if err := sigAff.Unmarshal(signature); err != nil {
		k.Logger().Error("Failed to unmarshal final signature", "error", err)
		return false
	}

	var groupPubKey bls12381.G2Affine
	if err := groupPubKey.Unmarshal(groupPubKeyBytes); err != nil {
		k.Logger().Error("Failed to unmarshal group public key", "error", err)
		return false
	}

	messageG1, err := k.hashToG1(messageHash)
	if err != nil {
		k.Logger().Error("Failed to hash message to G1", "error", err)
		return false
	}

	_, _, _, g2Gen := bls12381.Generators()

	// e(signature, G2_generator) * e(messageG1, -groupPubKey) == 1
	var negGroupPubKey bls12381.G2Affine
	negGroupPubKey.Neg(&groupPubKey)

	isValid, err := bls12381.PairingCheck(
		[]bls12381.G1Affine{sigAff, messageG1},
		[]bls12381.G2Affine{g2Gen, negGroupPubKey},
	)
	if err != nil {
		k.Logger().Error("Failed to compute pairing check", "error", err)
		return false
	}
	return isValid
}

// verifyFinalSignatureBlst verifies an aggregated signature against a group public key using blst.
func (k Keeper) verifyFinalSignatureBlst(signature []byte, messageHash []byte, groupPubKeyBytes []byte) bool {
	g1Signature := new(blst.P1Affine).Uncompress(signature)
	if g1Signature == nil {
		k.Logger().Error("Failed to unmarshal final signature with blst")
		return false
	}
	// Full signature validation (subgroup check + reject infinity).
	if !g1Signature.SigValidate(true) {
		k.Logger().Error("Invalid final signature with blst (SigValidate failed)")
		return false
	}

	groupPubKey := new(blst.P2Affine).Uncompress(groupPubKeyBytes)
	if groupPubKey == nil {
		k.Logger().Error("Failed to unmarshal group public key with blst")
		return false
	}
	// Public key validation (subgroup check + reject identity).
	if !groupPubKey.KeyValidate() {
		k.Logger().Error("Invalid group public key with blst (KeyValidate failed)")
		return false
	}

	// Hash message to G1 (using gnark-crypto hashToG1 for consistency)
	messageG1Gnark, err := k.hashToG1(messageHash)
	if err != nil {
		k.Logger().Error("Failed to hash message to G1", "error", err)
		return false
	}
	msgG1Bytes := messageG1Gnark.Bytes()
	messageG1Blst := new(blst.P1Affine).Uncompress(msgG1Bytes[:])
	if messageG1Blst == nil {
		k.Logger().Error("Failed to uncompress message G1 with blst")
		return false
	}

	g2Gen := blst.P2Generator().ToAffine()

	// e(signature, G2_generator) * e(messageG1, -groupPubKey) == 1
	negGroupPubKey := new(blst.P2).Sub(groupPubKey).ToAffine()

	ml := blst.Fp12MillerLoopN([]blst.P2Affine{*g2Gen, *negGroupPubKey}, []blst.P1Affine{*g1Signature, *messageG1Blst})
	ml.FinalExp()
	one := blst.Fp12One()
	return ml.Equals(&one)
}

// aggregateG2Points aggregates a list of G2 points using gnark-crypto.
//
// Deprecated: use aggregateG2PointsBlst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
func (k Keeper) aggregateG2Points(points [][]byte) ([]byte, error) {
	var aggregate bls12381.G2Affine
	aggregate.SetInfinity()

	for i, pb := range points {
		var p bls12381.G2Affine
		if err := p.Unmarshal(pb); err != nil {
			return nil, fmt.Errorf("failed to unmarshal point at index %d: %w", i, err)
		}
		aggregate.Add(&aggregate, &p)
	}

	res := aggregate.Bytes()
	return res[:], nil
}

// aggregateG2PointsBlst aggregates a list of G2 points using blst.
func (k Keeper) aggregateG2PointsBlst(points [][]byte) ([]byte, error) {
	if len(points) == 0 {
		return new(blst.P2).ToAffine().Compress(), nil
	}

	blstPoints := make([]*blst.P2Affine, 0, len(points))
	for i, pb := range points {
		p := new(blst.P2Affine).Uncompress(pb)
		if p == nil {
			return nil, fmt.Errorf("failed to uncompress point at index %d with blst", i)
		}
		// Points are untrusted inputs (dealer commitments), so enforce subgroup membership.
		// Note: commitments may be infinity for zero coefficients; InG2 accepts infinity.
		if !p.InG2() {
			return nil, fmt.Errorf("point at index %d is not in G2 subgroup", i)
		}
		blstPoints = append(blstPoints, p)
	}

	res := blst.P2AffinesAdd(blstPoints)
	return res.ToAffine().Compress(), nil
}

// DecompressG2To256 converts a 96-byte compressed G2 point into a 256-byte uncompressed format
// using gnark-crypto. Format: (X.c0, X.c1, Y.c0, Y.c1) each as 64-byte big-endian limb.
//
// Deprecated: use DecompressG2To256Blst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
func (k Keeper) DecompressG2To256(groupPublicKey []byte) ([]byte, error) {
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}
	var g2 bls12381.G2Affine
	if err := g2.Unmarshal(groupPublicKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal compressed G2 key: %w", err)
	}

	var uncompressed []byte
	appendFp64 := func(e fp.Element) {
		be48 := e.Bytes()
		var limb [64]byte
		copy(limb[64-48:], be48[:])
		uncompressed = append(uncompressed, limb[:]...)
	}

	appendFp64(g2.X.A0)
	appendFp64(g2.X.A1)
	appendFp64(g2.Y.A0)
	appendFp64(g2.Y.A1)

	return uncompressed, nil
}

// DecompressG2To256Blst converts a 96-byte compressed G2 point into a 256-byte uncompressed format
// using blst. Format: (X.c0, X.c1, Y.c0, Y.c1) each as 64-byte big-endian limb.
func (k Keeper) DecompressG2To256Blst(groupPublicKey []byte) ([]byte, error) {
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}
	p := new(blst.P2Affine).Uncompress(groupPublicKey)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress G2 key with blst")
	}

	// blst.Serialize() returns [X.c1, X.c0, Y.c1, Y.c0] (IETF standard)
	// each as a 48-byte big-endian element.
	raw := p.Serialize()

	// We need [X.c0, X.c1, Y.c0, Y.c1] to match gnark-crypto
	// and pad each to 64 bytes.
	uncompressed := make([]byte, 256)

	// Copy X.c0 (from raw[48:96]) to limb 0
	copy(uncompressed[0*64+16:1*64], raw[48:96])
	// Copy X.c1 (from raw[0:48]) to limb 1
	copy(uncompressed[1*64+16:2*64], raw[0:48])
	// Copy Y.c0 (from raw[144:192]) to limb 2
	copy(uncompressed[2*64+16:3*64], raw[144:192])
	// Copy Y.c1 (from raw[96:144]) to limb 3
	copy(uncompressed[3*64+16:4*64], raw[96:144])

	return uncompressed, nil
}

// DecompressG1To128 converts a 48-byte compressed G1 point into a 128-byte uncompressed format
// using gnark-crypto. Format: (X, Y) each as 64-byte big-endian limb.
//
// Deprecated: use DecompressG1To128Blst. The gnark-crypto implementation is kept only
// for legacy/reference purposes and is intended to be removed in a future cleanup.
func (k Keeper) DecompressG1To128(signature []byte) ([]byte, error) {
	if len(signature) != 48 {
		return nil, fmt.Errorf("invalid signature length: expected 48 bytes, got %d", len(signature))
	}
	var g1 bls12381.G1Affine
	if err := g1.Unmarshal(signature); err != nil {
		return nil, fmt.Errorf("failed to unmarshal signature: %w", err)
	}

	var uncompressed []byte
	appendFp64 := func(e fp.Element) {
		be48 := e.Bytes()
		var limb [64]byte
		copy(limb[64-48:], be48[:])
		uncompressed = append(uncompressed, limb[:]...)
	}

	appendFp64(g1.X)
	appendFp64(g1.Y)

	return uncompressed, nil
}

// DecompressG1To128Blst converts a 48-byte compressed G1 point into a 128-byte uncompressed format
// using blst. Format: (X, Y) each as 64-byte big-endian limb.
func (k Keeper) DecompressG1To128Blst(signature []byte) ([]byte, error) {
	if len(signature) != 48 {
		return nil, fmt.Errorf("invalid signature length: expected 48 bytes, got %d", len(signature))
	}
	p := new(blst.P1Affine).Uncompress(signature)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress signature with blst")
	}

	// blst.Serialize() returns [X, Y] (big-endian 48-byte elements)
	raw := p.Serialize()

	uncompressed := make([]byte, 128)
	// Copy X to limb 0 (padded)
	copy(uncompressed[16:64], raw[0:48])
	// Copy Y to limb 1 (padded)
	copy(uncompressed[64+16:128], raw[48:96])

	return uncompressed, nil
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
