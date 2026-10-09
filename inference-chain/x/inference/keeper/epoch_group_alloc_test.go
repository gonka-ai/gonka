package keeper_test

import (
	"runtime"
	"testing"
	"unsafe"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Building an EpochGroup must not box a fresh copy of the Keeper for every role it plays.
func TestGetEpochGroupCopiesKeeperOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 1})
	_, err := k.GetEpochGroup(ctx, 1, "")
	require.NoError(t, err)

	const n = 200
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		_, _ = k.GetEpochGroup(ctx, 1, "")
	}
	runtime.ReadMemStats(&after)
	perCall := (after.TotalAlloc - before.TotalAlloc) / n
	keeperSize := uint64(unsafe.Sizeof(keeper.Keeper{}))
	require.Less(t, perCall, 3*keeperSize, "bytes per GetEpochGroup: %d, Keeper is %d", perCall, keeperSize)
}
