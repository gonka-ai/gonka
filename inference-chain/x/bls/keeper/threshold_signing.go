package keeper

import (
	"encoding/binary"
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"golang.org/x/crypto/sha3"

	"github.com/productscience/inference/x/bls/types"
)

// RequestThresholdSignature is the main entry point for other modules to request BLS threshold signatures
func (k Keeper) RequestThresholdSignature(ctx sdk.Context, signingData types.SigningData) error {
	// Validate current epoch has completed DKG
	epochBLSData, found := k.GetEpochBLSData(ctx, signingData.CurrentEpochId)
	if !found {
		return fmt.Errorf("epoch BLS data not found for epoch %d", signingData.CurrentEpochId)
	}

	// Verify epoch has completed DKG (has group public key)
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED && epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_SIGNED {
		return fmt.Errorf("epoch %d DKG not completed, current phase: %s", signingData.CurrentEpochId, epochBLSData.DkgPhase.String())
	}

	if len(epochBLSData.GroupPublicKey) == 0 {
		return fmt.Errorf("epoch %d has no group public key", signingData.CurrentEpochId)
	}

	// Validate uniqueness - ensure request_id doesn't already exist
	key := types.ThresholdSigningRequestKey(signingData.RequestId)
	kvStore := k.storeService.OpenKVStore(ctx)
	existingValue, err := kvStore.Get(key)
	if err != nil {
		return fmt.Errorf("failed to check request uniqueness: %w", err)
	}
	if existingValue != nil {
		return fmt.Errorf("request_id already exists: %x", signingData.RequestId)
	}

	// Encode data using Ethereum-compatible abi.encodePacked format
	encodedData := k.encodeSigningData(signingData)

	// Compute message hash using keccak256 (Ethereum-compatible)
	hash := sha3.NewLegacyKeccak256()
	hash.Write(encodedData)
	messageHash := hash.Sum(nil)

	// Calculate deadline block height
	params := k.GetParams(ctx)
	deadlineBlockHeight := ctx.BlockHeight() + int64(params.SigningDeadlineBlocks)

	// Create threshold signing request
	request := &types.ThresholdSigningRequest{
		RequestId:           signingData.RequestId,
		CurrentEpochId:      signingData.CurrentEpochId,
		ChainId:             signingData.ChainId,
		Data:                signingData.Data,
		EncodedData:         encodedData,
		MessageHash:         messageHash,
		Status:              types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		PartialSignatures:   []types.PartialSignature{},
		FinalSignature:      []byte{},
		CreatedBlockHeight:  ctx.BlockHeight(),
		DeadlineBlockHeight: deadlineBlockHeight,
	}

	// Store the request
	requestBytes := k.cdc.MustMarshal(request)
	err = kvStore.Set(key, requestBytes)
	if err != nil {
		return fmt.Errorf("failed to store threshold signing request: %w", err)
	}

	// Store expiration index entry for efficient deadline management
	expirationKey := types.ExpirationIndexKey(deadlineBlockHeight, signingData.RequestId)
	err = kvStore.Set(expirationKey, []byte{}) // Empty value, just for ordering
	if err != nil {
		return fmt.Errorf("failed to store expiration index entry: %w", err)
	}

	// Emit event for controllers (message event, not block event)
	err = ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningRequested{
		RequestId:           signingData.RequestId,
		CurrentEpochId:      signingData.CurrentEpochId,
		EncodedData:         encodedData,
		MessageHash:         messageHash,
		DeadlineBlockHeight: deadlineBlockHeight,
	})
	if err != nil {
		return fmt.Errorf("failed to emit threshold signing requested event: %w", err)
	}

	return nil
}

// GetSigningStatus returns the status of a threshold signing request by request_id
func (k Keeper) GetSigningStatus(ctx sdk.Context, requestID []byte) (*types.ThresholdSigningRequest, error) {
	key := types.ThresholdSigningRequestKey(requestID)
	kvStore := k.storeService.OpenKVStore(ctx)

	requestBytes, err := kvStore.Get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get threshold signing request: %w", err)
	}
	if requestBytes == nil {
		return nil, fmt.Errorf("threshold signing request not found: %x", requestID)
	}

	var request types.ThresholdSigningRequest
	k.cdc.MustUnmarshal(requestBytes, &request)
	return &request, nil
}

