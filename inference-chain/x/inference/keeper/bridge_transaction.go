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
	BridgeTransactionKeyPrefix = "BridgeTx/"
)

// generateBridgeTransactionKey creates a unique key for bridge transactions
// Format: chainId_blockNumber_receiptIndex
func generateBridgeTransactionKey(chainId, blockNumber, receiptIndex string) string {
	return fmt.Sprintf("%s_%s_%s", chainId, blockNumber, receiptIndex)
}

// HasBridgeTransaction checks if a bridge transaction has been processed
func (k Keeper) HasBridgeTransaction(ctx context.Context, chainId, blockNumber, receiptIndex string) bool {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	key := generateBridgeTransactionKey(chainId, blockNumber, receiptIndex)
	return store.Has([]byte(key))
}

// SetBridgeTransaction stores a bridge transaction
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	// Generate proper unique key using chainId
	key := generateBridgeTransactionKey(tx.ChainId, tx.BlockNumber, tx.ReceiptIndex)

	// Update the Id field to match our storage key for consistency
	tx.Id = key

	bz := k.cdc.MustMarshal(tx)
	store.Set([]byte(key), bz)
}

// GetBridgeTransaction retrieves a bridge transaction
func (k Keeper) GetBridgeTransaction(ctx context.Context, chainId, blockNumber, receiptIndex string) (*types.BridgeTransaction, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))
	key := generateBridgeTransactionKey(chainId, blockNumber, receiptIndex)
	bz := store.Get([]byte(key))
	if bz == nil {
		return nil, false
	}

	var tx types.BridgeTransaction
	k.cdc.MustUnmarshal(bz, &tx)
	return &tx, true
}
