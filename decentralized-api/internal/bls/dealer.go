// Package bls_dkg implements Distributed Key Generation (DKG) for BLS signatures
//
// Using github.com/consensys/gnark-crypto for Ethereum-compatible BLS12-381 implementation
// - Production-ready with audit reports
// - Excellent performance and active maintenance
// - Full compliance with IETF BLS standards
//
// Example integration:
// import (
//     "github.com/Consensys/gnark-crypto/ecc/bls12-381"
//     "github.com/Consensys/gnark-crypto/ecc/bls12-381/fr"
// )

package bls

import (
	"crypto/rand"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/cosmos/cosmos-sdk/crypto/ecies"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// DEALER METHODS - All methods operate on BlsManager

// ProcessKeyGenerationInitiated handles the EventKeyGenerationInitiated event
func (bm *BlsManager) ProcessKeyGenerationInitiated(event *chainevents.JSONRPCResponse) error {
	// Extract event data from chain event (typed event from EmitTypedEvent)
	epochIDs, ok := event.Result.Events["inference.bls.EventKeyGenerationInitiated.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in key generation initiated event")
	}

	// Unquote the epoch_id value (handles JSON-encoded strings like "\"1\"")
	unquotedEpochID, err := utils.UnquoteEventValue(epochIDs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote epoch_id: %w", err)
	}

	epochID, err := strconv.ParseUint(unquotedEpochID, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse epoch_id: %w", err)
	}

	totalSlotsStrs, ok := event.Result.Events["inference.bls.EventKeyGenerationInitiated.i_total_slots"]
	if !ok || len(totalSlotsStrs) == 0 {
		return fmt.Errorf("i_total_slots not found in event")
	}

	// Unquote the total_slots value
	unquotedTotalSlots, err := utils.UnquoteEventValue(totalSlotsStrs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote i_total_slots: %w", err)
	}

	totalSlots, err := strconv.ParseUint(unquotedTotalSlots, 10, 32)
	if err != nil {
		return fmt.Errorf("failed to parse i_total_slots: %w", err)
	}

	tDegreesStrs, ok := event.Result.Events["inference.bls.EventKeyGenerationInitiated.t_slots_degree"]
	if !ok || len(tDegreesStrs) == 0 {
		return fmt.Errorf("t_slots_degree not found in event")
	}

	// Unquote the t_slots_degree value
	unquotedTDegree, err := utils.UnquoteEventValue(tDegreesStrs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote t_slots_degree: %w", err)
	}

	tDegree, err := strconv.ParseUint(unquotedTDegree, 10, 32)
	if err != nil {
		return fmt.Errorf("failed to parse t_slots_degree: %w", err)
	}

	logging.Debug("Processing DKG key generation initiated", inferenceTypes.BLS,
		"epochID", epochID, "totalSlots", totalSlots, "tDegree", tDegree, "dealer", bm.cosmosClient.GetAddress())

	// Parse participants from event
	participants, err := bm.parseParticipantsFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse participants: %w", err)
	}

	// Check if this node is a participant
	isParticipant := false
	for _, participant := range participants {
		if participant.Address == bm.cosmosClient.GetAddress() {
			isParticipant = true
			break
		}
	}

	if !isParticipant {
		logging.Debug("Not a participant in this DKG round", inferenceTypes.BLS,
			"epochID", epochID, "address", bm.cosmosClient.GetAddress())
		return nil
	}

	logging.Debug("This node is a participant in DKG", inferenceTypes.BLS,
		"epochID", epochID, "participantCount", len(participants))

	// Generate dealer part
	dealerPart, err := bm.generateDealerPart(epochID, uint32(totalSlots), uint32(tDegree), participants)
	if err != nil {
		return fmt.Errorf("failed to generate dealer part: %w", err)
	}

	// Submit dealer part to chain
	err = bm.cosmosClient.SubmitDealerPart(dealerPart)
	if err != nil {
		return fmt.Errorf("failed to submit dealer part: %w", err)
	}

	logging.Info("Successfully submitted dealer part", inferenceTypes.BLS,
		"epochID", epochID, "dealer", bm.cosmosClient.GetAddress())

	return nil
}

