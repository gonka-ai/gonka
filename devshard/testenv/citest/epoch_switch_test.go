//go:build testenvci

package citest

import (
	"context"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestEpochSwitch verifies POST /testenv/epoch advance fast-forwards mock-chain to the
// next PoC start block, rolls next_poc_start forward, and surfaces a higher
// CurrentEpochId on GetRuntimeConfig while devshardd is running.
//
// Do not assert on the first long-poll wake: AdvanceEpoch floods blocks and can
// notify mid-catch-up (params height ahead of epoch). Wait Eventually for the
// settled epoch + height, same pattern as other citests.
func TestEpochSwitch(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-epoch-switch-*")
	client := harness.HTTPClient()
	harness.WaitStackHealthy(t, stack, eps)

	mockDapi := harness.MockDAPIFromEndpoints(eps)
	grpcConn := harness.DialMockDAPI(t, mockDapi.GRPC)
	nm := gen.NewNodeManagerClient(grpcConn)
	ctx := context.Background()

	epochLength := cfg.Epoch.EpochLength
	if epochLength == 0 {
		epochLength = 400
	}

	harness.Step(t, "baseline mock-chain epoch snapshot")
	before := harness.GetMockChainSnapshot(t, cfg, eps, client)
	require.Equal(t, uint64(1), before.EpochIndex)
	require.Greater(t, before.NextPocStart, before.BlockHeight)
	targetHeight := before.NextPocStart
	t.Logf("citest: block=%d poc_start=%d next_poc=%d",
		before.BlockHeight, before.PocStart, before.NextPocStart)

	harness.Step(t, "baseline GetRuntimeConfig epoch=%d", before.EpochIndex)
	baseline, err := harness.GetRuntimeConfigOnce(ctx, nm, 0)
	require.NoError(t, err)
	require.False(t, baseline.Unchanged)
	startEpoch := baseline.Config.CurrentEpochId
	startHeight := baseline.Config.ParamsBlockHeight
	require.Equal(t, before.EpochIndex, startEpoch)

	harness.Step(t, "advance epoch (target next PoC start=%d)", targetHeight)
	harness.PatchTestenvEpochAdvance(t, client, mockDapi.HTTP)

	harness.Step(t, "wait until GetRuntimeConfig shows epoch > %d and params height >= %d",
		startEpoch, targetHeight)
	var settled *gen.GetRuntimeConfigResponse
	ok := harness.AssertEventually(t, 45*time.Second, 500*time.Millisecond, func() bool {
		resp, err := harness.GetRuntimeConfigOnce(ctx, nm, 0)
		if err != nil || resp.Unchanged || resp.Config == nil {
			return false
		}
		if resp.Config.CurrentEpochId <= startEpoch {
			return false
		}
		if resp.Config.ParamsBlockHeight < targetHeight {
			return false
		}
		settled = resp
		return true
	})
	require.True(t, ok, "runtime config did not settle to epoch>%d params_height>=%d (start height=%d)",
		startEpoch, targetHeight, startHeight)
	require.NotNil(t, settled)
	t.Logf("citest: settled epoch=%d params_block_height=%d",
		settled.Config.CurrentEpochId, settled.Config.ParamsBlockHeight)

	harness.Step(t, "mock-chain caught up to next PoC start and rolled next_poc forward")
	after := harness.GetMockChainSnapshot(t, cfg, eps, client)
	require.Equal(t, settled.Config.CurrentEpochId, after.EpochIndex)
	require.GreaterOrEqual(t, after.BlockHeight, targetHeight)
	require.Equal(t, targetHeight, after.PocStart)
	require.Equal(t, targetHeight+epochLength, after.NextPocStart)
}
