package keeper

import (
	"bytes"
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
	epochBLSData, err := k.GetEpochBLSData(ctx, signingData.CurrentEpochId)
	if err != nil {
		return fmt.Errorf("failed to get epoch %d BLS data: %w", signingData.CurrentEpochId, err)
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get parameters: %w", err)
	}

	if err := k.validateThresholdSigningEpochPhase(ctx, &epochBLSData, params); err != nil {
		return err
	}

	if len(epochBLSData.GroupPublicKey) == 0 {
		return fmt.Errorf("epoch %d has no group public key", signingData.CurrentEpochId)
	}

	maxSigningAttempts := params.MaxSigningAttempts
	if maxSigningAttempts == 0 {
		maxSigningAttempts = types.DefaultParams().MaxSigningAttempts
	}
	attempt := uint32(1)

	// Validate uniqueness - ensure request_id doesn't already exist
	key := types.ThresholdSigningRequestKey(signingData.RequestId)
	kvStore := k.storeService.OpenKVStore(ctx)
	existingValue, err := kvStore.Get(key)
	if err != nil {
		return fmt.Errorf("failed to check request uniqueness: %w", err)
	}
	if existingValue != nil {
		var existing types.ThresholdSigningRequest
		if err := k.cdc.Unmarshal(existingValue, &existing); err != nil {
			return fmt.Errorf("failed to unmarshal existing threshold signing request: %w", err)
		}

		// Allow retry only after terminal no-signature outcomes
		if existing.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED &&
			existing.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED {
			return fmt.Errorf("request_id already exists: %x (status: %s)", signingData.RequestId, existing.Status.String())
		}
		if !bytes.Equal(existing.ChainId, signingData.ChainId) || !equalSigningDataFields(existing.Data, signingData.Data) {
			return fmt.Errorf("request_id payload mismatch: %x", signingData.RequestId)
		}
		if existing.Attempt >= maxSigningAttempts {
			return fmt.Errorf("max signing attempts reached for request_id: %x (attempts: %d)", signingData.RequestId, existing.Attempt)
		}
		attempt = existing.Attempt + 1

		k.Logger().Info("Retrying threshold signing request after failed attempt",
			"request_id", fmt.Sprintf("%x", signingData.RequestId),
			"previous_status", existing.Status.String(),
			"previous_deadline_block_height", existing.DeadlineBlockHeight,
			"next_attempt", attempt,
			"max_signing_attempts", maxSigningAttempts)

		// Defense-in-depth cleanup in case a stale expiration index entry remains
		k.removeFromExpirationIndex(ctx, existing.DeadlineBlockHeight, signingData.RequestId)

		// Clear any partial-sig sub-keys left over from the previous
		// attempt. A FAILED/EXPIRED attempt may have written some
		// submitter entries before terminating; without clearing them
		// here, the next retry's GetSigningStatus would rehydrate stale
		// signatures that no longer correspond to the current message.
		if err := k.DeleteThresholdPartialSignaturesForRequest(ctx, signingData.RequestId); err != nil {
			return fmt.Errorf("failed to clear previous-attempt partial signatures for request %x: %w", signingData.RequestId, err)
		}
	}

	// Encode data using Ethereum-compatible abi.encodePacked format
	encodedData := k.encodeSigningData(signingData)

	// Compute message hash using keccak256 (Ethereum-compatible)
	hash := sha3.NewLegacyKeccak256()
	hash.Write(encodedData)
	messageHash := hash.Sum(nil)

	// Calculate deadline block height
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
		Attempt:             attempt,
	}

	// Store the request
	requestBytes, err := k.cdc.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal threshold signing request: %w", err)
	}
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

