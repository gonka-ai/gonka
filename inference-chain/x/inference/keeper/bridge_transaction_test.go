package keeper_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCleanupOldBridgeTransactions_NumericComparison(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)

	// Store transactions with block numbers that would sort incorrectly as strings
	// String sort: "10" < "9" (wrong), Numeric sort: 9 < 10 (correct)
	txs := []types.BridgeTransaction{
		{ChainId: "ethereum", BlockNumber: "2", ReceiptIndex: "0", ContractAddress: "0xaaa", OwnerAddress: "owner1", Amount: "100", ReceiptsRoot: "root1"},
		{ChainId: "ethereum", BlockNumber: "9", ReceiptIndex: "0", ContractAddress: "0xbbb", OwnerAddress: "owner2", Amount: "200", ReceiptsRoot: "root2"},
		{ChainId: "ethereum", BlockNumber: "10", ReceiptIndex: "0", ContractAddress: "0xccc", OwnerAddress: "owner3", Amount: "300", ReceiptsRoot: "root3"},
		{ChainId: "ethereum", BlockNumber: "100", ReceiptIndex: "0", ContractAddress: "0xddd", OwnerAddress: "owner4", Amount: "400", ReceiptsRoot: "root4"},
	}

	for i := range txs {
		k.SetBridgeTransaction(ctx, &txs[i])
	}

	// Cleanup blocks older than block 10 (should delete blocks 2 and 9, NOT 10 or 100)
	deleted, err := k.CleanupOldBridgeTransactions(ctx, "ethereum", "10")
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "should delete blocks 2 and 9 (both < 10)")
}

func TestCleanupOldBridgeTransactions_InvalidMaxBlockNumber(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)

	_, err := k.CleanupOldBridgeTransactions(ctx, "ethereum", "not-a-number")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid maxBlockNumber")
}
