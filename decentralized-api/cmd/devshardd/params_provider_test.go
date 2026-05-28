package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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

// fakeQueryClient is a partial chaintypes.QueryClient that only implements
// Params + EpochInfo (the methods ChainParamsFetcher uses).
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

// errOnlyNMClient is a NodeManagerClient that returns the same gRPC error on
// every GetRuntimeConfig call. Used to stage the auto-probe outcome.
type errOnlyNMClient struct {
	gen.NodeManagerClient
	err   error
	calls atomic.Int32
}

func (c *errOnlyNMClient) GetRuntimeConfig(ctx context.Context, in *gen.GetRuntimeConfigRequest, opts ...grpc.CallOption) (*gen.GetRuntimeConfigResponse, error) {
	c.calls.Add(1)
	return nil, c.err
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

func clearSourceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS", "")
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

func TestRuntimeConfigSettingsFromEnv_Override(t *testing.T) {
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "45")
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS", "7")

	maxWait, slack := runtimeConfigSettingsFromEnv()
	assert.Equal(t, 45*time.Second, maxWait)
	assert.Equal(t, 7*time.Second, slack)
}

func TestChainParamsSettingsFromEnv_Defaults(t *testing.T) {
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS", "")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS", "")
	refresh, initial := chainParamsSettingsFromEnv()
	assert.Equal(t, 60*time.Second, refresh)
	assert.Equal(t, 5*time.Second, initial)
}

func TestChainParamsSettingsFromEnv_Override(t *testing.T) {
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS", "10")
	t.Setenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS", "2")
	refresh, initial := chainParamsSettingsFromEnv()
	assert.Equal(t, 10*time.Second, refresh)
	assert.Equal(t, 2*time.Second, initial)
}

func TestParamsSourceFromEnv(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", paramsSourceAuto},
		{"auto", paramsSourceAuto},
		{"AUTO", paramsSourceAuto},
		{" grpc ", paramsSourceGRPC},
		{"chain", paramsSourceChain},
		{"bogus", paramsSourceAuto}, // invalid → auto with warn log
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Setenv("DEVSHARDD_PARAMS_SOURCE", tc.in)
			assert.Equal(t, tc.want, paramsSourceFromEnv())
		})
	}
}

// ---------------------------------------------------------------------------
// newParamsProvider — selector behavior
// ---------------------------------------------------------------------------

func TestNewParamsProvider_Auto_ProbeSucceeds_PicksGRPC(t *testing.T) {
	clearSourceEnv(t)
	srv := testserver.New()
	// First handler answers the probe; second handler feeds the long-poll
	// provider's first real poll. Subsequent polls fall through to the
	// testserver's "Unchanged" default which keeps the snapshot stable.
	srv.SetHandlers(
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(10, 2, "raw")),
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(10, 2, "raw")),
	)
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, nil, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceGRPC, res.Source)
	require.NotNil(t, res.RegisterEpochPrune)

	snap := waitRuntimeSnapshot(t, res.Provider.(runtimeconfig.Provider), 10)
	assert.Equal(t, uint64(2), snap.CurrentEpochID)
}

func TestNewParamsProvider_Auto_ProbeUnimplemented_PicksChain(t *testing.T) {
	clearSourceEnv(t)
	nm := &errOnlyNMClient{err: status.Error(codes.Unimplemented, "method GetRuntimeConfig not implemented")}
	mlClient := mlnode.ClientForTest(nm)

	chainFake := newChainFake(t, true, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceChain, res.Source)

	// chainProvider's synchronous initial refresh should have populated the snapshot.
	snap := res.Provider.Snapshot()
	assert.Equal(t, uint64(5), snap.CurrentEpochID)
	assert.Equal(t, int64(500), snap.ParamsBlockHeight)
	assert.True(t, snap.DevshardRequestsEnabled)
	assert.Equal(t, int32(1), nm.calls.Load(), "probe should fire exactly once")
}

