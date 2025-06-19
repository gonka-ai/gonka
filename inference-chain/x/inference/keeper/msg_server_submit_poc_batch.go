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
	currentEpochGroup, err := k.Keeper.GetCurrentEpochGroupOrNil(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocBatch] Failed to get current epoch group", types.PoC, "error", err)
		return nil, sdkerrors.Wrap(err, "Failed to get current epoch group")
	}
	epochContext := types.NewEpochContext(currentEpochGroup.GroupData, *epochParams, currentBlockHeight)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocBatch] message start block height doesn't match the upcoming epoch group", types.PoC,
			"msg.PocStageStartBlockHeight", startBlockHeight)
		errMsg := fmt.Sprintf("[SubmitPocBatch] message start block height doesn't match the upcoming epoch group. msg.PocStageStartBlockHeight = %d", startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
		k.LogError(PocFailureTag+"PoC exchange window is closed.", types.PoC,
			"msg.PocStageStartBlockHeight", startBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"epochContext", epochContext)
		errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
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
