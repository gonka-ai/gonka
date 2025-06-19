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
	currentEpochGroup, err := k.Keeper.GetCurrentEpochGroupOrNil(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocBatch] Failed to get current epoch group", types.PoC, "error", err)
		return nil, sdkerrors.Wrap(err, "Failed to get current epoch group")
	}
	epochContext := types.NewEpochContext(currentEpochGroup.GroupData, *epochParams, currentBlockHeight)

	// TODO: fix log messages
	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] message start block height doesn't match the upcoming epoch group", types.PoC,
			"msg.PocStageStartBlockHeight", startBlockHeight)
		errMsg := fmt.Sprintf("[SubmitPocValidation] message start block height doesn't match the upcoming epoch group. msg.PocStageStartBlockHeight = %d", startBlockHeight)
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
	}

	if !epochContext.IsValidationExchangeWindow(startBlockHeight) {
		k.LogError(PocFailureTag+"[SubmitPocValidation] PoC validation exchange window is closed.", types.PoC, "msg.BlockHeight", startBlockHeight, "currentBlockHeight", currentBlockHeight)
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
