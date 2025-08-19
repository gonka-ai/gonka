package keeper

import (
	"context"
	"encoding/binary"
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
	newEpochBLSData, found := ms.GetEpochBLSData(ctx, msg.NewEpochId)
	if !found {
		ms.Keeper.LogError("New epoch not found", "new_epoch_id", msg.NewEpochId)
		return nil, fmt.Errorf("new epoch %d not found", msg.NewEpochId)
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
	previousEpochBLSData, found := ms.GetEpochBLSData(ctx, previousEpochId)
	if !found {
		ms.Keeper.LogError("Previous epoch not found", "previous_epoch_id", previousEpochId)
		return nil, fmt.Errorf("previous epoch %d not found", previousEpochId)
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

	// Validate slot ownership - ensure submitted slot indices match participant's assigned range
	expectedSlots := make([]uint32, 0)
	for i := participantInfo.SlotStartIndex; i <= participantInfo.SlotEndIndex; i++ {
		expectedSlots = append(expectedSlots, i)
	}

	// Check if submitted slot indices exactly match expected slots
	if len(msg.SlotIndices) != len(expectedSlots) {
		ms.Keeper.LogError("Slot indices count mismatch", "expected_slots_count", len(expectedSlots), "submitted_slots_count", len(msg.SlotIndices))
		return nil, fmt.Errorf("slot indices count mismatch: expected %d, got %d", len(expectedSlots), len(msg.SlotIndices))
	}

	for i, slotIndex := range msg.SlotIndices {
		if slotIndex != expectedSlots[i] {
			ms.Keeper.LogError("Invalid slot index", "expected_slot_index", expectedSlots[i], "submitted_slot_index", slotIndex)
			return nil, fmt.Errorf("invalid slot index at position %d: expected %d, got %d", i, expectedSlots[i], slotIndex)
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
		ms.cdc.MustUnmarshal(bz, validationState)

		// Check if participant already submitted
		for _, partialSig := range validationState.PartialSignatures {
			if partialSig.ParticipantAddress == msg.Creator {
				ms.Keeper.LogError("Participant already submitted group key validation signature", "creator", msg.Creator)
				return nil, fmt.Errorf("participant %s already submitted group key validation signature", msg.Creator)
			}
		}
	}

	// Verify BLS partial signature against participant's computed individual public key
	if !ms.verifyBLSPartialSignature(msg.PartialSignature, validationState.MessageHash, &previousEpochBLSData, msg.SlotIndices) {
		ms.Keeper.LogError("Invalid BLS signature verification", "creator", msg.Creator)
		return nil, fmt.Errorf("invalid BLS signature verification failed for participant %s", msg.Creator)
	}
	ms.Keeper.LogInfo("Valid signature received", "creator", msg.Creator, "slots_count", len(msg.SlotIndices))

	// Add the partial signature
	partialSignature := &types.PartialSignature{
		ParticipantAddress: msg.Creator,
		SlotIndices:        msg.SlotIndices,
		Signature:          msg.PartialSignature,
	}
	validationState.PartialSignatures = append(validationState.PartialSignatures, *partialSignature)

	// Update slots covered
	validationState.SlotsCovered += uint32(len(msg.SlotIndices))

	// Check if we have sufficient participation (>50% of previous epoch slots)
	requiredSlots := previousEpochBLSData.ITotalSlots/2 + 1
	ms.Keeper.LogInfo("Checking for signature readiness", "required_slots", requiredSlots, "slots_covered", validationState.SlotsCovered)
	if validationState.SlotsCovered >= requiredSlots {
		ms.Keeper.LogInfo("Enough signatures collected, validating group key")
		// Aggregate signatures and finalize validation
		finalSignature, aggErr := ms.aggregateBLSPartialSignatures(validationState.PartialSignatures)
		if aggErr != nil {
			ms.Keeper.LogError("Failed to aggregate partial signatures", "error", aggErr.Error())
			return nil, fmt.Errorf("failed to aggregate partial signatures: %w", aggErr)
		}
		validationState.FinalSignature = finalSignature
		validationState.Status = types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_VALIDATED

		// Store the final signature in the new epoch's EpochBLSData and transition to SIGNED phase
		newEpochBLSData.ValidationSignature = validationState.FinalSignature
		newEpochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
		ms.SetEpochBLSData(ctx, newEpochBLSData)
		ms.Keeper.LogInfo("Group key validation completed", "new_epoch_id", msg.NewEpochId, "slots_covered", validationState.SlotsCovered)

		// Emit success event
		err := ctx.EventManager().EmitTypedEvent(&types.EventGroupKeyValidated{
			NewEpochId:     msg.NewEpochId,
			FinalSignature: validationState.FinalSignature,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to emit EventGroupKeyValidated: %w", err)
		}
	}

	// Store updated validation state
	bz = ms.cdc.MustMarshal(validationState)
	if err := store.Set([]byte(validationStateKey), bz); err != nil {
		return nil, fmt.Errorf("failed to store validation state: %w", err)
	}

	return &types.MsgSubmitGroupKeyValidationSignatureResponse{}, nil
}

// computeValidationMessageHash computes the message hash for group key validation
// This follows the Ethereum-compatible format: abi.encodePacked(previous_epoch_id, chain_id, new_epoch_id, data[0], data[1], data[2])
func (ms msgServer) computeValidationMessageHash(ctx sdk.Context, groupPublicKey []byte, previousEpochId, newEpochId uint64) ([]byte, error) {
	// Split the 96-byte G2 public key into 3x32-byte chunks
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}

	// Get chain ID - convert to bytes32 format for Ethereum compatibility
	chainIdStr := ctx.ChainID()
	chainIdBytes := make([]byte, 32)
	copy(chainIdBytes[32-len(chainIdStr):], []byte(chainIdStr)) // Right-pad with zeros

	// Implement Ethereum-compatible abi.encodePacked
	// Format: abi.encodePacked(previous_epoch_id, chain_id, new_epoch_id, data[0], data[1], data[2])
	var encodedData []byte

	// Add previous_epoch_id (uint64 -> 8 bytes big endian)
	previousEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(previousEpochBytes, previousEpochId)
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
