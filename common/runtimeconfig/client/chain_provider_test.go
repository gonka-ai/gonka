package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFetcher is a scriptable ChainParamsFetcher for table-driven tests. Each
// call consumes one response from the queue; once exhausted the last response
// repeats forever so the background loop can keep polling.
type fakeFetcher struct {
	mu        sync.Mutex
	responses []fakeFetchResponse
	calls     int32
}

type fakeFetchResponse struct {
	snap Snapshot
	err  error
}

func (f *fakeFetcher) FetchSnapshot(ctx context.Context) (Snapshot, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return Snapshot{}, errors.New("fakeFetcher: no responses scripted")
	}
	r := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	return r.snap, r.err
}

func (f *fakeFetcher) Calls() int32 { return atomic.LoadInt32(&f.calls) }

func chainTestConfig(t *testing.T, f *fakeFetcher, opts ...func(*ChainConfig)) ChainConfig {
	t.Helper()
	cfg := ChainConfig{
		Fetcher:         f,
		RefreshInterval: 25 * time.Millisecond,
		InitialTimeout:  100 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func waitChainHeight(t *testing.T, p Provider, height int64) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := p.Snapshot()
		if s.ParamsBlockHeight >= height {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for chain snapshot params_block_height >= %d (got %d)", height, p.Snapshot().ParamsBlockHeight)
	return Snapshot{}
}

func TestChainProvider_InitialFetchPopulatesSnapshot_v0_2_13Chain(t *testing.T) {
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{
				ParamsBlockHeight:                 42,
				CurrentEpochID:                    7,
				LogprobsMode:                      "raw",
				DevshardRequestsEnabled:           true,
				MaxNonce:                          1500,
				RefusalTimeout:                    60,
				ExecutionTimeout:                  1200,
				ValidationRate:                    5000,
				VoteThresholdFactor: 50,
			}},
		},
	}

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f))
	require.NoError(t, err)

	snap := p.Snapshot()
	assert.Equal(t, int64(42), snap.ParamsBlockHeight)
	assert.Equal(t, uint64(7), snap.CurrentEpochID)
	assert.Equal(t, "raw", snap.LogprobsMode)
	assert.True(t, snap.DevshardRequestsEnabled)
	assert.Equal(t, uint32(1500), snap.MaxNonce)
	assert.Equal(t, int64(60), snap.RefusalTimeout)
	assert.Equal(t, int64(1200), snap.ExecutionTimeout)
	assert.Equal(t, uint32(5000), snap.ValidationRate)
	assert.Equal(t, uint32(50), snap.VoteThresholdFactor)
	assert.False(t, snap.ServedAt.IsZero(), "ServedAt should be stamped at apply time")
}

func TestChainProvider_v0_2_12Chain_ZeroNewFieldsPreserveCompiledDefaults(t *testing.T) {
	// On a v0.2.12 chain the new DevshardEscrowParams fields decode as zero.
	// The chain provider must store those zeros so downstream
	// ApplyLiveSessionParams "if > 0 override" semantics fall back to compiled
	// defaults instead of nuking SessionConfig.
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{
				ParamsBlockHeight:       10,
				CurrentEpochID:          3,
				LogprobsMode:            "processed",
				DevshardRequestsEnabled: true,
				MaxNonce:                0,
				// All v0.2.13 fields zero (not present on chain).
			}},
		},
	}

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f))
	require.NoError(t, err)
	snap := p.Snapshot()
	assert.Equal(t, int64(0), snap.RefusalTimeout)
	assert.Equal(t, int64(0), snap.ExecutionTimeout)
	assert.Equal(t, uint32(0), snap.ValidationRate)
	assert.Equal(t, uint32(0), snap.VoteThresholdFactor)
	assert.Equal(t, uint32(0), snap.MaxNonce)
}

func TestChainProvider_AvailabilityTrackerReceivesUpdates(t *testing.T) {
	tracker := newRecordingSink(false, 0, 0)
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{
				ParamsBlockHeight:       5,
				CurrentEpochID:          2,
				DevshardRequestsEnabled: true,
				ServedAt:                time.Unix(1_700_000_000, 0),
			}},
		},
	}

	ctx := testContext(t)
	_, err := NewChain(ctx, chainTestConfig(t, f, func(c *ChainConfig) { c.Availability = tracker }))
	require.NoError(t, err)

	avail := tracker.view()
	assert.True(t, avail.Enabled, "tracker should reflect chain DevshardRequestsEnabled")
	assert.Equal(t, uint64(2), avail.EpochID)
	assert.Equal(t, int64(1_700_000_000), avail.Time)
}

