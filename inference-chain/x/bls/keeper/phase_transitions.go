package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// ProcessDKGPhaseTransitions checks the currently active DKG epoch and transitions it to the next phase if deadline has passed
func (k Keeper) ProcessDKGPhaseTransitions(ctx sdk.Context) error {
	// Get the currently active epoch ID
	activeEpochID := k.GetActiveEpochID(ctx)
	if activeEpochID == 0 {
		// No active DKG - this is normal
		return nil
	}

	// Process phase transition for the active epoch
	return k.ProcessDKGPhaseTransitionForEpoch(ctx, activeEpochID)
}

// ProcessDKGPhaseTransitionForEpoch checks a specific epoch's DKG and transitions it if needed
func (k Keeper) ProcessDKGPhaseTransitionForEpoch(ctx sdk.Context, epochID uint64) error {
	epochBLSData, found := k.GetEpochBLSData(ctx, epochID)
	if !found {
		return fmt.Errorf("EpochBLSData not found for epoch %d", epochID)
	}

	// Skip completed or failed DKGs
	if epochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED || epochBLSData.DkgPhase == types.DKGPhase_DKG_PHASE_FAILED {
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
			// TODO: Implement transition to completion phase in future tasks (task VII.2)
			// This should either transition to COMPLETED (clear active epoch) or FAILED (clear active epoch)
			k.Logger().Info("DKG verifying phase deadline reached", "epochId", epochBLSData.EpochId)
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
		params := k.GetParams(ctx)
		currentBlockHeight := ctx.BlockHeight()

		epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING
		epochBLSData.VerifyingPhaseDeadlineBlock = currentBlockHeight + params.VerificationPhaseDurationBlocks

		// Store updated epoch data
		k.SetEpochBLSData(ctx, *epochBLSData)

		// Emit event for verifying phase started
		if err := ctx.EventManager().EmitTypedEvent(&types.EventVerifyingPhaseStarted{
			EpochId:                     epochBLSData.EpochId,
			VerifyingPhaseDeadlineBlock: uint64(epochBLSData.VerifyingPhaseDeadlineBlock),
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
		k.SetEpochBLSData(ctx, *epochBLSData)

		// Clear active epoch since DKG process is complete (failed)
		k.SetActiveEpochID(ctx, 0)

		// Emit event for DKG failure
		failureReason := fmt.Sprintf("Insufficient participation in dealing phase: %d slots with dealer parts out of %d total slots (required: >%d)",
			slotsWithDealerParts, epochBLSData.ITotalSlots, epochBLSData.ITotalSlots/2)

		if err := ctx.EventManager().EmitTypedEvent(&types.EventDKGFailed{
			EpochId: epochBLSData.EpochId,
			Reason:  failureReason,
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
