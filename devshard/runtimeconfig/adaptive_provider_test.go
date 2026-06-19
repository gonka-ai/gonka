package runtimeconfig

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/nodemanager/gen"
	"devshard/runtimeconfig/testserver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClock drives supervisor timers deterministically in tests.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		deadline: c.now.Add(d),
		ch:       make(chan time.Time, 1),
	}
	c.timers = append(c.timers, t)
	return t.ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var pending []*fakeTimer
	for _, t := range c.timers {
		if !t.deadline.After(c.now) {
			select {
			case t.ch <- c.now:
			default:
			}
		} else {
			pending = append(pending, t)
		}
	}
	c.timers = pending
	c.mu.Unlock()
}

// scriptNMClient returns scripted errors or configs per call index.
type scriptNMClient struct {
	gen.NodeManagerClient
	mu       sync.Mutex
	handlers []func() (*gen.GetRuntimeConfigResponse, error)
	calls    atomic.Int32
}

func (c *scriptNMClient) GetRuntimeConfig(ctx context.Context, in *gen.GetRuntimeConfigRequest, opts ...grpc.CallOption) (*gen.GetRuntimeConfigResponse, error) {
	c.mu.Lock()
	idx := int(c.calls.Add(1) - 1)
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
	c.mu.Unlock()
	return fn()
}

func adaptiveTestConfig(t *testing.T, clock *fakeClock, client gen.NodeManagerClient, fetcher *fakeFetcher) AdaptiveConfig {
	t.Helper()
	floor := time.Duration(0)
	return AdaptiveConfig{
		GRPC: Config{
			Client:              client,
			ServerMaxWait:       10 * time.Millisecond,
			ClientDeadlineSlack: 10 * time.Millisecond,
			ErrorBackoffMin:     5 * time.Millisecond,
			ErrorBackoffMax:     15 * time.Millisecond,
			UnchangedRetryFloor: &floor,
			Clock:               clock,
		},
		Chain: ChainConfig{
			Fetcher:         fetcher,
			RefreshInterval: 20 * time.Millisecond,
			InitialTimeout:  50 * time.Millisecond,
			Clock:           clock,
		},
		GRPCStale:          80 * time.Millisecond,
		GRPCReprobe:        30 * time.Millisecond,
		FailbackProbes:     2,
		ProbeTimeout:       50 * time.Millisecond,
		StaleCheckInterval: 10 * time.Millisecond,
		Log:                slog.Default(),
		Clock:              clock,
	}
}

func cleanupAdaptive(t *testing.T, cancel context.CancelFunc, p AdaptiveProvider) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() {
			p.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Log("adaptive provider shutdown timed out")
		}
	})
}

func waitActiveSource(t *testing.T, p AdaptiveProvider, want string, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if p.ActiveSource() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for active source %q (got %q)", want, p.ActiveSource())
}

func TestAdaptive_BootUnimplemented_StartsChain(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "not implemented")
			},
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 100, CurrentEpochID: 3, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	p, err := NewAdaptive(ctx, adaptiveTestConfig(t, clock, client, fetcher))
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitActiveSource(t, p, SourceActiveChain, 2*time.Second)
	assert.Equal(t, int64(100), p.Snapshot().ParamsBlockHeight)
}

func TestAdaptive_FailbackAfterDapiUpgrade(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	okResp := func() (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{
			Config: TestRuntimeConfigProto(50, 2, "raw"),
		}, nil
	}
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "not implemented")
			},
			okResp, okResp,
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 10, CurrentEpochID: 1, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, client, fetcher)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitActiveSource(t, p, SourceActiveChain, 2*time.Second)

	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)

	waitActiveSource(t, p, SourceActiveGRPC, 3*time.Second)
	waitForHeight(t, p, 50)
}

func TestAdaptive_FailoverOnGRPCStale(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			// Boot probe: RPC works (does not apply).
			func() (*gen.GetRuntimeConfigResponse, error) {
				return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
			},
			// First long-poll apply.
			func() (*gen.GetRuntimeConfigResponse, error) {
				return &gen.GetRuntimeConfigResponse{
					Config: TestRuntimeConfigProto(20, 1, "raw"),
				}, nil
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unavailable, "dapi down")
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unavailable, "dapi down")
			},
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 200, CurrentEpochID: 9, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, client, fetcher)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitForHeight(t, p, 20)
	waitActiveSource(t, p, SourceActiveGRPC, time.Second)

	clock.Advance(cfg.GRPCStale + cfg.StaleCheckInterval)
	time.Sleep(50 * time.Millisecond)

	waitActiveSource(t, p, SourceActiveChain, 3*time.Second)
	snap := waitChainHeight(t, p, 200)
	assert.Equal(t, uint64(9), snap.CurrentEpochID)
}

