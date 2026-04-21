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

// Bridge companion of x/bls/keeper/gas_regression_test.go: calling
// SetBridgeTransaction with a rehydrated tx.Validators triggers a
// KeySet sync that rewrites every prior confirmation. Handlers null
// tx.Validators before Set; this test pins that invariant.

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

	const N = 16
	for i := 0; i < N; i++ {
		require.NoError(t, k.AddBridgeTransactionValidator(sdkCtx, tx, fmt.Sprintf("validator-%d", i)))
	}
	k.SetBridgeTransaction(sdkCtx, tx)

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
		"nulled-validators call must write only the base tx; got %d, ceiling %d", used, boundedCeiling)

	got2, found := k.GetBridgeTransactionByContent(sdkCtx, tx)
	require.True(t, found)
	got2.TotalValidationPower = int64(N + 2)
	// leave got2.Validators populated from the Get

	metered2 := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	k.SetBridgeTransaction(metered2, got2)
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*3,
		"populated (%d) must cost notably more than nulled (%d)", usedPopulated, used)
}
