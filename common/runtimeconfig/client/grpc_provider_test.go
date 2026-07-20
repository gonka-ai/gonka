package client

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/nodemanager/gen"
	"common/runtimeconfig/client/testserver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func zeroRetryFloor() *time.Duration {
	d := time.Duration(0)
	return &d
}

func testConfig(t *testing.T, client gen.NodeManagerClient, opts ...func(*Config)) Config {
	t.Helper()
	cfg := Config{
		Client:              client,
		ServerMaxWait:       200 * time.Millisecond,
		ClientDeadlineSlack: 50 * time.Millisecond,
		ErrorBackoffMin:     10 * time.Millisecond,
		ErrorBackoffMax:     40 * time.Millisecond,
		UnchangedRetryFloor: zeroRetryFloor(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func waitForHeight(t *testing.T, p Provider, height int64) Snapshot {
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
	return Snapshot{}
}

func TestGRPCProvider_InitialFetchSuccess(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(TestRuntimeConfigProto(100, 1, "raw")))
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)

	snap := waitForHeight(t, p, 100)
	assert.Equal(t, int64(100), snap.ParamsBlockHeight)
	assert.Equal(t, uint64(1), snap.CurrentEpochID)
	assert.Equal(t, "raw", snap.LogprobsMode)
}

func TestGRPCProvider_LongPoll_AppliesOnChange(t *testing.T) {
	srv := testserver.New()
	cfg1 := TestRuntimeConfigProto(100, 1, "raw")
	cfg2 := TestRuntimeConfigProto(101, 2, "processed")
	srv.SetHandlers(
		testserver.FullConfig(cfg1),
		testserver.FullConfig(cfg2),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 100)

	snap := waitForHeight(t, p, 101)
	assert.Equal(t, uint64(2), snap.CurrentEpochID)
	assert.Equal(t, "processed", snap.LogprobsMode)
}

func TestGRPCProvider_LongPoll_UnchangedKeepsSnapshot(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(100, 1, "raw")),
		testserver.Unchanged(),
		testserver.Unchanged(),
	)
	client := testserver.Dial(t, srv)

	var epochFires atomic.Int32
	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 100)

	cancelEpoch := p.OnEpochChange(func(_, _ uint64) { epochFires.Add(1) })
	defer cancelEpoch()

	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int64(100), p.Snapshot().ParamsBlockHeight)
	assert.Equal(t, int32(0), epochFires.Load())
}

func TestGRPCProvider_LongPoll_NextRequestUsesNewHeight(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(100, 1, "raw")),
		testserver.Unchanged(),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 100)

	deadline := time.Now().Add(2 * time.Second)
	for len(srv.Calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	calls := srv.Calls()
	require.GreaterOrEqual(t, len(calls), 2)
	assert.Equal(t, int64(0), calls[0].GetClientParamsBlockHeight())
	assert.Equal(t, int64(100), calls[1].GetClientParamsBlockHeight())
}

func TestGRPCProvider_ServerNotSyncedPausesBetweenPolls(t *testing.T) {
	srv := testserver.New()
	// Repeat full-config-at-height-0: a single handler would be consumed and the fake
	// server would return immediate unchanged (UnchangedRetryFloor=0 in testConfig),
	// which is not what dapi does while params are not yet published.
	cfg0 := TestRuntimeConfigProto(0, 0, "raw")
	handlers := make([]testserver.Handler, 8)
	for i := range handlers {
		handlers[i] = testserver.FullConfig(cfg0)
	}
	srv.SetHandlers(handlers...)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	cfg := testConfig(t, client, func(c *Config) {
		c.ErrorBackoffMin = 50 * time.Millisecond
	})
	_, err := New(ctx, cfg)
	require.NoError(t, err)

	time.Sleep(30 * time.Millisecond)
	require.Equal(t, 1, len(srv.Calls()), "expected one initial_fetch while server height is 0, not a busy loop")

	time.Sleep(60 * time.Millisecond)
	calls := len(srv.Calls())
	assert.GreaterOrEqual(t, calls, 2, "second poll after server-not-synced backoff")
	assert.LessOrEqual(t, calls, 3, "expected backoff between polls while height stays 0, got %d calls", calls)
}

