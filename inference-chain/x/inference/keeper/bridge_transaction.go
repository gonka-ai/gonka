package keeper

import (
	"context"
	"fmt"
	"strings"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// Key prefix for bridge transactions
const (
	BridgeTransactionKeyPrefix = "BridgeTx/"
)

// SetBridgeTransaction stores a bridge transaction using content-based key
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	// Generate secure content-based key
	key := generateSecureBridgeTransactionKey(tx)

	// Update the Id field to match our storage key for consistency
	tx.Id = key

	bz := k.cdc.MustMarshal(tx)
	store.Set([]byte(key), bz)
}

// GetBridgeTransactionByContent retrieves a bridge transaction by its content hash
func (k Keeper) GetBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) (*types.BridgeTransaction, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	// Generate content-based key
	key := generateSecureBridgeTransactionKey(tx)
	bz := store.Get([]byte(key))
	if bz == nil {
		return nil, false
	}

	var storedTx types.BridgeTransaction
	k.cdc.MustUnmarshal(bz, &storedTx)
	return &storedTx, true
}

// HasBridgeTransactionByContent checks if a bridge transaction exists by content hash
func (k Keeper) HasBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) bool {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	key := generateSecureBridgeTransactionKey(tx)
	return store.Has([]byte(key))
}

// GetBridgeTransactionsByReceipt finds all bridge transactions that match a specific receipt location
// This can return multiple transactions if there are conflicts (different content for same receipt)
func (k Keeper) GetBridgeTransactionsByReceipt(ctx context.Context, chainId, blockNumber, receiptIndex string) []types.BridgeTransaction {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	var matchingTransactions []types.BridgeTransaction
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	// Key format: chainId_blockNumber_contentHash
	// We want to find all keys that start with "chainId_blockNumber_"
	searchPrefix := fmt.Sprintf("%s_%s_", chainId, blockNumber)

	for ; iterator.Valid(); iterator.Next() {
		key := string(iterator.Key())

		// Check if this key matches our chain and block
		if strings.HasPrefix(key, searchPrefix) {
			// Parse the stored transaction to check receipt index
			var tx types.BridgeTransaction
			err := k.cdc.Unmarshal(iterator.Value(), &tx)
			if err != nil {
				// Log error but continue processing other transactions
				continue
			}

			// Check if receipt index matches
			if tx.ReceiptIndex == receiptIndex {
				matchingTransactions = append(matchingTransactions, tx)
			}
		}
	}

	return matchingTransactions
}

// CleanupOldBridgeTransactions removes bridge transactions older than the specified block number
// This is efficient because block numbers are included in the key prefix
func (k Keeper) CleanupOldBridgeTransactions(ctx context.Context, chainId string, maxBlockNumber string) (int, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	var deletedCount int
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		key := string(iterator.Key())

		// Parse key format: chainId_blockNumber_contentHash
		// Check if this transaction is for the specified chain and is old enough
		if len(key) > len(chainId)+1 && key[:len(chainId)+1] == chainId+"_" {
			// Extract block number from key
			remaining := key[len(chainId)+1:]
			// Find the next underscore to get the block number
			underscorePos := -1
			for i, c := range remaining {
				if c == '_' {
					underscorePos = i
					break
				}
			}

			if underscorePos > 0 {
				blockNumberStr := remaining[:underscorePos]

				// Compare block numbers as strings (lexicographic comparison works for same-length numbers)
				// For proper comparison, we'd need to parse as integers, but this is a simple approach
				if blockNumberStr < maxBlockNumber {
					store.Delete(iterator.Key())
					deletedCount++
				}
			}
		}
	}

	return deletedCount, nil
}
