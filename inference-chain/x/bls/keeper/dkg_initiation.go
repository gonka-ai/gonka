package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// InitiateKeyGenerationForEpoch initiates DKG for a given epoch with finalized participants
func (k Keeper) InitiateKeyGenerationForEpoch(ctx sdk.Context, epochID uint64, finalizedParticipants []types.ParticipantWithWeightAndKey) error {
	// Get module parameters
	params := k.GetParams(ctx)
	iTotalSlots := params.ITotalSlots
	tSlotsDegree := iTotalSlots - params.TSlotsDegreeOffset // Calculate t from offset

	// Perform deterministic slot assignment based on percentage weights
	blsParticipants, err := k.AssignSlots(finalizedParticipants, iTotalSlots)
	if err != nil {
		return fmt.Errorf("failed to assign slots: %w", err)
	}

	// Calculate phase deadlines
	currentHeight := ctx.BlockHeight()
	dealingPhaseDeadline := currentHeight + params.DealingPhaseDurationBlocks
	verifyingPhaseDeadline := dealingPhaseDeadline + params.VerificationPhaseDurationBlocks

	// Initialize DealerParts array with empty objects (not nil pointers) to prevent marshaling panic
	dealerParts := make([]*types.DealerPartStorage, len(blsParticipants))
	for i := range dealerParts {
		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress:     "", // Will be set when participant submits their part
			Commitments:       [][]byte{},
			ParticipantShares: []*types.EncryptedSharesForParticipant{},
		}
	}

	// Initialize VerificationSubmissions array with empty objects to use index-based access
	verificationSubmissions := make([]*types.VerificationVectorSubmission, len(blsParticipants))
	for i := range verificationSubmissions {
		verificationSubmissions[i] = &types.VerificationVectorSubmission{
			DealerValidity: []bool{}, // Empty array indicates no submission yet
		}
	}

	// Create EpochBLSData
	epochBLSData := types.EpochBLSData{
		EpochId:                     epochID,
		ITotalSlots:                 iTotalSlots,
		TSlotsDegree:                tSlotsDegree,
		Participants:                blsParticipants,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock:   dealingPhaseDeadline,
		VerifyingPhaseDeadlineBlock: verifyingPhaseDeadline,
		GroupPublicKey:              []byte{},
		DealerParts:                 dealerParts,
		VerificationSubmissions:     verificationSubmissions,
	}

	// Store the EpochBLSData
	k.SetEpochBLSData(ctx, epochBLSData)

	// Set this as the active epoch since only one DKG can be active at a time
	k.SetActiveEpochID(ctx, epochID)

	// Emit EventKeyGenerationInitiated
	event := types.EventKeyGenerationInitiated{
		EpochId:      epochID,
		ITotalSlots:  iTotalSlots,
		TSlotsDegree: tSlotsDegree,
		Participants: blsParticipants,
	}

	ctx.EventManager().EmitTypedEvent(&event)

	k.Logger().Info(
		"DKG initiated for epoch",
		"epoch_id", epochID,
		"participants", len(blsParticipants),
		"total_slots", iTotalSlots,
		"t_degree", tSlotsDegree,
		"dealing_deadline", dealingPhaseDeadline,
	)

	return nil
}

// AssignSlots performs deterministic slot assignment based on percentage weights
func (k Keeper) AssignSlots(participants []types.ParticipantWithWeightAndKey, totalSlots uint32) ([]types.BLSParticipantInfo, error) {
	if len(participants) == 0 {
		return nil, fmt.Errorf("no participants provided")
	}

	// Calculate total weight to normalize
	totalWeight := math.LegacyZeroDec()
	for _, p := range participants {
		totalWeight = totalWeight.Add(p.PercentageWeight)
	}

	if totalWeight.IsZero() {
		return nil, fmt.Errorf("total weight is zero")
	}

	blsParticipants := make([]types.BLSParticipantInfo, 0, len(participants))
	currentSlot := uint32(0)

	for i, participant := range participants {
		// Calculate number of slots for this participant
		participantRatio := participant.PercentageWeight.Quo(totalWeight)
		participantSlots := participantRatio.MulInt64(int64(totalSlots)).TruncateInt64()

		// Handle last participant to ensure all slots are assigned
		if i == len(participants)-1 {
			participantSlots = int64(totalSlots) - int64(currentSlot)
		}

		if participantSlots <= 0 {
			return nil, fmt.Errorf("participant %s has zero or negative slots", participant.Address)
		}

		startIndex := currentSlot
		endIndex := currentSlot + uint32(participantSlots) - 1

		// Ensure we don't exceed total slots
		if endIndex >= totalSlots {
			endIndex = totalSlots - 1
		}

		blsParticipant := types.BLSParticipantInfo{
			Address:            participant.Address,
			PercentageWeight:   participant.PercentageWeight,
			Secp256K1PublicKey: participant.Secp256k1PublicKey,
			SlotStartIndex:     startIndex,
			SlotEndIndex:       endIndex,
		}

		blsParticipants = append(blsParticipants, blsParticipant)
		currentSlot = endIndex + 1

		k.Logger().Debug(
			"Assigned slots to participant",
			"address", participant.Address,
			"weight", participant.PercentageWeight.String(),
			"slots", fmt.Sprintf("[%d, %d]", startIndex, endIndex),
			"slot_count", participantSlots,
		)
	}

	// Verify all slots are assigned
	if currentSlot != totalSlots {
		return nil, fmt.Errorf("slot assignment error: assigned %d slots but expected %d", currentSlot, totalSlots)
	}

	return blsParticipants, nil
}

// SetEpochBLSData stores EpochBLSData in the state
func (k Keeper) SetEpochBLSData(ctx sdk.Context, epochBLSData types.EpochBLSData) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.EpochBLSDataKey(epochBLSData.EpochId)
	value := k.cdc.MustMarshal(&epochBLSData)
	store.Set(key, value)
}

// GetEpochBLSData retrieves EpochBLSData from the state
func (k Keeper) GetEpochBLSData(ctx sdk.Context, epochID uint64) (types.EpochBLSData, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.EpochBLSDataKey(epochID)

	value, err := store.Get(key)
	if err != nil || value == nil {
		return types.EpochBLSData{}, false
	}

	var epochBLSData types.EpochBLSData
	k.cdc.MustUnmarshal(value, &epochBLSData)
	return epochBLSData, true
}
