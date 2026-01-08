package keeper

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type PoCWindowType int

const (
	PoCWindowBatch PoCWindowType = iota
	PoCWindowValidation
)

func (k Keeper) CheckPoCMessageTooLate(ctx sdk.Context, startBlockHeight int64, windowType PoCWindowType) error {
	currentBlockHeight := ctx.BlockHeight()

	if startBlockHeight > currentBlockHeight {
		// It may filter legit transaction if the node is behind (node lag / state sync),
		// But hope that it will be propogated by other nodes
		// TODO: In the next release, skip the filter on CheckTx, and enforce only on DeliverTx.
		k.Logger().Debug(
			"[ValidatePocPeriod] POC submission is too early",
			"startBlockHeight", startBlockHeight,
			"currentBlockHeight", currentBlockHeight,
		)
		return errorsmod.Wrapf(
			types.ErrPocWrongStartBlockHeight,
			"POC submission is too early: startBlockHeight=%d, currentBlockHeight=%d",
			startBlockHeight, currentBlockHeight,
		)
	}

	activeEvent, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.Logger().Debug("[ValidatePocPeriod] Error checking confirmation PoC event", "error", err)
	}

	if isActive && activeEvent != nil {
		return k.checkConfirmationPoCMessageTooLate(ctx, activeEvent, startBlockHeight, currentBlockHeight, windowType)
	}

	return k.checkRegularPoCMessageTooLate(ctx, startBlockHeight, currentBlockHeight, windowType)
}

func (k Keeper) checkConfirmationPoCMessageTooLate(ctx sdk.Context, event *types.ConfirmationPoCEvent, startBlockHeight, currentBlockHeight int64, windowType PoCWindowType) error {
	if startBlockHeight != event.TriggerHeight {
		k.Logger().Debug(
			"[ValidatePocPeriod] Confirmation PoC: start block height mismatch",
			"startBlockHeight", startBlockHeight,
			"triggerHeight", event.TriggerHeight,
			"currentBlockHeight", currentBlockHeight,
		)
		return errorsmod.Wrapf(
			types.ErrPocWrongStartBlockHeight,
			"Confirmation PoC start block height mismatch: expected %d, got %d",
			event.TriggerHeight, startBlockHeight,
		)
	}

	epochParams := k.GetParams(ctx).EpochParams

	switch windowType {
	case PoCWindowBatch:
		if currentBlockHeight > event.GetExchangeEnd(epochParams) {
			k.Logger().Debug(
				"[ValidatePocPeriod] Confirmation PoC: outside batch submission window",
				"currentBlockHeight", currentBlockHeight,
				"generationStartHeight", event.GenerationStartHeight,
				"exchangeEndHeight", event.GetExchangeEnd(epochParams),
			)
			return errorsmod.Wrap(types.ErrPocTooLate, "Confirmation PoC is past generation phase")
		}

	case PoCWindowValidation:
		if currentBlockHeight > event.GetValidationEnd(epochParams) {
			k.Logger().Debug(
				"[ValidatePocPeriod] Confirmation PoC: outside validation window",
				"currentBlockHeight", currentBlockHeight,
				"validationStartHeight", event.GetValidationStart(epochParams),
				"validationEndHeight", event.GetValidationEnd(epochParams),
			)
			return errorsmod.Wrap(types.ErrPocTooLate, "Confirmation PoC not in validation phase")
		}
	}

	return nil
}

func (k Keeper) checkRegularPoCMessageTooLate(ctx sdk.Context, startBlockHeight, currentBlockHeight int64, windowType PoCWindowType) error {
	epochParams := k.GetParams(ctx).EpochParams
	currentEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		k.Logger().Debug(
			"[ValidatePocPeriod] Failed to get effective epoch",
			"currentBlockHeight", currentBlockHeight,
		)
		return nil
	}
	currentEpochContext := types.NewEpochContext(*currentEpoch, *epochParams)
	if startBlockHeight <= currentEpochContext.StartOfPoC() {
		k.Logger().Debug(
			"[ValidatePocPeriod] Start block height is for PoC stage that already finished",
			"currentBlockHeight", currentBlockHeight,
			"startBlockHeight", startBlockHeight,
			"startOfPoC", currentEpochContext.StartOfPoC(),
		)
		return errorsmod.Wrap(
			types.ErrUpcomingEpochNotFound,
			fmt.Sprintf("PoC stage already finished %d", currentBlockHeight),
		)
	}
	// startBlockHeight > currentEpochContext.StartOfPoC()

	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if !found {
		k.Logger().Debug(
			"[ValidatePocPeriod] Failed to get upcoming epoch while current block is past startBlock",
			"currentBlockHeight", currentBlockHeight,
			"startBlockHeight", startBlockHeight,
			"startOfPoC", currentEpochContext.StartOfPoC(),
		)
		return errorsmod.Wrap(
			types.ErrUpcomingEpochNotFound,
			fmt.Sprintf("PoC stage already finished %d", currentBlockHeight),
		)
	}

	upcomingEpochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !upcomingEpochContext.IsStartOfPocStage(startBlockHeight) {
		k.Logger().Debug(
			"[ValidatePocPeriod] Start block height doesn't match upcoming epoch",
			"startBlockHeight", startBlockHeight,
			"expectedStartBlockHeight", upcomingEpochContext.PocStartBlockHeight,
			"currentBlockHeight", currentBlockHeight,
		)
		return errorsmod.Wrapf(
			types.ErrPocWrongStartBlockHeight,
			"Start block height %d doesn't match upcoming epoch PoC start %d",
			startBlockHeight, upcomingEpochContext.PocStartBlockHeight,
		)
	}

	switch windowType {
	case PoCWindowBatch:
		if currentBlockHeight > upcomingEpochContext.PoCExchangeDeadline() {
			k.Logger().Debug(
				"[ValidatePocPeriod] PoC exchange window closed",
				"startBlockHeight", startBlockHeight,
				"currentBlockHeight", currentBlockHeight,
				"pocStartBlockHeight", upcomingEpochContext.PocStartBlockHeight,
				"pocExchangeDeadline", upcomingEpochContext.PoCExchangeDeadline(),
			)
			return errorsmod.Wrapf(
				types.ErrPocTooLate,
				"PoC exchange window closed at block %d",
				currentBlockHeight,
			)
		}

	case PoCWindowValidation:
		if currentBlockHeight > upcomingEpochContext.EndOfPoCValidation() {
			k.Logger().Debug(
				"[ValidatePocPeriod] Validation exchange window closed",
				"startBlockHeight", startBlockHeight,
				"currentBlockHeight", currentBlockHeight,
				"pocStartBlockHeight", upcomingEpochContext.PocStartBlockHeight,
			)
			return errorsmod.Wrapf(
				types.ErrPocTooLate,
				"Validation exchange window closed at block %d",
				currentBlockHeight,
			)
		}
	}

	return nil
}
