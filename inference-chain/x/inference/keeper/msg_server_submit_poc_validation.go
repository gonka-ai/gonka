package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

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
		epochParams := k.GetParams(ctx).EpochParams
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
		validation := toPoCValidation(msg, currentBlockHeight)
		validation.PocStageStartBlockHeight = activeEvent.TriggerHeight // Use trigger_height as key
		k.SetPoCValidation(ctx, *validation)
		k.LogInfo("[SubmitPocValidation] Confirmation PoC validation stored", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"triggerHeight", activeEvent.TriggerHeight)

		return &types.MsgSubmitPocValidationResponse{}, nil
	}

	// Regular PoC logic
	epochParams := k.Keeper.GetParams(ctx).EpochParams
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

	validation := toPoCValidation(msg, currentBlockHeight)
	k.SetPoCValidation(ctx, *validation)

	return &types.MsgSubmitPocValidationResponse{}, nil
}

func toPoCValidation(msg *types.MsgSubmitPocValidation, currentBlockHeight int64) *types.PoCValidation {
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
