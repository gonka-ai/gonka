package keeper

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/cosmos/cosmos-sdk/crypto/ecies"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
)

const (
	dkgOpeningSeedLen = 32
	dkgShareBytesLen  = 32
)

// applyDealerComplaintOutcomes applies complaint outcomes to the candidate valid dealer set.
// Current policy:
// - missing/invalid dealer response => dealer fault (dealer removed)
// - valid dealer response => false complaint (complainer removed if in candidate set)
func (k Keeper) applyDealerComplaintOutcomes(epochBLSData *types.EpochBLSData, candidateValidDealers []bool) ([]bool, error) {
	finalValidDealers := make([]bool, len(candidateValidDealers))
	copy(finalValidDealers, candidateValidDealers)

	dealerFaults := make(map[int]struct{})
	complainerFaults := make(map[int]struct{})

	for i := range epochBLSData.DealerComplaints {
		complaint := epochBLSData.DealerComplaints[i]
		dealerIndex := int(complaint.DealerIndex)
		complainerIndex := int(complaint.ComplainerIndex)

		if dealerIndex < 0 || dealerIndex >= len(finalValidDealers) {
			continue
		}
		if complainerIndex < 0 || complainerIndex >= len(finalValidDealers) {
			continue
		}
		if !candidateValidDealers[dealerIndex] {
			// Complaints against non-candidate dealers do not affect final set.
			continue
		}

		ok, err := k.verifyDealerComplaintResponse(epochBLSData, &complaint)
		if err != nil {
			return nil, fmt.Errorf("failed to verify complaint for dealer %d and complainer %d: %w", dealerIndex, complainerIndex, err)
		}

		if ok {
			complainerFaults[complainerIndex] = struct{}{}
		} else {
			dealerFaults[dealerIndex] = struct{}{}
		}
	}

	for dealerIndex := range dealerFaults {
		finalValidDealers[dealerIndex] = false
	}
	for complainerIndex := range complainerFaults {
		finalValidDealers[complainerIndex] = false
	}

	return finalValidDealers, nil
}

func (k Keeper) verifyDealerComplaintResponse(epochBLSData *types.EpochBLSData, complaint *types.DealerComplaint) (bool, error) {
	if complaint == nil {
		return false, nil
	}
	if !complaint.ResponseSubmitted {
		return false, nil
	}
	if len(complaint.ResponseShareBytes) != dkgShareBytesLen {
		return false, nil
	}
	if len(complaint.ResponseOpeningMaterial) != dkgOpeningSeedLen {
		return false, nil
	}

	dealerIndex := int(complaint.DealerIndex)
	complainerIndex := int(complaint.ComplainerIndex)
	if dealerIndex >= len(epochBLSData.DealerParts) || complainerIndex >= len(epochBLSData.Participants) {
		return false, nil
	}

	dealerPart := epochBLSData.DealerParts[dealerIndex]
	if dealerPart == nil {
		return false, nil
	}
	if complainerIndex >= len(dealerPart.ParticipantShares) || dealerPart.ParticipantShares[complainerIndex] == nil {
		return false, nil
	}

	participantShares := dealerPart.ParticipantShares[complainerIndex].EncryptedShares
	complainer := epochBLSData.Participants[complainerIndex]
	if complainer.SlotEndIndex < complainer.SlotStartIndex {
		return false, nil
	}
	numSlots := int(complainer.SlotEndIndex-complainer.SlotStartIndex) + 1
	if numSlots <= 0 || len(participantShares) == 0 || len(participantShares)%numSlots != 0 {
		return false, nil
	}
	keysPerSlot := len(participantShares) / numSlots
	allowedKeys := buildAllowedSecp256k1Keys(complainer)
	if keysPerSlot == 0 || keysPerSlot != len(allowedKeys) {
		return false, nil
	}

	slotIndex := complaint.DisputedSlotIndex
	ciphertextIndex := int(complaint.DisputedCiphertextIndex)
	if ciphertextIndex < 0 || ciphertextIndex >= len(participantShares) {
		return false, nil
	}
	if slotIndex < complainer.SlotStartIndex || slotIndex > complainer.SlotEndIndex {
		return false, nil
	}
	slotOffset := int(slotIndex - complainer.SlotStartIndex)
	slotStart := slotOffset * keysPerSlot
	slotEnd := slotStart + keysPerSlot
	if ciphertextIndex < slotStart || ciphertextIndex >= slotEnd {
		return false, nil
	}

	keyIndex := ciphertextIndex - slotStart
	complainerPubKey := allowedKeys[keyIndex]

	reencryptedCiphertext, err := encryptWithSeedForParticipant(complaint.ResponseShareBytes, complainerPubKey, complaint.ResponseOpeningMaterial)
	if err != nil {
		return false, nil
	}
	if !bytes.Equal(reencryptedCiphertext, participantShares[ciphertextIndex]) {
		return false, nil
	}

	share := &fr.Element{}
	share.SetBytes(complaint.ResponseShareBytes)
	validShare, err := k.verifyShareAgainstCommitmentsBlst(share, slotIndex, dealerPart.Commitments)
	if err != nil {
		return false, nil
	}
	if !validShare {
		return false, nil
	}

	return true, nil
}

