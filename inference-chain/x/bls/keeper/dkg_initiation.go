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
	params := k.GetParams(ctx)
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

	// Count how many participants actually carry weight; we must be able to give each of them >= 1 slot.
	nonZeroCount := 0
	for _, p := range sortedParticipants {
		if !p.PercentageWeight.IsZero() {
			nonZeroCount++
		}
	}

	// If we have more non-zero participants than slots, select the top N by weight
	if nonZeroCount > int(totalSlots) {
		// Calculate weight of participants that will be excluded for logging
		excludedWeight := math.LegacyZeroDec()
		excludedCount := nonZeroCount - int(totalSlots)

		// Sort by weight descending, then by address for determinism
		sort.Slice(sortedParticipants, func(i, j int) bool {
			if sortedParticipants[i].PercentageWeight.Equal(sortedParticipants[j].PercentageWeight) {
				return sortedParticipants[i].Address < sortedParticipants[j].Address
			}
			return sortedParticipants[i].PercentageWeight.GT(sortedParticipants[j].PercentageWeight)
		})

		// Calculate weight of excluded participants (those beyond totalSlots)
		for i := int(totalSlots); i < len(sortedParticipants); i++ {
			excludedWeight = excludedWeight.Add(sortedParticipants[i].PercentageWeight)
		}

		// Defensive check: verify we can safely slice (should always be true given nonZeroCount > totalSlots)
		if int(totalSlots) > len(sortedParticipants) {
			return nil, fmt.Errorf("internal error: totalSlots %d exceeds participant count %d", totalSlots, len(sortedParticipants))
		}

		// Keep only top totalSlots participants
		sortedParticipants = sortedParticipants[:totalSlots]

		// Recalculate total weight for the selected participants
		totalWeight = math.LegacyZeroDec()
		for _, p := range sortedParticipants {
			totalWeight = totalWeight.Add(p.PercentageWeight)
		}

		// Critical safety check: verify totalWeight is not zero after selection
		if totalWeight.IsZero() {
			return nil, fmt.Errorf("total weight is zero after participant selection")
		}

		excludedPercentage := excludedWeight.Quo(totalWeight.Add(excludedWeight)).Mul(math.LegacyNewDec(100))

		k.Logger().Warn(
			"Participant count exceeds available slots, selected top participants by weight",
			"original_participant_count", nonZeroCount,
			"selected_participant_count", totalSlots,
			"excluded_participant_count", excludedCount,
			"excluded_weight_percentage", excludedPercentage.String(),
		)
	}

	// 3. Allocate floor(ratio * totalSlots) slots to each participant and remember the fractional remainders.
	// Note: Arrays are sized after potential participant truncation to match final count
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

	// 4. Ensure every non-zero-weight participant has at least one slot.
	for i, p := range sortedParticipants {
		if p.PercentageWeight.IsZero() {
			continue
		}
		if assigned[i] > 0 {
			continue
		}

		donor := findDonorIndex(assigned, remainders, sortedParticipants)
		if donor == -1 {
			return nil, fmt.Errorf("unable to assign at least one slot to participant %s", p.Address)
		}

		assigned[donor]--
		assigned[i]++
	}

	// 5. Final validation: slot counts should sum to totalSlots.
	checkTotal := int64(0)
	for _, cnt := range assigned {
		checkTotal += cnt
	}
	if checkTotal != int64(totalSlots) {
		return nil, fmt.Errorf("slot assignment mismatch: expected %d, got %d", totalSlots, checkTotal)
	}

	// 6. Build the BLS participant list with contiguous slot ranges.
	blsParticipants := make([]types.BLSParticipantInfo, 0, len(sortedParticipants))
	currentSlot := uint32(0)
	for i, participant := range sortedParticipants {
		slotCount := assigned[i]
		if slotCount <= 0 {
			continue
		}

		startIndex := currentSlot
		endIndex := startIndex + uint32(slotCount) - 1
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
			"slot_count", slotCount,
		)
	}

	// Verify all slots are assigned
	if currentSlot != totalSlots {
		return nil, fmt.Errorf("slot assignment error: assigned %d slots but expected %d", currentSlot, totalSlots)
	}

	return blsParticipants, nil
}

func findDonorIndex(assigned []int64, remainders []math.LegacyDec, participants []types.ParticipantWithWeightAndKey) int {
	donor := -1
	for i, p := range participants {
		if p.PercentageWeight.IsZero() {
			continue
		}
		if assigned[i] <= 1 {
			continue
		}
		if donor == -1 {
			donor = i
			continue
		}

		if assigned[i] > assigned[donor] {
			donor = i
			continue
		}
		if assigned[i] == assigned[donor] {
			ri := remainders[i]
			rd := remainders[donor]
			if !ri.Equal(rd) {
				if ri.LT(rd) {
					donor = i
				}
				continue
			}
			if participants[i].Address < participants[donor].Address {
				donor = i
			}
		}
	}
	return donor
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