func (k Keeper) validateThresholdSigningEpochPhase(ctx sdk.Context, epochBLSData *types.EpochBLSData, params types.Params) error {
	switch epochBLSData.DkgPhase {
	case types.DKGPhase_DKG_PHASE_SIGNED:
		return nil
	case types.DKGPhase_DKG_PHASE_COMPLETED:
		if epochBLSData.DisputingPhaseDeadlineBlock <= 0 {
			return fmt.Errorf("epoch %d DKG is completed but not signed; signed phase required (missing disputing deadline metadata)", epochBLSData.EpochId)
		}

		if params.CompletedFallbackBlocks == 0 {
			return fmt.Errorf("epoch %d DKG is completed but not signed; completed fallback is disabled", epochBLSData.EpochId)
		}

		fallbackActivationBlock := epochBLSData.DisputingPhaseDeadlineBlock + params.CompletedFallbackBlocks
		if ctx.BlockHeight() < fallbackActivationBlock {
			return fmt.Errorf(
				"epoch %d DKG is completed but not signed; fallback available at block %d (current: %d)",
				epochBLSData.EpochId,
				fallbackActivationBlock,
				ctx.BlockHeight(),
			)
		}

		k.Logger().Warn(
			"Allowing threshold signing for completed epoch after signed timeout fallback",
			"epoch_id", epochBLSData.EpochId,
			"current_block_height", ctx.BlockHeight(),
			"fallback_activation_block", fallbackActivationBlock,
			"fallback_timeout_blocks", params.CompletedFallbackBlocks,
		)
		return nil
	default:
		return fmt.Errorf("epoch %d DKG is not signed, current phase: %s", epochBLSData.EpochId, epochBLSData.DkgPhase.String())
	}
}

// GetSigningStatus returns the status of a threshold signing request by
// request_id. PartialSignatures are rehydrated from per-submitter sub-keys
// so callers see the same shape they always have.
//
// Backward compatibility: if the base struct still has PartialSignatures
// inline (e.g. an entry written by a pre-split handler), those serve as
// the baseline and any sub-key entries take precedence by submitter. This
// lets the split take effect immediately after upgrade without a separate
// migration path for in-flight requests.
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
	err = k.cdc.Unmarshal(requestBytes, &request)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal threshold signing request: %w", err)
	}

	// Build baseline from any inline legacy entries, keyed by submitter for
	// quick lookup when the sub-key iterator overlays. Empty entries
	// (ParticipantAddress == "") are dropped.
	bySubmitter := make(map[string]int, len(request.PartialSignatures))
	merged := make([]types.PartialSignature, 0, len(request.PartialSignatures))
	for _, ps := range request.PartialSignatures {
		if ps.ParticipantAddress == "" {
			continue
		}
		bySubmitter[ps.ParticipantAddress] = len(merged)
		merged = append(merged, ps)
	}

	it := k.thresholdPartialSigStore(ctx, requestID).Iterator(nil, nil)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		var ps types.PartialSignature
		if err := k.cdc.Unmarshal(it.Value(), &ps); err != nil {
			return nil, fmt.Errorf("unmarshal threshold partial signature: %w", err)
		}
		if existingIdx, ok := bySubmitter[ps.ParticipantAddress]; ok {
			merged[existingIdx] = ps
			continue
		}
		merged = append(merged, ps)
	}
	if len(merged) == 0 {
		request.PartialSignatures = nil
	} else {
		request.PartialSignatures = merged
	}

	return &request, nil
}

func (k Keeper) CancelThresholdSignature(ctx sdk.Context, requestID []byte) error {
	request, err := k.GetSigningStatus(ctx, requestID)
	if err != nil {
		return err
	}

	if request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED {
		return nil
	}

	if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED &&
		request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED {
		return fmt.Errorf("can only cancel failed or expired requests, current status: %s", request.Status.String())
	}

	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED
	return k.storeThresholdSigningRequest(ctx, request)
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
		err := k.cdc.Unmarshal(iterator.Value(), &request)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal threshold signing request: %w", err)
		}

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

func equalSigningDataFields(existing, incoming [][]byte) bool {
	if len(existing) != len(incoming) {
		return false
	}
	for i := range existing {
		if !bytes.Equal(existing[i], incoming[i]) {
			return false
		}
	}
	return true
}