func buildAllowedSecp256k1Keys(participant types.BLSParticipantInfo) [][]byte {
	keys := make([][]byte, 0, 1+len(participant.AllowedSecp256K1PublicKeys))
	keys = append(keys, participant.Secp256K1PublicKey)
	keys = append(keys, participant.AllowedSecp256K1PublicKeys...)
	return keys
}

func encryptWithSeedForParticipant(data []byte, secp256k1PubKeyBytes []byte, seed []byte) ([]byte, error) {
	if len(seed) != dkgOpeningSeedLen {
		return nil, fmt.Errorf("invalid seed length, expected %d bytes, got %d", dkgOpeningSeedLen, len(seed))
	}

	eciesPubKey, err := parseECIESPublicKeyFromCompressed(secp256k1PubKeyBytes)
	if err != nil {
		return nil, err
	}
	ciphertext, err := ecies.Encrypt(newDeterministicSeedReader(seed), eciesPubKey, data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("ECIES encryption failed: %w", err)
	}
	return ciphertext, nil
}

func parseECIESPublicKeyFromCompressed(secp256k1PubKeyBytes []byte) (*ecies.PublicKey, error) {
	if len(secp256k1PubKeyBytes) != 33 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key format, expected 33 bytes, got %d bytes", len(secp256k1PubKeyBytes))
	}
	if secp256k1PubKeyBytes[0] != 0x02 && secp256k1PubKeyBytes[0] != 0x03 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key prefix, expected 0x02 or 0x03, got 0x%x", secp256k1PubKeyBytes[0])
	}

	pubKey, err := secp256k1.ParsePubKey(secp256k1PubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse secp256k1 public key: %w", err)
	}
	return ecies.ImportECDSAPublic(pubKey.ToECDSA()), nil
}

type deterministicSeedReader struct {
	seed    []byte
	counter uint64
	buf     []byte
}

func newDeterministicSeedReader(seed []byte) io.Reader {
	seedCopy := make([]byte, len(seed))
	copy(seedCopy, seed)
	return &deterministicSeedReader{seed: seedCopy}
}

func (r *deterministicSeedReader) Read(p []byte) (int, error) {
	// Go's crypto stack may call randutil.MaybeReadByte and consume one random byte
	// unpredictably; avoid advancing state for single-byte reads to keep outputs stable.
	if len(p) == 1 {
		counter := r.counter
		buf := append([]byte(nil), r.buf...)
		n := 0
		for n < len(p) {
			if len(buf) == 0 {
				var ctr [8]byte
				binary.BigEndian.PutUint64(ctr[:], counter)
				counter++
				block := sha256.Sum256(append(append([]byte{}, r.seed...), ctr[:]...))
				buf = block[:]
			}
			copied := copy(p[n:], buf)
			buf = buf[copied:]
			n += copied
		}
		return n, nil
	}

	n := 0
	for n < len(p) {
		if len(r.buf) == 0 {
			var ctr [8]byte
			binary.BigEndian.PutUint64(ctr[:], r.counter)
			r.counter++
			block := sha256.Sum256(append(append([]byte{}, r.seed...), ctr[:]...))
			r.buf = block[:]
		}
		copied := copy(p[n:], r.buf)
		r.buf = r.buf[copied:]
		n += copied
	}
	return n, nil
}

func (k Keeper) verifyShareAgainstCommitmentsBlst(share *fr.Element, slotIndex uint32, commitments [][]byte) (bool, error) {
	if len(commitments) == 0 {
		return false, fmt.Errorf("no commitments provided")
	}

	points := make([]*blst.P2Affine, len(commitments))
	for j, cb := range commitments {
		if len(cb) != 96 {
			return false, fmt.Errorf("invalid commitment length at index %d: %d, expected 96", j, len(cb))
		}
		p := new(blst.P2Affine).Uncompress(cb)
		if p == nil {
			return false, fmt.Errorf("failed to uncompress commitment at index %d", j)
		}
		if !p.InG2() {
			return false, fmt.Errorf("commitment at index %d is not in G2 subgroup", j)
		}
		points[j] = p
	}

	slotIndexFr := &fr.Element{}
	slotIndexFr.SetUint64(uint64(slotIndex + 1))
	slotIndexPower := &fr.Element{}
	slotIndexPower.SetOne()

	scalars := make([]byte, len(commitments)*32)
	for j := 0; j < len(commitments); j++ {
		pBytes := slotIndexPower.Bytes()
		for i := 0; i < 16; i++ {
			pBytes[i], pBytes[31-i] = pBytes[31-i], pBytes[i]
		}
		copy(scalars[j*32:(j+1)*32], pBytes[:])
		slotIndexPower.Mul(slotIndexPower, slotIndexFr)
	}

	expectedCommitment := blst.P2AffinesMult(points, scalars, 255).ToAffine()
	shareBytes := share.Bytes()
	for i := 0; i < 16; i++ {
		shareBytes[i], shareBytes[31-i] = shareBytes[31-i], shareBytes[i]
	}
	actualCommitment := blst.P2Generator().Mult(shareBytes[:], 255).ToAffine()

	return actualCommitment.Equals(expectedCommitment), nil
}
