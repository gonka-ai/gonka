package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
	"golang.org/x/crypto/sha3"
)

func (k Keeper) LogInfo(msg string, keyvals ...interface{}) {
	k.Logger().Info(msg, append(keyvals, "subsystem", "BLS")...)
}

func (k Keeper) LogError(msg string, keyvals ...interface{}) {
	k.Logger().Error(msg, append(keyvals, "subsystem", "BLS")...)
}

func (k Keeper) LogWarn(msg string, keyvals ...interface{}) {
	k.Logger().Warn(msg, append(keyvals, "subsystem", "BLS")...)
}

func (k Keeper) LogDebug(msg string, keyVals ...interface{}) {
	k.Logger().Debug(msg, append(keyVals, "subsystem", "BLS")...)
}

// SubmitGroupKeyValidationSignature handles the submission of partial signatures for group key validation
func (ms msgServer) SubmitGroupKeyValidationSignature(goCtx context.Context, msg *types.MsgSubmitGroupKeyValidationSignature) (*types.MsgSubmitGroupKeyValidationSignatureResponse, error) {
	ms.Keeper.LogInfo("Processing group key validation signature", "new_epoch_id", msg.NewEpochId, "creator", msg.Creator)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Genesis case: Epoch 1 doesn't need validation (no previous epoch)
	if msg.NewEpochId == 1 {
		ms.Keeper.LogInfo("Rejecting group key validation for genesis epoch", "new_epoch_id", msg.NewEpochId)
		return nil, fmt.Errorf("epoch 1 does not require group key validation (genesis case)")
	}

	previousEpochId := msg.NewEpochId - 1

	// Get the new epoch's BLS data to get the group public key being validated
	newEpochBLSData, err := ms.GetEpochBLSData(ctx, msg.NewEpochId)
	if err != nil {
		ms.Keeper.LogError("Failed to get new epoch BLS data", "new_epoch_id", msg.NewEpochId, "error", err.Error())
		return nil, fmt.Errorf("failed to get new epoch %d BLS data: %w", msg.NewEpochId, err)
	}

	// Ensure the new epoch has completed DKG
	if newEpochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED && newEpochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_SIGNED {
		ms.Keeper.LogError("Invalid DKG phase for group key validation", "new_epoch_id", msg.NewEpochId, "current_phase", newEpochBLSData.DkgPhase.String())
		return nil, fmt.Errorf("new epoch %d DKG is not completed (current phase: %s)", msg.NewEpochId, newEpochBLSData.DkgPhase.String())
	}

	// If already signed, silently ignore the submission
	if newEpochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED {
		ms.Keeper.LogInfo("Group key validation already completed", "new_epoch_id", msg.NewEpochId)
		return &types.MsgSubmitGroupKeyValidationSignatureResponse{}, nil
	}

	// Get the previous epoch's BLS data for slot validation and signature verification
	previousEpochBLSData, err := ms.GetEpochBLSData(ctx, previousEpochId)
	if err != nil {
		if errors.Is(err, types.ErrEpochBLSDataNotFound) {
			// Emit a searchable event and continue using current epoch data as fallback
			ms.Keeper.LogWarn("Previous epoch not found - using current epoch for validation", "previous_epoch_id", previousEpochId, "new_epoch_id", msg.NewEpochId)
			ctx.EventManager().EmitTypedEvent(&types.EventGroupKeyValidationFailed{
				NewEpochId: msg.NewEpochId,
				Reason:     fmt.Sprintf("previous_epoch_missing_fallback:%d", previousEpochId),
			})

			previousEpochBLSData = newEpochBLSData
		} else {
			ms.Keeper.LogError("Failed to get previous epoch BLS data", "previous_epoch_id", previousEpochId, "error", err.Error())
			return nil, fmt.Errorf("failed to get previous epoch %d BLS data: %w", previousEpochId, err)
		}
	}

	// Find the participant in the previous epoch
	participantIndex := -1
	var participantInfo *types.BLSParticipantInfo
	for i, participant := range previousEpochBLSData.Participants {
		if participant.Address == msg.Creator {
			participantIndex = i
			participantInfo = &participant
			break
		}
	}

	if participantIndex == -1 {
		ms.Keeper.LogError("Participant not found in previous epoch", "creator", msg.Creator, "previous_epoch_id", previousEpochId)
		return nil, fmt.Errorf("participant %s not found in previous epoch %d", msg.Creator, previousEpochId)
	}

	// Validate slot ownership - ensure each submitted slot is within participant's assigned range
	for _, slotIndex := range msg.SlotIndices {
		if slotIndex < participantInfo.SlotStartIndex || slotIndex > participantInfo.SlotEndIndex {
			ms.Keeper.LogError("Submitted slot out of participant range", "slot_index", slotIndex, "range_start", participantInfo.SlotStartIndex, "range_end", participantInfo.SlotEndIndex)
			return nil, fmt.Errorf("submitted slot %d outside participant range [%d, %d]", slotIndex, participantInfo.SlotStartIndex, participantInfo.SlotEndIndex)
		}
	}

	// Check or create GroupKeyValidationState
	var validationState *types.GroupKeyValidationState
	validationStateKey := fmt.Sprintf("group_validation_%d", msg.NewEpochId)

	// Try to get existing validation state
	store := ms.storeService.OpenKVStore(ctx)
	bz, err := store.Get([]byte(validationStateKey))
	if err != nil {
		ms.Keeper.LogError("Failed to get validation state", "new_epoch_id", msg.NewEpochId, "error", err.Error())
		return nil, fmt.Errorf("failed to get validation state: %w", err)
	}

	if bz == nil {
		// First signature for this epoch - create validation state
		validationState = &types.GroupKeyValidationState{
			NewEpochId:      msg.NewEpochId,
			PreviousEpochId: previousEpochId,
			Status:          types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_COLLECTING_SIGNATURES,
			SlotsCovered:    0,
		}
		ms.Keeper.LogInfo("Created new validation state", "new_epoch_id", msg.NewEpochId, "previous_epoch_id", previousEpochId)

		// Prepare validation data for message hash
		messageHash, err := ms.computeValidationMessageHash(ctx, newEpochBLSData.GroupPublicKey, previousEpochId, msg.NewEpochId)
		if err != nil {
			ms.Keeper.LogError("Failed to compute message hash", "error", err.Error())
			return nil, fmt.Errorf("failed to compute message hash: %w", err)
		}
		validationState.MessageHash = messageHash
	} else {
		// Existing validation state
		validationState = &types.GroupKeyValidationState{}
		err = ms.cdc.Unmarshal(bz, validationState)
		if err != nil {
			ms.Keeper.LogError("Failed to unmarshal validation state", "error", err.Error())
			return nil, fmt.Errorf("failed to unmarshal validation state: %w", err)
		}
	}

	// Reject duplicate slots (already covered)
	seen := make(map[uint32]struct{})
	for _, ps := range validationState.PartialSignatures {
		for _, idx := range ps.SlotIndices {
			seen[idx] = struct{}{}
		}
	}

	// The partial signature payload is a concatenation of 48-byte G1 signatures, one per slot index.
	// Ensure the payload shape matches the submitted slot list so we can filter both in lockstep.
	if len(msg.PartialSignature)%48 != 0 {
		return nil, fmt.Errorf("invalid partial signature length: %d (must be multiple of 48)", len(msg.PartialSignature))
	}
	if len(msg.PartialSignature)/48 != len(msg.SlotIndices) {
		return nil, fmt.Errorf("signature count mismatch: got %d signatures for %d slots", len(msg.PartialSignature)/48, len(msg.SlotIndices))
	}

	filteredSlots := make([]uint32, 0, len(msg.SlotIndices))
	filteredSig := make([]byte, 0, len(msg.PartialSignature))
	for i, idx := range msg.SlotIndices {
		if _, ok := seen[idx]; ok {
			ms.Keeper.LogWarn("Ignoring duplicate slot submission", "slot_index", idx, "creator", msg.Creator)
			continue
		}
		seen[idx] = struct{}{}
		filteredSlots = append(filteredSlots, idx)
		start := i * 48
		end := start + 48
		filteredSig = append(filteredSig, msg.PartialSignature[start:end]...)
	}
	if len(filteredSlots) == 0 {
		return nil, fmt.Errorf("no new slots in submission")
	}

	// Verify BLS partial signature against participant's computed individual public key
	if !ms.verifyBLSPartialSignatureBlst(filteredSig, validationState.MessageHash, &previousEpochBLSData, filteredSlots) {
		ms.Keeper.LogError("Invalid BLS signature verification", "creator", msg.Creator)
		return nil, fmt.Errorf("invalid BLS signature verification failed for participant %s", msg.Creator)
	}
	ms.Keeper.LogInfo("Valid signature received", "creator", msg.Creator, "slots_count", len(filteredSlots))

	// Add the partial signature
	partialSignature := &types.PartialSignature{
		ParticipantAddress: msg.Creator,
		SlotIndices:        filteredSlots,
		Signature:          filteredSig,
	}
	validationState.PartialSignatures = append(validationState.PartialSignatures, *partialSignature)

	// Update slots covered
	validationState.SlotsCovered += uint32(len(filteredSlots))

	// Check if we have sufficient participation (>50% of previous epoch slots)
	requiredSlots := previousEpochBLSData.ITotalSlots/2 + 1
	ms.Keeper.LogInfo("Checking for signature readiness", "required_slots", requiredSlots, "slots_covered", validationState.SlotsCovered)
	if validationState.SlotsCovered >= requiredSlots {
		ms.Keeper.LogInfo("Enough signatures collected, validating group key")
		// Aggregate signatures and finalize validation
		finalSignature, aggErr := ms.aggregateBLSPartialSignaturesBlst(validationState.PartialSignatures)
		if aggErr != nil {
			ms.Keeper.LogError("Failed to aggregate partial signatures", "error", aggErr.Error())
			return nil, fmt.Errorf("failed to aggregate partial signatures: %w", aggErr)
		}

		// Verify aggregated final signature against previous epoch group public key
		if !ms.verifyFinalSignatureBlst(finalSignature, validationState.MessageHash, previousEpochBLSData.GroupPublicKey) {
			ms.Keeper.LogError(
				"Final aggregated signature verification failed",
				"previous_epoch_id", previousEpochId,
				"hash32_hex", fmt.Sprintf("%x", validationState.MessageHash),
			)
			return nil, fmt.Errorf("final aggregated signature failed verification against previous epoch group key")
		}

		validationState.FinalSignature = finalSignature
		validationState.Status = types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_VALIDATED

		// Store the final signature in the new epoch's EpochBLSData and transition to SIGNED phase
		newEpochBLSData.ValidationSignature = validationState.FinalSignature
		newEpochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
		if err := ms.SetEpochBLSData(ctx, newEpochBLSData); err != nil {
			ms.Keeper.LogError("Failed to save updated epoch BLS data", "new_epoch_id", msg.NewEpochId, "error", err.Error())
			return nil, fmt.Errorf("failed to save updated epoch %d BLS data: %w", msg.NewEpochId, err)
		}
		ms.Keeper.LogInfo("Group key validation completed", "new_epoch_id", msg.NewEpochId, "slots_covered", validationState.SlotsCovered)

		// Emit success event
		err = ctx.EventManager().EmitTypedEvent(&types.EventGroupKeyValidated{
			NewEpochId:     msg.NewEpochId,
			FinalSignature: validationState.FinalSignature,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to emit EventGroupKeyValidated: %w", err)
		}
	}

	// Store updated validation state
	bz, err = ms.cdc.Marshal(validationState)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal validation state: %w", err)
	}
	err = store.Set([]byte(validationStateKey), bz)
	if err != nil {
		return nil, fmt.Errorf("failed to store validation state: %w", err)
	}

	return &types.MsgSubmitGroupKeyValidationSignatureResponse{}, nil
}