// ListActiveSigningRequests returns all active threshold signing requests for a given epoch
func (k Keeper) ListActiveSigningRequests(ctx sdk.Context, currentEpochID uint64) ([]*types.ThresholdSigningRequest, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	signingStore := prefix.NewStore(store, types.ThresholdSigningRequestPrefix)

	var activeRequests []*types.ThresholdSigningRequest

	iterator := signingStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var request types.ThresholdSigningRequest
		k.cdc.MustUnmarshal(iterator.Value(), &request)

		// Filter by epoch and active status
		if request.CurrentEpochId == currentEpochID &&
			(request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_PENDING_SIGNING ||
				request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES) {
			activeRequests = append(activeRequests, &request)
		}
	}

	return activeRequests, nil
}

// encodeSigningData encodes signing data using Ethereum-compatible abi.encodePacked format
func (k Keeper) encodeSigningData(signingData types.SigningData) []byte {
	// abi.encodePacked(currentEpochID, chainID, requestID, data[0], data[1], ...)
	var encoded []byte

	// Add currentEpochID (8 bytes, big endian)
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, signingData.CurrentEpochId)
	encoded = append(encoded, epochBytes...)

	// Add chainID (32 bytes)
	encoded = append(encoded, signingData.ChainId...)

	// Add requestID (32 bytes)
	encoded = append(encoded, signingData.RequestId...)

	// Add each data element (32 bytes each)
	for _, dataElement := range signingData.Data {
		encoded = append(encoded, dataElement...)
	}

	return encoded
}

// AddPartialSignature adds a partial signature to a threshold signing request and checks for completion
func (k Keeper) AddPartialSignature(ctx sdk.Context, requestID []byte, slotIndices []uint32, partialSignature []byte, submitter string) error {
	// Get current request
	request, err := k.GetSigningStatus(ctx, requestID)
	if err != nil {
		return err
	}

	// Validate request state
	if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES {
		return fmt.Errorf("request is not collecting signatures, current status: %s", request.Status.String())
	}

	// Check deadline
	if ctx.BlockHeight() > request.DeadlineBlockHeight {
		// Mark as expired and emit failure event
		request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED

		// Remove from expiration index since it's no longer collecting signatures
		k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)

		if err := k.storeThresholdSigningRequest(ctx, request); err != nil {
			return err
		}
		return k.emitThresholdSigningFailed(ctx, requestID, request.CurrentEpochId, "request expired")
	}

	// Get current epoch BLS data for validation
	epochBLSData, found := k.GetEpochBLSData(ctx, request.CurrentEpochId)
	if !found {
		return fmt.Errorf("epoch BLS data not found for epoch %d", request.CurrentEpochId)
	}

	// Validate submitter owns the claimed slot indices
	if err := k.validateSlotOwnership(ctx, submitter, slotIndices, &epochBLSData); err != nil {
		return fmt.Errorf("slot ownership validation failed: %w", err)
	}

	// Verify partial signature cryptographically
	if err := k.verifyPartialSignature(partialSignature, request.MessageHash, slotIndices, &epochBLSData); err != nil {
		return fmt.Errorf("partial signature verification failed: %w", err)
	}

	// Check if this participant already submitted (prevent double-submission)
	for _, existingSig := range request.PartialSignatures {
		if existingSig.ParticipantAddress == submitter {
			return fmt.Errorf("participant %s already submitted partial signature", submitter)
		}
	}

	// Count current slot coverage before adding new signature
	currentSlotsCovered := uint32(0)
	for _, partialSig := range request.PartialSignatures {
		currentSlotsCovered += uint32(len(partialSig.SlotIndices))
	}

	// Add partial signature to request
	request.PartialSignatures = append(request.PartialSignatures, types.PartialSignature{
		ParticipantAddress: submitter,
		SlotIndices:        slotIndices,
		Signature:          partialSignature,
	})

	// Check if threshold reached and aggregate
	if err := k.checkThresholdAndAggregate(ctx, request, &epochBLSData); err != nil {
		return fmt.Errorf("threshold check and aggregation failed: %w", err)
	}

	// Store updated request
	return k.storeThresholdSigningRequest(ctx, request)
}