func (k Keeper) maybeAutoRetryThresholdSigningRequest(ctx sdk.Context, request *types.ThresholdSigningRequest, triggerReason string) (bool, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get parameters: %w", err)
	}

	maxSigningAttempts := params.MaxSigningAttempts
	if maxSigningAttempts == 0 {
		maxSigningAttempts = types.DefaultParams().MaxSigningAttempts
	}

	signingDeadlineBlocks := params.SigningDeadlineBlocks
	if signingDeadlineBlocks <= 0 {
		signingDeadlineBlocks = types.DefaultParams().SigningDeadlineBlocks
	}

	if request.Attempt >= maxSigningAttempts {
		return false, nil
	}

	previousAttempt := request.Attempt
	previousDeadlineBlockHeight := request.DeadlineBlockHeight

	cacheCtx, writeCache := ctx.CacheContext()

	retryReq := *request
	retryReq.Attempt++
	retryReq.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES
	retryReq.PartialSignatures = nil
	retryReq.FinalSignature = []byte{}
	retryReq.DeadlineBlockHeight = cacheCtx.BlockHeight() + signingDeadlineBlocks

	// Clear any partial-sig sub-keys from the previous attempt so the new
	// attempt starts with an empty collection. Without this, prior-attempt
	// signatures would leak into the new attempt's rehydrated view.
	if err := k.DeleteThresholdPartialSignaturesForRequest(cacheCtx, retryReq.RequestId); err != nil {
		return false, fmt.Errorf("failed to clear prior-attempt partial signatures for request %x: %w", retryReq.RequestId, err)
	}

	k.removeFromExpirationIndex(cacheCtx, previousDeadlineBlockHeight, retryReq.RequestId)
	kvStore := k.storeService.OpenKVStore(cacheCtx)
	expirationKey := types.ExpirationIndexKey(retryReq.DeadlineBlockHeight, retryReq.RequestId)
	if err := kvStore.Set(expirationKey, []byte{}); err != nil {
		return false, fmt.Errorf("failed to store expiration index entry for retry: %w", err)
	}

	if err := k.storeThresholdSigningRequest(cacheCtx, &retryReq); err != nil {
		return false, err
	}

	if err := cacheCtx.EventManager().EmitTypedEvent(&types.EventThresholdSigningRequested{
		RequestId:           retryReq.RequestId,
		CurrentEpochId:      retryReq.CurrentEpochId,
		EncodedData:         retryReq.EncodedData,
		MessageHash:         retryReq.MessageHash,
		DeadlineBlockHeight: retryReq.DeadlineBlockHeight,
	}); err != nil {
		return false, fmt.Errorf("failed to emit threshold signing requested event on retry: %w", err)
	}

	writeCache()
	ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())

	k.Logger().Info("Automatically retrying threshold signing request",
		"request_id", fmt.Sprintf("%x", retryReq.RequestId),
		"trigger_reason", triggerReason,
		"attempt_from", previousAttempt,
		"attempt_to", retryReq.Attempt,
		"epoch_id", retryReq.CurrentEpochId,
		"deadline_from", previousDeadlineBlockHeight,
		"deadline_to", retryReq.DeadlineBlockHeight,
		"max_signing_attempts", maxSigningAttempts,
	)

	return true, nil
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
		retried, retryErr := k.maybeAutoRetryThresholdSigningRequest(ctx, request, "request expired")
		if retryErr != nil {
			k.Logger().Error("Failed to auto-retry expired threshold signing request, falling back to EXPIRED",
				"request_id", fmt.Sprintf("%x", requestID), "error", retryErr)
		} else if retried {
			return nil
		}

		return k.finalizeFailedThresholdSigningRequest(
			ctx,
			request,
			types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED,
			"request expired",
		)
	}

	// Get current epoch BLS data for validation
	epochBLSData, err := k.GetEpochBLSData(ctx, request.CurrentEpochId)
	if err != nil {
		return fmt.Errorf("failed to get epoch %d BLS data: %w", request.CurrentEpochId, err)
	}

	// Reject duplicate slot indices
	seen := make(map[uint32]struct{})
	for _, slot := range slotIndices {
		if _, dup := seen[slot]; dup {
			return fmt.Errorf("duplicate slot index: %d", slot)
		}
		seen[slot] = struct{}{}
	}

	// Validate submitter owns the claimed slot indices
	if err := k.validateSlotOwnership(ctx, submitter, slotIndices, &epochBLSData); err != nil {
		return fmt.Errorf("slot ownership validation failed: %w", err)
	}

	// Verify partial signature cryptographically
	if err := k.verifyPartialSignature(partialSignature, request.MessageHash, slotIndices, &epochBLSData); err != nil {
		return fmt.Errorf("partial signature verification failed: %w", err)
	}

	// O(1) duplicate check via direct sub-key lookup, replacing the
	// prior O(N) scan through request.PartialSignatures.
	if k.HasThresholdPartialSignature(ctx, request.RequestId, submitter) {
		return fmt.Errorf("participant %s already submitted partial signature", submitter)
	}

	// Persist the new partial signature to its own sub-key. Write cost is
	// bounded by this submitter's own payload, independent of how many
	// other signers have already submitted — that is the whole point of
	// the split (see ThresholdPartialSigRequestPrefix doc for the
	// gas-scaling rationale).
	newPartial := types.PartialSignature{
		ParticipantAddress: submitter,
		SlotIndices:        slotIndices,
		Signature:          partialSignature,
	}
	if err := k.SetThresholdPartialSignature(ctx, request.RequestId, &newPartial); err != nil {
		return fmt.Errorf("failed to persist threshold partial signature: %w", err)
	}

	// Keep the in-memory view in sync so checkThresholdAndAggregate sees
	// the new entry without an extra ListThresholdPartialSignatures call.
	request.PartialSignatures = append(request.PartialSignatures, newPartial)

	// Persist the base request with PartialSignatures nil'd so
	// storeThresholdSigningRequest's sync loop doesn't redundantly
	// rewrite every existing signer's sub-key on each submission. The
	// entry we just added is already in its sub-key, and every other
	// entry is already in its own sub-key from earlier txs.
	inMemoryPartials := request.PartialSignatures
	request.PartialSignatures = nil
	if err := k.storeThresholdSigningRequest(ctx, request); err != nil {
		return err
	}
	request.PartialSignatures = inMemoryPartials

	// Check if threshold reached and aggregate
	if err := k.checkThresholdAndAggregate(ctx, request, &epochBLSData); err != nil {
		return fmt.Errorf("threshold check and aggregation failed: %w", err)
	}

	return nil
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
	if !k.verifyBLSPartialSignatureBlst(partialSignature, messageHash, epochBLSData, slotIndices) {
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

	// Use DKG threshold t+1, where t is configured via TSlotsDegreeOffset at epoch creation.
	threshold := epochBLSData.TSlotsDegree + 1

	if totalSlotsCovered < threshold {
		// Not enough signatures yet, keep collecting
		return nil
	}

	// Threshold reached - aggregate signatures
	finalSignature, err := k.aggregatePartialSignatures(request.PartialSignatures, epochBLSData)
	if err != nil {
		retried, retryErr := k.maybeAutoRetryThresholdSigningRequest(ctx, request, "signature aggregation failed")
		if retryErr != nil {
			k.Logger().Error("Failed to auto-retry failed threshold signing request, falling back to FAILED",
				"request_id", fmt.Sprintf("%x", request.RequestId), "error", retryErr)
		} else if retried {
			return nil
		}

		reason := fmt.Sprintf("signature aggregation failed: %v", err)
		return k.finalizeFailedThresholdSigningRequest(
			ctx,
			request,
			types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED,
			reason,
		)
	}

	// Success - update request with final signature
	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	request.FinalSignature = finalSignature

	// Remove from expiration index since it's no longer collecting signatures
	k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)

	// Persist terminal state before event emission
	if err := k.storeThresholdSigningRequest(ctx, request); err != nil {
		return err
	}

	if err := k.runThresholdSigningCompletedPostProcess(ctx, request.RequestId, request.CurrentEpochId); err != nil {
		k.Logger().Error("Failed to run threshold signing completion hooks",
			"request_id", fmt.Sprintf("%x", request.RequestId), "error", err)
		k.enqueueCompletedPostProcessRetry(ctx, request.RequestId)
	}

	// Emit completion event
	if err := k.emitThresholdSigningCompleted(ctx, request.RequestId, request.CurrentEpochId,
		finalSignature, totalSlotsCovered); err != nil {
		return err
	}
	return nil
}

