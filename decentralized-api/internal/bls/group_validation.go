package bls

import (
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"

	"crypto/sha256"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
	"golang.org/x/crypto/sha3"
)

const (
	validatorLogTag = "BLS Group Key Validator: "
)

// GROUP KEY VALIDATION METHODS - All methods operate on BlsManager

// ProcessGroupPublicKeyGenerated handles validation signing when a new group public key is generated
func (bm *BlsManager) ProcessGroupPublicKeyGeneratedToSign(event *chainevents.JSONRPCResponse) error {
	// Extract epochID from event
	epochIDs, ok := event.Result.Events["inference.bls.EventGroupPublicKeyGenerated.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in group public key generated event")
	}

	// Unquote the epoch_id value
	unquotedEpochID, err := utils.UnquoteEventValue(epochIDs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote epoch_id: %w", err)
	}

	newEpochID, err := strconv.ParseUint(unquotedEpochID, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse epoch_id: %w", err)
	}

	logging.Debug(validatorLogTag+"Processing group key validation", inferenceTypes.BLS, "newEpochID", newEpochID)

	// Genesis case: Epoch 1 doesn't need validation (no previous epoch)
	if newEpochID == 1 {
		logging.Info(validatorLogTag+"Skipping validation for genesis epoch", inferenceTypes.BLS, "epochID", newEpochID)
		return nil
	}

	previousEpochID := newEpochID - 1

	previousEpochResult, err := bm.GetOrRecoverVerificationResult(previousEpochID)
	if err != nil {
		logging.Warn(validatorLogTag+"Failed to get previous epoch result, skipping validation", inferenceTypes.BLS,
			"newEpochID", newEpochID,
			"previousEpochID", previousEpochID,
			"error", err)
		return nil
	}
	if !previousEpochResult.IsParticipant {
		logging.Debug(validatorLogTag+"Not a participant in previous epoch, skipping validation", inferenceTypes.BLS,
			"newEpochID", newEpochID,
			"previousEpochID", previousEpochID)
		return nil
	}

	// Extract new group public key from event
	groupPublicKeyStrs, ok := event.Result.Events["inference.bls.EventGroupPublicKeyGenerated.group_public_key"]
	if !ok || len(groupPublicKeyStrs) == 0 {
		return fmt.Errorf("group_public_key not found in event")
	}

	// Unquote and decode the group public key
	unquotedGroupPublicKey, err := utils.UnquoteEventValue(groupPublicKeyStrs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote group_public_key: %w", err)
	}

	// The group public key should be base64 encoded
	groupPublicKeyBytes, err := utils.DecodeBase64IfPossible(unquotedGroupPublicKey)
	if err != nil {
		return fmt.Errorf("failed to decode group public key: %w", err)
	}

	if len(groupPublicKeyBytes) != 96 {
		return fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKeyBytes))
	}

	// Extract chain ID from event
	chainIDs, ok := event.Result.Events["inference.bls.EventGroupPublicKeyGenerated.chain_id"]
	if !ok || len(chainIDs) == 0 {
		return fmt.Errorf("chain_id not found in group public key generated event")
	}
	chainID, err := utils.UnquoteEventValue(chainIDs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote chain_id: %w", err)
	}

	// Compute the validation message hash
	messageHash, err := bm.computeValidationMessageHash(groupPublicKeyBytes, previousEpochID, newEpochID, chainID)
	if err != nil {
		return fmt.Errorf("failed to compute validation message hash: %w", err)
	}

	// Create partial signature using previous epoch slot shares
	partialSignature, slotIndices, err := bm.createPartialSignature(messageHash, previousEpochResult)
	if err != nil {
		return fmt.Errorf("failed to create partial signature: %w", err)
	}

	// Submit the group key validation signature
	msg := &blstypes.MsgSubmitGroupKeyValidationSignature{
		Creator:          bm.cosmosClient.GetAddress(),
		NewEpochId:       newEpochID,
		SlotIndices:      slotIndices,
		PartialSignature: partialSignature,
	}

	err = bm.cosmosClient.SubmitGroupKeyValidationSignature(msg)
	if err != nil {
		return fmt.Errorf("failed to submit group key validation signature: %w", err)
	}

	logging.Info(validatorLogTag+"Successfully submitted group key validation signature", inferenceTypes.BLS,
		"newEpochID", newEpochID,
		"previousEpochID", previousEpochID,
		"slotIndices", slotIndices)

	return nil
}

