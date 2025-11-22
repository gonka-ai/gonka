package bls

import (
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const verifierLogTag = "[bls-verifier] "

// ProcessVerifyingPhaseStarted handles the EventVerifyingPhaseStarted event
func (bm *BlsManager) ProcessVerifyingPhaseStarted(event *chainevents.JSONRPCResponse) error {
	// Extract event data from chain event (typed event from EmitTypedEvent)
	epochIDs, ok := event.Result.Events["inference.bls.EventVerifyingPhaseStarted.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in verifying phase started event")
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

	existingResult := bm.GetVerificationResult(epochID)
	if existingResult != nil &&
		(existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_VERIFYING ||
			existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED ||
			existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED) {
		logging.Info(verifierLogTag+"Verification already completed for this epoch", inferenceTypes.BLS,
			"epochID", epochID,
			"existingPhase", existingResult.DkgPhase,
			"isParticipant", existingResult.IsParticipant)
		return nil
	}

	// Now access the rest of the event fields as before
	deadlineStrs, ok := event.Result.Events["inference.bls.EventVerifyingPhaseStarted.verifying_phase_deadline_block"]
	if !ok || len(deadlineStrs) == 0 {
		return fmt.Errorf("verifying_phase_deadline_block not found in event")
	}

	// Unquote the deadline value
	unquotedDeadline, err := utils.UnquoteEventValue(deadlineStrs[0])
	if err != nil {
		return fmt.Errorf("failed to unquote verifying_phase_deadline_block: %w", err)
	}

	deadlineBlock, err := strconv.ParseUint(unquotedDeadline, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse verifying_phase_deadline_block: %w", err)
	}

	logging.Info(verifierLogTag+"Processing DKG verifying phase started", inferenceTypes.BLS,
		"epochID", epochID, "deadlineBlock", deadlineBlock, "verifier", bm.cosmosClient.GetAccountAddress())

	// Extract epoch data from event instead of querying chain
	epochData, err := bm.extractEpochDataFromVerifyingEvent(event)
	if err != nil {
		return fmt.Errorf("failed to extract epoch data from event: %w", err)
	}

	// Setup, perform verification, and store result for this epoch using event data
	completed, err := bm.setupAndPerformVerification(epochID, epochData)
	if err != nil {
		return fmt.Errorf("failed to setup and perform verification for epoch %d: %w", epochID, err)
	}

	// If we're not a participant, return early
	if !completed {
		return nil
	}

	// Submit verification vector
	err = bm.submitVerificationVectorSimplified(epochID)
	if err != nil {
		return fmt.Errorf("failed to submit verification vector: %w", err)
	}

	return nil
}

// setupAndPerformVerification handles epoch data validation, participant setup, verification, and storage
// Returns true if verification was completed and stored, false if we're not a participant or not in correct phase
func (bm *BlsManager) setupAndPerformVerification(epochID uint64, epochData *types.EpochBLSData) (bool, error) {
	// Create new verification result for this epoch
	verificationResult := &VerificationResult{
		EpochID: epochID,
	}

	// Set the DKG phase from epoch data
	verificationResult.DkgPhase = epochData.DkgPhase

	// Validate we're in the correct phase
	if epochData.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
		logging.Debug(verifierLogTag+"DKG not in verifying phase", inferenceTypes.BLS,
			"epochID", epochID,
			"currentPhase", epochData.DkgPhase)
		return false, nil // Return false to indicate we should skip verification
	}

	// Find our participant info
	myAddress := bm.cosmosClient.GetAccountAddress()
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
		logging.Debug(verifierLogTag+"Not a participant in this DKG round", inferenceTypes.BLS,
			"epochID", epochID,
			"myAddress", myAddress,
			"participantCount", len(epochData.Participants))
		return false, nil // Return false to indicate we should skip verification
	}

	// Set participant info in verification result
	verificationResult.IsParticipant = true
	verificationResult.SlotRange = [2]uint32{myParticipant.SlotStartIndex, myParticipant.SlotEndIndex}

	logging.Debug(verifierLogTag+"Found participant info from epoch data", inferenceTypes.BLS,
		"epochID", epochID,
		"participantIndex", myParticipantIndex,
		"slotRange", verificationResult.SlotRange,
		"dealerPartsCount", len(epochData.DealerParts),
		"totalSlots", epochData.ITotalSlots,
		"tDegree", epochData.TSlotsDegree)

	// Perform verification and reconstruction
	err := bm.performVerificationAndReconstruction(verificationResult, epochData.DealerParts, myParticipantIndex)
	if err != nil {
		return false, fmt.Errorf("failed to perform verification and reconstruction: %w", err)
	}

	// Store the completed verification result in cache
	bm.storeVerificationResult(verificationResult)

	return true, nil
}

