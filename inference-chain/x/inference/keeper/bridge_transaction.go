package keeper

import (
	"context"
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetBridgeTransaction stores a bridge transaction using content-based key
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	key, id, err := buildBridgeTransactionKey(tx)
	if err != nil {
		k.LogError("Bridge exchange: Failed to build bridge transaction key",
			types.Messages,
			"error", err,
		)
		return
	}

	tx.Id = id
	if err := k.BridgeTransactionsMap.Set(ctx, key, *tx); err != nil {
		k.LogError("Bridge exchange: Failed to store bridge transaction",
			types.Messages,
			"chainId", tx.ChainId,
			"blockNumber", tx.BlockNumber,
			"receiptIndex", tx.ReceiptIndex,
			"error", err,
		)
	}
}

// GetBridgeTransactionByContent retrieves a bridge transaction by its content hash
func (k Keeper) GetBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) (*types.BridgeTransaction, bool) {
	key, _, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return nil, false
	}

	storedTx, err := k.BridgeTransactionsMap.Get(ctx, key)
	if err != nil {
		return nil, false
	}
	return &storedTx, true
}

// HasBridgeTransactionByContent checks if a bridge transaction exists by content hash
func (k Keeper) HasBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) bool {
	key, _, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return false
	}

	has, err := k.BridgeTransactionsMap.Has(ctx, key)
	if err != nil {
		return false
	}
	return has
}

// GetBridgeTransactionsByReceipt finds all bridge transactions that match a specific receipt location
// This can return multiple transactions if there are conflicts (different content for same receipt)
func (k Keeper) GetBridgeTransactionsByReceipt(ctx context.Context, chainId, blockNumber, receiptIndex string) []types.BridgeTransaction {
	iter, err := k.BridgeTransactionsMap.Iterate(ctx, collections.NewPrefixedTripleRange[string, string, string](chainId))
	if err != nil {
		k.LogError("Bridge exchange: Failed to iterate bridge transactions by chain",
			types.Messages,
			"chainId", chainId,
			"error", err,
		)
		return nil
	}
	defer iter.Close()

	values, err := iter.Values()
	if err != nil {
		k.LogError("Bridge exchange: Failed to collect bridge transactions by chain",
			types.Messages,
			"chainId", chainId,
			"error", err,
		)
		return nil
	}

	var matchingTransactions []types.BridgeTransaction
	for _, tx := range values {
		if tx.BlockNumber == blockNumber && tx.ReceiptIndex == receiptIndex {
			matchingTransactions = append(matchingTransactions, tx)
		}
	}

	return matchingTransactions
}

// CleanupOldBridgeTransactions removes bridge transactions older than the specified block number
// This is efficient because block numbers are included in the key prefix
func (k Keeper) CleanupOldBridgeTransactions(ctx context.Context, chainId string, maxBlockNumber string) (int, error) {
	iter, err := k.BridgeTransactionsMap.Iterate(ctx, collections.NewPrefixedTripleRange[string, string, string](chainId))
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	values, err := iter.Values()
	if err != nil {
		return 0, err
	}

	var deletedCount int
	var firstErr error
	for _, tx := range values {
		if tx.BlockNumber < maxBlockNumber {
			if err := k.removeBridgeTransactionByID(ctx, tx.Id); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			deletedCount++
		}
	}

	return deletedCount, firstErr
}

func buildBridgeTransactionKey(tx *types.BridgeTransaction) (collections.Triple[string, string, string], string, error) {
	key := generateSecureBridgeTransactionKey(tx)
	parts := strings.SplitN(key, "_", 3)
	if len(parts) != 3 {
		return collections.Triple[string, string, string]{}, "", fmt.Errorf("invalid bridge transaction key: %s", key)
	}
	return collections.Join3(parts[0], parts[1], parts[2]), key, nil
}

func (k Keeper) removeBridgeTransactionByID(ctx context.Context, id string) error {
	parts := strings.SplitN(id, "_", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid bridge transaction id: %s", id)
	}
	return k.BridgeTransactionsMap.Remove(ctx, collections.Join3(parts[0], parts[1], parts[2]))
}
