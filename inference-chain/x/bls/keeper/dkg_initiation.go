package keeper

import (
	"fmt"
	"sort"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// InitiateKeyGenerationForEpoch initiates DKG for a given epoch with finalized participants
func (k Keeper) InitiateKeyGenerationForEpoch(ctx sdk.Context, epochID uint64, finalizedParticipants []types.ParticipantWithWeightAndKey) error {
	// Get module parameters
	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get parameters: %w", err)
	}
	iTotalSlots := params.ITotalSlots
	tSlotsDegree := iTotalSlots - params.TSlotsDegreeOffset // Calculate t from offset

	// Perform deterministic slot assignment based on percentage weights
	blsParticipants, err := k.AssignSlots(ctx, finalizedParticipants, iTotalSlots)
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
	if err := k.SetEpochBLSData(ctx, epochBLSData); err != nil {
		return fmt.Errorf("failed to store epoch %d BLS data: %w", epochID, err)
	}

	// Set this as the active epoch since only one DKG can be active at a time
	k.SetActiveEpochID(ctx, epochID)

	// Emit EventKeyGenerationInitiated
	event := types.EventKeyGenerationInitiated{
		EpochId:      epochID,
		ITotalSlots:  iTotalSlots,
		TSlotsDegree: tSlotsDegree,
		Participants: blsParticipants,
	}

	if err := ctx.EventManager().EmitTypedEvent(&event); err != nil {
		return fmt.Errorf("failed to emit key generation initiated event for epoch %d: %w", epochID, err)
	}

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
func (k Keeper) AssignSlots(ctx sdk.Context, participants []types.ParticipantWithWeightAndKey, totalSlots uint32) ([]types.BLSParticipantInfo, error) {
	if len(participants) == 0 {
		return nil, fmt.Errorf("no participants provided")
	}

	// 1. Calculate total weight to normalize percentage values into ratios.
	totalWeight := math.LegacyZeroDec()
	for _, p := range participants {
		totalWeight = totalWeight.Add(p.PercentageWeight)
	}

	if totalWeight.IsZero() {
		return nil, fmt.Errorf("total weight is zero")
	}

	// 2. Sort by address so every node processes participants in exactly the same order.
	sortedParticipants := make([]types.ParticipantWithWeightAndKey, len(participants))
	copy(sortedParticipants, participants)
	sort.Slice(sortedParticipants, func(i, j int) bool {
		return sortedParticipants[i].Address < sortedParticipants[j].Address
	})

	// 3. Allocate floor(ratio * totalSlots) slots to each participant and remember the fractional remainders.
	// Slot allocation is strictly weight-based over the full participant set. Participants may receive zero slots.
	assigned := make([]int64, len(sortedParticipants))
	remainders := make([]math.LegacyDec, len(sortedParticipants))
	assignedTotal := int64(0)

	for i, participant := range sortedParticipants {
		if participant.PercentageWeight.IsZero() {
			continue
		}

		ratio := participant.PercentageWeight.Quo(totalWeight)
		slotDec := ratio.MulInt64(int64(totalSlots))
		floor := slotDec.TruncateInt64()
		remainder := slotDec.Sub(math.LegacyNewDec(floor))
		if remainder.IsNegative() {
			remainder = math.LegacyZeroDec()
		}

		assigned[i] = floor
		remainders[i] = remainder
		assignedTotal += floor
	}

	// Remaining slots are distributed by largest remainder, breaking ties by address.
	remaining := int64(totalSlots) - assignedTotal
	if remaining < 0 {
		return nil, fmt.Errorf("slot assignment error: floor allocations exceed total slots")
	}

	if remaining > 0 {
		indices := make([]int, 0, len(sortedParticipants))
		for i, p := range sortedParticipants {
			if p.PercentageWeight.IsZero() {
				continue
			}
			indices = append(indices, i)
		}

		sort.SliceStable(indices, func(i, j int) bool {
			ri := remainders[indices[i]]
			rj := remainders[indices[j]]
			switch {
			case ri.Equal(rj):
				return sortedParticipants[indices[i]].Address < sortedParticipants[indices[j]].Address
			default:
				return ri.GT(rj)
			}
		})

		for _, idx := range indices {
			if remaining == 0 {
				break
			}
			assigned[idx]++
			remaining--
		}
	}

	// 4. Final validation: slot counts should sum to totalSlots.
	checkTotal := int64(0)
	for _, cnt := range assigned {
		checkTotal += cnt
	}
	if checkTotal != int64(totalSlots) {
		return nil, fmt.Errorf("slot assignment mismatch: expected %d, got %d", totalSlots, checkTotal)
	}

	// Log the amount of non-zero voting power that got zero slots under strict weight allocation.
	nonZeroCount := 0
	excludedCount := 0
	excludedWeight := math.LegacyZeroDec()
	for i, p := range sortedParticipants {
		if p.PercentageWeight.IsZero() {
			continue
		}
		nonZeroCount++
		if assigned[i] == 0 {
			excludedCount++
			excludedWeight = excludedWeight.Add(p.PercentageWeight)
		}
	}
	if excludedCount > 0 {
		excludedPercentage := excludedWeight.Quo(totalWeight).Mul(math.LegacyNewDec(100))
		k.Logger().Warn(
			"Some non-zero-weight participants received zero slots under strict weight allocation",
			"non_zero_participant_count", nonZeroCount,
			"excluded_participant_count", excludedCount,
			"excluded_weight_percentage", excludedPercentage.String(),
			"total_slots", totalSlots,
		)
	}

	// 5. Build the BLS participant list with contiguous slot ranges.
	blsParticipants := make([]types.BLSParticipantInfo, 0, len(sortedParticipants))
	currentSlot := uint32(0)
	for i, participant := range sortedParticipants {
		slotCount := assigned[i]
		if slotCount <= 0 {
			continue
		}

		startIndex := currentSlot
		endIndex := startIndex + uint32(slotCount) - 1

		// Older versions clamped endIndex to totalSlots - 1 as a
		// defensive fallback. The assignment uses fixed-point decimals (LegacyDec),
		// not floating-point math. With the current checks (including checkTotal
		// above), this branch should be unreachable, so we fail fast instead of
		// masking a logic bug with silent clamping.
		if endIndex >= totalSlots {
			return nil, fmt.Errorf("slot assignment overflow: ending slot index %d exceeds total slots %d", endIndex, totalSlots)
		}

		blsParticipant := types.BLSParticipantInfo{
			Address:                    participant.Address,
			PercentageWeight:           participant.PercentageWeight,
			Secp256K1PublicKey:         participant.Secp256k1PublicKey,
			AllowedSecp256K1PublicKeys: participant.AllowedSecp256k1PublicKeys,
			SlotStartIndex:             startIndex,
			SlotEndIndex:               endIndex,
		}

		blsParticipants = append(blsParticipants, blsParticipant)
		currentSlot = endIndex + 1

		k.Logger().Debug(
			"Assigned slots to participant",
			"address", participant.Address,
			"weight", participant.PercentageWeight.String(),
			"slots", fmt.Sprintf("[%d, %d]", startIndex, endIndex),
			"slot_count", slotCount,
		)
	}

	// Verify all slots are assigned
	if currentSlot != totalSlots {
		return nil, fmt.Errorf("slot assignment error: assigned %d slots but expected %d", currentSlot, totalSlots)
	}

	return blsParticipants, nil
}

// SetEpochBLSData stores EpochBLSData in the state
func (k Keeper) SetEpochBLSData(ctx sdk.Context, epochBLSData types.EpochBLSData) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.EpochBLSDataKey(epochBLSData.EpochId)
	value, err := k.cdc.Marshal(&epochBLSData)
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// GetEpochBLSData retrieves EpochBLSData from the state
func (k Keeper) GetEpochBLSData(ctx sdk.Context, epochID uint64) (types.EpochBLSData, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.EpochBLSDataKey(epochID)

	value, err := store.Get(key)
	if err != nil {
		return types.EpochBLSData{}, err
	}
	if value == nil {
		return types.EpochBLSData{}, types.ErrEpochBLSDataNotFound
	}

	var epochBLSData types.EpochBLSData
	err = k.cdc.Unmarshal(value, &epochBLSData)
	if err != nil {
		return types.EpochBLSData{}, err
	}
	return epochBLSData, nil
}
