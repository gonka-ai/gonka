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
			if err := k.transitionFromVerifyingToDisputing(ctx, &epochBLSData); err != nil {
				return fmt.Errorf("failed to progress DKG from verifying phase for epoch %d: %w", epochID, err)
			}
		}
	case types.DKGPhase_DKG_PHASE_DISPUTING:
		if currentBlockHeight >= epochBLSData.DisputingPhaseDeadlineBlock {
			if err := k.finalizeDisputingPhase(ctx, &epochBLSData); err != nil {
				return fmt.Errorf("failed to progress DKG from disputing phase for epoch %d: %w", epochID, err)
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
		failureReason := fmt.Sprintf("Insufficient participation in dealing phase: %d slots with dealer parts out of %d total slots (required: >%d)",
			slotsWithDealerParts, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)

		return k.MarkDKGAsFailed(ctx, epochBLSData, failureReason)
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

func (k Keeper) transitionFromVerifyingToDisputing(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
	// Calculate total slots covered by participants who submitted verification vectors
	slotsWithVerification := k.CalculateSlotsWithVerificationVectors(epochBLSData)

	k.Logger().Info("Checking DKG verification participation",
		"epochId", epochBLSData.EpochId,
		"slotsWithVerification", slotsWithVerification,
		"totalSlots", epochBLSData.ITotalSlots,
		"requiredSlots", epochBLSData.ITotalSlots/2)

	// Check if we have sufficient verification participation (more than half the slots)
	if slotsWithVerification <= epochBLSData.ITotalSlots/2 {
		failureReason := fmt.Sprintf("Insufficient participation in verification phase: %d slots with verification vectors out of %d total slots (required: >%d)",
			slotsWithVerification, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)
		return k.MarkDKGAsFailed(ctx, epochBLSData, failureReason)
	}

	candidateValidDealers, err := k.DetermineValidDealersWithConsensus(epochBLSData)
	if err != nil {
		k.Logger().Error("DKG failed", "epochId", epochBLSData.EpochId, "error", err)
		return k.MarkDKGAsFailed(ctx, epochBLSData, fmt.Sprintf("failed to determine candidate valid dealers: %v", err))
	}

	// Calculate total slots covered by candidate valid dealers
	var candidateDealerSlots uint32 = 0
	for i, participant := range epochBLSData.Participants {
		if i < len(candidateValidDealers) && candidateValidDealers[i] {
			participantSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1
			candidateDealerSlots += participantSlots
		}
	}

	k.Logger().Info("Checking candidate valid dealers slots",
		"epochId", epochBLSData.EpochId,
		"candidateDealerSlots", candidateDealerSlots,
		"totalSlots", epochBLSData.ITotalSlots,
		"requiredSlots", epochBLSData.ITotalSlots/2)

	if candidateDealerSlots <= epochBLSData.ITotalSlots/2 {
		failureReason := fmt.Sprintf("Insufficient candidate valid dealer slots: %d slots out of %d total slots (required: >%d)",
			candidateDealerSlots, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)
		return k.MarkDKGAsFailed(ctx, epochBLSData, failureReason)
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}
	currentBlockHeight := ctx.BlockHeight()

	epochBLSData.CandidateValidDealers = candidateValidDealers

	filteredComplaints := make([]types.DealerComplaint, 0, len(epochBLSData.DealerComplaints))
	for _, complaint := range epochBLSData.DealerComplaints {
		dealerIdx := complaint.DealerIndex
		if dealerIdx >= uint32(len(candidateValidDealers)) {
			continue
		}
		// Keep only complaints against dealers that passed vote-based candidacy.
		if candidateValidDealers[int(dealerIdx)] {
			filteredComplaints = append(filteredComplaints, complaint)
		}
	}
	// Wipe all existing complaint sub-keys so filtered-out entries don't
	// linger. SetEpochBLSData below will resync the kept subset via its
	// inline sync loop.
	if err := k.DeleteDealerComplaintsForEpoch(ctx, epochBLSData.EpochId); err != nil {
		return fmt.Errorf("failed to clear dealer complaint sub-keys for epoch %d: %w", epochBLSData.EpochId, err)
	}
	epochBLSData.DealerComplaints = filteredComplaints

	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_DISPUTING
	epochBLSData.DisputingPhaseDeadlineBlock = currentBlockHeight + params.DisputePhaseDurationBlocks

	if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
		return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
	}

	if err := ctx.EventManager().EmitTypedEvent(&types.EventDisputePhaseStarted{
		EpochId:                     epochBLSData.EpochId,
		DisputingPhaseDeadlineBlock: uint64(epochBLSData.DisputingPhaseDeadlineBlock),
		EpochData:                   *epochBLSData,
	}); err != nil {
		return fmt.Errorf("failed to emit EventDisputePhaseStarted for epoch %d: %w", epochBLSData.EpochId, err)
	}

	k.Logger().Info("DKG transitioned to DISPUTING phase",
		"epochId", epochBLSData.EpochId,
		"candidateValidDealers", countTrueBooleans(candidateValidDealers),
		"disputingDeadline", epochBLSData.DisputingPhaseDeadlineBlock)

	return nil
}

// CompleteDKG finalizes DKG from DISPUTING->COMPLETED/FAILED.
func (k Keeper) CompleteDKG(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
	if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_DISPUTING {
		return fmt.Errorf("DKG for epoch %d is not in DISPUTING phase (current phase: %s)", epochBLSData.EpochId, epochBLSData.DkgPhase.String())
	}
	return k.finalizeDisputingPhase(ctx, epochBLSData)
}

func (k Keeper) finalizeDisputingPhase(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
	finalValidDealers := epochBLSData.CandidateValidDealers
	if len(finalValidDealers) != len(epochBLSData.Participants) {
		return fmt.Errorf(
			"invalid candidate valid dealers length for epoch %d: got %d, expected %d",
			epochBLSData.EpochId,
			len(finalValidDealers),
			len(epochBLSData.Participants),
		)
	}

	var err error
	finalValidDealers, err = k.applyDealerComplaintOutcomes(epochBLSData, finalValidDealers)
	if err != nil {
		return k.MarkDKGAsFailed(ctx, epochBLSData, fmt.Sprintf("failed to apply complaint outcomes: %v", err))
	}

	// Calculate total slots covered by final valid dealers
	var finalDealerSlots uint32 = 0
	for i, participant := range epochBLSData.Participants {
		if i < len(finalValidDealers) && finalValidDealers[i] {
			participantSlots := participant.SlotEndIndex - participant.SlotStartIndex + 1
			finalDealerSlots += participantSlots
		}
	}

	k.Logger().Info("Checking final valid dealers slots",
		"epochId", epochBLSData.EpochId,
		"finalDealerSlots", finalDealerSlots,
		"totalSlots", epochBLSData.ITotalSlots,
		"requiredSlots", epochBLSData.ITotalSlots/2)

	if finalDealerSlots <= epochBLSData.ITotalSlots/2 {
		failureReason := fmt.Sprintf("Insufficient final valid dealer slots: %d slots out of %d total slots (required: >%d)",
			finalDealerSlots, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)
		return k.MarkDKGAsFailed(ctx, epochBLSData, failureReason)
	}

	groupPublicKey, err := k.ComputeGroupPublicKey(epochBLSData, finalValidDealers)
	if err != nil {
		return k.MarkDKGAsFailed(ctx, epochBLSData, fmt.Sprintf("failed to compute group public key: %v", err))
	}

	epochBLSData.GroupPublicKey = groupPublicKey
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_COMPLETED
	epochBLSData.ValidDealers = finalValidDealers

	slotPublicKeys, err := k.PrecomputeSlotPublicKeysBlst(epochBLSData)
	if err != nil {
		return k.MarkDKGAsFailed(ctx, epochBLSData, fmt.Sprintf("failed to precompute slot public keys: %v", err))
	}
	epochBLSData.SlotPublicKeys = slotPublicKeys

	if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
		return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
	}

	k.ClearActiveEpochID(ctx)

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
		"validDealersCount", countTrueBooleans(finalValidDealers),
		"groupPublicKeySize", len(groupPublicKey))

	return nil
}