func TestGRPCProvider_LongPoll_SendsConfiguredMaxWait(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw")))
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	cfg := testConfig(t, client, func(c *Config) { c.ServerMaxWait = 45 * time.Second })
	_, err := New(ctx, cfg)
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for len(srv.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEmpty(t, srv.Calls())
	assert.Equal(t, int32(45), srv.Calls()[0].GetMaxWaitSeconds())
}

func TestGRPCProvider_LongPoll_ClientDeadlineIsServerMaxWaitPlusSlack(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw")))

	deadlineCh := make(chan time.Time, 1)
	callStartCh := make(chan time.Time, 1)
	rec := &recordingClient{
		inner: testserver.Dial(t, srv),
		onCall: func(ctx context.Context) {
			select {
			case callStartCh <- time.Now():
			default:
			}
			if d, ok := ctx.Deadline(); ok {
				select {
				case deadlineCh <- d:
				default:
				}
			}
		},
	}

	ctx := testContext(t)
	serverWait := 200 * time.Millisecond
	slack := 50 * time.Millisecond
	_, err := New(ctx, testConfig(t, rec, func(c *Config) {
		c.ServerMaxWait = serverWait
		c.ClientDeadlineSlack = slack
	}))
	require.NoError(t, err)

	var gotDeadline, callStart time.Time
	select {
	case callStart = <-callStartCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for GetRuntimeConfig call")
	}
	select {
	case gotDeadline = <-deadlineCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for call deadline")
	}

	assert.InDelta(t, float64(serverWait+slack), float64(gotDeadline.Sub(callStart)), float64(100*time.Millisecond))
}

type recordingClient struct {
	inner  gen.NodeManagerClient
	onCall func(context.Context)
}

func (r *recordingClient) GetRuntimeConfig(ctx context.Context, in *gen.GetRuntimeConfigRequest, opts ...grpc.CallOption) (*gen.GetRuntimeConfigResponse, error) {
	if r.onCall != nil {
		r.onCall(ctx)
	}
	return r.inner.GetRuntimeConfig(ctx, in, opts...)
}

func (r *recordingClient) AcquireMLNode(ctx context.Context, in *gen.AcquireMLNodeRequest, opts ...grpc.CallOption) (*gen.AcquireMLNodeResponse, error) {
	return r.inner.AcquireMLNode(ctx, in, opts...)
}

func (r *recordingClient) ReleaseMLNode(ctx context.Context, in *gen.ReleaseMLNodeRequest, opts ...grpc.CallOption) (*gen.ReleaseMLNodeResponse, error) {
	return r.inner.ReleaseMLNode(ctx, in, opts...)
}

func TestGRPCProvider_LongPoll_ServerTimeoutDoesNotApply(t *testing.T) {
	srv := testserver.New()
	handlers := []testserver.Handler{testserver.FullConfig(TestRuntimeConfigProto(100, 1, "raw"))}
	for i := 0; i < 10; i++ {
		handlers = append(handlers, testserver.Unchanged())
	}
	srv.SetHandlers(handlers...)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 100)

	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int64(100), p.Snapshot().ParamsBlockHeight)
	assert.Equal(t, uint64(1), p.Snapshot().CurrentEpochID)
}

func TestGRPCProvider_Unimplemented_ExitsLoop(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.Error(status.Error(codes.Unimplemented, "method GetRuntimeConfig not implemented")),
		// Anything past the first call should never happen — assert via call count below.
		testserver.FullConfig(TestRuntimeConfigProto(99, 99, "raw")),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	_, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)

	// Wait for the loop to observe Unimplemented and exit.
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.Calls()) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(srv.Calls()), 1, "expected at least one probe call")

	// Sleep beyond max backoff to confirm the loop did not retry.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 1, len(srv.Calls()),
		"Unimplemented must be terminal — loop should exit after the first call, not retry")
}