// performVerificationAndReconstruction performs the core verification and share reconstruction logic
func (bm *BlsManager) performVerificationAndReconstruction(verificationResult *VerificationResult, dealerParts []*types.DealerPartStorage, myParticipantIndex int) error {
	logging.Debug(verifierLogTag+"Starting share verification and reconstruction", inferenceTypes.BLS,
		"epochID", verificationResult.EpochID,
		"slotRange", verificationResult.SlotRange,
		"dealerPartsCount", len(dealerParts),
		"myParticipantIndex", myParticipantIndex)

	// Initialize arrays
	numSlots := int(verificationResult.SlotRange[1] - verificationResult.SlotRange[0] + 1)
	verificationResult.DealerShares = make([][]fr.Element, len(dealerParts))
	verificationResult.DealerValidity = make([]bool, len(dealerParts))
	verificationResult.AggregatedShares = make([]fr.Element, numSlots)

	// First iterate over dealers
	for dealerIndex, dealerPart := range dealerParts {
		logging.Debug(verifierLogTag+"Processing dealer", inferenceTypes.BLS, "dealerIndex", dealerIndex)

		// Check if dealer part exists
		if dealerPart == nil {
			logging.Debug(verifierLogTag+"Skipping empty dealer part", inferenceTypes.BLS, "dealerIndex", dealerIndex)
			verificationResult.DealerShares[dealerIndex] = make([]fr.Element, 0) // Empty array
			verificationResult.DealerValidity[dealerIndex] = false
			continue
		}

		// Check if we have shares for our participant index
		if myParticipantIndex >= len(dealerPart.ParticipantShares) {
			logging.Warn(verifierLogTag+"No shares for our participant index", inferenceTypes.BLS,
				"dealerIndex", dealerIndex,
				"myParticipantIndex", myParticipantIndex)
			verificationResult.DealerShares[dealerIndex] = make([]fr.Element, 0) // Empty array
			verificationResult.DealerValidity[dealerIndex] = false
			continue
		}

		participantShares := dealerPart.ParticipantShares[myParticipantIndex]
		if participantShares == nil {
			logging.Debug(verifierLogTag+"No shares from dealer", inferenceTypes.BLS,
				"dealerIndex", dealerIndex)
			verificationResult.DealerShares[dealerIndex] = make([]fr.Element, 0) // Empty array
			verificationResult.DealerValidity[dealerIndex] = false
			continue
		}

		// Initialize dealer shares array
		dealerSlotShares := make([]fr.Element, numSlots)
		allSlotsValid := true
		dealerKeyIndex := -1 // Track which key index works for this dealer

		// Iterate over all slots for this dealer
		for slotOffset := 0; slotOffset < numSlots; slotOffset++ {
			slotIndex := verificationResult.SlotRange[0] + uint32(slotOffset)

			// Try to decrypt share for this slot (may have multiple ciphertexts due to warm keys)
			decryptedShare, keyIndex, err := bm.decryptShareForSlot(participantShares.EncryptedShares, slotOffset, numSlots, dealerIndex, slotIndex, dealerKeyIndex)
			if err != nil {
				logging.Warn(verifierLogTag+"Failed to decrypt any ciphertext for slot", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex,
					"error", err)
				allSlotsValid = false
				break
			}

			// Remember the key index that worked for this dealer
			dealerKeyIndex = keyIndex

			// Verify the share against dealer's commitments
			isValid, err := bm.verifyShareAgainstCommitments(decryptedShare, slotIndex, dealerPart.Commitments)
			if err != nil {
				logging.Warn(verifierLogTag+"Failed to verify share", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex,
					"error", err)
				allSlotsValid = false
				break
			}

			if !isValid {
				logging.Warn(verifierLogTag+"Share verification failed", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex)
				allSlotsValid = false
				break
			}

			// Store valid decrypted share
			dealerSlotShares[slotOffset] = *decryptedShare

			logging.Debug(verifierLogTag+"Successfully processed share", inferenceTypes.BLS,
				"dealerIndex", dealerIndex,
				"slotIndex", slotIndex)
		}

		// Store dealer results
		if allSlotsValid {
			verificationResult.DealerShares[dealerIndex] = dealerSlotShares
			verificationResult.DealerValidity[dealerIndex] = true
			logging.Debug(verifierLogTag+"Dealer validation successful", inferenceTypes.BLS,
				"dealerIndex", dealerIndex,
				"processedSlots", len(dealerSlotShares))
		} else {
			verificationResult.DealerShares[dealerIndex] = make([]fr.Element, 0) // Empty array
			verificationResult.DealerValidity[dealerIndex] = false
			logging.Debug(verifierLogTag+"Dealer validation failed", inferenceTypes.BLS,
				"dealerIndex", dealerIndex)
		}
	}

	// Now aggregate shares per slot
	for slotOffset := 0; slotOffset < numSlots; slotOffset++ {
		slotIndex := verificationResult.SlotRange[0] + uint32(slotOffset)
		aggregatedShare := &fr.Element{}
		aggregatedShare.SetZero()

		// Sum up shares from all valid dealers for this slot
		for dealerIndex := 0; dealerIndex < len(dealerParts); dealerIndex++ {
			if verificationResult.DealerValidity[dealerIndex] && len(verificationResult.DealerShares[dealerIndex]) > slotOffset {
				aggregatedShare.Add(aggregatedShare, &verificationResult.DealerShares[dealerIndex][slotOffset])
			}
		}

		// Store aggregated share
		verificationResult.AggregatedShares[slotOffset] = *aggregatedShare

		logging.Debug(verifierLogTag+"Completed slot share reconstruction", inferenceTypes.BLS,
			"slotIndex", slotIndex,
			"slotOffset", slotOffset,
			"finalShare", aggregatedShare.String())
	}

	logging.Info(verifierLogTag+"Completed verification and reconstruction", inferenceTypes.BLS,
		"epochID", verificationResult.EpochID,
		"validDealers", countTrueValues(verificationResult.DealerValidity),
		"totalDealers", len(dealerParts),
		"processedSlots", len(verificationResult.AggregatedShares))

	return nil
}

