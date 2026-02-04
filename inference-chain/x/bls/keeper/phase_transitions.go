package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// ProcessDKGPhaseTransitions checks the currently active DKG epoch and transitions it to the next phase if deadline has passed
func (k Keeper) ProcessDKGPhaseTransitions(ctx sdk.Context) error {
	// Get the currently active epoch ID
	activeEpochID, found := k.GetActiveEpochID(ctx)
	if !found || activeEpochID == 0 {
		// No active DKG - this is normal
		return nil
	}

	// Process phase transition for the active epoch
	return k.ProcessDKGPhaseTransitionForEpoch(ctx, activeEpochID)
}

// ProcessDKGPhaseTransitionForEpoch checks a specific epoch's DKG and transitions it if needed
func (k Keeper) ProcessDKGPhaseTransitionForEpoch(ctx sdk.Context, epochID uint64) error {
	epochBLSData, err := k.GetEpochBLSData(ctx, epochID)
	if err != nil {
		return fmt.Errorf("failed to get EpochBLSData for epoch %d: %w", epochID, err)
	}

	// Skip completed or failed DKGs
	if epochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED ||
		epochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED ||
		epochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_FAILED {
		return nil
	}

	currentBlockHeight := ctx.BlockHeight()

	switch epochBLSData.DkgPhase {
	case types.DKGPhase_DKG_PHASE_DEALING:
		if currentBlockHeight >= epochBLSData.DealingPhaseDeadlineBlock {
			if err := k.TransitionToVerifyingPhase(ctx, &epochBLSData); err != nil {
				return fmt.Errorf("failed to transition DKG to verifying phase for epoch %d: %w", epochID, err)
			}
		}
	case types.DKGPhase_DKG_PHASE_VERIFYING:
		if currentBlockHeight >= epochBLSData.VerifyingPhaseDeadlineBlock {
			if err := k.CompleteDKG(ctx, &epochBLSData); err != nil {
				return fmt.Errorf("failed to complete DKG for epoch %d: %w", epochID, err)
			}
		}
	}

	return nil
}

// TransitionToVerifyingPhase transitions a DKG from DEALING phase to either VERIFYING or FAILED based on participation
func (k Keeper) TransitionToVerifyingPhase(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_DEALING {
		return fmt.Errorf("DKG for epoch %d is not in DEALING phase, current phase: %s", epochBLSData.EpochId, epochBLSData.DkgPhase.String())
	}

	// Calculate total slots covered by participants who submitted dealer parts
	slotsWithDealerParts := k.CalculateSlotsWithDealerParts(epochBLSData)

	k.Logger().Info("Checking DKG participation",
		"epochId", epochBLSData.EpochId,
		"slotsWithDealerParts", slotsWithDealerParts,
		"totalSlots", epochBLSData.ITotalSlots,
		"requiredSlots", epochBLSData.ITotalSlots/2)

	// Check if we have sufficient participation (more than half the slots)
	if slotsWithDealerParts > epochBLSData.ITotalSlots/2 {
		// Sufficient participation - transition to VERIFYING
		params, err := k.GetParams(ctx)
		if err != nil {
			return fmt.Errorf("failed to get params: %w", err)
		}
		currentBlockHeight := ctx.BlockHeight()

		epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING
		epochBLSData.VerifyingPhaseDeadlineBlock = currentBlockHeight + params.VerificationPhaseDurationBlocks

		// Store updated epoch data
		if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
			return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
		}

		// Emit event for verifying phase started
		if err := ctx.EventManager().EmitTypedEvent(&types.EventVerifyingPhaseStarted{
			EpochId:                     epochBLSData.EpochId,
			VerifyingPhaseDeadlineBlock: uint64(epochBLSData.VerifyingPhaseDeadlineBlock),
			EpochData:                   *epochBLSData,
		}); err != nil {
			return fmt.Errorf("failed to emit EventVerifyingPhaseStarted for epoch %d: %w", epochBLSData.EpochId, err)
		}

		k.Logger().Info("DKG transitioned to VERIFYING phase",
			"epochId", epochBLSData.EpochId,
			"verifyingDeadline", epochBLSData.VerifyingPhaseDeadlineBlock)

	} else {
		// Insufficient participation - mark as FAILED
		epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_FAILED

		// Store updated epoch data
		if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
			return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
		}

		// Clear active epoch since DKG process is complete (failed)
		k.ClearActiveEpochID(ctx)

		// Emit event for DKG failure
		failureReason := fmt.Sprintf("Insufficient participation in dealing phase: %d slots with dealer parts out of %d total slots (required: >%d)",
			slotsWithDealerParts, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)

		if err := ctx.EventManager().EmitTypedEvent(&types.EventDKGFailed{
			EpochId:   epochBLSData.EpochId,
			Reason:    failureReason,
			EpochData: *epochBLSData,
		}); err != nil {
			return fmt.Errorf("failed to emit EventDKGFailed for epoch %d: %w", epochBLSData.EpochId, err)
		}

		k.Logger().Info("DKG marked as FAILED due to insufficient participation",
			"epochId", epochBLSData.EpochId,
			"reason", failureReason)
	}

	return nil
}

