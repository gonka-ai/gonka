package keeper_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

// Once unregistered, the per-block check no longer reads params.
func TestCheckAndUnregisterRestrictionSkipsParamsAfterUnregister(t *testing.T) {
	k, ctx := keepertest.RestrictionsKeeper(t)
	params := types.DefaultParams()
	params.RestrictionEndBlock = 1000
	require.NoError(t, k.SetParams(ctx, params))
	ctx = ctx.WithBlockHeight(2000)
	require.NoError(t, k.CheckAndUnregisterRestriction(ctx))

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	require.NoError(t, k.CheckAndUnregisterRestriction(ctx))
	ctx.MultiStore().SetTracer(nil)

	enc := base64.StdEncoding.EncodeToString(types.ParamsKey)
	require.NotContains(t, trace.String(), `"key":"`+enc+`"`)
	require.False(t, k.IsRestrictionActive(ctx))
}

// A fee payment to the fee collector is allowed without reading params.
func TestSendRestrictionFeePaymentSkipsParams(t *testing.T) {
	k, ctx := keepertest.RestrictionsKeeper(t)
	params := types.DefaultParams()
	params.RestrictionEndBlock = 1000
	require.NoError(t, k.SetParams(ctx, params))

	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	from := sdk.AccAddress(bytes.Repeat([]byte{1}, 20))
	coins := sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1))
	for _, height := range []int64{500, 2000} {
		hctx := ctx.WithBlockHeight(height).WithGasMeter(storetypes.NewInfiniteGasMeter())
		var trace bytes.Buffer
		hctx.MultiStore().SetTracer(&trace)
		to, err := k.SendRestrictionFn(hctx, from, feeCollector, coins)
		hctx.MultiStore().SetTracer(nil)
		require.NoError(t, err)
		require.Equal(t, feeCollector, to)
		enc := base64.StdEncoding.EncodeToString(types.ParamsKey)
		require.NotContains(t, trace.String(), `"key":"`+enc+`"`)
		require.Zero(t, hctx.GasMeter().GasConsumed())
	}
}
