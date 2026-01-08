package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	"github.com/productscience/inference/x/inference/types"
)

// SetSettleAmount sets a specific settleAmount in the store by participant
func (k Keeper) SetSettleAmount(ctx context.Context, settleAmount types.SettleAmount) {
	addr, err := sdk.AccAddressFromBech32(settleAmount.Participant)
	if err != nil {
		panic(err)
	}
	if err := k.SettleAmounts.Set(ctx, addr, settleAmount); err != nil {
		panic(err)
	}
}

// GetSettleAmount returns a settleAmount by participant
func (k Keeper) GetSettleAmount(
	ctx context.Context,
	participant string,
) (val types.SettleAmount, found bool) {
	addr, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return val, false
	}
	v, err := k.SettleAmounts.Get(ctx, addr)
	if err != nil {
		return val, false
	}
	return v, true
}

// RemoveSettleAmount removes a settleAmount from the store
func (k Keeper) RemoveSettleAmount(
	ctx context.Context,
	participant string,
) {
	addr, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return
	}
	_ = k.SettleAmounts.Remove(ctx, addr)
}

// GetAllSettleAmount returns all settleAmount entries
func (k Keeper) GetAllSettleAmount(ctx context.Context) (list []types.SettleAmount) {
	iter, err := k.SettleAmounts.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	vals, err := iter.Values()
	if err != nil {
		return nil
	}
	return vals
}

// transferUnclaimedSettleAmountToGovernance transfers coins from an unclaimed settle amount to governance (internal helper).
func (k Keeper) transferUnclaimedSettleAmountToGovernance(ctx context.Context, settleAmount types.SettleAmount, reason string) error {
	totalCoins := settleAmount.GetTotalCoins()
	if totalCoins > 0 {
		coins, err := types.GetCoins(int64(totalCoins))
		if err != nil {
			return err
		}
		memo := reason + ":" + settleAmount.Participant
		err = k.BankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, govtypes.ModuleName, coins, memo)
		if err != nil {
			k.LogError("Error transferring unclaimed settle amount coins to governance", types.Settle, "error", err, "participant", settleAmount.Participant, "amount", totalCoins)
			return err
		}
		k.SafeLogSubAccountTransaction(ctx, types.ModuleName, settleAmount.Participant, types.SettleSubAccount, totalCoins, reason)
		k.LogInfo("Transferred unclaimed settle amount to governance", types.Settle, "participant", settleAmount.Participant, "amount", totalCoins, "reason", reason)
	}
	return nil
}

// SetSettleAmountWithGovernanceTransfer sets a settle amount, transferring any existing unclaimed amount to governance first.
func (k Keeper) SetSettleAmountWithGovernanceTransfer(ctx context.Context, settleAmount types.SettleAmount) error {
	// Transfer existing settle amount if it exists
	existingSettle, found := k.GetSettleAmount(ctx, settleAmount.Participant)
	if found {
		err := k.transferUnclaimedSettleAmountToGovernance(ctx, existingSettle, "expired claim")
		if err != nil {
			return err
		}
	}

	// Set the new settle amount
	k.SetSettleAmount(ctx, settleAmount)
	k.SafeLogSubAccountTransaction(ctx, types.ModuleName, settleAmount.Participant, types.SettleSubAccount, settleAmount.GetTotalCoins(), "awaiting claim")
	k.SafeLogSubAccountTransactionUint(ctx, settleAmount.Participant, types.ModuleName, types.OwedSubAccount, settleAmount.WorkCoins, "moved to settled")
	return nil
}

// TransferOldSettleAmountsToGovernance transfers and removes all settle amounts older than the specified epoch.
func (k Keeper) TransferOldSettleAmountsToGovernance(ctx context.Context, beforeEpochIndex uint64) error {
	allSettleAmounts := k.GetAllSettleAmount(ctx)
	for _, settleAmount := range allSettleAmounts {
		if settleAmount.EpochIndex < beforeEpochIndex {
			err := k.transferUnclaimedSettleAmountToGovernance(ctx, settleAmount, "expired")
			if err != nil {
				return err
			}
			k.RemoveSettleAmount(ctx, settleAmount.Participant)
		}
	}
	return nil
}