func TestGRPCProvider_LongPoll_ErrorBackoff(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.Error(status.Error(codes.Unavailable, "down")),
		testserver.Error(status.Error(codes.Unavailable, "down")),
		testserver.Error(status.Error(codes.Unavailable, "down")),
		testserver.FullConfig(TestRuntimeConfigProto(50, 1, "raw")),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	cfg := testConfig(t, client)
	p, err := New(ctx, cfg)
	require.NoError(t, err)

	snap := waitForHeight(t, p, 50)
	assert.Equal(t, int64(50), snap.ParamsBlockHeight)
	assert.GreaterOrEqual(t, len(srv.Calls()), 4)
}

func TestGRPCProvider_BackCompat_OldServer_FastUnchangedThrottledByFloor(t *testing.T) {
	floor := 80 * time.Millisecond
	floorPtr := &floor
	srv := testserver.New()
	handlers := make([]testserver.Handler, 20)
	for i := range handlers {
		handlers[i] = testserver.Unchanged()
	}
	srv.SetHandlers(handlers...)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	_, err := New(ctx, testConfig(t, client, func(c *Config) {
		c.UnchangedRetryFloor = floorPtr
		c.ServerMaxWait = time.Second
	}))
	require.NoError(t, err)

	time.Sleep(5 * floor)
	calls := len(srv.Calls())
	// At most ~5 calls in 5*floor window (initial + spaced polls)
	assert.LessOrEqual(t, calls, 8, "calls=%d should be throttled by floor", calls)
	assert.GreaterOrEqual(t, calls, 2)
}

func TestGRPCProvider_BackCompat_OldServer_StillAppliesOnChange(t *testing.T) {
	srv := testserver.New()
	handlers := make([]testserver.Handler, 5)
	for i := range handlers {
		handlers[i] = testserver.Unchanged()
	}
	handlers = append(handlers, testserver.FullConfig(TestRuntimeConfigProto(200, 3, "raw")))
	srv.SetHandlers(handlers...)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client, func(c *Config) {
		f := 5 * time.Millisecond
		c.UnchangedRetryFloor = &f
	}))
	require.NoError(t, err)

	snap := waitForHeight(t, p, 200)
	assert.Equal(t, uint64(3), snap.CurrentEpochID)
}

func TestGRPCProvider_BackCompat_NewServer_NoFloorPenalty(t *testing.T) {
	serverWait := 120 * time.Millisecond
	srv := testserver.New()
	secondDone := make(chan struct{}, 1)
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(10, 1, "raw")),
		func(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
			resp, err := testserver.DelayedUnchanged(serverWait)(ctx, req)
			secondDone <- struct{}{}
			return resp, err
		},
	)
	client := testserver.Dial(t, srv)

	floor := 500 * time.Millisecond
	floorPtr := &floor
	ctx := testContext(t)
	start := time.Now()
	_, err := New(ctx, testConfig(t, client, func(c *Config) {
		c.ServerMaxWait = serverWait
		c.UnchangedRetryFloor = floorPtr
	}))
	require.NoError(t, err)

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second long-poll to complete")
	}
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, serverWait)
	assert.Less(t, elapsed, serverWait+floor, "should not add floor on top of server hold")
}

func TestGRPCProvider_BackCompat_SendsMaxWaitSeconds(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw")))
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	wait := 37 * time.Second
	_, err := New(ctx, testConfig(t, client, func(c *Config) { c.ServerMaxWait = wait }))
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for len(srv.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int32(37), srv.Calls()[0].GetMaxWaitSeconds())
}

func TestGRPCProvider_LongPoll_ContextCancelStopsLoop(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw")),
		srv.BlockNext(),
	)
	client := testserver.Dial(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for len(srv.Calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	callsAfter := len(srv.Calls())
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, callsAfter, len(srv.Calls()), "loop should stop after cancel")
}

func TestGRPCProvider_LongPoll_NoConcurrentCalls(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw")),
		srv.BlockNext(),
		testserver.Unchanged(),
		testserver.Unchanged(),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	_, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)

	deadline := time.Now().Add(2 * time.Second)
	for srv.MaxInFlight() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, int32(1), srv.MaxInFlight())
	srv.ReleaseBlocked()
}

