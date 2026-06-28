package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	devshardpkg "devshard"
	"devshard/mlnode"
	"devshard/nodemanager/gen"
	"devshard/runtimeconfig"
	"devshard/runtimeconfig/testserver"

	chaintypes "github.com/productscience/inference/x/inference/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeQueryClient struct {
	chaintypes.QueryClient

	paramsFn    func(ctx context.Context, in *chaintypes.QueryParamsRequest, opts ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error)
	epochInfoFn func(ctx context.Context, in *chaintypes.QueryEpochInfoRequest, opts ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error)
}

func (f *fakeQueryClient) Params(ctx context.Context, in *chaintypes.QueryParamsRequest, opts ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
	return f.paramsFn(ctx, in, opts...)
}
func (f *fakeQueryClient) EpochInfo(ctx context.Context, in *chaintypes.QueryEpochInfoRequest, opts ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
	return f.epochInfoFn(ctx, in, opts...)
}

type fakeQueryProvider struct{ qc chaintypes.QueryClient }

func (p fakeQueryProvider) NewInferenceQueryClient() chaintypes.QueryClient { return p.qc }

func newChainFake(t *testing.T, paramsEnabled bool, epoch uint64) fakeQueryProvider {
	t.Helper()
	return fakeQueryProvider{qc: &fakeQueryClient{
		paramsFn: func(_ context.Context, _ *chaintypes.QueryParamsRequest, _ ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return &chaintypes.QueryParamsResponse{
				Params: chaintypes.Params{
					ValidationParams: &chaintypes.ValidationParams{LogprobsMode: "raw"},
					DevshardEscrowParams: &chaintypes.DevshardEscrowParams{
						DevshardRequestsEnabled: paramsEnabled,
						MaxNonce:                500,
					},
				},
			}, nil
		},
		epochInfoFn: func(_ context.Context, _ *chaintypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
			return &chaintypes.QueryEpochInfoResponse{
				LatestEpoch: chaintypes.Epoch{Index: epoch, PocStartBlockHeight: int64(epoch) * 100},
			}, nil
		},
	}}
}

type errOnlyNMClient struct {
	gen.NodeManagerClient
	err error
}

func (c *errOnlyNMClient) GetRuntimeConfig(ctx context.Context, in *gen.GetRuntimeConfigRequest, opts ...grpc.CallOption) (*gen.GetRuntimeConfigResponse, error) {
	return nil, c.err
}

// scriptNMClient returns scripted GetRuntimeConfig outcomes per call (Phase 5 integration tests).
type scriptNMClient struct {
	gen.NodeManagerClient
	mu       sync.Mutex
	handlers []func() (*gen.GetRuntimeConfigResponse, error)
	callN    int
}

func (c *scriptNMClient) GetRuntimeConfig(ctx context.Context, in *gen.GetRuntimeConfigRequest, opts ...grpc.CallOption) (*gen.GetRuntimeConfigResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.callN
	c.callN++
	var fn func() (*gen.GetRuntimeConfigResponse, error)
	if idx < len(c.handlers) {
		fn = c.handlers[idx]
	} else if len(c.handlers) > 0 {
		fn = c.handlers[len(c.handlers)-1]
	} else {
		fn = func() (*gen.GetRuntimeConfigResponse, error) {
			return nil, status.Error(codes.Unavailable, "no handler")
		}
	}
	return fn()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func waitRuntimeSnapshot(t *testing.T, p runtimeconfig.Provider, height int64) runtimeconfig.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := p.Snapshot()
		if s.ParamsBlockHeight >= height {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for params_block_height >= %d (got %d)", height, p.Snapshot().ParamsBlockHeight)
	return runtimeconfig.Snapshot{}
}

func waitActiveSource(t *testing.T, active func() string, want string, max ...time.Duration) {
	t.Helper()
	timeout := 3 * time.Second
	if len(max) > 0 {
		timeout = max[0]
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if active() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for active source %q (got %q)", want, active())
}

func clearSourceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_GRPC_STALE_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_GRPC_REPROBE_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_GRPC_FAILBACK_PROBES", "")
}

// ---------------------------------------------------------------------------
// Env / settings
// ---------------------------------------------------------------------------

func TestRuntimeConfigSettingsFromEnv_Defaults(t *testing.T) {
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "")
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS", "")

	maxWait, slack := runtimeConfigSettingsFromEnv()
	assert.Equal(t, 60*time.Second, maxWait)
	assert.Equal(t, 5*time.Second, slack)
}

func TestChainParamsSettingsFromEnv_Defaults(t *testing.T) {
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS", "")
	refresh, initial := chainParamsSettingsFromEnv()
	assert.Equal(t, 60*time.Second, refresh)
	assert.Equal(t, 5*time.Second, initial)
}

func TestParamsSourceFromEnv(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", paramsSourceAuto},
		{"auto", paramsSourceAuto},
		{" grpc ", paramsSourceGRPC},
		{"chain", paramsSourceChain},
		{"bogus", paramsSourceAuto},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Setenv("DEVSHARDD_PARAMS_SOURCE", tc.in)
			assert.Equal(t, tc.want, paramsSourceFromEnv())
		})
	}
}

// ---------------------------------------------------------------------------
// newParamsProvider — adaptive (default)
// ---------------------------------------------------------------------------

