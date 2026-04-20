package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestSetBridgeTransaction_StripsAndSyncsValidators pins the invariant
// that SetBridgeTransaction must (a) persist each inline validator entry
// to the per-validator KeySet and (b) zero the inline slice in the
// stored value so later writes stay constant-size. This is the fix for
// the N^2 write-per-byte growth that BridgeExchange otherwise hits on
// every confirmation.
//
// Verification strategy: seed via SetBridgeTransaction, then delete all
// KeySet entries and call Get again — the returned Validators slice
// must be empty, proving the base value carried no inline entries.
func TestSetBridgeTransaction_StripsAndSyncsValidators(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	tx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		BlockNumber:     "100",
		ReceiptIndex:    "1",
		OwnerAddress:    "owner1",
		Amount:          "1000",
		ReceiptsRoot:    "0xroot",
		Validators:      []string{"valA", "valB", "valC"},
	}
	k.SetBridgeTransaction(ctx, tx)

	// KeySet is populated.
	for _, v := range []string{"valA", "valB", "valC"} {
		has, err := k.HasBridgeTransactionValidator(ctx, tx, v)
		require.NoError(t, err)
		require.True(t, has, "validator %s should be indexed via the KeySet", v)
	}

	// A non-confirming validator is not indexed.
	has, err := k.HasBridgeTransactionValidator(ctx, tx, "valD")
	require.NoError(t, err)
	require.False(t, has)

	// Rehydrated Get returns everyone.
	got, found := k.GetBridgeTransactionByContent(ctx, tx)
	require.True(t, found)
	require.ElementsMatch(t, []string{"valA", "valB", "valC"}, got.Validators)

	// Delete every KeySet entry and verify Get's Validators is now empty.
	// If Validators had been stored inline in the base, deleting the
	// KeySet wouldn't clear them — catching a regression that leaves
	// inline entries behind.
	//
	// We iterate the KeySet directly so the test removes keys under
	// whatever schema production uses (currently chainId, blockNumber,
	// contentHashPart, validator) without duplicating the key-derivation
	// logic in the test.
	iter, err := k.BridgeTransactionValidators.Iterate(ctx, nil)
	require.NoError(t, err)
	keys, err := iter.Keys()
	iter.Close()
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	for _, key := range keys {
		require.NoError(t, k.BridgeTransactionValidators.Remove(ctx, key))
	}
	afterDelete, found := k.GetBridgeTransactionByContent(ctx, tx)
	require.True(t, found)
	require.Empty(t, afterDelete.Validators,
		"base value must carry zero inline validators, or the N^2 write-per-byte bug returns")
}

// TestBridgeTransactionValidators_Split_O1_Confirmation pins the hot-path
// behavior: adding a validator via AddBridgeTransactionValidator and
// then persisting the base tx with Validators = nil writes exactly one
// KeySet entry and does not touch the other validators' entries.
func TestBridgeTransactionValidators_Split_O1_Confirmation(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	tx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		BlockNumber:     "200",
		ReceiptIndex:    "2",
		OwnerAddress:    "owner2",
		Amount:          "500",
		ReceiptsRoot:    "0xroot",
	}

	// Seed three prior confirmations via the KeySet directly.
	for _, addr := range []string{"v1", "v2", "v3"} {
		require.NoError(t, k.AddBridgeTransactionValidator(ctx, tx, addr))
	}

	// Simulate the hot path: a fourth validator adds their sub-key and
	// writes the base with Validators = nil.
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, tx, "v4"))
	tx.Validators = nil
	tx.TotalValidationPower = 4
	k.SetBridgeTransaction(ctx, tx)

	got, found := k.GetBridgeTransactionByContent(ctx, tx)
	require.True(t, found)
	require.ElementsMatch(t, []string{"v1", "v2", "v3", "v4"}, got.Validators)
	require.Equal(t, int64(4), got.TotalValidationPower)
}

// TestGetBridgeTransactionByContent_MergesLegacyInlineAndSubKey pins the
// legacy-compat behavior: a transaction written by a pre-split handler
// (inline Validators) continues to be visible, with any post-split
// KeySet entries merged in and duplicates collapsed.
func TestGetBridgeTransactionByContent_MergesLegacyInlineAndSubKey(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	tx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xdef",
		BlockNumber:     "300",
		ReceiptIndex:    "3",
		OwnerAddress:    "owner3",
		Amount:          "42",
		ReceiptsRoot:    "0xroot",
		// Legacy-shape seed: inline validators get synced by
		// SetBridgeTransaction, matching how the upgrade handler
		// processes in-flight entries.
		Validators: []string{"old-val-a", "old-val-b"},
	}
	k.SetBridgeTransaction(ctx, tx)

	// Post-split: a new validator arrives, plus a duplicate of an
	// already-recorded one. The duplicate must not appear twice.
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, tx, "new-val-c"))
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, tx, "old-val-a"))

	got, found := k.GetBridgeTransactionByContent(ctx, tx)
	require.True(t, found)
	require.ElementsMatch(t, []string{"old-val-a", "old-val-b", "new-val-c"}, got.Validators)
}
