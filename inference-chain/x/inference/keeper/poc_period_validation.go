package keeper

import (
	"errors"
	"fmt"

	"cosmossdk.io/collections"
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

	params, err := k.GetParams(ctx)
	if err != nil {
		k.Logger().Debug("[ValidatePocPeriod] Error getting params", "error", err)
		return err
	}
	epochParams := params.EpochParams

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
	params, err := k.GetParams(ctx)
	if err != nil {
		k.Logger().Debug("[ValidatePocPeriod] Error getting params", "error", err)
		return err
	}
	epochParams := params.EpochParams
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

// CheckPoCV2StoreCommitMempool is CheckTx/Recheck hygiene for StoreCommit only.
// The handler still uses the inclusive window (1108) and per-model 1137.
//
//  1. Next-block closed: reject when committed height >= exchange deadline so
//     leftovers are not packed into deadline+1.
//  2. Recheck overlap: drop mempool leftovers whose model already committed at
//     this height with count >= the leftover (cannot succeed on the next block).
func (k Keeper) CheckPoCV2StoreCommitMempool(ctx sdk.Context, msg *types.MsgPoCV2StoreCommit) error {
	if msg == nil {
		return nil
	}
	if err := k.checkPoCV2StoreCommitNextBlockClosed(ctx, msg.PocStageStartBlockHeight); err != nil {
		return err
	}
	if ctx.IsReCheckTx() {
		return k.checkPoCV2StoreCommitRecheckOverlap(ctx, msg)
	}
	return nil
}

func (k Keeper) checkPoCV2StoreCommitNextBlockClosed(ctx sdk.Context, startBlockHeight int64) error {
	currentBlockHeight := ctx.BlockHeight()
	deadline, err := k.poCV2StoreCommitDeadline(ctx, startBlockHeight)
	if err != nil {
		return err
	}
	if deadline <= 0 {
		return nil
	}
	if currentBlockHeight >= deadline {
		k.Logger().Debug(
			"[ValidatePocPeriod] StoreCommit mempool: next block is past exchange deadline",
			"startBlockHeight", startBlockHeight,
			"currentBlockHeight", currentBlockHeight,
			"deadline", deadline,
		)
		return errorsmod.Wrapf(
			types.ErrPocTooLate,
			"PoC exchange window closed for next block at height %d (deadline %d)",
			currentBlockHeight,
			deadline,
		)
	}
	return nil
}

func (k Keeper) poCV2StoreCommitDeadline(ctx sdk.Context, startBlockHeight int64) (int64, error) {
	activeEvent, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.Logger().Debug("[ValidatePocPeriod] Error checking confirmation PoC event", "error", err)
	}
	if isActive && activeEvent != nil && startBlockHeight == activeEvent.TriggerHeight {
		params, err := k.GetParams(ctx)
		if err != nil {
			return 0, err
		}
		return activeEvent.GetExchangeEnd(params.EpochParams), nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return 0, err
	}
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if !found {
		return 0, nil
	}
	ec := types.NewEpochContext(*upcomingEpoch, *params.EpochParams)
	if !ec.IsStartOfPocStage(startBlockHeight) {
		return 0, nil
	}
	return ec.PoCExchangeDeadline(), nil
}

func (k Keeper) checkPoCV2StoreCommitRecheckOverlap(ctx sdk.Context, msg *types.MsgPoCV2StoreCommit) error {
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return err
	}
	currentBlockHeight := ctx.BlockHeight()
	for _, entry := range msg.Entries {
		if entry == nil || entry.ModelId == "" {
			continue
		}
		pk := pocV2StoreCommitKey(msg.PocStageStartBlockHeight, addr, entry.ModelId)
		existing, err := k.PoCV2StoreCommits.Get(ctx, pk)
		if err != nil {
			if errors.Is(err, collections.ErrNotFound) {
				continue
			}
			return err
		}
		if existing.CommitBlockHeight == currentBlockHeight && entry.Count <= existing.Count {
			k.Logger().Debug(
				"[ValidatePocPeriod] StoreCommit Recheck: overlapping model already committed this height",
				"model_id", entry.ModelId,
				"currentBlockHeight", currentBlockHeight,
				"existingCount", existing.Count,
				"entryCount", entry.Count,
			)
			return errorsmod.Wrapf(
				types.ErrIllegalState,
				"only one commit per block allowed for model %q",
				entry.ModelId,
			)
		}
	}
	return nil
}
