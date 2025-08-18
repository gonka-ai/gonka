package bls

import (
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
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

	// Check if we participated in the previous epoch
	previousEpochResult := bm.GetVerificationResult(previousEpochID)
	if previousEpochResult == nil || !previousEpochResult.IsParticipant {
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
// Format: abi.encodePacked(previous_epoch_id, chain_id, new_epoch_id, data[0], data[1], data[2])
func (bm *BlsManager) computeValidationMessageHash(groupPublicKey []byte, previousEpochID, newEpochID uint64, chainID string) ([]byte, error) {
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}

	chainIdBytes := make([]byte, 32)
	copy(chainIdBytes[32-len(chainID):], []byte(chainID)) // Right-pad with zeros

	// Implement Ethereum-compatible abi.encodePacked
	var encodedData []byte

	// Add previous_epoch_id (uint64 -> 8 bytes big endian)
	previousEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(previousEpochBytes, previousEpochID)
	encodedData = append(encodedData, previousEpochBytes...)

	// Add chain_id (32 bytes)
	encodedData = append(encodedData, chainIdBytes...)

	// Note: Removed new_epoch_id from hash as it doesn't provide additional security
	// Format: abi.encodePacked(previous_epoch_id, chain_id, data[0], data[1], data[2])

	// Add data[0] (first 32 bytes of group public key)
	encodedData = append(encodedData, groupPublicKey[0:32]...)

	// Add data[1] (second 32 bytes of group public key)
	encodedData = append(encodedData, groupPublicKey[32:64]...)

	// Add data[2] (last 32 bytes of group public key)
	encodedData = append(encodedData, groupPublicKey[64:96]...)

	// Compute keccak256 hash (Ethereum-compatible)
	hash := sha3.NewLegacyKeccak256()
	hash.Write(encodedData)
	return hash.Sum(nil), nil
}

// createPartialSignature creates a BLS partial signature for the validation message
func (bm *BlsManager) createPartialSignature(messageHash []byte, previousEpochResult *VerificationResult) ([]byte, []uint32, error) {
	if len(previousEpochResult.AggregatedShares) == 0 {
		return nil, nil, fmt.Errorf("no aggregated shares available for previous epoch")
	}

	// Hash message to G1 point
	messageG1, err := bm.hashToG1(messageHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to hash message to G1: %w", err)
	}

	// Create slot indices array for our assigned slots
	slotIndices := make([]uint32, 0)
	for i := previousEpochResult.SlotRange[0]; i <= previousEpochResult.SlotRange[1]; i++ {
		slotIndices = append(slotIndices, i)
	}

	// Initialize aggregated signature as G1 identity
	var aggregatedSignature bls12381.G1Affine

	// For each slot in our range, compute partial signature and aggregate
	for slotOffset := range slotIndices {
		if slotOffset >= len(previousEpochResult.AggregatedShares) {
			return nil, nil, fmt.Errorf("slot offset %d exceeds available aggregated shares %d", slotOffset, len(previousEpochResult.AggregatedShares))
		}

		// Get the slot share for this slot
		slotShare := &previousEpochResult.AggregatedShares[slotOffset]

		// Compute partial signature: signature = slotShare * messageG1
		var partialSignature bls12381.G1Affine
		partialSignature.ScalarMultiplication(&messageG1, slotShare.BigInt(new(big.Int)))

		// Add to aggregated signature
		aggregatedSignature.Add(&aggregatedSignature, &partialSignature)
	}

	// Return compressed signature bytes
	signatureBytes := aggregatedSignature.Bytes()
	return signatureBytes[:], slotIndices, nil
}

// hashToG1 converts a hash to a G1 point using the same method as the chain
func (bm *BlsManager) hashToG1(hash []byte) (bls12381.G1Affine, error) {
	var result bls12381.G1Affine

	// Try up to 256 attempts to find a valid point (same as chain implementation)
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
		if bm.trySetFromHash(&result, attempt) {
			return result, nil
		}
	}

	return result, fmt.Errorf("failed to hash to G1 point after 256 attempts")
}

// trySetFromHash attempts to create a valid G1 point from a hash (same as chain implementation)
func (bm *BlsManager) trySetFromHash(point *bls12381.G1Affine, hash []byte) bool {
	// Create field element from hash
	var x fr.Element
	x.SetBytes(hash)

	// Use the hash as a scalar multiplier with the generator
	g1GenJac, _, _, _ := bls12381.Generators()
	var g1Gen bls12381.G1Affine
	g1Gen.FromJacobian(&g1GenJac)

	// Multiply generator by the scalar derived from hash
	scalar := x.BigInt(new(big.Int))
	point.ScalarMultiplication(&g1Gen, scalar)

	// Check if the point is valid (always true for scalar multiplication of generator)
	return !point.IsInfinity()
}
