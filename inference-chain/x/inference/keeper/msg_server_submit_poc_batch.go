package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocBatch(goCtx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight
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
	}

	k.SetPocBatch(ctx, storedBatch)

	return &types.MsgSubmitPocBatchResponse{}, nil
}
