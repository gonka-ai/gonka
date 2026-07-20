//go:build testenvci

package citest

import (
	"context"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/testenv/citest/harness"
	"devshard/testenv/mockchain/adminface"

	"github.com/stretchr/testify/require"
)

// TestS3_ParamsLongPoll verifies mock-chain /testenv/params → mock-dapi chain poll →
// GetRuntimeConfig long-poll wake while devshardd children are running in the stack.
func TestS3_ParamsLongPoll(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, _, eps := harness.BootS1Stack(t, "citest-s3-*")
	client := harness.HTTPClient()
	harness.WaitS1Healthy(t, stack, eps)

	mockDapi := harness.MockDAPIFromConfig(stack.LoadConfig(t))
	grpcConn := harness.DialMockDAPI(t, mockDapi.GRPC)
	nm := gen.NewNodeManagerClient(grpcConn)
	ctx := context.Background()

	harness.Step(t, "baseline GetRuntimeConfig from mock-dapi gRPC (%s)", mockDapi.GRPC)
	baseline, err := harness.GetRuntimeConfigOnce(ctx, nm, 0)
	require.NoError(t, err)
	require.False(t, baseline.Unchanged)
	require.NotNil(t, baseline.Config)
	startHeight := baseline.Config.ParamsBlockHeight
	startMaxNonce := baseline.Config.MaxNonce
	t.Logf("citest: baseline params_block_height=%d max_nonce=%d refusal_timeout=%d",
		startHeight, startMaxNonce, baseline.Config.RefusalTimeout)

	const (
		patchedMaxNonce         = uint32(877)
		patchedRefusalTimeout   = int64(4242)
		patchedExecutionTimeout = int64(8888)
	)

	harness.Step(t, "long-poll blocked at height %d; patching /testenv/params", startHeight)
	type pollResult struct {
		resp *gen.GetRuntimeConfigResponse
		err  error
	}
	done := make(chan pollResult, 1)
	pollCtx, cancelPoll := context.WithTimeout(ctx, 30*time.Second)
	defer cancelPoll()
	go func() {
		r, err := harness.WaitRuntimeConfigLongPoll(pollCtx, nm, startHeight, 25*time.Second)
		done <- pollResult{resp: r, err: err}
	}()

	time.Sleep(200 * time.Millisecond)
	harness.PatchTestenvParams(t, client, mockDapi.HTTP, adminface.ParamsRequest{
		MaxNonce:         ptrUint32(patchedMaxNonce),
		RefusalTimeout:   ptrInt64(patchedRefusalTimeout),
		ExecutionTimeout: ptrInt64(patchedExecutionTimeout),
	})

	var updated *gen.GetRuntimeConfigResponse
	select {
	case r := <-done:
		require.NoError(t, r.err)
		updated = r.resp
	case <-time.After(20 * time.Second):
		t.Fatal("long-poll did not wake after POST /testenv/params")
	}

	require.False(t, updated.Unchanged)
	require.NotNil(t, updated.Config)
	require.Greater(t, updated.Config.ParamsBlockHeight, startHeight)
	require.Equal(t, patchedMaxNonce, updated.Config.MaxNonce)
	require.Equal(t, patchedRefusalTimeout, updated.Config.RefusalTimeout)
	require.Equal(t, patchedExecutionTimeout, updated.Config.ExecutionTimeout)
	t.Logf("citest: long-poll woke at params_block_height=%d max_nonce=%d",
		updated.Config.ParamsBlockHeight, updated.Config.MaxNonce)

	harness.Step(t, "caught-up client gets immediate unchanged (GetRuntimeConfig protocol)")
	immediate, err := harness.GetRuntimeConfigOnce(ctx, nm, updated.Config.ParamsBlockHeight)
	require.NoError(t, err)
	require.True(t, immediate.Unchanged)
	require.Nil(t, immediate.Config)

	harness.Step(t, "stale-height client still receives patched lane-C snapshot from mock-dapi")
	stale, err := harness.GetRuntimeConfigOnce(ctx, nm, startHeight)
	require.NoError(t, err)
	require.False(t, stale.Unchanged)
	require.Equal(t, patchedMaxNonce, stale.Config.MaxNonce)
	require.Equal(t, patchedRefusalTimeout, stale.Config.RefusalTimeout)
	require.Equal(t, patchedExecutionTimeout, stale.Config.ExecutionTimeout)
	require.Greater(t, stale.Config.ParamsBlockHeight, startHeight)
}

func ptrUint32(v uint32) *uint32 { return &v }
func ptrInt64(v int64) *int64    { return &v }
