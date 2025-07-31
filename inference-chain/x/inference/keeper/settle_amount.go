package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetSettleAmount set a specific settleAmount in the store from its index
func (k Keeper) SetSettleAmount(ctx context.Context, settleAmount types.SettleAmount) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	b := k.cdc.MustMarshal(&settleAmount)
	store.Set(types.SettleAmountKey(
		settleAmount.Participant,
	), b)
}

// GetSettleAmount returns a settleAmount from its index
func (k Keeper) GetSettleAmount(
	ctx context.Context,
	participant string,

) (val types.SettleAmount, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))

	b := store.Get(types.SettleAmountKey(
		participant,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveSettleAmount removes a settleAmount from the store
func (k Keeper) RemoveSettleAmount(
	ctx context.Context,
	participant string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	store.Delete(types.SettleAmountKey(
		participant,
	))
}

// GetAllSettleAmount returns all settleAmount
func (k Keeper) GetAllSettleAmount(ctx context.Context) (list []types.SettleAmount) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.SettleAmount
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
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
func (k Keeper) BurnOldSettleAmounts(ctx context.Context, beforeEpochHeight uint64) error {
	allSettleAmounts := k.GetAllSettleAmount(ctx)
	for _, settleAmount := range allSettleAmounts {
		if settleAmount.PocStartHeight < beforeEpochHeight {
			err := k.burnSettleAmount(ctx, settleAmount, "expired")
			if err != nil {
				return err
			}
			k.RemoveSettleAmount(ctx, settleAmount.Participant)
		}
	}
	return nil
}
