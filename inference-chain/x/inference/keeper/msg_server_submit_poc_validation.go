package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight
	epochParams := k.Keeper.GetParams(ctx).EpochParams
	upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
	if !found {
		k.LogError(PocFailureTag+"[SubmitPocBatch] Failed to get upcoming epoch", types.PoC, "currentBlockHeight", currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "[SubmitPocBatch] Failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] message start block height doesn't match the upcoming epoch", types.PoC,
			"msg.PocStageStartBlockHeight", startBlockHeight,
			"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"epochContext", epochContext)
		errMsg := fmt.Sprintf("[SubmitPocValidation] message start block height doesn't match the upcoming epoch. "+
			"msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
			startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochContext.IsValidationExchangeWindow(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] PoC validation exchange window is closed.", types.PoC,
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
