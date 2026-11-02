package inference_test

import (
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Measure EndBlock store gas, then add timed and permanent marker grants and advance past expiration.
//  2. Assert EndBlock costs the same, emits no expiry, and leaves each historical grant unchanged.
func TestEndBlockWarmKeyExpirationDoesNotScanGrants(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)
	expires := time.Unix(100, 900_000_000)
	ctx = ctx.WithBlockHeight(10).WithBlockTime(time.Unix(50, 0))
	endBlock := func(height int64, now time.Time) uint64 {
		blockCtx := ctx.WithBlockHeight(height).WithBlockTime(now).
			WithGasMeter(storetypes.NewInfiniteGasMeter()).WithEventManager(sdk.NewEventManager())
		require.NoError(t, am.EndBlock(blockCtx))
		require.Empty(t, blockCtx.EventManager().Events())
		return blockCtx.GasMeter().GasConsumed()
	}
	baseline := endBlock(11, time.Unix(101, 0))
	require.Positive(t, baseline, "store reads must be metered")
	granter := sdk.AccAddress([]byte("warm-key-log-granter!"))
	entries := make([]types.WarmKeyLogEntry, 128)
	for i := range entries {
		grantee := make(sdk.AccAddress, 20)
		grantee[0], grantee[1] = byte(i), 1
		var expiration *time.Time
		if i%2 == 0 {
			expiration = &expires
		}
		var err error
		entries[i], err = k.RecordWarmKeyGrant(ctx, granter, grantee, expiration)
		require.NoError(t, err)
	}
	for _, height := range []int64{11, 12} {
		require.Equal(t, baseline, endBlock(height, time.Unix(101, 0)), "grant count must not add recurring work")
		got, err := k.WarmKeyLogEntries(ctx, granter)
		require.NoError(t, err)
		require.Equal(t, entries, got)
	}
}