// parseParticipantsFromEvent extracts participant information from the event
func (bm *BlsManager) parseParticipantsFromEvent(event *chainevents.JSONRPCResponse) ([]ParticipantInfo, error) {
	// Get the participants field - this should be a JSON-encoded array
	participantStrs, ok := event.Result.Events["inference.bls.EventKeyGenerationInitiated.participants"]
	if !ok || len(participantStrs) == 0 {
		return nil, fmt.Errorf("participants not found in event")
	}

	// The participants field should be a JSON-encoded array of BLSParticipantInfo objects
	// First, unquote the JSON string if it's quoted
	unquotedParticipants, err := utils.UnquoteEventValue(participantStrs[0])
	if err != nil {
		return nil, fmt.Errorf("failed to unquote participants: %w", err)
	}

	// Parse the JSON array into BLSParticipantInfo objects
	var blsParticipants []types.BLSParticipantInfo
	err = json.Unmarshal([]byte(unquotedParticipants), &blsParticipants)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal participants JSON: %w", err)
	}

	if len(blsParticipants) == 0 {
		return nil, fmt.Errorf("no participants found in event")
	}

	// Convert BLSParticipantInfo to ParticipantInfo
	participants := make([]ParticipantInfo, len(blsParticipants))
	for i, blsParticipant := range blsParticipants {
		participants[i] = ParticipantInfo{
			Address:            blsParticipant.Address,
			Secp256K1PublicKey: blsParticipant.Secp256K1PublicKey,
			SlotStartIndex:     blsParticipant.SlotStartIndex,
			SlotEndIndex:       blsParticipant.SlotEndIndex,
		}

		logging.Debug("Parsed participant from event", inferenceTypes.BLS,
			"index", i, "address", blsParticipant.Address,
			"slotStart", blsParticipant.SlotStartIndex,
			"slotEnd", blsParticipant.SlotEndIndex)
	}

	logging.Info("Successfully parsed participants from event", inferenceTypes.BLS,
		"participantCount", len(participants))

	return participants, nil
}

// generateDealerPart generates the dealer's contribution to the DKG
func (bm *BlsManager) generateDealerPart(epochID uint64, totalSlots, tDegree uint32, participants []ParticipantInfo) (*types.MsgSubmitDealerPart, error) {
	logging.Debug("Generating dealer part", inferenceTypes.BLS,
		"epochID", epochID, "totalSlots", totalSlots, "tDegree", tDegree, "participantCount", len(participants))

	// Generate secret BLS polynomial Poly_k(x) of degree tDegree
	polynomial, err := generateRandomPolynomial(tDegree)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random polynomial: %w", err)
	}

	// Compute public commitments to coefficients (C_kj = g * a_kj, G2 points)
	commitments := computeG2Commitments(polynomial)

	// Create encrypted shares for participants using deterministic array indexing
	encryptedSharesForParticipants := make([]types.EncryptedSharesForParticipant, len(participants))
	for i, participant := range participants {
		// Get all allowed public keys for this participant (including warm keys)
		allowedPubKeys, err := bm.getAllowedPubKeysForParticipant(participant.Address)
		if err != nil {
			return nil, fmt.Errorf("failed to get allowed public keys for participant %s: %w",
				participant.Address, err)
		}

		// Calculate number of slots for this participant
		numSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1

		// For warm keys: store multiple encryptions per slot consecutively
		// Total ciphertexts = numSlots * numKeys
		totalCiphertexts := numSlots * uint32(len(allowedPubKeys))
		encryptedShares := make([][]byte, totalCiphertexts)
		ciphertextIndex := 0

		for slotOffset := uint32(0); slotOffset < numSlots; slotOffset++ {
			slotIndex := participant.SlotStartIndex + slotOffset

			// Compute scalar share share_ki = Poly_k(slotIndex+1) to match chain's x-domain
			share := evaluatePolynomial(polynomial, slotIndex+1)
			shareBytes := share.Marshal()

			// Encrypt the same share for all allowed public keys
			for keyIndex, pubKeyBase64 := range allowedPubKeys {
				// Convert base64 public key to secp256k1 bytes
				pubKeyBytes, err := bm.convertPubKeyToSecp256k1Bytes(pubKeyBase64)
				if err != nil {
					logging.Debug("Failed to convert public key, skipping", inferenceTypes.BLS,
						"participant", participant.Address, "keyIndex", keyIndex,
						"pubKeyBase64", pubKeyBase64, "error", err)
					continue
				}

				// Encrypt share using this public key
				encryptedShare, err := encryptForParticipant(shareBytes, pubKeyBytes)
				if err != nil {
					logging.Warn("Failed to encrypt share for public key, skipping", inferenceTypes.BLS,
						"participant", participant.Address, "slotIndex", slotIndex,
						"keyIndex", keyIndex, "pubKeyBase64", pubKeyBase64, "error", err)
					continue
				}

				encryptedShares[ciphertextIndex] = encryptedShare
				ciphertextIndex++
			}
		}

		// Trim to actual successful encryptions
		encryptedShares = encryptedShares[:ciphertextIndex]

		encryptedSharesForParticipants[i] = types.EncryptedSharesForParticipant{
			EncryptedShares: encryptedShares,
		}

		logging.Debug("Generated encrypted shares for participant with warm keys", inferenceTypes.BLS,
			"participantIndex", i, "participant", participant.Address,
			"slotStart", participant.SlotStartIndex, "slotEnd", participant.SlotEndIndex,
			"numSlots", numSlots, "allowedKeys", len(allowedPubKeys),
			"totalCiphertexts", len(encryptedShares))
	}

	dealerPart := &types.MsgSubmitDealerPart{
		Creator:                        bm.cosmosClient.GetAddress(),
		EpochId:                        epochID,
		Commitments:                    commitments,
		EncryptedSharesForParticipants: encryptedSharesForParticipants,
	}

	logging.Info("Generated dealer part with actual cryptography", inferenceTypes.BLS,
		"epochID", epochID, "commitmentsCount", len(commitments),
		"participantsCount", len(encryptedSharesForParticipants),
		"note", "Using gnark-crypto for BLS12-381 cryptography")

	return dealerPart, nil
}

