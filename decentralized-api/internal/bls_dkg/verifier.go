package bls_dkg

import (
	"crypto/ecdsa"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
)

const verifierLogTag = "[bls-verifier] "

// SlotShare represents a verified and aggregated secret share for a specific slot
type SlotShare struct {
	SlotIndex   uint32
	SecretShare *fr.Element
}

// Verifier handles the verification phase of DKG
type Verifier struct {
	cosmosClient cosmosclient.CosmosMessageClient
	privateKey   *ecdsa.PrivateKey

	// Verification results for current epoch
	currentEpochID uint64
	slotShares     map[uint32]*fr.Element // slot_index -> secret_share
	isParticipant  bool
	slotRange      [2]uint32 // [start_index, end_index]
}

// NewVerifier creates a new DKG verifier instance
func NewVerifier(cosmosClient cosmosclient.CosmosMessageClient) *Verifier {
	return &Verifier{
		cosmosClient: cosmosClient,
		slotShares:   make(map[uint32]*fr.Element),
	}
}

// ProcessVerifyingPhaseStarted handles the transition to verification phase
func (v *Verifier) ProcessVerifyingPhaseStarted(event *chainevents.JSONRPCResponse) error {
	// Extract epochID from event
	epochIDs, ok := event.Result.Events["verifying_phase_started.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in verifying phase started event")
	}

	epochID, err := strconv.ParseUint(epochIDs[0], 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse epoch_id: %w", err)
	}

	slog.Info(verifierLogTag+"Processing verifying phase started", "epochID", epochID)

	v.currentEpochID = epochID
	v.slotShares = make(map[uint32]*fr.Element)

	// Query complete epoch BLS data
	blsQueryClient := v.cosmosClient.NewBLSQueryClient()
	epochResp, err := blsQueryClient.EpochBLSData(*v.cosmosClient.GetContext(), &types.QueryEpochBLSDataRequest{
		EpochId: epochID,
	})
	if err != nil {
		return fmt.Errorf("failed to query epoch BLS data: %w", err)
	}

	epochData := epochResp.EpochData

	// Validate we're in the correct phase
	if epochData.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
		slog.Info(verifierLogTag+"DKG not in verifying phase",
			"epochID", epochID,
			"currentPhase", epochData.DkgPhase)
		return nil
	}

	// Find our participant info
	myAddress := v.cosmosClient.GetAddress()
	var myParticipantIndex int = -1
	var myParticipant *types.BLSParticipantInfo

	for i, participant := range epochData.Participants {
		if participant.Address == myAddress {
			myParticipantIndex = i
			myParticipant = &participant
			break
		}
	}

	if myParticipantIndex == -1 {
		slog.Info(verifierLogTag+"Not a participant in this DKG round",
			"epochID", epochID,
			"myAddress", myAddress,
			"participantCount", len(epochData.Participants))
		return nil
	}

	// Set participant info from epoch data
	v.isParticipant = true
	v.slotRange = [2]uint32{myParticipant.SlotStartIndex, myParticipant.SlotEndIndex}

	slog.Info(verifierLogTag+"Found participant info from epoch data",
		"epochID", epochID,
		"participantIndex", myParticipantIndex,
		"slotRange", v.slotRange,
		"dealerPartsCount", len(epochData.DealerParts),
		"totalSlots", epochData.ITotalSlots,
		"tDegree", epochData.TSlotsDegree)

	// Perform verification and reconstruction with the dealer parts from epoch data
	err = v.verifyAndReconstructSharesWithParticipantIndex(epochData.DealerParts, myParticipantIndex)
	if err != nil {
		return fmt.Errorf("failed to verify and reconstruct shares: %w", err)
	}

	// Submit verification vector
	err = v.submitVerificationVectorSimplified(epochData.DealerParts)
	if err != nil {
		return fmt.Errorf("failed to submit verification vector: %w", err)
	}

	return nil
}