func TestNewParamsProvider_Adaptive_GRPCAvailable(t *testing.T) {
	clearSourceEnv(t)
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(10, 2, "raw")),
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(10, 2, "raw")),
	)
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))
	chainFake := newChainFake(t, true, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceAdaptive, res.Source)
	require.NotNil(t, res.ActiveSource)

	waitRuntimeSnapshot(t, res.Provider.(runtimeconfig.Provider), 10)
	assert.Equal(t, runtimeconfig.SourceActiveGRPC, res.ActiveSource())
}

func TestNewParamsProvider_Adaptive_UnimplementedFallsToChain(t *testing.T) {
	clearSourceEnv(t)
	nm := &errOnlyNMClient{err: status.Error(codes.Unimplemented, "not implemented")}
	mlClient := mlnode.ClientForTest(nm)
	chainFake := newChainFake(t, true, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceAdaptive, res.Source)

	waitActiveSource(t, res.ActiveSource, runtimeconfig.SourceActiveChain)
	snap := res.Provider.Snapshot()
	assert.Equal(t, uint64(5), snap.CurrentEpochID)
	assert.Equal(t, int64(500), snap.ParamsBlockHeight)
}

func TestNewParamsProvider_Adaptive_OutageAndRecovery_ActiveSource(t *testing.T) {
	// End-to-end via newParamsProvider: old dapi (Unimplemented) → chain, then
	// dapi upgrade (reprobe) → gRPC. Stale-window failover is covered in
	// devshard/runtimeconfig adaptive_provider_test.go with a fake clock.
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_GRPC_REPROBE_SECONDS", "1")
	t.Setenv("DEVSHARDD_PARAMS_GRPC_FAILBACK_PROBES", "2")

	ok := func() (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{
			Config: runtimeconfig.TestRuntimeConfigProto(30, 2, "raw"),
		}, nil
	}
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "not implemented")
			},
			ok, ok,
			ok,
		},
	}
	mlClient := mlnode.ClientForTest(client)
	chainFake := newChainFake(t, true, 7)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, res.ActiveSource)
	assert.Equal(t, paramsSourceAdaptive, res.Source)

	waitActiveSource(t, res.ActiveSource, runtimeconfig.SourceActiveChain, 5*time.Second)
	assert.Equal(t, uint64(7), res.Provider.Snapshot().CurrentEpochID)

	time.Sleep(2500 * time.Millisecond)
	waitActiveSource(t, res.ActiveSource, runtimeconfig.SourceActiveGRPC, 10*time.Second)
	waitRuntimeSnapshot(t, res.Provider.(runtimeconfig.Provider), 30)
}

func TestNewParamsProvider_Adaptive_TransientGRPCStillTriesLongPoll(t *testing.T) {
	clearSourceEnv(t)
	nm := &errOnlyNMClient{err: status.Error(codes.Unavailable, "dapi restarting")}
	mlClient := mlnode.ClientForTest(nm)
	chainFake := newChainFake(t, true, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceAdaptive, res.Source)
	// Boot probe is Unavailable → supervisor starts on gRPC; chain not primary yet.
	assert.Equal(t, runtimeconfig.SourceActiveGRPC, res.ActiveSource())
}

func TestNewParamsProvider_DeprecatedGRPC_UsesAdaptive(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "grpc")

	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(8, 1, "raw")),
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(8, 1, "raw")),
	)
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))
	chainFake := newChainFake(t, true, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceAdaptive, res.Source)
	waitRuntimeSnapshot(t, res.Provider.(runtimeconfig.Provider), 8)
}

func TestNewParamsProvider_EnvOverride_Chain(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "chain")

	chainFake := newChainFake(t, false, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, nil, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceChain, res.Source)
	assert.Nil(t, res.ActiveSource)
	assert.False(t, res.Provider.Snapshot().DevshardRequestsEnabled)
}

func TestNewParamsProvider_Adaptive_RequiresRecorder(t *testing.T) {
	clearSourceEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, nil, mlnode.ClientForTest(&errOnlyNMClient{}), nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InferenceQueryClientProvider")
}

func TestNewParamsProvider_Adaptive_RequiresMLClient(t *testing.T) {
	clearSourceEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, newChainFake(t, true, 1), nil, nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NodeManager")
}

func TestNewParamsProvider_Adaptive_RecordsAvailabilityOnApply(t *testing.T) {
	clearSourceEnv(t)
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(5, 1, "raw")),
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(5, 1, "raw")),
	)
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))
	chainFake := newChainFake(t, true, 1)
	tracker := devshardpkg.NewAvailabilityTracker(false, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, chainFake, mlClient, tracker, slog.Default())
	require.NoError(t, err)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		avail := tracker.CurrentAvailability()
		if avail.Enabled && avail.EpochID == 1 && avail.Time > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: availability=%+v", tracker.CurrentAvailability())
}

func TestNewParamsProvider_Chain_RecordsAvailabilityOnApply(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "chain")

	chainFake := newChainFake(t, true, 9)
	tracker := devshardpkg.NewAvailabilityTracker(false, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, chainFake, nil, tracker, slog.Default())
	require.NoError(t, err)

	avail := tracker.CurrentAvailability()
	assert.True(t, avail.Enabled)
	assert.Equal(t, uint64(9), avail.EpochID)
}

func TestDevshardd_NoLegacyChainParamsProviderTickerInMain(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	mainPath := filepath.Join(filepath.Dir(filename), "main.go")
	body, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	s := string(body)

	assert.NotContains(t, s, "chainParamsProvider")
	assert.NotContains(t, s, "newChainParamsProvider")
	assert.False(t, strings.Contains(s, "time.NewTicker(60 * time.Second)"))
	assert.NotContains(t, s, "QueryEpochInfoRequest")
}