// aggregatePartialSignatures combines partial signatures into final signature
func (k Keeper) aggregatePartialSignatures(partialSigs []types.PartialSignature, epochBLSData *types.EpochBLSData) ([]byte, error) {
	if len(partialSigs) == 0 {
		return nil, fmt.Errorf("no partial signatures to aggregate")
	}

	// Use shared BLS aggregation function
	return k.aggregateBLSPartialSignaturesBlst(partialSigs)
}

// StoreThresholdSigningRequest exposes storeThresholdSigningRequest so
// out-of-package migration code (e.g. the v0.2.12 upgrade handler) can
// drive the sync-and-strip pass that splits legacy inline
// PartialSignatures into per-submitter sub-keys. Handlers in this
// package should continue to call storeThresholdSigningRequest directly.
func (k Keeper) StoreThresholdSigningRequest(ctx sdk.Context, request *types.ThresholdSigningRequest) error {
	return k.storeThresholdSigningRequest(ctx, request)
}

// storeThresholdSigningRequest stores a threshold signing request.
//
// PartialSignatures are persisted out-of-band under per-submitter sub-keys
// (see SetThresholdPartialSignature and ThresholdPartialSigRequestPrefix).
// Any non-empty entries on the in-memory request are synced to sub-keys
// here; the base struct is persisted with PartialSignatures zeroed so
// writes stay constant-size as signers accumulate.
//
// The hot path (AddPartialSignature) bypasses this sync loop by nulling
// out PartialSignatures before calling storeThresholdSigningRequest — it
// wrote its own sub-key entry directly and every other signer's entry is
// already persisted from their earlier tx.
func (k Keeper) storeThresholdSigningRequest(ctx sdk.Context, request *types.ThresholdSigningRequest) error {
	// Sync any inline partial signatures to sub-keys. Callers that
	// pre-populate the slice (genesis import, retry reset with a
	// populated copy, tests) hit this path. Runtime hot-path callers
	// should null out PartialSignatures first.
	for i := range request.PartialSignatures {
		ps := request.PartialSignatures[i]
		if ps.ParticipantAddress == "" {
			continue
		}
		if err := k.SetThresholdPartialSignature(ctx, request.RequestId, &ps); err != nil {
			return fmt.Errorf("sync partial sig for submitter %s: %w", ps.ParticipantAddress, err)
		}
	}

	key := types.ThresholdSigningRequestKey(request.RequestId)
	kvStore := k.storeService.OpenKVStore(ctx)

	// Copy so we don't mutate the caller's request.
	baseCopy := *request
	baseCopy.PartialSignatures = nil

	requestBytes, err := k.cdc.Marshal(&baseCopy)
	if err != nil {
		return fmt.Errorf("failed to marshal threshold signing request: %w", err)
	}
	return kvStore.Set(key, requestBytes)
}