func (k Keeper) MarkDKGAsFailed(ctx sdk.Context, epochBLSData *types.EpochBLSData, failureReason string) error {
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_FAILED

	if err := k.SetEpochBLSData(ctx, *epochBLSData); err != nil {
		return fmt.Errorf("failed to set EpochBLSData for epoch %d: %w", epochBLSData.EpochId, err)
	}

	k.ClearActiveEpochID(ctx)

	if err := ctx.EventManager().EmitTypedEvent(&types.EventDKGFailed{
		EpochId:   epochBLSData.EpochId,
		Reason:    failureReason,
		EpochData: *epochBLSData,
	}); err != nil {
		return fmt.Errorf("failed to emit EventDKGFailed for epoch %d: %w", epochBLSData.EpochId, err)
	}

	k.Logger().Info("DKG marked as FAILED",
		"epochId", epochBLSData.EpochId,
		"reason", failureReason)

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

// DetermineValidDealersWithConsensus determines which dealers are valid under weighted slot quorum
func (k Keeper) DetermineValidDealersWithConsensus(epochBLSData *types.EpochBLSData) ([]bool, error) {
	participantCount := len(epochBLSData.Participants)
	if participantCount == 0 {
		return nil, fmt.Errorf("no participants found for epoch %d", epochBLSData.EpochId)
	}

	validDealers := make([]bool, participantCount)
	totalSlots := uint64(epochBLSData.ITotalSlots)
	quorumSlots := totalSlots/2 + 1

	for dealerIndex := 0; dealerIndex < participantCount; dealerIndex++ {
		dealerParticipant := epochBLSData.Participants[dealerIndex]
		if dealerParticipant.SlotEndIndex < dealerParticipant.SlotStartIndex {
			return nil, fmt.Errorf("invalid slot range for dealer %d in epoch %d", dealerIndex, epochBLSData.EpochId)
		}
		dealerOwnSlots := uint64(dealerParticipant.SlotEndIndex-dealerParticipant.SlotStartIndex) + 1
		dealerOwnSlotsAtLeastHalf := totalSlots > 0 && dealerOwnSlots*2 >= totalSlots
		maxPossibleNonSelfVotingSlots := uint64(0)
		if dealerOwnSlots <= totalSlots {
			maxPossibleNonSelfVotingSlots = totalSlots - dealerOwnSlots
		}

		var validVotingSlotsExcludingDealer uint64

		for verifierIndex, verification := range epochBLSData.VerificationSubmissions {
			if verification == nil || len(verification.DealerValidity) == 0 {
				continue
			}
			if verifierIndex >= participantCount {
				continue
			}
			if verifierIndex == dealerIndex {
				continue
			}
			if dealerIndex >= len(verification.DealerValidity) || !verification.DealerValidity[dealerIndex] {
				continue
			}

			participant := epochBLSData.Participants[verifierIndex]
			if participant.SlotEndIndex < participant.SlotStartIndex {
				return nil, fmt.Errorf("invalid slot range for participant %d in epoch %d", verifierIndex, epochBLSData.EpochId)
			}
			verifierSlots := uint64(participant.SlotEndIndex-participant.SlotStartIndex) + 1
			validVotingSlotsExcludingDealer += verifierSlots
		}

		dealerIsValid := totalSlots > 0 && validVotingSlotsExcludingDealer >= quorumSlots
		dealerSubmittedParts := dealerIndex < len(epochBLSData.DealerParts) &&
			epochBLSData.DealerParts[dealerIndex] != nil &&
			epochBLSData.DealerParts[dealerIndex].DealerAddress != "" &&
			len(epochBLSData.DealerParts[dealerIndex].Commitments) > 0

		if dealerSubmittedParts && totalSlots > 0 && maxPossibleNonSelfVotingSlots < quorumSlots {
			k.Logger().Warn("Dealer cannot reach weighted quorum with self-vote excluded",
				"epochId", epochBLSData.EpochId,
				"dealerIndex", dealerIndex,
				"dealerOwnSlots", dealerOwnSlots,
				"dealerOwnSlotsAtLeastHalf", dealerOwnSlotsAtLeastHalf,
				"maxNonSelfVotingSlots", maxPossibleNonSelfVotingSlots,
				"quorumSlots", quorumSlots,
				"totalSlots", totalSlots,
				"receivedVotingSlotsExcludingDealer", validVotingSlotsExcludingDealer)
		}

		validDealers[dealerIndex] = dealerIsValid && dealerSubmittedParts
	}

	return validDealers, nil
}

func countTrueBooleans(values []bool) int {
	count := 0
	for _, v := range values {
		if v {
			count++
		}
	}
	return count
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