// computeValidationMessageHash computes the validation message hash using the same format as the chain
// Format: abi.encodePacked(previous_epoch_id (8 BE), sha256(chain_id) (32), new_group_key_uncompressed_256 (X.c0||X.c1||Y.c0||Y.c1; each 64-byte BE limb))
func (bm *BlsManager) computeValidationMessageHash(groupPublicKey []byte, previousEpochID, newEpochID uint64, chainID string) ([]byte, error) {
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}

	// Decompress 96-byte compressed G2 key
	var g2 bls12381.G2Affine
	if err := g2.Unmarshal(groupPublicKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal compressed G2 key: %w", err)
	}

	// Use sha256(chainID) to form 32-byte chain id (matches keeper)
	gonkaIdHash := sha256.Sum256([]byte(chainID))
	chainIdBytes := gonkaIdHash[:]

	// Implement Ethereum-compatible abi.encodePacked
	var encodedData []byte

	// Add previous_epoch_id (uint64 -> 8 bytes big endian)
	previousEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(previousEpochBytes, previousEpochID)
	encodedData = append(encodedData, previousEpochBytes...)

	// Add chain_id (32 bytes)
	encodedData = append(encodedData, chainIdBytes...)

	// Append uncompressed G2 (256 bytes): X.c0||X.c1||Y.c0||Y.c1, each 64-byte big-endian (48-byte left-padded)
	appendFp64 := func(e fp.Element) {
		be48 := e.Bytes()
		var limb [64]byte
		copy(limb[64-48:], be48[:])
		encodedData = append(encodedData, limb[:]...)
	}
	appendFp64(g2.X.A0) // c0
	appendFp64(g2.X.A1) // c1
	appendFp64(g2.Y.A0) // c0
	appendFp64(g2.Y.A1) // c1

	// Compute keccak256 hash (Ethereum-compatible)
	hash := sha3.NewLegacyKeccak256()
	hash.Write(encodedData)
	return hash.Sum(nil), nil
}

// createPartialSignature creates per-slot BLS partial signatures for the validation message.
// Returns a concatenation of 48-byte compressed G1 signatures (one per slot, in SlotIndices order),
// and the corresponding absolute SlotIndices.
func (bm *BlsManager) createPartialSignature(messageHash []byte, previousEpochResult *VerificationResult) ([]byte, []uint32, error) {
	if len(previousEpochResult.AggregatedShares) == 0 {
		return nil, nil, fmt.Errorf("no aggregated shares available for previous epoch")
	}

	// Hash message to G1 point
	messageG1, err := bm.hashToG1(messageHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to hash message to G1: %w", err)
	}

	// Create slot indices array for our assigned slots (absolute slot IDs)
	slotIndices := make([]uint32, 0, int(previousEpochResult.SlotRange[1]-previousEpochResult.SlotRange[0]+1))
	for abs := previousEpochResult.SlotRange[0]; abs <= previousEpochResult.SlotRange[1]; abs++ {
		slotIndices = append(slotIndices, abs)
	}

	// For each relative slot offset in our range, compute signing key using consensus ValidDealers,
	// then compute per-slot partial signature: sig_rel = (sum_dealers share[rel]) * H
	var concatenated []byte
	for rel := 0; rel < len(slotIndices); rel++ {
		// Sum shares across dealers using consensus ValidDealers mask, falling back to DealerValidity if consensus not present
		var signingKey fr.Element
		signingKey.SetZero()
		for dealerIdx := 0; dealerIdx < len(previousEpochResult.DealerShares); dealerIdx++ {
			// Check consensus valid dealers if available, otherwise skip if dealer shares missing
			isValidDealer := true
			if len(previousEpochResult.ValidDealers) == len(previousEpochResult.DealerShares) {
				isValidDealer = previousEpochResult.ValidDealers[dealerIdx]
			}
			if !isValidDealer {
				continue
			}
			shares := previousEpochResult.DealerShares[dealerIdx]
			if len(shares) == 0 || rel >= len(shares) {
				continue
			}
			signingKey.Add(&signingKey, &shares[rel])
		}
		// Compute partial signature for this slot offset: signature = signingKey * H
		var partialSignature bls12381.G1Affine
		partialSignature.ScalarMultiplication(&messageG1, signingKey.BigInt(new(big.Int)))
		// Append compressed 48-byte signature
		sigBytes := partialSignature.Bytes()
		concatenated = append(concatenated, sigBytes[:]...)
	}

	return concatenated, slotIndices, nil
}

// hashToG1 converts a 32-byte hash to a G1 point using the same method as the chain:
// Left-pad to 48-byte fp.Element, SWU map, isogeny, and clear cofactor.
func (bm *BlsManager) hashToG1(hash []byte) (bls12381.G1Affine, error) {
	var out bls12381.G1Affine
	if len(hash) != 32 {
		return out, fmt.Errorf("message hash must be 32 bytes, got %d", len(hash))
	}
	var be [48]byte
	copy(be[48-32:], hash)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&p.X, &p.Y)
	out.ClearCofactor(&p)
	return out, nil
}