// decryptShareForSlot tries to decrypt a share for a specific slot, handling warm keys
// For warm keys, multiple ciphertexts per slot are stored consecutively in the encrypted_shares array
// Returns the decrypted share and the key index that worked (for reuse in subsequent slots)
// dealerKeyIndex: -1 means try all keys, >= 0 means try this key index first
func (bm *BlsManager) decryptShareForSlot(encryptedShares [][]byte, slotOffset, numSlots, dealerIndex int, slotIndex uint32, dealerKeyIndex int) (*fr.Element, int, error) {
	totalCiphertexts := len(encryptedShares)
	if totalCiphertexts == 0 {
		return nil, -1, fmt.Errorf("no encrypted shares available")
	}

	// Get our current public key for logging
	ourPubKey := bm.cosmosClient.GetAccountPubKey()

	// Calculate keys per slot: totalCiphertexts = numSlots * keysPerSlot
	if totalCiphertexts%numSlots != 0 {
		return nil, -1, fmt.Errorf("invalid encrypted shares array length: %d ciphertexts for %d slots (not evenly divisible)", totalCiphertexts, numSlots)
	}

	keysPerSlot := totalCiphertexts / numSlots

	// Calculate the range of ciphertexts for this specific slot
	startIndex := slotOffset * keysPerSlot
	endIndex := startIndex + keysPerSlot

	if endIndex > totalCiphertexts {
		return nil, -1, fmt.Errorf("calculated ciphertext range [%d:%d] exceeds array bounds %d", startIndex, endIndex, totalCiphertexts)
	}

	// If we already know which key index works, use it exclusively
	if dealerKeyIndex >= 0 && dealerKeyIndex < keysPerSlot {
		targetCipherIndex := startIndex + dealerKeyIndex
		if len(encryptedShares[targetCipherIndex]) > 0 {
			decryptedShare, err := bm.decryptShare(encryptedShares[targetCipherIndex])
			if err == nil {
				// Same key index worked again
				return decryptedShare, dealerKeyIndex, nil
			} else {
				// Known key index failed - this is an error since all slots should use same key
				return nil, -1, fmt.Errorf("failed to decrypt with known key index %d for slot %d: %w", dealerKeyIndex, slotIndex, err)
			}
		} else {
			return nil, -1, fmt.Errorf("invalid ciphertext at known key index %d for slot %d", dealerKeyIndex, slotIndex)
		}
	}

	// First slot: try each ciphertext until one decrypts successfully
	for keyIndex := 0; keyIndex < keysPerSlot; keyIndex++ {
		cipherIndex := startIndex + keyIndex
		encryptedShare := encryptedShares[cipherIndex]
		if len(encryptedShare) == 0 {
			continue // Skip empty ciphertexts
		}

		// Try to decrypt this ciphertext
		decryptedShare, err := bm.decryptShare(encryptedShare)
		if err != nil {
			// This ciphertext didn't decrypt with our key, try the next one
			continue
		}

		// Successfully decrypted! Return both the share and the key index
		return decryptedShare, keyIndex, nil
	}

	// If we get here, none of the ciphertexts for this slot could be decrypted
	return nil, -1, fmt.Errorf("failed to decrypt any of %d ciphertexts for slot %d with our key %s", keysPerSlot, slotIndex, ourPubKey)
}