// thresholdPartialSigStore returns a prefix.Store scoped to all partial
// signatures collected for a single threshold signing request. Sub-keys
// within the returned store are the submitter address bytes produced by
// types.ThresholdPartialSigSubKey.
func (k Keeper) thresholdPartialSigStore(ctx sdk.Context, requestID []byte) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.ThresholdPartialSigRequestPrefix(requestID))
}

// SetThresholdPartialSignature writes a single partial signature under its
// own sub-key. Cost is constant in the number of signers that already
// submitted for this request, so every signer pays the same gas
// regardless of submission order. This is the hot path called by
// AddPartialSignature.
func (k Keeper) SetThresholdPartialSignature(ctx sdk.Context, requestID []byte, ps *types.PartialSignature) error {
	if ps == nil {
		return fmt.Errorf("nil threshold partial signature")
	}
	if ps.ParticipantAddress == "" {
		return fmt.Errorf("threshold partial signature missing participant address")
	}
	value, err := k.cdc.Marshal(ps)
	if err != nil {
		return fmt.Errorf("marshal threshold partial signature: %w", err)
	}
	k.thresholdPartialSigStore(ctx, requestID).Set(types.ThresholdPartialSigSubKey(ps.ParticipantAddress), value)
	return nil
}

// GetThresholdPartialSignature reads the partial signature submitted by a
// specific participant for the given request. Returns (nil, nil) when no
// submission exists.
func (k Keeper) GetThresholdPartialSignature(ctx sdk.Context, requestID []byte, submitter string) (*types.PartialSignature, error) {
	value := k.thresholdPartialSigStore(ctx, requestID).Get(types.ThresholdPartialSigSubKey(submitter))
	if value == nil {
		return nil, nil
	}
	var ps types.PartialSignature
	if err := k.cdc.Unmarshal(value, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}

// HasThresholdPartialSignature reports whether a partial signature exists
// for the given (request, submitter) pair without unmarshaling the value.
// Used by AddPartialSignature for the O(1) duplicate check.
func (k Keeper) HasThresholdPartialSignature(ctx sdk.Context, requestID []byte, submitter string) bool {
	return k.thresholdPartialSigStore(ctx, requestID).Has(types.ThresholdPartialSigSubKey(submitter))
}

// ListThresholdPartialSignatures returns every partial signature collected
// for a request, in ascending submitter-bytes order. Used by GetSigningStatus
// rehydration and by the aggregation threshold check.
func (k Keeper) ListThresholdPartialSignatures(ctx sdk.Context, requestID []byte) ([]types.PartialSignature, error) {
	it := k.thresholdPartialSigStore(ctx, requestID).Iterator(nil, nil)
	defer it.Close()

	var out []types.PartialSignature
	for ; it.Valid(); it.Next() {
		var ps types.PartialSignature
		if err := k.cdc.Unmarshal(it.Value(), &ps); err != nil {
			return nil, fmt.Errorf("unmarshal threshold partial signature: %w", err)
		}
		out = append(out, ps)
	}
	return out, nil
}

// DeleteThresholdPartialSignaturesForRequest removes every partial
// signature sub-key for a request. Called on retry reset (to clear the
// prior attempt's accumulated sigs) and on terminal failure cleanup.
func (k Keeper) DeleteThresholdPartialSignaturesForRequest(ctx sdk.Context, requestID []byte) error {
	store := k.thresholdPartialSigStore(ctx, requestID)
	it := store.Iterator(nil, nil)

	var keysToDelete [][]byte
	for ; it.Valid(); it.Next() {
		keysToDelete = append(keysToDelete, append([]byte(nil), it.Key()...))
	}
	it.Close()

	for _, key := range keysToDelete {
		store.Delete(key)
	}
	return nil
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
	k.Logger().Error("Threshold signing failed",
		"request_id", fmt.Sprintf("%x", requestID),
		"current_epoch_id", epochID,
		"reason", reason)

	return ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningFailed{
		RequestId:      requestID,
		CurrentEpochId: epochID,
		Reason:         reason,
	})
}

