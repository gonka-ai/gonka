package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"fmt"
	"github.com/productscience/inference/x/inference/proofofcompute"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocBatch(goCtx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	if !proofofcompute.IsStartOfPoCStage(startBlockHeight) {
		k.LogError(PocFailureTag+"start block height must be divisible by EpochLength", "EpochLength", proofofcompute.EpochLength, "msg.BlockHeight", startBlockHeight)
		errMsg := fmt.Sprintf("start block height must be divisible by %d. msg.BlockHeight = %d", proofofcompute.EpochLength, startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !proofofcompute.IsPoCExchangeWindow(startBlockHeight, currentBlockHeight) {
		k.LogError(PocFailureTag+"PoC exchange window is closed.", "msg.BlockHeight", startBlockHeight, "currentBlockHeight", currentBlockHeight)
		errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
	}

	storedBatch := types.PoCBatch{
		ParticipantAddress:       msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		ReceivedAtBlockHeight:    currentBlockHeight,
		Nonces:                   msg.Nonces,
		Dist:                     msg.Dist,
	}

	k.SetPocBatch(ctx, storedBatch)

	return &types.MsgSubmitPocBatchResponse{}, nil
}
