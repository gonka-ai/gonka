package keeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetBridgeTransaction stores a bridge transaction.
//
// The Validators slice is split off into a per-validator KeySet so the
// base BridgeTransaction value stays constant-size as more validators
// confirm. Any inline Validators entries on the input tx are synced to
// the KeySet here; the stored value is persisted with Validators zeroed.
// Hot-path callers should add the new validator via
// AddBridgeTransactionValidator directly and clear Validators on the
// in-memory tx before calling SetBridgeTransaction to avoid redundant
// re-sync writes on every confirmation.
func (k Keeper) SetBridgeTransaction(ctx context.Context, tx *types.BridgeTransaction) {
	key, id, contentHashPart, err := buildBridgeTransactionKey(tx)
	if err != nil {
		k.LogError("Bridge exchange: Failed to build bridge transaction key",
			types.Messages,
			"error", err,
		)
		return
	}

	// Sync any inline validator entries to the KeySet. Duplicates are a
	// no-op at the KeySet level. We key by contentHashPart (matching the
	// parent map) so conflict transactions (same chain/block/receipt but
	// different content) maintain separate validator sets.
	for _, validator := range tx.Validators {
		if validator == "" {
			continue
		}
		if err := k.BridgeTransactionValidators.Set(ctx, collections.Join4(tx.ChainId, tx.BlockNumber, contentHashPart, validator)); err != nil {
			k.LogError("Bridge exchange: Failed to sync validator to keyset",
				types.Messages,
				"chainId", tx.ChainId,
				"blockNumber", tx.BlockNumber,
				"contentHashPart", contentHashPart,
				"validator", validator,
				"error", err,
			)
			return
		}
	}

	// Persist a stripped copy so the base value stays small regardless
	// of how many validators have confirmed. We copy to avoid mutating
	// the caller's struct.
	stripped := *tx
	stripped.Id = id
	stripped.Validators = nil
	tx.Id = id
	if err := k.BridgeTransactionsMap.Set(ctx, key, stripped); err != nil {
		k.LogError("Bridge exchange: Failed to store bridge transaction",
			types.Messages,
			"chainId", tx.ChainId,
			"blockNumber", tx.BlockNumber,
			"contentHashPart", contentHashPart,
			"error", err,
		)
	}
}

// AddBridgeTransactionValidator records that `validator` confirmed the
// transaction identified by its content hash. Cost is constant in the
// number of prior confirmations, so every validator's tx pays the same
// gas regardless of confirmation order.
func (k Keeper) AddBridgeTransactionValidator(ctx context.Context, tx *types.BridgeTransaction, validator string) error {
	_, _, contentHashPart, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return err
	}
	return k.BridgeTransactionValidators.Set(ctx, collections.Join4(tx.ChainId, tx.BlockNumber, contentHashPart, validator))
}

// HasBridgeTransactionValidator reports whether `validator` has already
// confirmed the transaction. Used by the bridge-exchange handler for its
// O(1) duplicate check, replacing the prior O(N) slice scan.
func (k Keeper) HasBridgeTransactionValidator(ctx context.Context, tx *types.BridgeTransaction, validator string) (bool, error) {
	_, _, contentHashPart, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return false, err
	}
	return k.BridgeTransactionValidators.Has(ctx, collections.Join4(tx.ChainId, tx.BlockNumber, contentHashPart, validator))
}

// ListBridgeTransactionValidators returns every validator that has
// confirmed the transaction, in ascending bech32-byte order. Used by
// GetBridgeTransactionByContent's rehydration path and by tests.
func (k Keeper) ListBridgeTransactionValidators(ctx context.Context, tx *types.BridgeTransaction) ([]string, error) {
	_, _, contentHashPart, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return nil, err
	}
	rng := collections.NewSuperPrefixedQuadRange3[string, string, string, string](tx.ChainId, tx.BlockNumber, contentHashPart)
	it, err := k.BridgeTransactionValidators.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	keys, err := it.Keys()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.K4())
	}
	return out, nil
}

// hydrateBridgeTransactionValidators merges any sub-keyed validator
// confirmations into the tx's Validators slice. Any legacy inline
// entries already on the tx are kept as the baseline and deduplicated
// against the sub-key contents. Used by every query/read path so
// consumers always see the full validator set regardless of whether
// entries live inline (pre-upgrade) or in sub-keys (post-upgrade).
func (k Keeper) hydrateBridgeTransactionValidators(ctx context.Context, tx *types.BridgeTransaction) {
	subKeyed, err := k.ListBridgeTransactionValidators(ctx, tx)
	if err != nil || len(subKeyed) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(tx.Validators)+len(subKeyed))
	merged := make([]string, 0, len(tx.Validators)+len(subKeyed))
	for _, v := range tx.Validators {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		merged = append(merged, v)
	}
	for _, v := range subKeyed {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		merged = append(merged, v)
	}
	tx.Validators = merged
}