func TestAdaptive_UnimplementedMidRun_SwitchesChain(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return &gen.GetRuntimeConfigResponse{
					Config: TestRuntimeConfigProto(5, 1, "raw"),
				}, nil
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "gone")
			},
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 99, CurrentEpochID: 4, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	p, err := NewAdaptive(ctx, adaptiveTestConfig(t, clock, client, fetcher))
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitForHeight(t, p, 5)
	// Wait for second poll to return Unimplemented.
	waitActiveSource(t, p, SourceActiveChain, 3*time.Second)
}

func TestAdaptive_OnlyOneRunnerApplies(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(10, 1, "raw")),
		testserver.FullConfig(TestRuntimeConfigProto(11, 2, "raw")),
	)
	grpcClient := testserver.Dial(t, srv)

	clock := newFakeClock(time.Unix(0, 0))
	inner := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 500, CurrentEpochID: 8, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, grpcClient, inner)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitForHeight(t, p, 10)
	before := inner.Calls()

	clock.Advance(100 * time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	// gRPC active: chain fetcher should not be polled.
	assert.Equal(t, before, inner.Calls())
	assert.Equal(t, SourceActiveGRPC, p.ActiveSource())
}

func TestAdaptive_RoundTrip_GRPCChainGRPC(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	okAt50 := func() (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{
			Config: TestRuntimeConfigProto(50, 3, "raw"),
		}, nil
	}
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return &gen.GetRuntimeConfigResponse{
					Config: TestRuntimeConfigProto(20, 1, "raw"),
				}, nil
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unavailable, "down")
			},
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unavailable, "down")
			},
			okAt50, okAt50, okAt50,
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 200, CurrentEpochID: 9, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, client, fetcher)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitForHeight(t, p, 20)
	waitActiveSource(t, p, SourceActiveGRPC, time.Second)

	clock.Advance(cfg.GRPCStale + cfg.StaleCheckInterval)
	time.Sleep(30 * time.Millisecond)
	waitActiveSource(t, p, SourceActiveChain, 3*time.Second)

	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	waitActiveSource(t, p, SourceActiveGRPC, 3*time.Second)
	waitForHeight(t, p, 50)
}

func TestAdaptive_OnEpochChange_FiresOnceOnChainTransition(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "not implemented")
			},
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 100, CurrentEpochID: 1, LogprobsMode: "raw"}},
			{snap: Snapshot{ParamsBlockHeight: 200, CurrentEpochID: 2, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, client, fetcher)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	var transitions []struct{ old, new uint64 }
	var mu sync.Mutex
	unsub := p.OnEpochChange(func(oldE, newE uint64) {
		mu.Lock()
		transitions = append(transitions, struct{ old, new uint64 }{oldE, newE})
		mu.Unlock()
	})
	defer unsub()

	waitActiveSource(t, p, SourceActiveChain, 2*time.Second)
	waitChainHeight(t, p, 100)

	clock.Advance(cfg.Chain.RefreshInterval)
	time.Sleep(30 * time.Millisecond)
	waitChainHeight(t, p, 200)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, transitions, 1)
	assert.Equal(t, uint64(1), transitions[0].old)
	assert.Equal(t, uint64(2), transitions[0].new)
}

func TestAdaptive_FailbackHysteresis_NeedsConsecutiveProbes(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	ok := func() (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
	}
	fail := func() (*gen.GetRuntimeConfigResponse, error) {
		return nil, status.Error(codes.Unavailable, "nope")
	}
	client := &scriptNMClient{
		handlers: []func() (*gen.GetRuntimeConfigResponse, error){
			func() (*gen.GetRuntimeConfigResponse, error) {
				return nil, status.Error(codes.Unimplemented, "not implemented")
			},
			ok,  // reprobe 1 — streak 1, still chain
			fail, // reprobe 2 — reset streak
			ok, ok, // reprobes 3–4 — failback to gRPC
		},
	}
	fetcher := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 10, CurrentEpochID: 1, LogprobsMode: "raw"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := adaptiveTestConfig(t, clock, client, fetcher)
	p, err := NewAdaptive(ctx, cfg)
	require.NoError(t, err)
	cleanupAdaptive(t, cancel, p)

	waitActiveSource(t, p, SourceActiveChain, 2*time.Second)

	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, SourceActiveChain, p.ActiveSource(), "one healthy probe must not fail back")

	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, SourceActiveChain, p.ActiveSource(), "failed reprobe must reset streak")

	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	clock.Advance(cfg.GRPCReprobe)
	time.Sleep(20 * time.Millisecond)
	waitActiveSource(t, p, SourceActiveGRPC, 3*time.Second)
}