func (k Keeper) maybeCloseRetryAfterFailedPostProcess(ctx sdk.Context, request *types.ThresholdSigningRequest, reason string) bool {
	cacheCtx, writeCache := ctx.CacheContext()

	closeRetry, err := k.Hooks().AfterThresholdSigningFailed(cacheCtx, request.RequestId, request.CurrentEpochId, reason)
	if err != nil {
		k.Logger().Error("Failed to run threshold signing failure hooks",
			"request_id", fmt.Sprintf("%x", request.RequestId), "error", err)
		return false
	}

	if closeRetry {
		request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED
		if err := k.storeThresholdSigningRequest(cacheCtx, request); err != nil {
			k.Logger().Error("Failed to store cancelled threshold signing request",
				"request_id", fmt.Sprintf("%x", request.RequestId), "error", err)
			return false
		}
	}

	writeCache()
	ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())
	return closeRetry
}

func (k Keeper) runThresholdSigningCompletedPostProcess(ctx sdk.Context, requestID []byte, epochID uint64) error {
	cacheCtx, writeCache := ctx.CacheContext()

	if err := k.Hooks().AfterThresholdSigningCompleted(cacheCtx, requestID, epochID); err != nil {
		return err
	}

	writeCache()
	ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())
	return nil
}

func (k Keeper) enqueueCompletedPostProcessRetry(ctx sdk.Context, requestID []byte) {
	if len(requestID) == 0 {
		return
	}

	kvStore := k.storeService.OpenKVStore(ctx)
	retryKey := types.CompletedPostProcessRetryKey(requestID)
	if err := kvStore.Set(retryKey, []byte{}); err != nil {
		k.Logger().Error("Failed to enqueue threshold signing completion retry",
			"request_id", fmt.Sprintf("%x", requestID), "error", err)
	}
}

