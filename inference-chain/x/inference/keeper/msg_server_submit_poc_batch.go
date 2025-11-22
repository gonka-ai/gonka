package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocBatch(goCtx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.NodeId == "" {
		k.LogError(PocFailureTag+"[SubmitPocBatch] NodeId is empty", types.PoC,
			"participant", msg.Creator,
			"msg.NodeId", msg.NodeId)
		return nil, sdkerrors.Wrap(types.ErrPocNodeIdEmpty, "NodeId is empty")
	}

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event first
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocBatch] Error checking confirmation PoC event", types.PoC, "error", err)
		// Continue with regular PoC check
	}

	// Route to confirmation PoC handler if active and in GENERATION phase
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocBatch] Confirmation PoC: start block height mismatch", types.PoC,
				"participant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocBatch] Confirmation PoC active but start block height doesn't match. "+
				"participant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the batch submission window (generation + exchange period)
		epochParams := k.GetParams(ctx).EpochParams
		if !activeEvent.IsInBatchSubmissionWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocBatch] Confirmation PoC: outside batch submission window", types.PoC,
				"participant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"generationStartHeight", activeEvent.GenerationStartHeight,
				"exchangeEndHeight", activeEvent.GetExchangeEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC batch submission window closed")
		}

		// Store batch using trigger_height as key
		storedBatch := types.PoCBatch{
			ParticipantAddress:       msg.Creator,
			PocStageStartBlockHeight: activeEvent.TriggerHeight, // Use trigger_height as key
			ReceivedAtBlockHeight:    currentBlockHeight,
			Nonces:                   msg.Nonces,
			Dist:                     msg.Dist,
			BatchId:                  msg.BatchId,
			NodeId:                   msg.NodeId,
		}

		k.SetPocBatch(ctx, storedBatch)
		k.LogInfo("[SubmitPocBatch] Confirmation PoC batch stored", types.PoC,
			"participant", msg.Creator,
			"triggerHeight", activeEvent.TriggerHeight,
			"nodeId", msg.NodeId)

		return &types.MsgSubmitPocBatchResponse{}, nil
	}

	// Regular PoC logic
	epochParams := k.Keeper.GetParams(goCtx).EpochParams
	upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
	if !found {
		k.LogError(PocFailureTag+"[SubmitPocBatch] Failed to get upcoming epoch", types.PoC,
			"participant", msg.Creator,
			"currentBlockHeight", currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "Failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocBatch] message start block height doesn't match the upcoming epoch group", types.PoC,
			"participant", msg.Creator,
			"msg.PocStageStartBlockHeight", startBlockHeight,
			"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
			"currentBlockHeight", currentBlockHeight)
		errMsg := fmt.Sprintf("[SubmitPocBatch] message start block height doesn't match the upcoming epoch group. "+
			"participant = %s. msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
			msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
		k.LogError(PocFailureTag+"PoC exchange window is closed.", types.PoC,
			"participant", msg.Creator,
			"msg.PocStageStartBlockHeight", startBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight)
		errMsg := fmt.Sprintf("PoC exchange window is closed. "+
			"participant = %s. msg.BlockHeight = %d, currentBlockHeight = %d, epochContext.PocStartBlockHeight = %d",
			msg.Creator, startBlockHeight, currentBlockHeight, epochContext.PocStartBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
	}

	storedBatch := types.PoCBatch{
		ParticipantAddress:       msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		ReceivedAtBlockHeight:    currentBlockHeight,
		Nonces:                   msg.Nonces,
		Dist:                     msg.Dist,
		BatchId:                  msg.BatchId,
		NodeId:                   msg.NodeId,
	}

	k.SetPocBatch(ctx, storedBatch)

	return &types.MsgSubmitPocBatchResponse{}, nil
}
