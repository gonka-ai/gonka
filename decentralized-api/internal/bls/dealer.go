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
	"common/logging"
	"crypto/rand"
	"crypto/sha256"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"

	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/cosmos/cosmos-sdk/crypto/ecies"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	blst "github.com/supranational/blst/bindings/go"
)

const dkgOpeningSeedLen = 32

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
		if isQueuedForRetry(err) {
			logging.Warn("Dealer part queued for retry", inferenceTypes.BLS,
				"epochID", epochID, "dealer", bm.cosmosClient.GetAddress(), "error", err)
			return queuedForRetryError("submit dealer part", err)
		}
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
			Address:                    blsParticipant.Address,
			Secp256K1PublicKey:         blsParticipant.Secp256K1PublicKey,
			AllowedSecp256K1PublicKeys: blsParticipant.AllowedSecp256K1PublicKeys,
			SlotStartIndex:             blsParticipant.SlotStartIndex,
			SlotEndIndex:               blsParticipant.SlotEndIndex,
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
	commitments := computeG2CommitmentsBlst(polynomial)

	// Create encrypted shares for participants using deterministic array indexing.
	encryptedSharesForParticipants := make([]types.EncryptedSharesForParticipant, len(participants))
	openingRecords := make([]dealerOpeningPersistRecord, 0)
	for i, participant := range participants {
		// Build encryption key set from on-chain participant snapshot (primary + additional).
		allowedPubKeys, err := participantAllowedPubKeys(participant)
		if err != nil {
			return nil, fmt.Errorf("failed to build allowed public keys from snapshot for participant %s: %w",
				participant.Address, err)
		}
		if len(allowedPubKeys) == 0 {
			return nil, fmt.Errorf("participant %s has no allowed public keys", participant.Address)
		}

		// Calculate number of slots for this participant
		numSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1

		// For warm keys: store multiple encryptions per slot consecutively
		// Total ciphertexts = numSlots * numKeys
		totalCiphertexts := numSlots * uint32(len(allowedPubKeys))
		encryptedShares := make([][]byte, totalCiphertexts)
		ciphertextIndex := uint32(0)

		for slotOffset := uint32(0); slotOffset < numSlots; slotOffset++ {
			slotIndex := participant.SlotStartIndex + slotOffset

			// Compute scalar share share_ki = Poly_k(slotIndex+1) to match chain's x-domain
			share := evaluatePolynomial(polynomial, slotIndex+1)
			shareBytes := share.Marshal()

			// Encrypt the same share for all allowed public keys from snapshot.
			for _, pubKeyBytes := range allowedPubKeys {
				seed := make([]byte, dkgOpeningSeedLen)
				if _, err := rand.Read(seed); err != nil {
					return nil, fmt.Errorf("failed to generate opening seed: %w", err)
				}

				// Encrypt share using deterministic seed so revealed openings can be verified on-chain.
				encryptedShare, err := encryptForParticipantWithSeed(shareBytes, pubKeyBytes, seed)
				if err != nil {
					return nil, fmt.Errorf("failed to encrypt share for participant %s slot %d: %w", participant.Address, slotIndex, err)
				}

				encryptedShares[int(ciphertextIndex)] = encryptedShare
				openingRecords = append(openingRecords, dealerOpeningPersistRecord{
					epochID:         epochID,
					recipientIndex:  uint32(i),
					ciphertextIndex: ciphertextIndex,
					slotIndex:       slotIndex,
					shareBytes:      shareBytes,
					seed:            seed,
				})
				ciphertextIndex++
			}
		}

		encryptedSharesForParticipants[i] = types.EncryptedSharesForParticipant{
			EncryptedShares: encryptedShares,
		}

		logging.Debug("Generated encrypted shares for participant with warm keys", inferenceTypes.BLS,
			"participantIndex", i, "participant", participant.Address,
			"slotStart", participant.SlotStartIndex, "slotEnd", participant.SlotEndIndex,
			"numSlots", numSlots, "allowedKeys", len(allowedPubKeys),
			"totalCiphertexts", len(encryptedShares))
	}

	if err := bm.storeDealerOpeningRecordsBatch(openingRecords); err != nil {
		return nil, fmt.Errorf("failed to persist dealer openings for epoch %d: %w", epochID, err)
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

// computeG2CommitmentsBlst computes G2 commitments for polynomial coefficients using blst.
func computeG2CommitmentsBlst(coefficients []*fr.Element) [][]byte {
	commitments := make([][]byte, len(coefficients))

	for i, coeff := range coefficients {
		coeffBytes := coeff.Bytes()
		// Convert to little-endian for blst
		for j := 0; j < 16; j++ {
			coeffBytes[j], coeffBytes[31-j] = coeffBytes[31-j], coeffBytes[j]
		}

		commitment := blst.P2Generator().Mult(coeffBytes[:], 255).ToAffine()
		commitments[i] = commitment.Compress()
	}
	return commitments
}

// evaluatePolynomial evaluates polynomial at given x using Horner's method
func evaluatePolynomial(polynomial []*fr.Element, x uint32) *fr.Element {
	if len(polynomial) == 0 {
		return new(fr.Element).SetZero()
	}

	if len(polynomial) == 1 {
		return new(fr.Element).Set(polynomial[0])
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
	return encryptForParticipantWithReader(data, secp256k1PubKeyBytes, rand.Reader)
}

func encryptForParticipantWithSeed(data []byte, secp256k1PubKeyBytes []byte, seed []byte) ([]byte, error) {
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

func encryptForParticipantWithReader(data []byte, secp256k1PubKeyBytes []byte, entropy io.Reader) ([]byte, error) {
	eciesPubKey, err := parseECIESPublicKeyFromCompressed(secp256k1PubKeyBytes)
	if err != nil {
		return nil, err
	}

	// Encrypt the data using the same method as the modified Cosmos keyring
	// This ensures compatibility: dealer encryption → keyring decryption
	ciphertext, err := ecies.Encrypt(entropy, eciesPubKey, data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("ECIES encryption failed: %w", err)
	}

	return ciphertext, nil
}

func parseECIESPublicKeyFromCompressed(secp256k1PubKeyBytes []byte) (*ecies.PublicKey, error) {
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
	return ecies.ImportECDSAPublic(ecdsaPubKey), nil
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

func participantAllowedPubKeys(participant ParticipantInfo) ([][]byte, error) {
	if len(participant.Secp256K1PublicKey) != 33 {
		return nil, fmt.Errorf("invalid primary secp256k1 public key length for participant %s: %d", participant.Address, len(participant.Secp256K1PublicKey))
	}

	allowedPubKeys := make([][]byte, 0, 1+len(participant.AllowedSecp256K1PublicKeys))

	primaryKey := append([]byte(nil), participant.Secp256K1PublicKey...)
	allowedPubKeys = append(allowedPubKeys, primaryKey)

	for idx, additionalKey := range participant.AllowedSecp256K1PublicKeys {
		if len(additionalKey) != 33 {
			return nil, fmt.Errorf("invalid additional secp256k1 public key length at index %d for participant %s: %d", idx, participant.Address, len(additionalKey))
		}

		keyCopy := append([]byte(nil), additionalKey...)
		allowedPubKeys = append(allowedPubKeys, keyCopy)
	}

	return allowedPubKeys, nil
}

// All BLS cryptographic functions have been implemented above using gnark-crypto
