package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// submitPocValidationV1 handles V1 validation submission (on-chain PoCValidation storage).
func (k msgServer) submitPocValidationV1(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Participant access gating: blocklisted accounts cannot participate in PoC (as validator or validated participant).
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] validator participant is blocked from PoC", types.PoC, "validatorParticipant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	exists, err := k.HasPoCValidation(ctx, startBlockHeight, msg.ParticipantAddress, msg.Creator)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidation] Error checking existing validation", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"error", err)
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "error checking existing validation")
	}
	if exists {
		k.LogWarn(PocFailureTag+"[SubmitPocValidation] Duplicate validation rejected", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrPocValidationAlreadyExists, "PoC validation already exists for this participant from this validator")
	}

	// Check for active confirmation PoC event first
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidation] Error checking confirmation PoC event", types.PoC, "error", err)
		// Continue with regular PoC check
	}

	// Route to confirmation PoC handler if active and in VALIDATION phase
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocValidation] Confirmation PoC: start block height mismatch", types.PoC,
				"participant", msg.ParticipantAddress,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidation] Confirmation PoC active but start block height doesn't match. "+
				"participant = %s. validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.ParticipantAddress, msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the validation window
		confirmParams, err := k.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := confirmParams.EpochParams
		if !activeEvent.IsInValidationWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocValidation] Confirmation PoC: outside validation window", types.PoC,
				"participant", msg.ParticipantAddress,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"validationStartHeight", activeEvent.GetValidationStart(epochParams),
				"validationEndHeight", activeEvent.GetValidationEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC validation window closed")
		}

		// Store validation using trigger_height as key
		validation := toPoCValidationV1(msg, currentBlockHeight)
		validation.PocStageStartBlockHeight = activeEvent.TriggerHeight // Use trigger_height as key
		if err := k.SetPoCValidation(ctx, *validation); err != nil {
			return nil, err
		}
		k.LogInfo("[SubmitPocValidation] Confirmation PoC validation stored", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"triggerHeight", activeEvent.TriggerHeight)

		return &types.MsgSubmitPocValidationResponse{}, nil
	}

	// Regular PoC logic
	regularParams, err := k.Keeper.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	epochParams := regularParams.EpochParams
	upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
	if !found {
		k.LogError(PocFailureTag+"[SubmitPocValidation] Failed to get upcoming epoch", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"currentBlockHeight", currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "[SubmitPocBatch] Failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] message start block height doesn't match the upcoming epoch", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"msg.PocStageStartBlockHeight", startBlockHeight,
			"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"epochContext", epochContext)
		errMsg := fmt.Sprintf("[SubmitPocValidation] message start block height doesn't match the upcoming epoch. "+
			"participant = %s. validatorParticipant = %s"+
			"msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
			msg.ParticipantAddress, msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] PoC validation exchange window is closed.", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"msg.BlockHeight", startBlockHeight,
			"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"epochContext", epochContext)
		errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
	}

	validation := toPoCValidationV1(msg, currentBlockHeight)
	if err := k.SetPoCValidation(ctx, *validation); err != nil {
		return nil, err
	}

	return &types.MsgSubmitPocValidationResponse{}, nil
}

func toPoCValidationV1(msg *types.MsgSubmitPocValidation, currentBlockHeight int64) *types.PoCValidation {
	return &types.PoCValidation{
		ParticipantAddress:          msg.ParticipantAddress,
		ValidatorParticipantAddress: msg.Creator,
		PocStageStartBlockHeight:    msg.PocStageStartBlockHeight,
		ValidatedAtBlockHeight:      currentBlockHeight,
		Nonces:                      msg.Nonces,
		Dist:                        msg.Dist,
		ReceivedDist:                msg.ReceivedDist,
		RTarget:                     msg.RTarget,
		FraudThreshold:              msg.FraudThreshold,
		NInvalid:                    msg.NInvalid,
		ProbabilityHonest:           msg.ProbabilityHonest,
		FraudDetected:               msg.FraudDetected,
	}
}