// verifyAndReconstructSharesWithParticipantIndex performs the core verification and share reconstruction logic
func (v *Verifier) verifyAndReconstructSharesWithParticipantIndex(dealerParts []*types.DealerPartStorage, myParticipantIndex int) error {
	slog.Info(verifierLogTag+"Starting share verification and reconstruction",
		"epochID", v.currentEpochID,
		"slotRange", v.slotRange,
		"dealerPartsCount", len(dealerParts),
		"myParticipantIndex", myParticipantIndex)

	// For each slot in our range, reconstruct the secret share
	for slotIndex := v.slotRange[0]; slotIndex <= v.slotRange[1]; slotIndex++ {
		aggregatedShare := &fr.Element{} // Initialize to zero
		aggregatedShare.SetZero()

		slog.Debug(verifierLogTag+"Processing slot", "slotIndex", slotIndex)

		// Process shares from each dealer
		for dealerIndex, dealerPart := range dealerParts {
			if dealerPart == nil {
				slog.Debug(verifierLogTag+"Skipping empty dealer part", "dealerIndex", dealerIndex)
				continue
			}

			// Find our encrypted shares from this dealer
			if myParticipantIndex >= len(dealerPart.ParticipantShares) {
				slog.Warn(verifierLogTag+"No shares for our participant index",
					"dealerIndex", dealerIndex,
					"myParticipantIndex", myParticipantIndex)
				continue
			}

			participantShares := dealerPart.ParticipantShares[myParticipantIndex]
			if participantShares == nil {
				slog.Debug(verifierLogTag+"No shares from dealer",
					"dealerIndex", dealerIndex)
				continue
			}

			// Calculate slot offset within our range
			slotOffset := slotIndex - v.slotRange[0]
			if int(slotOffset) >= len(participantShares.EncryptedShares) {
				slog.Warn(verifierLogTag+"Slot offset out of bounds",
					"dealerIndex", dealerIndex,
					"slotOffset", slotOffset,
					"availableShares", len(participantShares.EncryptedShares))
				continue
			}

			encryptedShare := participantShares.EncryptedShares[slotOffset]
			if len(encryptedShare) == 0 {
				slog.Debug(verifierLogTag+"Empty encrypted share",
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex)
				continue
			}

			// Decrypt the share
			decryptedShare, err := v.decryptShare(encryptedShare)
			if err != nil {
				slog.Warn(verifierLogTag+"Failed to decrypt share",
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex,
					"error", err)
				continue
			}

			// Verify the share against dealer's commitments
			isValid, err := v.verifyShareAgainstCommitments(decryptedShare, slotIndex, dealerPart.Commitments)
			if err != nil {
				slog.Warn(verifierLogTag+"Failed to verify share",
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex,
					"error", err)
				continue
			}

			if !isValid {
				slog.Warn(verifierLogTag+"Share verification failed",
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex)
				continue
			}

			// Add valid share to aggregated total
			aggregatedShare.Add(aggregatedShare, decryptedShare)

			slog.Debug(verifierLogTag+"Successfully processed share",
				"dealerIndex", dealerIndex,
				"slotIndex", slotIndex)
		}

		// Store the final aggregated share for this slot
		v.slotShares[slotIndex] = aggregatedShare

		slog.Info(verifierLogTag+"Completed slot share reconstruction",
			"slotIndex", slotIndex,
			"finalShare", aggregatedShare.String())
	}

	slog.Info(verifierLogTag+"Completed verification and reconstruction",
		"epochID", v.currentEpochID,
		"processedSlots", len(v.slotShares))

	return nil
}

// decryptShare decrypts an encrypted share using the cosmos-sdk keyring Decrypt API
func (v *Verifier) decryptShare(encryptedShare []byte) (*fr.Element, error) {
	// Use the cosmos-sdk keyring Decrypt method through the clean interface
	decryptedBytes, err := v.cosmosClient.DecryptBytes(encryptedShare)
	if err != nil {
		return nil, fmt.Errorf("keyring decryption failed: %w", err)
	}

	// Convert decrypted bytes back to fr.Element
	if len(decryptedBytes) != 32 {
		return nil, fmt.Errorf("unexpected decrypted share length: %d, expected 32", len(decryptedBytes))
	}

	share := &fr.Element{}
	share.SetBytes(decryptedBytes)

	return share, nil
}