// decryptShare decrypts an encrypted share using the cosmos-sdk keyring Decrypt API
func (bm *BlsManager) decryptShare(encryptedShare []byte) (*fr.Element, error) {
	// Use the cosmos-sdk keyring Decrypt method through the clean interface
	decryptedBytes, err := bm.cosmosClient.DecryptBytes(encryptedShare)
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
func (bm *BlsManager) verifyShareAgainstCommitments(share *fr.Element, slotIndex uint32, commitments [][]byte) (bool, error) {
	if len(commitments) == 0 {
		return false, fmt.Errorf("no commitments provided")
	}

	// Convert slot index to fr.Element for polynomial evaluation (x = slotIndex+1, to avoid x=0 and match chain)
	slotIndexFr := &fr.Element{}
	slotIndexFr.SetUint64(uint64(slotIndex + 1))

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

		// Update slotIndexPower for next iteration: slotIndexPower *= (slotIndex+1)
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
func (bm *BlsManager) submitVerificationVectorSimplified(epochID uint64) error {
	// Get verification result from cache
	verificationResult := bm.cache.Get(epochID)
	if verificationResult == nil {
		return fmt.Errorf("verification result not found in cache for epoch %d", epochID)
	}

	logging.Debug(verifierLogTag+"Submitting verification vector", inferenceTypes.BLS, "epochID", epochID)

	// Submit the verification vector using the dealer validity we already determined
	msg := &types.MsgSubmitVerificationVector{
		Creator:        bm.cosmosClient.GetAccountAddress(),
		EpochId:        epochID,
		DealerValidity: verificationResult.DealerValidity,
	}

	_, err := bm.cosmosClient.SubmitVerificationVector(msg)
	if err != nil {
		return fmt.Errorf("failed to submit verification vector: %w", err)
	}

	logging.Debug(verifierLogTag+"Successfully submitted verification vector", inferenceTypes.BLS,
		"epochID", epochID,
		"validDealers", countTrueValues(verificationResult.DealerValidity),
		"totalDealers", len(verificationResult.DealerValidity))

	return nil
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

// ProcessGroupPublicKeyGenerated handles the DKG completion event
func (bm *BlsManager) ProcessGroupPublicKeyGeneratedToVerify(event *chainevents.JSONRPCResponse) error {
	// Extract epochID from event
	epochIDs, ok := event.Result.Events["inference.bls.EventGroupPublicKeyGenerated.epoch_id"]
	if !ok || len(epochIDs) == 0 {
		return fmt.Errorf("epoch_id not found in group public key generated event")
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

	logging.Debug(verifierLogTag+"Processing group public key generated", inferenceTypes.BLS, "epochID", epochID)

	// Check if we already have a COMPLETED or SIGNED result for this epoch
	existingResult := bm.GetVerificationResult(epochID)
	if existingResult != nil && (existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED || existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED) {
		logging.Warn(verifierLogTag+"DKG already completed for this epoch", inferenceTypes.BLS,
			"epochID", epochID,
			"isParticipant", existingResult.IsParticipant)
		return nil
	}

	// Extract epoch data from event instead of querying chain
	epochData, err := bm.extractEpochDataFromGroupPublicKeyEvent(event)
	if err != nil {
		return fmt.Errorf("failed to extract epoch data from event: %w", err)
	}

	// Validate we're in the correct phase
	if epochData.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED && epochData.DkgPhase != types.DKGPhase_DKG_PHASE_SIGNED {
		logging.Warn(verifierLogTag+"DKG not in completed phase", inferenceTypes.BLS,
			"epochID", epochID,
			"currentPhase", epochData.DkgPhase)
		return fmt.Errorf("epoch %d is not in COMPLETED or SIGNED phase, current phase: %s", epochID, epochData.DkgPhase)
	}

	// If we don't have a VERIFYING result, we need to perform verification first
	if existingResult == nil || existingResult.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
		logging.Debug(verifierLogTag+"No verification result found, performing verification", inferenceTypes.BLS,
			"epochID", epochID,
			"existingPhase", func() string {
				if existingResult != nil {
					return existingResult.DkgPhase.String()
				}
				return "none"
			}())

		// Setup and perform verification to get our slot shares using event data
		completed, err := bm.setupAndPerformVerification(epochID, epochData)
		if err != nil {
			return fmt.Errorf("failed to setup and perform verification for epoch %d: %w", epochID, err)
		}

		if !completed {
			logging.Warn(verifierLogTag+"Not a participant in this DKG round", inferenceTypes.BLS, "epochID", epochID)
			return nil
		}

		// Get the updated verification result
		existingResult = bm.GetVerificationResult(epochID)
		if existingResult == nil {
			return fmt.Errorf("verification result not found after performing verification for epoch %d", epochID)
		}
	}

	// Update the verification result to COMPLETED phase and store group public key
	// Validate group public key format before storing (should be 96 bytes for compressed G2)
	if len(epochData.GroupPublicKey) != 96 {
		logging.Warn(verifierLogTag+"Invalid group public key length from epoch data", inferenceTypes.BLS,
			"epochID", epochID,
			"expectedBytes", 96,
			"actualBytes", len(epochData.GroupPublicKey))
		return fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(epochData.GroupPublicKey))
	}

	logging.Debug(verifierLogTag+"Group public key validated from epoch data", inferenceTypes.BLS,
		"epochID", epochID,
		"groupPubKeyBytes", len(epochData.GroupPublicKey))

	completedResult := &VerificationResult{
		EpochID:          epochID,
		DkgPhase:         types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant:    existingResult.IsParticipant,
		SlotRange:        existingResult.SlotRange,
		DealerShares:     existingResult.DealerShares,
		DealerValidity:   existingResult.DealerValidity,
		AggregatedShares: existingResult.AggregatedShares,
		ValidDealers:     epochData.ValidDealers,   // Store consensus valid dealers from event
		GroupPublicKey:   epochData.GroupPublicKey, // Store validated group public key from epoch data
	}

	// Store the completed verification result
	bm.storeVerificationResult(completedResult)

	logging.Info(verifierLogTag+"Successfully processed DKG completion", inferenceTypes.BLS,
		"epochID", epochID,
		"isParticipant", completedResult.IsParticipant,
		"slotRange", completedResult.SlotRange,
		"aggregatedSharesCount", len(completedResult.AggregatedShares),
		"phase", completedResult.DkgPhase)

	return nil
}

// extractEpochDataFromGroupPublicKeyEvent extracts epoch data from a group public key generated event
func (bm *BlsManager) extractEpochDataFromGroupPublicKeyEvent(event *chainevents.JSONRPCResponse) (*types.EpochBLSData, error) {
	// Extract epoch data from event - this should be a JSON-encoded object
	epochDataStrs, ok := event.Result.Events["inference.bls.EventGroupPublicKeyGenerated.epoch_data"]
	if !ok || len(epochDataStrs) == 0 {
		return nil, fmt.Errorf("epoch_data not found in group public key generated event")
	}

	// The epoch_data field should be a JSON-encoded EpochBLSData object
	// First, unquote the JSON string if it's quoted
	unquotedEpochData, err := utils.UnquoteEventValue(epochDataStrs[0])
	if err != nil {
		return nil, fmt.Errorf("failed to unquote epoch_data: %w", err)
	}

	// Parse the epoch data using the helper function that handles type conversions
	epochData, err := bm.parseEpochDataFromJSON(unquotedEpochData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse epoch_data: %w", err)
	}

	return epochData, nil
}

// parseEpochDataFromJSON parses epoch data from JSON with explicit type conversion for protobuf fields
func (bm *BlsManager) parseEpochDataFromJSON(jsonStr string) (*types.EpochBLSData, error) {
	// Parse the JSON into a map first to handle type conversions
	var epochDataMap map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &epochDataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON to map: %w", err)
	}

	// Manually convert string numbers to proper types for protobuf fields
	if epochIDStr, ok := epochDataMap["epoch_id"].(string); ok {
		if epochID, err := strconv.ParseUint(epochIDStr, 10, 64); err == nil {
			epochDataMap["epoch_id"] = epochID
		}
	}

	if iTotalSlotsStr, ok := epochDataMap["i_total_slots"].(string); ok {
		if iTotalSlots, err := strconv.ParseUint(iTotalSlotsStr, 10, 32); err == nil {
			epochDataMap["i_total_slots"] = uint32(iTotalSlots)
		}
	}

	if tSlotsDegreeStr, ok := epochDataMap["t_slots_degree"].(string); ok {
		if tSlotsDegree, err := strconv.ParseUint(tSlotsDegreeStr, 10, 32); err == nil {
			epochDataMap["t_slots_degree"] = uint32(tSlotsDegree)
		}
	}

	// Handle DKGPhase enum conversion
	if dkgPhaseStr, ok := epochDataMap["dkg_phase"].(string); ok {
		switch dkgPhaseStr {
		case "DKG_PHASE_UNDEFINED":
			epochDataMap["dkg_phase"] = int32(0)
		case "DKG_PHASE_DEALING":
			epochDataMap["dkg_phase"] = int32(1)
		case "DKG_PHASE_VERIFYING":
			epochDataMap["dkg_phase"] = int32(2)
		case "DKG_PHASE_COMPLETED":
			epochDataMap["dkg_phase"] = int32(3)
		case "DKG_PHASE_FAILED":
			epochDataMap["dkg_phase"] = int32(4)
		default:
			// Try to parse as number if it's a numeric string
			if dkgPhaseNum, err := strconv.ParseUint(dkgPhaseStr, 10, 32); err == nil {
				epochDataMap["dkg_phase"] = int32(dkgPhaseNum)
			}
		}
	}

	if dealingDeadlineStr, ok := epochDataMap["dealing_phase_deadline_block"].(string); ok {
		if dealingDeadline, err := strconv.ParseInt(dealingDeadlineStr, 10, 64); err == nil {
			epochDataMap["dealing_phase_deadline_block"] = dealingDeadline
		}
	}

	if verifyingDeadlineStr, ok := epochDataMap["verifying_phase_deadline_block"].(string); ok {
		if verifyingDeadline, err := strconv.ParseInt(verifyingDeadlineStr, 10, 64); err == nil {
			epochDataMap["verifying_phase_deadline_block"] = verifyingDeadline
		}
	}

	// Convert the map back to JSON with proper type handling
	convertedJSON, err := json.Marshal(epochDataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal converted epoch_data: %w", err)
	}

	// Now parse into the actual EpochBLSData struct
	var epochData types.EpochBLSData
	err = json.Unmarshal(convertedJSON, &epochData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal epoch_data JSON to struct: %w", err)
	}

	return &epochData, nil
}

// extractEpochDataFromVerifyingEvent extracts epoch data from a verifying event
func (bm *BlsManager) extractEpochDataFromVerifyingEvent(event *chainevents.JSONRPCResponse) (*types.EpochBLSData, error) {
	// Extract epoch data from event - this should be a JSON-encoded object
	epochDataStrs, ok := event.Result.Events["inference.bls.EventVerifyingPhaseStarted.epoch_data"]
	if !ok || len(epochDataStrs) == 0 {
		return nil, fmt.Errorf("epoch_data not found in verifying phase started event")
	}

	// The epoch_data field should be a JSON-encoded EpochBLSData object
	// First, unquote the JSON string if it's quoted
	unquotedEpochData, err := utils.UnquoteEventValue(epochDataStrs[0])
	if err != nil {
		return nil, fmt.Errorf("failed to unquote epoch_data: %w", err)
	}

	// Parse the epoch data using the helper function that handles type conversions
	epochData, err := bm.parseEpochDataFromJSON(unquotedEpochData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse epoch_data: %w", err)
	}

	return epochData, nil
}