func TestNewParamsProvider_Auto_ProbeTransientError_PicksGRPC(t *testing.T) {
	// Unavailable / DeadlineExceeded are transient; the selector keeps the
	// long-poll path so devshardd self-heals when dapi comes back.
	clearSourceEnv(t)
	nm := &errOnlyNMClient{err: status.Error(codes.Unavailable, "dapi restarting")}
	mlClient := mlnode.ClientForTest(nm)

	chainFake := newChainFake(t, true, 1) // present but should not be used

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, chainFake, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceGRPC, res.Source, "transient errors must not trigger chain fallback")
}

func TestNewParamsProvider_EnvOverride_Chain(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "chain")

	chainFake := newChainFake(t, false, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// mlClient is nil to prove the selector never touches NodeManager when
	// forced to chain.
	res, err := newParamsProvider(ctx, chainFake, nil, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceChain, res.Source)
	assert.False(t, res.Provider.Snapshot().DevshardRequestsEnabled)
	assert.Equal(t, uint64(3), res.Provider.Snapshot().CurrentEpochID)
}

func TestNewParamsProvider_EnvOverride_GRPC(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "grpc")

	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(8, 1, "raw")))
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, nil, mlClient, nil, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, paramsSourceGRPC, res.Source)
	waitRuntimeSnapshot(t, res.Provider.(runtimeconfig.Provider), 8)
}

func TestNewParamsProvider_EnvOverride_Chain_RequiresRecorder(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "chain")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, nil, nil, nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InferenceQueryClientProvider")
}

func TestNewParamsProvider_EnvOverride_GRPC_RequiresMLClient(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "grpc")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, nil, nil, nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NodeManager")
}

// ---------------------------------------------------------------------------
// Availability propagation through the chosen provider
// ---------------------------------------------------------------------------

func TestNewParamsProvider_GRPC_RecordsAvailabilityOnApply(t *testing.T) {
	clearSourceEnv(t)
	srv := testserver.New()
	// One handler for the auto-mode probe + one for the provider's first poll.
	srv.SetHandlers(
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(5, 1, "raw")),
		testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(5, 1, "raw")),
	)
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))

	tracker := devshardpkg.NewAvailabilityTracker(false, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, nil, mlClient, tracker, slog.Default())
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
	assert.Greater(t, avail.Time, int64(0))
}

func TestNewParamsProvider_Chain_FetcherErrorNonFatal(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv("DEVSHARDD_PARAMS_SOURCE", "chain")

	bad := fakeQueryProvider{qc: &fakeQueryClient{
		paramsFn: func(context.Context, *chaintypes.QueryParamsRequest, ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return nil, errors.New("transient chain failure")
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := newParamsProvider(ctx, bad, nil, nil, slog.Default())
	require.NoError(t, err, "chain provider construction must tolerate initial fetch failure")
	assert.Equal(t, paramsSourceChain, res.Source)
	// Defaults snapshot — LogprobsMode is filled by Config.applyDefaults.
	assert.Equal(t, int64(0), res.Provider.Snapshot().ParamsBlockHeight)
}

// ---------------------------------------------------------------------------
// Architecture regression guard
// ---------------------------------------------------------------------------

// TestDevshardd_NoLegacyChainParamsProviderTickerInMain guards against
// accidentally re-introducing the gm/microrelease "60s ticker inside main.go"
// pattern. The chain-poll fallback lives in runtimeconfig.chainProvider; main
// only owns the AvailabilityTracker seed (a one-shot Params query, which is
// intentionally allowed).
func TestDevshardd_NoLegacyChainParamsProviderTickerInMain(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	mainPath := filepath.Join(filepath.Dir(filename), "main.go")
	body, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	s := string(body)

	assert.NotContains(t, s, "chainParamsProvider")
	assert.NotContains(t, s, "newChainParamsProvider")
	assert.False(t, strings.Contains(s, "time.NewTicker(60 * time.Second)"),
		"60s chain params ticker must live in runtimeconfig.chainProvider, not main.go")
	// QueryParamsRequest IS allowed (seed). QueryEpochInfoRequest is NOT
	// allowed in main — that one stays inside ChainParamsFetcher.
	assert.NotContains(t, s, "QueryEpochInfoRequest",
		"epoch queries must stay inside ChainParamsFetcher; main only seeds availability")
}