// BLS CRYPTOGRAPHY FUNCTIONS using gnark-crypto

// generateRandomPolynomial generates random polynomial coefficients for BLS DKG
func generateRandomPolynomial(degree uint32) ([]*fr.Element, error) {
	coefficients := make([]*fr.Element, degree+1)
	for i := uint32(0); i <= degree; i++ {
		coeff := new(fr.Element)
		_, err := coeff.SetRandom()
		if err != nil {
			return nil, fmt.Errorf("failed to generate random coefficient %d: %w", i, err)
		}
		coefficients[i] = coeff
	}
	return coefficients, nil
}

// computeG2Commitments computes G2 commitments for polynomial coefficients
func computeG2Commitments(coefficients []*fr.Element) [][]byte {
	commitments := make([][]byte, len(coefficients))

	// Get the BLS12-381 G2 generator (4th return value is G2Affine)
	_, _, _, g2Gen := bls12381.Generators()

	for i, coeff := range coefficients {
		var commitment bls12381.G2Affine
		// Convert fr.Element to big.Int for scalar multiplication
		coeffBigInt := new(big.Int)
		coeff.BigInt(coeffBigInt)
		commitment.ScalarMultiplication(&g2Gen, coeffBigInt)
		// Use compressed format (96 bytes) instead of uncompressed (192 bytes)
		// This is more efficient for blockchain storage and network transmission
		compressedBytes := commitment.Bytes() // Returns [96]byte
		commitments[i] = compressedBytes[:]   // Convert to []byte slice
	}
	return commitments
}

// evaluatePolynomial evaluates polynomial at given x using Horner's method
func evaluatePolynomial(polynomial []*fr.Element, x uint32) *fr.Element {
	if len(polynomial) == 0 {
		return new(fr.Element).SetZero()
	}

	// Convert x to fr.Element
	xFr := new(fr.Element).SetUint64(uint64(x))

	// Start with highest degree coefficient
	result := new(fr.Element).Set(polynomial[len(polynomial)-1])

	// Apply Horner's method: result = result * x + coeff[i]
	for i := len(polynomial) - 2; i >= 0; i-- {
		result.Mul(result, xFr)
		result.Add(result, polynomial[i])
	}

	return result
}

