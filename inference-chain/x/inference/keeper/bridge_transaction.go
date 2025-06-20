package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// Key prefix for bridge transactions
const (
	BridgeTransactionKeyPrefix = "bridge_tx:"
)

// generateBridgeTransactionKey creates a unique key for bridge transactions
// Format: originChain_blockNumber_receiptIndex
func generateBridgeTransactionKey(originChain, blockNumber, receiptIndex string) string {
	return fmt.Sprintf("%s_%s_%s", originChain, blockNumber, receiptIndex)
}

// HasBridgeTransaction checks if a bridge transaction has been processed
func (k Keeper) HasBridgeTransaction(ctx context.Context, originChain, blockNumber, receiptIndex string) bool {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	key := generateBridgeTransactionKey(originChain, blockNumber, receiptIndex)
	return store.Has([]byte(key))
}

// SetBridgeTransaction stores a bridge transaction
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	// Generate proper unique key
	key := generateBridgeTransactionKey(tx.OriginChain, tx.BlockNumber, tx.ReceiptIndex)

	// Update the Id field to match our storage key for consistency
	tx.Id = key

	bz := k.cdc.MustMarshal(tx)
	store.Set([]byte(key), bz)
}

// GetBridgeTransaction retrieves a bridge transaction
func (k Keeper) GetBridgeTransaction(ctx context.Context, originChain, blockNumber, receiptIndex string) (*types.BridgeTransaction, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	key := generateBridgeTransactionKey(originChain, blockNumber, receiptIndex)
	bz := store.Get([]byte(key))
	if bz == nil {
		return nil, false
	}

	var tx types.BridgeTransaction
	k.cdc.MustUnmarshal(bz, &tx)
	return &tx, true
}