func (k Keeper) finalizeFailedThresholdSigningRequest(
	ctx sdk.Context,
	request *types.ThresholdSigningRequest,
	status types.ThresholdSigningStatus,
	reason string,
) error {
	request.Status = status
	request.FinalSignature = []byte{}

	k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)

	if err := k.storeThresholdSigningRequest(ctx, request); err != nil {
		return err
	}

	k.maybeCloseRetryAfterFailedPostProcess(ctx, request, reason)

	return k.emitThresholdSigningFailed(ctx, request.RequestId, request.CurrentEpochId, reason)
}

// removeFromExpirationIndex removes a request from the expiration index
func (k Keeper) removeFromExpirationIndex(ctx sdk.Context, deadlineBlockHeight int64, requestID []byte) {
	kvStore := k.storeService.OpenKVStore(ctx)
	expirationKey := types.ExpirationIndexKey(deadlineBlockHeight, requestID)

	// Delete the expiration index entry (ignore errors as it might not exist)
	_ = kvStore.Delete(expirationKey)
}

const defaultMaxExpiredRequestsPerBlock uint32 = 200
const defaultMaxCompletedPostProcessRetriesPerBlock uint32 = 100

var maxExpiredRequestsPerBlock = defaultMaxExpiredRequestsPerBlock
var maxCompletedPostProcessRetriesPerBlock = defaultMaxCompletedPostProcessRetriesPerBlock

// ProcessThresholdSigningDeadlines processes expired threshold signing requests efficiently using expiration index
func (k Keeper) ProcessThresholdSigningDeadlines(ctx sdk.Context) error {
	currentBlockHeight := ctx.BlockHeight()

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	expirationStore := prefix.NewStore(store, types.ExpirationIndexPrefix)

	iterator := expirationStore.Iterator(nil, nil)

	maxToProcess := maxExpiredRequestsPerBlock
	if maxToProcess == 0 {
		maxToProcess = defaultMaxExpiredRequestsPerBlock
	}

	type expiringItem struct {
		deadline int64
		reqID    []byte
	}

	var toProcess []expiringItem
	var badKeys [][]byte
	hasBacklog := false

	for ; iterator.Valid(); iterator.Next() {
		rawKey := iterator.Key()
		deadlineBlockHeight, requestID, err := parseExpirationIndexEntry(rawKey)
		if err != nil {
			k.Logger().Error("Failed to parse expiration index key, scheduling for deletion",
				"raw_key", fmt.Sprintf("%x", rawKey), "error", err)
			badKeys = append(badKeys, append([]byte(nil), rawKey...))

			if uint32(len(toProcess)+len(badKeys)) >= maxToProcess {
				iterator.Next()
				if iterator.Valid() {
					if nextDeadline, _, err := parseExpirationIndexEntry(iterator.Key()); err == nil && nextDeadline <= currentBlockHeight {
						hasBacklog = true
					} else if err != nil {
						hasBacklog = true
					}
				}
				break
			}
			continue
		}

		if deadlineBlockHeight > currentBlockHeight {
			break
		}

		toProcess = append(toProcess, expiringItem{
			deadline: deadlineBlockHeight,
			reqID:    append([]byte(nil), requestID...),
		})

		if uint32(len(toProcess)+len(badKeys)) >= maxToProcess {
			iterator.Next()
			if iterator.Valid() {
				if nextDeadline, _, err := parseExpirationIndexEntry(iterator.Key()); err == nil && nextDeadline <= currentBlockHeight {
					hasBacklog = true
				} else if err != nil {
					hasBacklog = true
				}
			}
			break
		}
	}
	iterator.Close()

	for _, badKey := range badKeys {
		expirationStore.Delete(badKey)
	}

	var expiredCount uint32
	var retriedCount uint32
	var staleIndexCount uint32

	for _, item := range toProcess {
		request, err := k.GetSigningStatus(ctx, item.reqID)
		if err != nil {
			k.Logger().Error("Failed to load threshold signing request for deadline processing",
				"request_id", fmt.Sprintf("%x", item.reqID),
				"deadline_block_height", item.deadline,
				"error", err)
			k.removeFromExpirationIndex(ctx, item.deadline, item.reqID)
			staleIndexCount++
			continue
		}

		if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES ||
			request.DeadlineBlockHeight != item.deadline {
			k.removeFromExpirationIndex(ctx, item.deadline, item.reqID)
			staleIndexCount++
			continue
		}

		retried, retryErr := k.maybeAutoRetryThresholdSigningRequest(ctx, request, "deadline expired")
		if retryErr != nil {
			k.Logger().Error("Failed to auto-retry expired threshold signing request, falling back to EXPIRED",
				"request_id", fmt.Sprintf("%x", item.reqID), "error", retryErr)
		} else if retried {
			retriedCount++
			continue
		}

		if err := k.finalizeFailedThresholdSigningRequest(
			ctx,
			request,
			types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED,
			"deadline expired",
		); err != nil {
			k.Logger().Error("Failed to finalize expired threshold signing request",
				"request_id", fmt.Sprintf("%x", item.reqID), "error", err)
			continue
		}

		expiredCount++
	}

	processedDueCount := uint32(len(toProcess) + len(badKeys))
	if processedDueCount > 0 || staleIndexCount > 0 || hasBacklog {
		k.Logger().Info("Processed expired threshold signing requests",
			"block_height", currentBlockHeight,
			"processed_due_count", processedDueCount,
			"expired_count", expiredCount)
		k.Logger().Debug("Threshold signing deadline processing details",
			"retried_count", retriedCount,
			"stale_index_count", staleIndexCount,
			"bad_keys_cleaned", len(badKeys),
			"max_per_block", maxToProcess,
			"has_backlog", hasBacklog)
	}

	return nil
}