// computeValidationMessageHash computes the message hash for group key validation.
// Uses Ethereum-compatible abi.encodePacked(previous_epoch_id [8], chain_id [32], new_group_key_uncompressed [256]).
func (ms msgServer) computeValidationMessageHash(ctx sdk.Context, groupPublicKey []byte, previousEpochId, newEpochId uint64) ([]byte, error) {
	// Decompress and format group key using helper (blst optimized)
	g2Uncompressed256, err := ms.Keeper.DecompressG2To256Blst(groupPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress group key: %w", err)
	}

	// Use GONKA_CHAIN_ID bytes32 (hash of chain-id string), consistent with bridge signing logic
	gonkaIdHash := sha256.Sum256([]byte(ctx.ChainID()))
	chainIdBytes := gonkaIdHash[:]

	var encodedData []byte

	// Add previous_epoch_id (uint64 -> 8 bytes big endian)
	previousEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(previousEpochBytes, previousEpochId)
	encodedData = append(encodedData, previousEpochBytes...)

	// Add chain_id (32 bytes)
	encodedData = append(encodedData, chainIdBytes...)

	// Add the 256-byte uncompressed G2 key
	encodedData = append(encodedData, g2Uncompressed256...)

	// Compute keccak256 hash (Ethereum-compatible)
	hash := sha3.NewLegacyKeccak256()
	hash.Write(encodedData)
	messageHash := hash.Sum(nil)

	return messageHash, nil
}