// GetBridgeTransactionByContent retrieves a bridge transaction by its
// content hash. The Validators slice is rehydrated from the per-validator
// KeySet. Any legacy inline Validators on the stored value serve as the
// baseline and duplicate entries are de-duplicated so the returned slice
// never carries a validator twice after mid-split migration.
func (k Keeper) GetBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) (*types.BridgeTransaction, bool) {
	key, _, _, err := buildBridgeTransactionKey(tx)
	if err != nil {
		return nil, false
	}

	storedTx, err := k.BridgeTransactionsMap.Get(ctx, key)
	if err != nil {
		return nil, false
	}

	k.hydrateBridgeTransactionValidators(ctx, &storedTx)
	return &storedTx, true
}

// HasBridgeTransactionByContent checks if a bridge transaction exists by content hash
func (k Keeper) HasBridgeTransactionByContent(ctx context.Context, tx *types.BridgeTransaction) bool {
	key, _, _, err := buildBridgeTransactionKey(tx)
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

	var matchingTransactions []types.BridgeTransaction
	for ; iter.Valid(); iter.Next() {
		tx, err := iter.Value()
		if err != nil {
			k.LogError("Bridge exchange: Failed to decode bridge transaction during receipt lookup",
				types.Messages,
				"chainId", chainId,
				"error", err,
			)
			continue
		}
		if tx.BlockNumber != blockNumber || tx.ReceiptIndex != receiptIndex {
			continue
		}
		k.hydrateBridgeTransactionValidators(ctx, &tx)
		matchingTransactions = append(matchingTransactions, tx)
	}

	return matchingTransactions
}

// CleanupOldBridgeTransactions removes bridge transactions older than the specified block number
// Note: This currently performs a full scan over chainId because block numbers are stored as strings and cannot be used in a lexicographical range query effectively.
func (k Keeper) CleanupOldBridgeTransactions(ctx context.Context, chainId string, maxBlockNumber string) (int, error) {
	maxBlockNum, err := strconv.ParseUint(maxBlockNumber, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid maxBlockNumber %s: %w", maxBlockNumber, err)
	}

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
		txBlockNum, err := strconv.ParseUint(tx.BlockNumber, 10, 64)
		if err != nil {
			// Skip transactions with invalid block numbers
			continue
		}

		if txBlockNum < maxBlockNum {
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

// buildBridgeTransactionKey returns the parent-map Triple key, the
// composite string ID, and the contentHashPart (parts[2]) as a separate
// value for use as the 3rd component of validator sub-keys. Keeping
// validator sub-keys aligned with the parent's content hash is what
// allows removeBridgeTransactionByID to prefix-delete confirmations
// reliably and prevents conflict transactions from sharing a set.
func buildBridgeTransactionKey(tx *types.BridgeTransaction) (collections.Triple[string, string, string], string, string, error) {
	key := generateSecureBridgeTransactionKey(tx)
	parts := strings.SplitN(key, "_", 3)
	if len(parts) != 3 {
		return collections.Triple[string, string, string]{}, "", "", fmt.Errorf("invalid bridge transaction key: %s", key)
	}
	return collections.Join3(parts[0], parts[1], parts[2]), key, parts[2], nil
}

func (k Keeper) removeBridgeTransactionByID(ctx context.Context, id string) error {
	parts := strings.SplitN(id, "_", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid bridge transaction id: %s", id)
	}
	// Clear any per-validator confirmation sub-keys before removing the
	// base transaction so the split-out records don't linger in state.
	rng := collections.NewSuperPrefixedQuadRange3[string, string, string, string](parts[0], parts[1], parts[2])
	it, err := k.BridgeTransactionValidators.Iterate(ctx, rng)
	if err != nil {
		return fmt.Errorf("iterate bridge transaction validators for removal: %w", err)
	}
	keys, err := it.Keys()
	it.Close()
	if err != nil {
		return fmt.Errorf("collect bridge transaction validator keys for removal: %w", err)
	}
	for _, k4 := range keys {
		if err := k.BridgeTransactionValidators.Remove(ctx, k4); err != nil {
			return fmt.Errorf("remove bridge transaction validator sub-key: %w", err)
		}
	}
	return k.BridgeTransactionsMap.Remove(ctx, collections.Join3(parts[0], parts[1], parts[2]))
}
