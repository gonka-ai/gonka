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

package bls_dkg

import (
	"crypto/rand"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"

	"github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

// Dealer handles the dealing phase of BLS DKG
type Dealer struct {
	cosmosClient cosmosclient.CosmosMessageClient
	address      string
}

// NewDealer creates a new dealer instance
func NewDealer(cosmosClient cosmosclient.CosmosMessageClient) *Dealer {
	return &Dealer{
		cosmosClient: cosmosClient,
		address:      cosmosClient.GetAddress(),
	}
}

// ProcessKeyGenerationInitiated handles the EventKeyGenerationInitiated event
func (d *Dealer) ProcessKeyGenerationInitiated(event *chainevents.JSONRPCResponse) error {
	// Extract event data from chain event
	epochIDs, ok := event.Result.Events["key_generation_initiated.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in key generation initiated event")
	}

	epochID, err := strconv.ParseUint(epochIDs[0], 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse epoch_id: %w", err)
	}

	totalSlotsStrs, ok := event.Result.Events["key_generation_initiated.i_total_slots"]
	if !ok || len(totalSlotsStrs) == 0 {
		return fmt.Errorf("i_total_slots not found in event")
	}

	totalSlots, err := strconv.ParseUint(totalSlotsStrs[0], 10, 32)
	if err != nil {
		return fmt.Errorf("failed to parse i_total_slots: %w", err)
	}

	tDegreesStrs, ok := event.Result.Events["key_generation_initiated.t_slots_degree"]
	if !ok || len(tDegreesStrs) == 0 {
		return fmt.Errorf("t_slots_degree not found in event")
	}

	tDegree, err := strconv.ParseUint(tDegreesStrs[0], 10, 32)
	if err != nil {
		return fmt.Errorf("failed to parse t_slots_degree: %w", err)
	}

	logging.Info("Processing DKG key generation initiated", inferenceTypes.System,
		"epochID", epochID, "totalSlots", totalSlots, "tDegree", tDegree, "dealer", d.address)

	// Parse participants from event
	participants, err := d.parseParticipantsFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse participants: %w", err)
	}

	// Check if this node is a participant
	isParticipant := false
	for _, participant := range participants {
		if participant.Address == d.address {
			isParticipant = true
			break
		}
	}

	if !isParticipant {
		logging.Debug("Not a participant in this DKG round", inferenceTypes.System,
			"epochID", epochID, "address", d.address)
		return nil
	}

	logging.Info("This node is a participant in DKG", inferenceTypes.System,
		"epochID", epochID, "participantCount", len(participants))

	// Generate dealer part
	dealerPart, err := d.generateDealerPart(epochID, uint32(totalSlots), uint32(tDegree), participants)
	if err != nil {
		return fmt.Errorf("failed to generate dealer part: %w", err)
	}

	// Submit dealer part to chain
	err = d.cosmosClient.SubmitDealerPart(dealerPart)
	if err != nil {
		return fmt.Errorf("failed to submit dealer part: %w", err)
	}

	logging.Info("Successfully submitted dealer part", inferenceTypes.System,
		"epochID", epochID, "dealer", d.address)

	return nil
}