// encryptForParticipant encrypts data for a specific participant using Cosmos-compatible ECIES
// This uses the same go-ethereum ECIES implementation that the modified Cosmos keyring uses
func encryptForParticipant(data []byte, secp256k1PubKeyBytes []byte) ([]byte, error) {
	// Validate the compressed secp256k1 public key format
	// (33 bytes: 0x02 or 0x03 + 32 bytes X)
	if len(secp256k1PubKeyBytes) != 33 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key format, expected 33 bytes, got %d bytes", len(secp256k1PubKeyBytes))
	}
	// Check for valid prefix (0x02 or 0x03)
	if secp256k1PubKeyBytes[0] != 0x02 && secp256k1PubKeyBytes[0] != 0x03 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key prefix, expected 0x02 or 0x03, got 0x%x", secp256k1PubKeyBytes[0])
	}

	// Use Decred secp256k1 to parse the compressed key bytes into a secp256k1.PublicKey
	pubKey, err := secp256k1.ParsePubKey(secp256k1PubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse secp256k1 public key: %w", err)
	}

	// Convert secp256k1.PublicKey to *ecdsa.PublicKey
	ecdsaPubKey := pubKey.ToECDSA()

	// Convert *ecdsa.PublicKey to *ecies.PublicKey using the same method as Cosmos keyring
	eciesPubKey := ecies.ImportECDSAPublic(ecdsaPubKey)

	// Encrypt the data using the same method as the modified Cosmos keyring
	// This ensures compatibility: dealer encryption → keyring decryption
	ciphertext, err := ecies.Encrypt(rand.Reader, eciesPubKey, data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("ECIES encryption failed: %w", err)
	}

	return ciphertext, nil
}

// getAllowedPubKeysForParticipant gets all allowed public keys for a participant (including warm keys via Authz)
func (bm *BlsManager) getAllowedPubKeysForParticipant(participantAddress string) ([]string, error) {
	queryClient := bm.cosmosClient.NewInferenceQueryClient()

	// Get all grantees (warm keys) allowed to act on behalf of this participant
	grantees, err := queryClient.GranteesByMessageType(bm.ctx, &inferenceTypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: participantAddress,
		MessageTypeUrl: "/inference.bls.MsgSubmitDealerPart", // BLS operations
	})
	if err != nil {
		// If querying grantees fails, log warning but continue with just the participant's own key
		logging.Warn("Failed to get grantees for participant, using only participant's own key", inferenceTypes.BLS,
			"participant", participantAddress, "error", err)
		grantees = &inferenceTypes.QueryGranteesByMessageTypeResponse{Grantees: []*inferenceTypes.Grantee{}}
	}

	// Get the participant's own public key
	participant, err := queryClient.InferenceParticipant(bm.ctx, &inferenceTypes.QueryInferenceParticipantRequest{
		Address: participantAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get participant's own public key: %w", err)
	}

	// Collect all allowed public keys (participant's own + warm keys)
	allowedPubKeys := make([]string, 0, len(grantees.Grantees)+1)

	// Add participant's own public key first
	allowedPubKeys = append(allowedPubKeys, participant.Pubkey)

	// Add all warm key public keys
	for _, grantee := range grantees.Grantees {
		allowedPubKeys = append(allowedPubKeys, grantee.PubKey)
	}

	logging.Debug("Retrieved allowed public keys for participant", inferenceTypes.BLS,
		"participant", participantAddress,
		"ownKey", participant.Pubkey,
		"warmKeysCount", len(grantees.Grantees),
		"totalKeys", len(allowedPubKeys))

	return allowedPubKeys, nil
}

// convertPubKeyToSecp256k1Bytes converts a base64-encoded public key string to secp256k1 bytes
func (bm *BlsManager) convertPubKeyToSecp256k1Bytes(pubKeyBase64 string) ([]byte, error) {
	// Decode base64-encoded public key
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 public key: %w", err)
	}

	// Validate secp256k1 compressed format (33 bytes)
	if len(pubKeyBytes) != 33 {
		return nil, fmt.Errorf("invalid secp256k1 public key length: expected 33 bytes, got %d", len(pubKeyBytes))
	}

	return pubKeyBytes, nil
}

// All BLS cryptographic functions have been implemented above using gnark-crypto
