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

// TestS4_EpochSwitch verifies POST /testenv/epoch advance fast-forwards mock-chain to the
// next PoC start block, rolls next_poc_start forward, and wakes GetRuntimeConfig long-poll
// with a higher CurrentEpochID while devshardd is running.
func TestS4_EpochSwitch(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootS1Stack(t, "citest-s4-*")
	client := harness.HTTPClient()
	harness.WaitS1Healthy(t, stack, eps)

	mockDapi := harness.MockDAPIFromConfig(cfg)
	grpcConn := harness.DialMockDAPI(t, mockDapi.GRPC)
	nm := gen.NewNodeManagerClient(grpcConn)
	ctx := context.Background()

	epochLength := cfg.Epoch.EpochLength
	if epochLength == 0 {
		epochLength = 400
	}

	harness.Step(t, "baseline mock-chain epoch snapshot")
	before := harness.GetMockChainSnapshot(t, cfg, client)
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

	harness.Step(t, "long-poll blocked at epoch %d height %d; advancing epoch", startEpoch, startHeight)
	type pollResult struct {
		resp *gen.GetRuntimeConfigResponse
		err  error
	}
	done := make(chan pollResult, 1)
	pollCtx, cancelPoll := context.WithTimeout(ctx, 45*time.Second)
	defer cancelPoll()
	go func() {
		r, err := harness.WaitRuntimeConfigLongPoll(pollCtx, nm, startHeight, 40*time.Second)
		done <- pollResult{resp: r, err: err}
	}()

	time.Sleep(200 * time.Millisecond)
	harness.PatchTestenvEpochAdvance(t, client, mockDapi.HTTP)

	var updated *gen.GetRuntimeConfigResponse
	select {
	case r := <-done:
		require.NoError(t, r.err)
		updated = r.resp
	case <-time.After(30 * time.Second):
		t.Fatal("long-poll did not wake after POST /testenv/epoch advance")
	}

	require.False(t, updated.Unchanged)
	require.NotNil(t, updated.Config)
	require.Greater(t, updated.Config.CurrentEpochId, startEpoch)
	require.Greater(t, updated.Config.ParamsBlockHeight, startHeight)
	t.Logf("citest: long-poll woke epoch=%d params_block_height=%d",
		updated.Config.CurrentEpochId, updated.Config.ParamsBlockHeight)

	harness.Step(t, "mock-chain caught up to next PoC start and rolled next_poc forward")
	after := harness.GetMockChainSnapshot(t, cfg, client)
	require.Equal(t, updated.Config.CurrentEpochId, after.EpochIndex)
	require.GreaterOrEqual(t, after.BlockHeight, targetHeight)
	require.Equal(t, targetHeight, after.PocStart)
	require.Equal(t, targetHeight+epochLength, after.NextPocStart)
}