// parseParticipantsFromEvent extracts participant information from the event
func (d *Dealer) parseParticipantsFromEvent(event *chainevents.JSONRPCResponse) ([]ParticipantInfo, error) {
	// Parse participant count first
	participantCountStrs, ok := event.Result.Events["key_generation_initiated.participants"]
	if !ok {
		return nil, fmt.Errorf("participants not found in event")
	}

	participantCount := len(participantCountStrs)
	if participantCount == 0 {
		return nil, fmt.Errorf("no participants found in event")
	}

	participants := make([]ParticipantInfo, 0, participantCount)

	// Parse each participant's data
	for i := 0; i < participantCount; i++ {
		// Parse address
		addressKey := fmt.Sprintf("key_generation_initiated.participants.%d.address", i)
		addresses, ok := event.Result.Events[addressKey]
		if !ok || len(addresses) == 0 {
			return nil, fmt.Errorf("participant %d address not found", i)
		}

		// Parse secp256k1 public key
		pubKeyKey := fmt.Sprintf("key_generation_initiated.participants.%d.secp256k1_public_key", i)
		pubKeyStrs, ok := event.Result.Events[pubKeyKey]
		if !ok || len(pubKeyStrs) == 0 {
			return nil, fmt.Errorf("participant %d secp256k1_public_key not found", i)
		}

		// Decode base64 public key
		pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyStrs[0])
		if err != nil {
			return nil, fmt.Errorf("failed to decode participant %d public key: %w", i, err)
		}

		// Parse slot start index
		slotStartKey := fmt.Sprintf("key_generation_initiated.participants.%d.slot_start_index", i)
		slotStartStrs, ok := event.Result.Events[slotStartKey]
		if !ok || len(slotStartStrs) == 0 {
			return nil, fmt.Errorf("participant %d slot_start_index not found", i)
		}

		slotStart, err := strconv.ParseUint(slotStartStrs[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse participant %d slot_start_index: %w", i, err)
		}

		// Parse slot end index
		slotEndKey := fmt.Sprintf("key_generation_initiated.participants.%d.slot_end_index", i)
		slotEndStrs, ok := event.Result.Events[slotEndKey]
		if !ok || len(slotEndStrs) == 0 {
			return nil, fmt.Errorf("participant %d slot_end_index not found", i)
		}

		slotEnd, err := strconv.ParseUint(slotEndStrs[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse participant %d slot_end_index: %w", i, err)
		}

		participants = append(participants, ParticipantInfo{
			Address:            addresses[0],
			Secp256k1PublicKey: pubKeyBytes,
			SlotStartIndex:     uint32(slotStart),
			SlotEndIndex:       uint32(slotEnd),
		})

		logging.Debug("Parsed participant from event", inferenceTypes.System,
			"index", i, "address", addresses[0], "slotStart", slotStart, "slotEnd", slotEnd)
	}

	logging.Info("Successfully parsed participants from event", inferenceTypes.System,
		"participantCount", len(participants))

	return participants, nil
}

// ParticipantInfo represents participant information for DKG
type ParticipantInfo struct {
	Address            string
	Secp256k1PublicKey []byte
	SlotStartIndex     uint32
	SlotEndIndex       uint32
}

// generateDealerPart generates the dealer's contribution to the DKG
func (d *Dealer) generateDealerPart(epochID uint64, totalSlots, tDegree uint32, participants []ParticipantInfo) (*types.MsgSubmitDealerPart, error) {
	logging.Info("Generating dealer part", inferenceTypes.System,
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
		// Calculate number of slots for this participant
		numSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1
		encryptedShares := make([][]byte, numSlots)

		for slotOffset := uint32(0); slotOffset < numSlots; slotOffset++ {
			slotIndex := participant.SlotStartIndex + slotOffset

			// Compute scalar share share_ki = Poly_k(slotIndex)
			share := evaluatePolynomial(polynomial, slotIndex)

			// Encrypt share_ki using participant.Secp256k1PublicKey with ECIES
			shareBytes := share.Marshal()
			encryptedShare, err := eciesEncrypt(shareBytes, participant.Secp256k1PublicKey)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt share for participant %s slot %d: %w",
					participant.Address, slotIndex, err)
			}
			encryptedShares[slotOffset] = encryptedShare
		}

		encryptedSharesForParticipants[i] = types.EncryptedSharesForParticipant{
			EncryptedShares: encryptedShares,
		}

		logging.Debug("Generated encrypted shares for participant", inferenceTypes.System,
			"participantIndex", i, "participant", participant.Address,
			"slotStart", participant.SlotStartIndex, "slotEnd", participant.SlotEndIndex,
			"numShares", len(encryptedShares))
	}

	dealerPart := &types.MsgSubmitDealerPart{
		Creator:                        d.address,
		EpochId:                        epochID,
		Commitments:                    commitments,
		EncryptedSharesForParticipants: encryptedSharesForParticipants,
	}

	logging.Info("Generated dealer part with actual cryptography", inferenceTypes.System,
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

// eciesEncrypt encrypts data using ECIES with secp256k1 public key
func eciesEncrypt(data []byte, secp256k1PubKeyBytes []byte) ([]byte, error) {
	// Validate the compressed secp256k1 public key format
	// (33 bytes: 0x02 or 0x03 + 32 bytes X)
	if len(secp256k1PubKeyBytes) != 33 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key format, expected 33 bytes, got %d bytes", len(secp256k1PubKeyBytes))
	}
	// Check for valid prefix (0x02 or 0x03)
	if secp256k1PubKeyBytes[0] != 0x02 && secp256k1PubKeyBytes[0] != 0x03 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key prefix, expected 0x02 or 0x03, got 0x%x", secp256k1PubKeyBytes[0])
	}

	// Use crypto.DecompressPubkey to parse the compressed key bytes into an ecdsa.PublicKey
	pubKey, err := crypto.DecompressPubkey(secp256k1PubKeyBytes) // pubKey is *ecdsa.PublicKey
	if err != nil {
		return nil, fmt.Errorf("failed to decompress secp256k1 public key: %w", err)
	}

	// Convert *ecdsa.PublicKey to *ecies.PublicKey
	eciesPubKey := ecies.ImportECDSAPublic(pubKey)

	// Encrypt the data.
	// ecies.ImportECDSAPublic sets default ECIES parameters (e.g., ECIES_AES128_SHA256).
	ciphertext, err := ecies.Encrypt(rand.Reader, eciesPubKey, data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("ECIES encryption failed: %w", err)
	}

	return ciphertext, nil
}

// All BLS cryptographic functions have been implemented above using gnark-crypto
