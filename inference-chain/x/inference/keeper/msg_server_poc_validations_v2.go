package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SubmitPocValidationsV2 handles batch submission of PoC v2 validations.
func (k msgServer) SubmitPocValidationsV2(goCtx context.Context, msg *types.MsgSubmitPocValidationsV2) (*types.MsgSubmitPocValidationsV2Response, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	isMigrationTracking := params.PocParams.ConfirmationPocV2Enabled && isActive && activeEvent != nil && activeEvent.EventSequence == 0
	if !params.PocParams.PocV2Enabled && !isMigrationTracking {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	// Participant access gating: blocklisted accounts cannot validate in PoC.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] validator is blocked from PoC", types.PoC, "validator", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	// Validate PoC window once at message level (all validations share the same height)
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: start block height mismatch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] Confirmation PoC active but start block height doesn't match. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the validation window
		confirmParams, err := k.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := confirmParams.EpochParams
		if !activeEvent.IsInValidationWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: outside validation window", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"validationStartHeight", activeEvent.GetValidationStart(epochParams),
				"validationEndHeight", activeEvent.GetValidationEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC validation window closed")
		}
	} else {
		// Regular PoC logic
		regularParams, err := k.Keeper.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := regularParams.EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Failed to get upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "[SubmitPocValidationsV2] Failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
				msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] PoC validation exchange window is closed.", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.BlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
		}
	}

	// Process each validation - skip failures, don't fail entire batch
	storedCount := 0
	for _, validation := range msg.Validations {
		// Check for duplicate submission (prevents vote flipping)
		exists, err := k.HasPocValidationV2(ctx, startBlockHeight, validation.ParticipantAddress, msg.Creator)
		if err != nil {
			k.LogWarn("[SubmitPocValidationsV2] Failed to check existing validation, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress,
				"error", err)
			continue
		}
		if exists {
			k.LogWarn("[SubmitPocValidationsV2] Validation already exists, skipping duplicate", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress,
				"stage", startBlockHeight)
			continue
		}

		// Store the v2 validation (combine message-level height with payload)
		storedValidation := types.PoCValidationV2{
			ParticipantAddress:          validation.ParticipantAddress,
			ValidatorParticipantAddress: msg.Creator,
			PocStageStartBlockHeight:    startBlockHeight,
			ValidatedWeight:             validation.ValidatedWeight,
		}

		if err := k.SetPocValidationV2(ctx, storedValidation); err != nil {
			k.LogWarn("[SubmitPocValidationsV2] Failed to store validation, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress,
				"error", err)
			continue
		}

		storedCount++
		k.LogInfo("[SubmitPocValidationsV2] Validation stored", types.PoC,
			"validator", msg.Creator,
			"participant", validation.ParticipantAddress,
			"validatedWeight", validation.ValidatedWeight)
	}

	k.LogInfo("[SubmitPocValidationsV2] Batch complete", types.PoC,
		"validator", msg.Creator,
		"totalInBatch", len(msg.Validations),
		"storedCount", storedCount)

	return &types.MsgSubmitPocValidationsV2Response{}, nil
}