func TestGRPCProvider_OnEpochChange_FiresOncePerTransition(t *testing.T) {
	var fires []struct{ old, new uint64 }
	var mu sync.Mutex

	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(10, 1, "a")),
		testserver.FullConfig(TestRuntimeConfigProto(11, 2, "a")),
		testserver.FullConfig(TestRuntimeConfigProto(12, 3, "a")),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	cancelListen := p.OnEpochChange(func(old, new uint64) {
		mu.Lock()
		fires = append(fires, struct{ old, new uint64 }{old, new})
		mu.Unlock()
	})
	defer cancelListen()

	waitForHeight(t, p, 12)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, fires, 2)
	assert.Equal(t, uint64(1), fires[0].old)
	assert.Equal(t, uint64(2), fires[0].new)
	assert.Equal(t, uint64(2), fires[1].old)
	assert.Equal(t, uint64(3), fires[1].new)
}

func TestGRPCProvider_OnEpochChange_NoFireOnInitialApply(t *testing.T) {
	var fires atomic.Int32
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(TestRuntimeConfigProto(5, 9, "raw")))
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	cancelListen := p.OnEpochChange(func(_, _ uint64) { fires.Add(1) })
	defer cancelListen()

	waitForHeight(t, p, 5)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), fires.Load())
}

func TestGRPCProvider_OnEpochChange_NoFireOnUnchanged(t *testing.T) {
	var fires atomic.Int32
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(1, 1, "raw")),
		testserver.Unchanged(),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	cancelListen := p.OnEpochChange(func(_, _ uint64) { fires.Add(1) })
	defer cancelListen()

	waitForHeight(t, p, 1)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(0), fires.Load())
}

func TestGRPCProvider_OnEpochChange_Cancel(t *testing.T) {
	var fires atomic.Int32
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(1, 1, "raw")),
		testserver.FullConfig(TestRuntimeConfigProto(2, 2, "raw")),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 1)

	cancelListen := p.OnEpochChange(func(_, _ uint64) { fires.Add(1) })
	cancelListen()

	waitForHeight(t, p, 2)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), fires.Load())
}

func TestGRPCProvider_OnEpochChange_PanicRecovered(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(1, 1, "raw")),
		testserver.FullConfig(TestRuntimeConfigProto(2, 2, "raw")),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)
	waitForHeight(t, p, 1)

	p.OnEpochChange(func(_, _ uint64) { panic("boom") })

	waitForHeight(t, p, 2)
	assert.Equal(t, int64(2), p.Snapshot().ParamsBlockHeight)
}

func TestGRPCProvider_Availability_RecordsServedAt(t *testing.T) {
	tracker := newRecordingSink(false, 0, 0)
	srv := testserver.New()
	srv.SetHandlers(
		testserver.FullConfig(TestRuntimeConfigProto(10, 4, "raw")),
		testserver.Unchanged(),
	)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client, func(c *Config) { c.Availability = tracker }))
	require.NoError(t, err)
	waitForHeight(t, p, 10)

	avail := tracker.view()
	assert.True(t, avail.Enabled)
	assert.Equal(t, uint64(4), avail.EpochID)
	assert.Equal(t, int64(1_700_000_000), avail.Time)

	before := avail
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, tracker.view())
}

func TestGRPCProvider_RaceSnapshot(t *testing.T) {
	srv := testserver.New()
	handlers := []testserver.Handler{testserver.FullConfig(TestRuntimeConfigProto(1, 0, "raw"))}
	for h := int64(2); h <= 50; h++ {
		height := h
		handlers = append(handlers, testserver.FullConfig(TestRuntimeConfigProto(height, uint64(height), "raw")))
	}
	srv.SetHandlers(handlers...)
	client := testserver.Dial(t, srv)

	ctx := testContext(t)
	p, err := New(ctx, testConfig(t, client))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Snapshot()
		}()
	}
	wg.Wait()
}

func TestNextBackoff(t *testing.T) {
	// nextBackoff returns prev/2 + jitter where jitter ∈ [0, prev).
	b := nextBackoff(0, time.Second, 10*time.Second)
	assert.GreaterOrEqual(t, b, 500*time.Millisecond)
	assert.LessOrEqual(t, b, 1500*time.Millisecond)

	b2 := nextBackoff(time.Second, time.Second, 10*time.Second)
	assert.GreaterOrEqual(t, b2, time.Second)
	assert.LessOrEqual(t, b2, 3*time.Second)
}