// validateSlotOwnership checks if the submitter owns the claimed slot indices
func (k Keeper) validateSlotOwnership(ctx sdk.Context, submitter string, slotIndices []uint32, epochBLSData *types.EpochBLSData) error {
	// Find submitter in epoch participants
	var participantStartSlot, participantEndSlot uint32
	found := false

	for _, participant := range epochBLSData.Participants {
		if participant.Address == submitter {
			participantStartSlot = participant.SlotStartIndex
			participantEndSlot = participant.SlotEndIndex
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("submitter %s not found in epoch %d participants", submitter, epochBLSData.EpochId)
	}

	// Verify all claimed slot indices are within submitter's range
	for _, claimedSlot := range slotIndices {
		if claimedSlot < participantStartSlot || claimedSlot > participantEndSlot {
			return fmt.Errorf("submitter %s does not own slot %d (valid range: %d-%d)",
				submitter, claimedSlot, participantStartSlot, participantEndSlot)
		}
	}

	return nil
}

// verifyPartialSignature verifies a partial signature against the message hash
func (k Keeper) verifyPartialSignature(partialSignature []byte, messageHash []byte, slotIndices []uint32, epochBLSData *types.EpochBLSData) error {
	// Basic guards; detailed signature payload validation is centralized in verifyBLSPartialSignature
	if len(messageHash) != 32 {
		return fmt.Errorf("invalid message hash length: expected 32 bytes, got %d", len(messageHash))
	}

	if len(slotIndices) == 0 {
		return fmt.Errorf("no slot indices provided")
	}

	// Verify BLS partial signature against participant's computed individual public key
	if !k.verifyBLSPartialSignature(partialSignature, messageHash, epochBLSData, slotIndices) {
		return fmt.Errorf("BLS signature verification failed")
	}

	return nil
}

// checkThresholdAndAggregate checks if enough partial signatures collected and aggregates them
func (k Keeper) checkThresholdAndAggregate(ctx sdk.Context, request *types.ThresholdSigningRequest, epochBLSData *types.EpochBLSData) error {
	// Calculate total slots covered by partial signatures
	totalSlotsCovered := uint32(0)
	for _, partialSig := range request.PartialSignatures {
		totalSlotsCovered += uint32(len(partialSig.SlotIndices))
	}

	// Get total slots from epoch (threshold = more than 50% of slots)
	totalSlots := epochBLSData.ITotalSlots
	threshold := totalSlots/2 + 1

	if totalSlotsCovered < threshold {
		// Not enough signatures yet, keep collecting
		return nil
	}

	// Threshold reached - aggregate signatures
	finalSignature, err := k.aggregatePartialSignatures(request.PartialSignatures, epochBLSData)
	if err != nil {
		// Aggregation failed - mark as failed
		request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED
		request.FinalSignature = []byte{}

		// Remove from expiration index since it's no longer collecting signatures
		k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)

		return k.emitThresholdSigningFailed(ctx, request.RequestId, request.CurrentEpochId,
			fmt.Sprintf("signature aggregation failed: %v", err))
	}

	// Success - update request with final signature
	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	request.FinalSignature = finalSignature

	// Remove from expiration index since it's no longer collecting signatures
	k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)

	// Emit completion event
	return k.emitThresholdSigningCompleted(ctx, request.RequestId, request.CurrentEpochId,
		finalSignature, totalSlotsCovered)
}

// aggregatePartialSignatures combines partial signatures into final signature
func (k Keeper) aggregatePartialSignatures(partialSigs []types.PartialSignature, epochBLSData *types.EpochBLSData) ([]byte, error) {
	if len(partialSigs) == 0 {
		return nil, fmt.Errorf("no partial signatures to aggregate")
	}

	// Use shared BLS aggregation function
	return k.aggregateBLSPartialSignatures(partialSigs)
}

