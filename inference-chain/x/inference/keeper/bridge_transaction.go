package keeper

import (
	"context"
	"log/slog"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// Key prefix for bridge transactions
const (
	BridgeTransactionKeyPrefix = "bridge_tx:"
)

// HasBridgeTransaction checks if a bridge transaction has been processed
func (k Keeper) HasBridgeTransaction(ctx context.Context, txHash string) bool {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	return store.Has([]byte(txHash))
}

// SetBridgeTransaction stores a bridge transaction
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	slog.Info("Setting bridge transaction",
		"index", tx.Index,
		"status", tx.Status,
		"validationCount", tx.ValidationCount)

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	bz := k.cdc.MustMarshal(tx)
	store.Set([]byte(tx.Index), bz)
}

// GetBridgeTransaction retrieves a bridge transaction
func (k Keeper) GetBridgeTransaction(ctx context.Context, txHash string) (*types.BridgeTransaction, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	bz := store.Get([]byte(txHash))
	if bz == nil {
		return nil, false
	}

	var tx types.BridgeTransaction
	k.cdc.MustUnmarshal(bz, &tx)
	return &tx, true
}
