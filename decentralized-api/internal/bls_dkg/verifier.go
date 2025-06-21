package bls_dkg

import (
	"decentralized-api/cosmosclient"
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

// VerificationResult holds the results of DKG verification for an epoch
type VerificationResult struct {
	EpochID          uint64
	DkgPhase         types.DKGPhase // The DKG phase when verification was performed
	IsParticipant    bool
	SlotRange        [2]uint32      // [start_index, end_index]
	DealerShares     [][]fr.Element // dealer_index -> [slot_shares...]
	DealerValidity   []bool         // dealer_index -> validity
	AggregatedShares []fr.Element   // slot_offset -> aggregated_share
	ValidDealers     []bool         // final consensus validity of each dealer (after majority voting)
	GroupPublicKey   []byte         // the final group public key (when DKG is completed)
}

// VerificationCache manages verification results for multiple epochs
type VerificationCache struct {
	results map[uint64]*VerificationResult // epochID -> VerificationResult
}

// NewVerificationCache creates a new verification cache
func NewVerificationCache() *VerificationCache {
	return &VerificationCache{
		results: make(map[uint64]*VerificationResult),
	}
}

// Store adds a verification result to the cache and cleans up old entries
func (vc *VerificationCache) Store(result *VerificationResult) {
	if result == nil {
		return
	}

	// Store the new result
	vc.results[result.EpochID] = result

	// Clean up old entries (keep only current and previous epoch)
	if result.EpochID >= 2 {
		epochToRemove := result.EpochID - 2
		if _, exists := vc.results[epochToRemove]; exists {
			delete(vc.results, epochToRemove)
			logging.Debug(verifierLogTag+"Removed old verification result from cache", inferenceTypes.BLS,
				"removedEpochID", epochToRemove,
				"currentEpochID", result.EpochID)
		}
	}

	logging.Debug(verifierLogTag+"Stored verification result in cache", inferenceTypes.BLS,
		"epochID", result.EpochID,
		"cachedEpochs", len(vc.results))
}

// Get retrieves a verification result for a specific epoch
func (vc *VerificationCache) Get(epochID uint64) *VerificationResult {
	return vc.results[epochID]
}

// GetCurrent returns the verification result for the highest epoch ID
func (vc *VerificationCache) GetCurrent() *VerificationResult {
	var current *VerificationResult
	var maxEpochID uint64 = 0

	for epochID, result := range vc.results {
		if epochID > maxEpochID {
			maxEpochID = epochID
			current = result
		}
	}

	return current
}

// GetCachedEpochs returns a list of all cached epoch IDs
func (vc *VerificationCache) GetCachedEpochs() []uint64 {
	epochs := make([]uint64, 0, len(vc.results))
	for epochID := range vc.results {
		epochs = append(epochs, epochID)
	}
	return epochs
}

// Verifier handles the verification phase of DKG
type Verifier struct {
	cosmosClient cosmosclient.CosmosMessageClient
	cache        *VerificationCache // Cache for multiple epochs
}

// NewVerifier creates a new DKG verifier instance
func NewVerifier(cosmosClient cosmosclient.CosmosMessageClient) *Verifier {
	return &Verifier{
		cosmosClient: cosmosClient,
		cache:        NewVerificationCache(),
	}
}

// ProcessVerifyingPhaseStarted handles the EventVerifyingPhaseStarted event
func (v *Verifier) ProcessVerifyingPhaseStarted(event *chainevents.JSONRPCResponse) error {
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

	existingResult := v.GetVerificationResult(epochID)
	if existingResult != nil &&
		(existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_VERIFYING ||
			existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED) {
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
		"epochID", epochID, "deadlineBlock", deadlineBlock, "verifier", v.cosmosClient.GetAddress())

	// Extract epoch data from event instead of querying chain
	epochData, err := v.extractEpochDataFromVerifyingEvent(event)
	if err != nil {
		return fmt.Errorf("failed to extract epoch data from event: %w", err)
	}

	// Setup, perform verification, and store result for this epoch using event data
	completed, err := v.setupAndPerformVerification(epochID, epochData)
	if err != nil {
		return fmt.Errorf("failed to setup and perform verification for epoch %d: %w", epochID, err)
	}

	// If we're not a participant, return early
	if !completed {
		return nil
	}

	// Submit verification vector
	err = v.submitVerificationVectorSimplified(epochID)
	if err != nil {
		return fmt.Errorf("failed to submit verification vector: %w", err)
	}

	return nil
}

// setupAndPerformVerification handles epoch data validation, participant setup, verification, and storage
// Returns true if verification was completed and stored, false if we're not a participant or not in correct phase
func (v *Verifier) setupAndPerformVerification(epochID uint64, epochData *types.EpochBLSData) (bool, error) {
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
	err := v.performVerificationAndReconstruction(verificationResult, epochData.DealerParts, myParticipantIndex)
	if err != nil {
		return false, fmt.Errorf("failed to perform verification and reconstruction: %w", err)
	}

	// Store the completed verification result in cache
	v.storeVerificationResult(verificationResult)

	return true, nil
}

// performVerificationAndReconstruction performs the core verification and share reconstruction logic
func (v *Verifier) performVerificationAndReconstruction(verificationResult *VerificationResult, dealerParts []*types.DealerPartStorage, myParticipantIndex int) error {
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

		// Iterate over all slots for this dealer
		for slotOffset := 0; slotOffset < numSlots; slotOffset++ {
			slotIndex := verificationResult.SlotRange[0] + uint32(slotOffset)

			// Check if we have encrypted share for this slot
			if slotOffset >= len(participantShares.EncryptedShares) {
				logging.Warn(verifierLogTag+"Slot offset out of bounds", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotOffset", slotOffset,
					"availableShares", len(participantShares.EncryptedShares))
				allSlotsValid = false
				break
			}

			encryptedShare := participantShares.EncryptedShares[slotOffset]
			if len(encryptedShare) == 0 {
				logging.Debug(verifierLogTag+"Empty encrypted share", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex)
				allSlotsValid = false
				break
			}

			// Decrypt the share
			decryptedShare, err := v.decryptShare(encryptedShare)
			if err != nil {
				logging.Warn(verifierLogTag+"Failed to decrypt share", inferenceTypes.BLS,
					"dealerIndex", dealerIndex,
					"slotIndex", slotIndex,
					"error", err)
				allSlotsValid = false
				break
			}

			// Verify the share against dealer's commitments
			isValid, err := v.verifyShareAgainstCommitments(decryptedShare, slotIndex, dealerPart.Commitments)
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
func (v *Verifier) submitVerificationVectorSimplified(epochID uint64) error {
	// Get verification result from cache
	verificationResult := v.cache.Get(epochID)
	if verificationResult == nil {
		return fmt.Errorf("verification result not found in cache for epoch %d", epochID)
	}

	logging.Debug(verifierLogTag+"Submitting verification vector", inferenceTypes.BLS, "epochID", epochID)

	// Submit the verification vector using the dealer validity we already determined
	msg := &types.MsgSubmitVerificationVector{
		Creator:        v.cosmosClient.GetAddress(),
		EpochId:        epochID,
		DealerValidity: verificationResult.DealerValidity,
	}

	_, err := v.cosmosClient.SubmitVerificationVector(msg)
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

// GetVerificationResult returns the verification result for a specific epoch
func (v *Verifier) GetVerificationResult(epochID uint64) *VerificationResult {
	return v.cache.Get(epochID)
}

// GetCurrentVerificationResult returns the current verification result (highest epoch)
func (v *Verifier) GetCurrentVerificationResult() *VerificationResult {
	return v.cache.GetCurrent()
}

// GetCachedEpochs returns all cached epoch IDs
func (v *Verifier) GetCachedEpochs() []uint64 {
	return v.cache.GetCachedEpochs()
}

// storeVerificationResult stores a verification result in the cache
// This method can be extended in the future for additional validation or processing
func (v *Verifier) storeVerificationResult(result *VerificationResult) {
	if result == nil {
		logging.Warn(verifierLogTag+"Attempted to store nil verification result", inferenceTypes.BLS)
		return
	}

	v.cache.Store(result)

	logging.Debug(verifierLogTag+"Stored verification result", inferenceTypes.BLS,
		"epochID", result.EpochID,
		"isParticipant", result.IsParticipant,
		"slotRange", result.SlotRange,
		"totalCachedEpochs", len(v.cache.GetCachedEpochs()))
}

// ProcessGroupPublicKeyGenerated handles the DKG completion event
func (v *Verifier) ProcessGroupPublicKeyGenerated(event *chainevents.JSONRPCResponse) error {
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

	// Check if we already have a COMPLETED result for this epoch
	existingResult := v.GetVerificationResult(epochID)
	if existingResult != nil && existingResult.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED {
		logging.Warn(verifierLogTag+"DKG already completed for this epoch", inferenceTypes.BLS,
			"epochID", epochID,
			"isParticipant", existingResult.IsParticipant)
		return nil
	}

	// Extract epoch data from event instead of querying chain
	epochData, err := v.extractEpochDataFromGroupPublicKeyEvent(event)
	if err != nil {
		return fmt.Errorf("failed to extract epoch data from event: %w", err)
	}

	// Validate we're in the correct phase
	if epochData.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED {
		logging.Warn(verifierLogTag+"DKG not in completed phase", inferenceTypes.BLS,
			"epochID", epochID,
			"currentPhase", epochData.DkgPhase)
		return fmt.Errorf("epoch %d is not in COMPLETED phase, current phase: %s", epochID, epochData.DkgPhase)
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
		completed, err := v.setupAndPerformVerification(epochID, epochData)
		if err != nil {
			return fmt.Errorf("failed to setup and perform verification for epoch %d: %w", epochID, err)
		}

		if !completed {
			logging.Warn(verifierLogTag+"Not a participant in this DKG round", inferenceTypes.BLS, "epochID", epochID)
			return nil
		}

		// Get the updated verification result
		existingResult = v.GetVerificationResult(epochID)
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
	v.storeVerificationResult(completedResult)

	logging.Info(verifierLogTag+"Successfully processed DKG completion", inferenceTypes.BLS,
		"epochID", epochID,
		"isParticipant", completedResult.IsParticipant,
		"slotRange", completedResult.SlotRange,
		"aggregatedSharesCount", len(completedResult.AggregatedShares),
		"phase", completedResult.DkgPhase)

	return nil
}

// extractEpochDataFromGroupPublicKeyEvent extracts epoch data from a group public key generated event
func (v *Verifier) extractEpochDataFromGroupPublicKeyEvent(event *chainevents.JSONRPCResponse) (*types.EpochBLSData, error) {
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
	epochData, err := v.parseEpochDataFromJSON(unquotedEpochData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse epoch_data: %w", err)
	}

	return epochData, nil
}

// parseEpochDataFromJSON parses epoch data from JSON with explicit type conversion for protobuf fields
func (v *Verifier) parseEpochDataFromJSON(jsonStr string) (*types.EpochBLSData, error) {
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
func (v *Verifier) extractEpochDataFromVerifyingEvent(event *chainevents.JSONRPCResponse) (*types.EpochBLSData, error) {
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
	epochData, err := v.parseEpochDataFromJSON(unquotedEpochData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse epoch_data: %w", err)
	}

	return epochData, nil
}
