package nodemanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func bumpRuntimeConfigAt(t *testing.T, cm *apiconfig.ConfigManager, height int64, epochID uint64, logprobsMode string) {
	t.Helper()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: logprobsMode}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(height, epochID))
}

func TestNodeManager_GetRuntimeConfig_LongPoll_ReturnsOnNotify(t *testing.T) {
	cm := testConfigManager(t)
	pt := testPhaseTrackerWithEpoch(1)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, pt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan *gen.GetRuntimeConfigResponse, 1)
	go func() {
		resp, err := srv.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          2,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	bumpRuntimeConfigAt(t, cm, 101, 1, "raw")

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.NotNil(t, resp.Config)
		require.Equal(t, int64(101), resp.Config.ParamsBlockHeight)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("expected RPC to return on notify well before max_wait")
	}
}

func TestNodeManager_GetRuntimeConfig_LongPoll_NoWakeWithoutRevisionChange(t *testing.T) {
	cm := testConfigManager(t)
	pt := testPhaseTrackerWithEpoch(1)
	populateRuntimeConfig(t, cm, 100, 1)
	srv := NewServer(&mockBroker{}, cm, pt)

	done := make(chan *gen.GetRuntimeConfigResponse, 1)
	go func() {
		resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          1,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	cm.SetRuntimeParamsBlockHeight(101)

	select {
	case resp := <-done:
		require.True(t, resp.Unchanged)
	case <-time.After(2 * time.Second):
		t.Fatal("expected timeout without notify when revision unchanged")
	}
}

func TestNodeManager_GetRuntimeConfig_LongPoll_TimesOut(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          1, // 1 second; test stays fast
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Nil(t, resp.Config)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second)
}

func TestNodeManager_GetRuntimeConfig_LongPoll_ImmediateWhenBehind(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 50,
		MaxWaitSeconds:          10,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 100*time.Millisecond)
	require.False(t, resp.Unchanged)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
}

func TestNodeManager_GetRuntimeConfig_LongPoll_ImmediateWhenClientZero(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 0,
		MaxWaitSeconds:          10,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 100*time.Millisecond)
	require.False(t, resp.Unchanged)
	require.NotNil(t, resp.Config)
}

func TestNodeManager_GetRuntimeConfig_LongPoll_ContextCancel(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := srv.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          30,
		})
		done <- err
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(2 * time.Second):
		t.Fatal("expected cancel to unblock RPC")
	}
}

func TestNodeManager_GetRuntimeConfig_LongPoll_BroadcastWakesAllWaiters(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	const n = 8
	var wg sync.WaitGroup
	results := make([]*gen.GetRuntimeConfigResponse, n)
	errs := make([]error, n)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
				ClientParamsBlockHeight: 100,
				MaxWaitSeconds:          5,
			})
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	bumpRuntimeConfigAt(t, cm, 101, 1, "raw")

	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.False(t, results[i].Unchanged)
		require.Equal(t, int64(101), results[i].Config.ParamsBlockHeight)
	}
}

func TestNodeManager_GetRuntimeConfig_LongPoll_NoLostUpdate(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	const rounds = 20
	for r := 0; r < rounds; r++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		done := make(chan *gen.GetRuntimeConfigResponse, 1)
		go func() {
			resp, err := srv.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
				ClientParamsBlockHeight: 100 + int64(r),
				MaxWaitSeconds:          2,
			})
			require.NoError(t, err)
			done <- resp
		}()

		time.Sleep(5 * time.Millisecond)
		mode := "raw"
		if r%2 == 0 {
			mode = "full"
		}
		bumpRuntimeConfigAt(t, cm, 100+int64(r)+1, 1, mode)

		select {
		case resp := <-done:
			cancel()
			require.False(t, resp.Unchanged)
			require.Greater(t, resp.Config.ParamsBlockHeight, int64(100+r))
		case <-ctx.Done():
			cancel()
			t.Fatalf("round %d: waiter did not wake after notify", r)
		}
	}
}

func TestNodeManager_GetRuntimeConfig_LongPoll_MaxWaitClampedToServerCap(t *testing.T) {
	t.Setenv("DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "1")
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          600,
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
}
