package keeper_test

import (
	"fmt"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Pins the same gas-regression invariant as
// x/bls/keeper/gas_regression_test.go, but for SetBridgeTransaction —
// the bridge-exchange hot path that inspired the whole split work.
//
// If a caller fetches a bridge transaction (rehydrating Validators from
// the per-validator KeySet), appends a new validator, and calls
// SetBridgeTransaction without first nulling tx.Validators, the sync
// loop at bridge_transaction.go:37-51 writes EVERY validator's sub-key.
// With N=16 prior confirmations, that's 16 redundant KeySet writes per
// confirmation — an O(N²) write-per-byte pattern.
//
// The fix pattern (used in msg_server_bridge_exchange.go:156, 279) is:
// null tx.Validators before Set. These tests pin that invariant at the
// Set function level, independent of which handler is calling.

func TestSetBridgeTransaction_GasBoundedWhenValidatorsNulled(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	tx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		BlockNumber:     "100",
		ReceiptIndex:    "1",
		OwnerAddress:    "owner1",
		Amount:          "1000",
		ReceiptsRoot:    "0xroot",
	}

	// Seed N prior validator confirmations via the O(1) KeySet path.
	const N = 16
	for i := 0; i < N; i++ {
		validator := fmt.Sprintf("validator-%d", i)
		require.NoError(t, k.AddBridgeTransactionValidator(sdkCtx, tx, validator))
	}

	// Write the base tx with Validators = nil — steady state after a
	// hot-path confirmation.
	k.SetBridgeTransaction(sdkCtx, tx)

	// Simulate a handler fetching the tx and mutating a scalar field
	// with Validators correctly nulled before Set.
	got, found := k.GetBridgeTransactionByContent(sdkCtx, tx)
	require.True(t, found)
	got.TotalValidationPower = int64(N + 1)
	got.Validators = nil

	metered := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	k.SetBridgeTransaction(metered, got)
	used := metered.GasMeter().GasConsumed() - start

	const boundedCeiling = storetypes.Gas(200_000)
	require.Less(t, used, boundedCeiling,
		"SetBridgeTransaction with nulled Validators must write only the small base tx; got %d gas (ceiling %d). If this fails, a caller is passing a rehydrated tx without nulling tx.Validators first — reintroducing the N² KeySet-write bug.", used, boundedCeiling)

	// Sanity: the populated-Validators path must cost more. If both
	// paths drift toward each other, the sync loop has regressed to a
	// no-op and these tests are silently trivial.
	got2, found := k.GetBridgeTransactionByContent(sdkCtx, tx)
	require.True(t, found)
	got2.TotalValidationPower = int64(N + 2)
	// leave got2.Validators populated from GetBridgeTransactionByContent

	metered2 := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	k.SetBridgeTransaction(metered2, got2)
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*3,
		"populated-Validators call (%d gas) must cost noticeably more than nulled-Validators call (%d gas) — otherwise the KeySet sync loop has been removed and this test no longer guards the invariant", usedPopulated, used)
}