// storeThresholdSigningRequest stores a threshold signing request
func (k Keeper) storeThresholdSigningRequest(ctx sdk.Context, request *types.ThresholdSigningRequest) error {
	key := types.ThresholdSigningRequestKey(request.RequestId)
	kvStore := k.storeService.OpenKVStore(ctx)

	requestBytes := k.cdc.MustMarshal(request)
	return kvStore.Set(key, requestBytes)
}

// emitThresholdSigningCompleted emits completion event
func (k Keeper) emitThresholdSigningCompleted(ctx sdk.Context, requestID []byte, epochID uint64, finalSignature []byte, participatingSlots uint32) error {
	return ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningCompleted{
		RequestId:          requestID,
		CurrentEpochId:     epochID,
		FinalSignature:     finalSignature,
		ParticipatingSlots: participatingSlots,
	})
}

// emitThresholdSigningFailed emits failure event
func (k Keeper) emitThresholdSigningFailed(ctx sdk.Context, requestID []byte, epochID uint64, reason string) error {
	return ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningFailed{
		RequestId:      requestID,
		CurrentEpochId: epochID,
		Reason:         reason,
	})
}

// removeFromExpirationIndex removes a request from the expiration index
func (k Keeper) removeFromExpirationIndex(ctx sdk.Context, deadlineBlockHeight int64, requestID []byte) {
	kvStore := k.storeService.OpenKVStore(ctx)
	expirationKey := types.ExpirationIndexKey(deadlineBlockHeight, requestID)

	// Delete the expiration index entry (ignore errors as it might not exist)
	_ = kvStore.Delete(expirationKey)
}

// ProcessThresholdSigningDeadlines processes expired threshold signing requests efficiently using expiration index
func (k Keeper) ProcessThresholdSigningDeadlines(ctx sdk.Context) error {
	currentBlockHeight := ctx.BlockHeight()

	// Get KV store
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Use expiration index prefix for the current block height
	// This only scans requests expiring exactly at this block height - O(requests_expiring_now) instead of O(all_requests)
	expirationPrefix := types.ExpirationIndexPrefixForBlock(currentBlockHeight)
	expirationStore := prefix.NewStore(store, expirationPrefix)

	iterator := expirationStore.Iterator(nil, nil)
	defer iterator.Close()

	var expiredCount uint32

	for ; iterator.Valid(); iterator.Next() {
		// Extract request_id from the key
		// Key format: {request_id} (within the block-specific prefix)
		requestID := iterator.Key()

		// Load the full request to update its status
		request, err := k.GetSigningStatus(ctx, requestID)
		if err != nil {
			k.Logger().Error("Failed to load threshold signing request for deadline processing",
				"request_id", fmt.Sprintf("%x", requestID), "error", err)
			continue // Skip this request and continue processing others
		}

		// Double-check that the request is still collecting signatures and actually expired
		if request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES &&
			currentBlockHeight >= request.DeadlineBlockHeight {

			// Mark as expired
			request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED

			// Store updated request
			if err := k.storeThresholdSigningRequest(ctx, request); err != nil {
				k.Logger().Error("Failed to store expired threshold signing request",
					"request_id", fmt.Sprintf("%x", requestID), "error", err)
				continue
			}

			// Remove from expiration index (cleanup)
			k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, requestID)

			// Emit failure event
			if err := k.emitThresholdSigningFailed(ctx, requestID, request.CurrentEpochId, "deadline expired"); err != nil {
				k.Logger().Error("Failed to emit threshold signing failed event",
					"request_id", fmt.Sprintf("%x", requestID), "error", err)
				// Continue processing even if event emission fails
			}

			expiredCount++
		}
	}

	// Log summary if any requests were processed
	if expiredCount > 0 {
		k.Logger().Info("Processed expired threshold signing requests",
			"block_height", currentBlockHeight,
			"expired_count", expiredCount)
	}

	return nil
}