// CalculateSlotsWithDealerParts calculates the total number of slots covered by participants who submitted dealer parts
func (k Keeper) CalculateSlotsWithDealerParts(epochBLSData *types.EpochBLSData) uint32 {
	var totalSlots uint32 = 0

	// Create a map to track which participant indices have submitted dealer parts
	hasSubmittedDealerPart := make(map[int]bool)
	for i, dealerPart := range epochBLSData.DealerParts {
		if dealerPart != nil && dealerPart.DealerAddress != "" {
			hasSubmittedDealerPart[i] = true
		}
	}

	// Sum up slots for participants who submitted dealer parts
	for i, participant := range epochBLSData.Participants {
		if hasSubmittedDealerPart[i] {
			// Calculate number of slots for this participant
			participantSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1
			totalSlots += participantSlots
		}
	}

	return totalSlots
}

// CompleteDKG attempts to complete the DKG by checking verification participation and computing group public key
func (k Keeper) CompleteDKG(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
		return fmt.Errorf("DKG for epoch %d is not in VERIFYING phase, current phase: %s", epochBLSData.EpochId, epochBLSData.DkgPhase.String())
	}

	// Calculate total slots covered by participants who submitted verification vectors
	slotsWithVerification := k.CalculateSlotsWithVerificationVectors(epochBLSData)

	k.Logger().Info("Checking DKG verification participation",
		"epochId", epochBLSData.EpochId,
		"slotsWithVerification", slotsWithVerification,
		"totalSlots", epochBLSData.ITotalSlots,
		"requiredSlots", epochBLSData.ITotalSlots/2)

	// Check if we have sufficient verification participation (more than half the slots)
	if slotsWithVerification > epochBLSData.ITotalSlots/2 {
		// Sufficient verification participation - compute group public key using dealer consensus
		validDealers, err := k.DetermineValidDealersWithConsensus(epochBLSData)
		if err != nil {
			return fmt.Errorf("failed to determine valid dealers for epoch %d: %w", epochBLSData.EpochId, err)
		}

		groupPublicKey, err := k.ComputeGroupPublicKey(epochBLSData, validDealers)
		if err != nil {
			return fmt.Errorf("failed to compute group public key for epoch %d: %w", epochBLSData.EpochId, err)
		}

		// Store group public key and mark as completed
		epochBLSData.GroupPublicKey = groupPublicKey
		epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_COMPLETED

		// Store valid dealers in epoch data
		epochBLSData.ValidDealers = validDealers

		// Precompute per-slot public keys for faster validation later
		slotPublicKeys, err := k.PrecomputeSlotPublicKeysBlst(epochBLSData)
		if err != nil {
			return fmt.Errorf("failed to precompute slot public keys for epoch %d: %w", epochBLSData.EpochId, err)
		}
		epochBLSData.SlotPublicKeys = slotPublicKeys

		// Store updated epoch data
		if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
			return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
		}

		// Clear active epoch since DKG process is complete (successfully)
		k.ClearActiveEpochID(ctx)

		// Emit event for successful DKG completion
		if err := ctx.EventManager().EmitTypedEvent(&types.EventGroupPublicKeyGenerated{
			EpochId:        epochBLSData.EpochId,
			GroupPublicKey: groupPublicKey,
			ITotalSlots:    epochBLSData.ITotalSlots,
			TSlotsDegree:   epochBLSData.TSlotsDegree,
			EpochData:      *epochBLSData,
			ChainId:        ctx.ChainID(),
		}); err != nil {
			return fmt.Errorf("failed to emit EventGroupPublicKeyGenerated for epoch %d: %w", epochBLSData.EpochId, err)
		}

		k.Logger().Info("DKG completed successfully",
			"epochId", epochBLSData.EpochId,
			"validDealersCount", func() int {
				count := 0
				for _, isValid := range validDealers {
					if isValid {
						count++
					}
				}
				return count
			}(),
			"groupPublicKeySize", len(groupPublicKey))

	} else {
		// Insufficient verification participation - mark as FAILED
		epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_FAILED

		// Store updated epoch data
		if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
			return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
		}

		// Clear active epoch since DKG process is complete (failed)
		k.ClearActiveEpochID(ctx)

		// Emit event for DKG failure
		failureReason := fmt.Sprintf("Insufficient participation in verification phase: %d slots with verification vectors out of %d total slots (required: >%d)",
			slotsWithVerification, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)

		if err := ctx.EventManager().EmitTypedEvent(&types.EventDKGFailed{
			EpochId:   epochBLSData.EpochId,
			Reason:    failureReason,
			EpochData: *epochBLSData,
		}); err != nil {
			return fmt.Errorf("failed to emit EventDKGFailed for epoch %d: %w", epochBLSData.EpochId, err)
		}

		k.Logger().Info("DKG marked as FAILED due to insufficient verification participation",
			"epochId", epochBLSData.EpochId,
			"reason", failureReason)
	}

	return nil
}