func (k Keeper) ProcessCompletedPostProcessRetries(ctx sdk.Context) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	retryStore := prefix.NewStore(store, types.CompletedPostProcessRetryPrefix)

	iterator := retryStore.Iterator(nil, nil)

	maxToProcess := maxCompletedPostProcessRetriesPerBlock
	if maxToProcess == 0 {
		maxToProcess = defaultMaxCompletedPostProcessRetriesPerBlock
	}

	var queuedRequestIDs [][]byte
	hasBacklog := false
	for ; iterator.Valid(); iterator.Next() {
		if uint32(len(queuedRequestIDs)) >= maxToProcess {
			hasBacklog = true
			break
		}
		queuedRequestIDs = append(queuedRequestIDs, append([]byte(nil), iterator.Key()...))
	}
	iterator.Close()

	var succeededCount uint32
	var failedCount uint32
	var staleCount uint32

	for _, requestID := range queuedRequestIDs {
		if len(requestID) == 0 {
			retryStore.Delete(requestID)
			staleCount++
			continue
		}

		request, err := k.GetSigningStatus(ctx, requestID)
		if err != nil {
			k.Logger().Error("Failed to load threshold signing request for completion retry",
				"request_id", fmt.Sprintf("%x", requestID), "error", err)
			retryStore.Delete(requestID)
			staleCount++
			continue
		}

		if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED {
			retryStore.Delete(requestID)
			staleCount++
			continue
		}

		if err := k.runThresholdSigningCompletedPostProcess(ctx, request.RequestId, request.CurrentEpochId); err != nil {
			k.Logger().Error("Failed to re-run threshold signing completion hooks",
				"request_id", fmt.Sprintf("%x", requestID), "error", err)
			failedCount++
			continue
		}

		retryStore.Delete(requestID)
		succeededCount++
	}

	processedCount := uint32(len(queuedRequestIDs))
	if processedCount > 0 || hasBacklog {
		k.Logger().Info("Processed threshold signing completion retries",
			"processed_count", processedCount,
			"succeeded_count", succeededCount,
			"failed_count", failedCount,
			"stale_count", staleCount,
			"max_per_block", maxToProcess,
			"has_backlog", hasBacklog)
	}

	return nil
}

func parseExpirationIndexEntry(key []byte) (int64, []byte, error) {
	if len(key) < 8 {
		return 0, nil, fmt.Errorf("invalid expiration index key length: %d", len(key))
	}

	deadlineBlockHeight := int64(binary.BigEndian.Uint64(key[:8]))
	requestID := append([]byte(nil), key[8:]...)
	if len(requestID) == 0 {
		return 0, nil, fmt.Errorf("missing request id in expiration index key")
	}

	return deadlineBlockHeight, requestID, nil
}
