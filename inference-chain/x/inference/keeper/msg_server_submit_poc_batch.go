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

	if !epochParams.IsStartOfPoCStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocBatch] start block height must be divisible by EpochLength", types.PoC, "EpochLength", epochParams.EpochLength, "msg.BlockHeight", startBlockHeight)
		errMsg := fmt.Sprintf("[SubmitPocBatch] start block height must be divisible by %d. msg.BlockHeight = %d", epochParams.EpochLength, startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochParams.IsPoCExchangeWindow(startBlockHeight, currentBlockHeight) {
		k.LogError(PocFailureTag+"PoC exchange window is closed.", types.PoC, "msg.BlockHeight", startBlockHeight, "currentBlockHeight", currentBlockHeight)
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