// CalculateSlotsWithVerificationVectors calculates the total number of slots covered by participants who submitted verification vectors
func (k Keeper) CalculateSlotsWithVerificationVectors(epochBLSData *types.EpochBLSData) uint32 {
	var totalSlots uint32 = 0

	// Sum up slots for participants who submitted verification vectors
	for i, participant := range epochBLSData.Participants {
		// Check if this participant submitted a verification vector
		if i < len(epochBLSData.VerificationSubmissions) &&
			epochBLSData.VerificationSubmissions[i] != nil &&
			len(epochBLSData.VerificationSubmissions[i].DealerValidity) > 0 {
			// Calculate number of slots for this participant
			participantSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1
			totalSlots += participantSlots
		}
	}

	return totalSlots
}

// DetermineValidDealersWithConsensus determines which dealers are valid based on majority consensus from verification vectors
func (k Keeper) DetermineValidDealersWithConsensus(epochBLSData *types.EpochBLSData) ([]bool, error) {
	participantCount := len(epochBLSData.Participants)
	if participantCount == 0 {
		return nil, fmt.Errorf("no participants found for epoch %d", epochBLSData.EpochId)
	}

	validDealers := make([]bool, participantCount)

	// For each dealer, count verification votes
	for dealerIndex := 0; dealerIndex < participantCount; dealerIndex++ {
		validVotes := 0
		totalVotes := 0

		// Count votes from all verifiers who submitted verification vectors
		for _, verification := range epochBLSData.VerificationSubmissions {
			if verification != nil && len(verification.DealerValidity) > 0 {
				// Check if this verification has a vote for this dealer
				if dealerIndex < len(verification.DealerValidity) {
					totalVotes++
					if verification.DealerValidity[dealerIndex] {
						validVotes++
					}
				}
			}
		}

		// Dealer is valid if more than 50% of verifiers approve AND they submitted dealer parts
		dealerIsValid := totalVotes > 0 && validVotes > totalVotes/2
		dealerSubmittedParts := dealerIndex < len(epochBLSData.DealerParts) &&
			epochBLSData.DealerParts[dealerIndex] != nil &&
			epochBLSData.DealerParts[dealerIndex].DealerAddress != ""

		validDealers[dealerIndex] = dealerIsValid && dealerSubmittedParts
	}

	return validDealers, nil
}

// ComputeGroupPublicKey aggregates the C_k0 commitments from valid dealers to form the group public key
func (k Keeper) ComputeGroupPublicKey(epochBLSData *types.EpochBLSData, validDealers []bool) ([]byte, error) {
	// Count valid dealers
	validDealerCount := 0
	for _, isValid := range validDealers {
		if isValid {
			validDealerCount++
		}
	}

	if validDealerCount == 0 {
		return nil, fmt.Errorf("no valid dealers found for epoch %d", epochBLSData.EpochId)
	}

	k.Logger().Info("Starting group public key computation",
		"epochId", epochBLSData.EpochId,
		"validDealersCount", validDealerCount)

	// Collect C_k0 commitments from valid dealers
	commitmentsToAggregate := make([][]byte, 0, validDealerCount)
	for dealerIndex, dealerIsValid := range validDealers {
		if !dealerIsValid {
			continue
		}

		if dealerIndex >= len(epochBLSData.DealerParts) {
			k.Logger().Warn("Invalid dealer index", "dealerIndex", dealerIndex, "totalDealers", len(epochBLSData.DealerParts))
			continue
		}

		dealerPart := epochBLSData.DealerParts[dealerIndex]
		if dealerPart == nil || len(dealerPart.Commitments) == 0 {
			k.Logger().Warn("No commitments found for dealer", "dealerIndex", dealerIndex)
			continue
		}

		commitmentsToAggregate = append(commitmentsToAggregate, dealerPart.Commitments[0])
	}

	if len(commitmentsToAggregate) == 0 {
		return nil, fmt.Errorf("no dealer commitments available to compute group public key for epoch %d", epochBLSData.EpochId)
	}

	// Use helper function to aggregate commitments
	groupPublicKeyBytes, err := k.aggregateG2PointsBlst(commitmentsToAggregate)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate commitments: %w", err)
	}

	k.Logger().Info("Completed group public key computation",
		"epochId", epochBLSData.EpochId,
		"validDealersCount", validDealerCount,
		"groupPublicKeySize", len(groupPublicKeyBytes))

	return groupPublicKeyBytes, nil
}