// verifyShareAgainstCommitments verifies a decrypted share against the dealer's polynomial commitments
func (v *Verifier) verifyShareAgainstCommitments(share *fr.Element, slotIndex uint32, commitments [][]byte) (bool, error) {
	if len(commitments) == 0 {
		return false, fmt.Errorf("no commitments provided")
	}

	// Convert slot index to fr.Element for polynomial evaluation
	slotIndexFr := &fr.Element{}
	slotIndexFr.SetUint64(uint64(slotIndex))

	// Evaluate the polynomial at slotIndex using the commitments
	// This computes: sum(commitments[j] * slotIndex^j) for j = 0 to degree
	var expectedCommitment bls12381.G2Affine
	// Start with identity (zero point) - G2 zero point
	expectedCommitment = bls12381.G2Affine{}

	// slotIndexPower starts at 1 (slotIndex^0)
	slotIndexPower := &fr.Element{}
	slotIndexPower.SetOne()

	for j, commitmentBytes := range commitments {
		// Parse commitment as compressed G2 point (96 bytes)
		if len(commitmentBytes) != 96 {
			return false, fmt.Errorf("invalid commitment length at index %d: %d, expected 96", j, len(commitmentBytes))
		}

		var commitment bls12381.G2Affine
		err := commitment.Unmarshal(commitmentBytes)
		if err != nil {
			return false, fmt.Errorf("failed to unmarshal commitment at index %d: %w", j, err)
		}

		// Multiply commitment by slotIndex^j
		var scaledCommitment bls12381.G2Affine
		scaledCommitment.ScalarMultiplication(&commitment, slotIndexPower.BigInt(new(big.Int)))

		// Add to running total
		expectedCommitment.Add(&expectedCommitment, &scaledCommitment)

		// Update slotIndexPower for next iteration: slotIndexPower *= slotIndex
		slotIndexPower.Mul(slotIndexPower, slotIndexFr)
	}

	// Compute g * share (where g is the G2 generator)
	var actualCommitment bls12381.G2Affine
	_, _, _, g2Gen := bls12381.Generators()
	actualCommitment.ScalarMultiplication(&g2Gen, share.BigInt(new(big.Int)))

	// Verify: actualCommitment == expectedCommitment
	return actualCommitment.Equal(&expectedCommitment), nil
}

// submitVerificationVectorSimplified constructs and submits the verification vector to the chain
func (v *Verifier) submitVerificationVectorSimplified(dealerParts []*types.DealerPartStorage) error {
	slog.Info(verifierLogTag+"Submitting verification vector", "epochID", v.currentEpochID)

	// Create dealer validity array based on dealer parts we received
	dealerValidity := make([]bool, len(dealerParts))

	// For each dealer, check if they provided valid shares for our slots
	for dealerIndex, dealerPart := range dealerParts {
		if dealerPart == nil {
			dealerValidity[dealerIndex] = false
			continue
		}

		// We consider a dealer valid if they provided commitments and we successfully
		// verified at least one share from them
		dealerValidity[dealerIndex] = len(dealerPart.Commitments) > 0
	}

	// Submit the verification vector
	msg := &types.MsgSubmitVerificationVector{
		Creator:        v.cosmosClient.GetAddress(),
		EpochId:        v.currentEpochID,
		DealerValidity: dealerValidity,
	}

	_, err := v.cosmosClient.SubmitVerificationVector(msg)
	if err != nil {
		return fmt.Errorf("failed to submit verification vector: %w", err)
	}

	slog.Info(verifierLogTag+"Successfully submitted verification vector",
		"epochID", v.currentEpochID,
		"validDealers", countTrueValues(dealerValidity),
		"totalDealers", len(dealerValidity))

	return nil
}

// GetSlotShares returns the current slot shares (for testing/debugging)
func (v *Verifier) GetSlotShares() map[uint32]*fr.Element {
	result := make(map[uint32]*fr.Element)
	for k, v := range v.slotShares {
		// Create a copy of the fr.Element
		copy := &fr.Element{}
		copy.Set(v)
		result[k] = copy
	}
	return result
}

// countTrueValues counts the number of true values in a boolean slice
func countTrueValues(values []bool) int {
	count := 0
	for _, v := range values {
		if v {
			count++
		}
	}
	return count
}