func TestChainProvider_InitialFetchErrorKeepsDefaultsAndRetries(t *testing.T) {
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{err: errors.New("temporary chain RPC outage")},
			{snap: Snapshot{
				ParamsBlockHeight:       99,
				CurrentEpochID:          1,
				DevshardRequestsEnabled: true,
				LogprobsMode:            "raw",
			}},
		},
	}

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f, func(c *ChainConfig) {
		c.RefreshInterval = 20 * time.Millisecond
	}))
	require.NoError(t, err)

	require.Equal(t, int64(0), p.Snapshot().ParamsBlockHeight,
		"initial failed fetch must leave Snapshot at Defaults (not panic)")

	waitChainHeight(t, p, 99)
}

func TestChainProvider_OnEpochChangeFiresOnTransition(t *testing.T) {
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 10, CurrentEpochID: 1, DevshardRequestsEnabled: true}},
			{snap: Snapshot{ParamsBlockHeight: 11, CurrentEpochID: 2, DevshardRequestsEnabled: true}},
			{snap: Snapshot{ParamsBlockHeight: 12, CurrentEpochID: 3, DevshardRequestsEnabled: true}},
		},
	}

	var fires []struct{ old, new uint64 }
	var mu sync.Mutex

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f, func(c *ChainConfig) {
		c.RefreshInterval = 10 * time.Millisecond
	}))
	require.NoError(t, err)
	cancelListen := p.OnEpochChange(func(old, new uint64) {
		mu.Lock()
		fires = append(fires, struct{ old, new uint64 }{old, new})
		mu.Unlock()
	})
	defer cancelListen()

	waitChainHeight(t, p, 12)
	// give listener goroutines a chance to land
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, fires, 2)
	assert.Equal(t, uint64(1), fires[0].old)
	assert.Equal(t, uint64(2), fires[0].new)
	assert.Equal(t, uint64(2), fires[1].old)
	assert.Equal(t, uint64(3), fires[1].new)
}

func TestChainProvider_OnEpochChangeDoesNotFireOnInitialApply(t *testing.T) {
	var fires atomic.Int32
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 5, CurrentEpochID: 9, DevshardRequestsEnabled: true}},
		},
	}

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f))
	require.NoError(t, err)
	cancelListen := p.OnEpochChange(func(_, _ uint64) { fires.Add(1) })
	defer cancelListen()

	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(0), fires.Load(),
		"initial apply must not fire listeners (gated by prev.ParamsBlockHeight > 0)")
}

func TestChainProvider_ContextCancelStopsLoop(t *testing.T) {
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{ParamsBlockHeight: 1, CurrentEpochID: 1, DevshardRequestsEnabled: true}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewChain(ctx, chainTestConfig(t, f, func(c *ChainConfig) {
		c.RefreshInterval = 10 * time.Millisecond
	}))
	require.NoError(t, err)

	// Let the loop perform a few refreshes.
	time.Sleep(50 * time.Millisecond)
	before := f.Calls()
	cancel()
	// Allow any in-flight refresh to drain.
	time.Sleep(80 * time.Millisecond)
	after := f.Calls()
	// At most one more call may complete after cancel (the in-flight one).
	assert.LessOrEqual(t, after-before, int32(1), "loop should stop after cancel; before=%d after=%d", before, after)
}

func TestChainProvider_LogprobsModeFallsBackToDefault(t *testing.T) {
	f := &fakeFetcher{
		responses: []fakeFetchResponse{
			{snap: Snapshot{
				ParamsBlockHeight:       1,
				CurrentEpochID:          1,
				LogprobsMode:            "", // chain returned empty
				DevshardRequestsEnabled: true,
			}},
		},
	}

	ctx := testContext(t)
	p, err := NewChain(ctx, chainTestConfig(t, f))
	require.NoError(t, err)

	assert.Equal(t, defaultLogprobsMode, p.Snapshot().LogprobsMode)
}

func TestChainProvider_RequiresFetcher(t *testing.T) {
	ctx := testContext(t)
	_, err := NewChain(ctx, ChainConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Fetcher")
}
