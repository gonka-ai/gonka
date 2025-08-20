package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

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

// burnSettleAmount burns coins from a settle amount (internal helper)
func (k Keeper) burnSettleAmount(ctx context.Context, settleAmount types.SettleAmount, reason string) error {
	totalCoins := settleAmount.GetTotalCoins()
	if totalCoins > 0 {
		err := k.BurnModuleCoins(ctx, int64(totalCoins), reason+":"+settleAmount.Participant)
		if err != nil {
			k.LogError("Error burning settle amount coins", types.Settle, "error", err, "participant", settleAmount.Participant, "amount", totalCoins)
			return err
		}
		k.BankKeeper.LogSubAccountTransaction(ctx, types.ModuleName, settleAmount.Participant, types.SettleSubAccount, sdk.NewInt64Coin(types.BaseCoin, int64(totalCoins)), reason)
		k.LogInfo("Burned settle amount", types.Settle, "participant", settleAmount.Participant, "amount", totalCoins, "reason", reason)
	}
	return nil
}

// SetSettleAmountWithBurn sets a settle amount, burning any existing unclaimed amount first
func (k Keeper) SetSettleAmountWithBurn(ctx context.Context, settleAmount types.SettleAmount) error {
	// Burn existing settle amount if it exists
	existingSettle, found := k.GetSettleAmount(ctx, settleAmount.Participant)
	if found {
		err := k.burnSettleAmount(ctx, existingSettle, "expired claim")
		if err != nil {
			return err
		}
	}

	// Set the new settle amount
	k.SetSettleAmount(ctx, settleAmount)
	k.BankKeeper.LogSubAccountTransaction(ctx, types.ModuleName, settleAmount.Participant, types.SettleSubAccount, sdk.NewInt64Coin(types.BaseCoin, int64(settleAmount.GetTotalCoins())), "awaiting claim")
	k.BankKeeper.LogSubAccountTransaction(ctx, settleAmount.Participant, types.ModuleName, types.OwedSubAccount, sdk.NewInt64Coin(types.BaseCoin, int64(settleAmount.WorkCoins)), "moved to settled")
	return nil
}

// BurnOldSettleAmounts burns and removes all settle amounts older than the specified epoch
func (k Keeper) BurnOldSettleAmounts(ctx context.Context, beforeEpochIndex uint64) error {
	allSettleAmounts := k.GetAllSettleAmount(ctx)
	for _, settleAmount := range allSettleAmounts {
		if settleAmount.EpochIndex < beforeEpochIndex {
			err := k.burnSettleAmount(ctx, settleAmount, "expired")
			if err != nil {
				return err
			}
			k.RemoveSettleAmount(ctx, settleAmount.Participant)
		}
	}
	return nil
}
