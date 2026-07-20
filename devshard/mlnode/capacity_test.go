package mlnode_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/mlnode"
	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type fakeCapacityServer struct {
	gen.UnimplementedNodeManagerServer
	mu      sync.Mutex
	handler func(context.Context, *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error)
	calls   atomic.Int32
}

func (s *fakeCapacityServer) ListNodeCapacity(ctx context.Context, req *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error) {
	s.calls.Add(1)
	s.mu.Lock()
	h := s.handler
	s.mu.Unlock()
	if h != nil {
		return h(ctx, req)
	}
	return &gen.ListNodeCapacityResponse{}, nil
}

func (s *fakeCapacityServer) setHandler(h func(context.Context, *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

func dialCapacity(t *testing.T, srv *fakeCapacityServer) gen.NodeManagerClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(func() { gs.Stop() })
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewNodeManagerClient(conn)
}

func TestNodeCapacityCache_PollUpsertAndRetain(t *testing.T) {
	srv := &fakeCapacityServer{}
	srv.setHandler(func(context.Context, *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error) {
		return &gen.ListNodeCapacityResponse{
			Nodes: []*gen.NodeCapacityEntry{
				{NodeId: "n1", Model: "m1", MaxConcurrent: 12, LockCount: 2},
			},
		}, nil
	})
	client := dialCapacity(t, srv)

	now := time.Unix(1_700_000_000, 0)
	cache := mlnode.NewCache(client, mlnode.CacheOptions{
		PollInterval: 20 * time.Millisecond,
		Now:          func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx)

	require.Eventually(t, func() bool {
		nc, ok := cache.Get("n1")
		return ok && nc.MaxConcurrent == 12 && nc.Source == mlnode.SourceDAPI
	}, time.Second, 10*time.Millisecond)
	require.True(t, cache.HasObservedCapacity())

	// Poll failure retains last-seen as stale.
	srv.setHandler(func(context.Context, *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error) {
		return nil, status.Error(codes.Unavailable, "dapi down")
	})
	require.Eventually(t, func() bool {
		nc, ok := cache.Get("n1")
		return ok && nc.Source == mlnode.SourceStale && nc.MaxConcurrent == 12
	}, time.Second, 10*time.Millisecond)
	require.True(t, cache.HasObservedCapacity(), "outage must not clear observed capacity")
}

func TestNodeCapacityCache_UnimplementedZeroChange(t *testing.T) {
	srv := &fakeCapacityServer{}
	srv.setHandler(func(context.Context, *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error) {
		return nil, status.Error(codes.Unimplemented, "method ListNodeCapacity not implemented")
	})
	client := dialCapacity(t, srv)

	cache := mlnode.NewCache(client, mlnode.CacheOptions{
		PollInterval:             20 * time.Millisecond,
		UnsupportedRetryInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx)

	require.Eventually(t, func() bool { return srv.calls.Load() >= 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	require.False(t, cache.HasObservedCapacity())
	_, ok := cache.Get("n1")
	require.False(t, ok)
	require.Equal(t, 0, cache.AvailableSlots("n1"))
}

func TestFallbackDivisor_FreshLoadMap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	load := map[uint64]float64{1: 0.1, 2: 0.2}
	delivered := now

	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now:                func() time.Time { return now },
		EscrowLoadStaleTTL: 10 * time.Minute,
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return load, delivered
		},
	})
	cache.ApplyPollForTest([]*gen.NodeCapacityEntry{
		{NodeId: "n1", Model: "m", MaxConcurrent: 40, LockCount: 0},
	})

	// len=2 < floor 4 → divisor 4
	assert.Equal(t, 4, cache.Divisor())
	assert.Equal(t, 10, cache.EffectiveMax("n1")) // 40/4

	load = map[uint64]float64{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1}
	assert.Equal(t, 6, cache.Divisor())
	assert.Equal(t, 6, cache.EffectiveMax("n1")) // 40/6

	// Still usable within 10m after "dapi down" (clock advances 9m).
	now = now.Add(9 * time.Minute)
	assert.Equal(t, 6, cache.Divisor())
}

func TestFallbackDivisor_StaleFallsBackTo4(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	delivered := now
	load := map[uint64]float64{1: 1, 2: 1, 3: 1, 4: 1, 5: 1}

	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now:                func() time.Time { return now },
		EscrowLoadStaleTTL: 10 * time.Minute,
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return load, delivered
		},
	})
	cache.ApplyPollForTest([]*gen.NodeCapacityEntry{
		{NodeId: "n1", Model: "m", MaxConcurrent: 40, LockCount: 0},
	})
	require.Equal(t, 5, cache.Divisor())

	now = now.Add(11 * time.Minute)
	assert.Equal(t, 4, cache.Divisor())
	assert.Equal(t, 10, cache.EffectiveMax("n1")) // 40/4
}

func TestNodeCapacityCache_InFlightAndAvailableSlots(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			// fresh empty → floor 4
			return map[uint64]float64{}, now
		},
	})
	cache.ApplyPollForTest([]*gen.NodeCapacityEntry{
		{NodeId: "n1", Model: "m1", MaxConcurrent: 20, LockCount: 1},
		{NodeId: "n2", Model: "m1", MaxConcurrent: 8, LockCount: 0},
	})

	// effectiveMax(n1)=max(1,20/4)=5; slots=5-1-0=4
	assert.Equal(t, 4, cache.AvailableSlots("n1"))
	assert.Equal(t, 2, cache.AvailableSlots("n2")) // 8/4=2
	assert.Equal(t, 4, cache.AvailableSlotsForModel("m1"))

	require.True(t, cache.TryAcquire("n1", "m1"))
	require.True(t, cache.TryAcquire("n1", "m1"))
	assert.Equal(t, 2, cache.AvailableSlots("n1"))

	cache.Release("n1", "m1")
	assert.Equal(t, 3, cache.AvailableSlots("n1"))

	// Fill remaining slots.
	require.True(t, cache.TryAcquire("n1", "m1"))
	require.True(t, cache.TryAcquire("n1", "m1"))
	require.True(t, cache.TryAcquire("n1", "m1"))
	assert.Equal(t, 0, cache.AvailableSlots("n1"))
	require.False(t, cache.TryAcquire("n1", "m1"))
}

func TestNodeCapacityCache_UnknownNodeBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return map[uint64]float64{}, now // fresh empty → floor divisor 4
		},
		UnknownMaxConcurrent: 8, // 8/4 → 2 synthetic slots
	})
	// A node dapi never reported: no capacity row, but must still be bounded.
	require.True(t, cache.TryAcquireUnknown("ghost", "m1"))
	require.True(t, cache.TryAcquireUnknown("ghost", "m1"))
	require.False(t, cache.TryAcquireUnknown("ghost", "m1"), "unknown node capped at unknownMaxConcurrent/divisor")

	// Different node has its own independent budget.
	require.True(t, cache.TryAcquireUnknown("ghost2", "m1"))

	// Releasing frees a slot back.
	cache.ReleaseUnknown("ghost", "m1")
	require.True(t, cache.TryAcquireUnknown("ghost", "m1"))
	require.False(t, cache.TryAcquireUnknown("ghost", "m1"))
}

func TestNodeCapacityCache_UnknownInFlightSurvivesPoll(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return map[uint64]float64{}, now
		},
		UnknownMaxConcurrent: 4, // 4/4 → 1 slot
	})
	require.True(t, cache.TryAcquireUnknown("ghost", "m1"))
	require.False(t, cache.TryAcquireUnknown("ghost", "m1"))

	// A poll that still does not include "ghost" must not wipe its live count.
	cache.ApplyPollForTest([]*gen.NodeCapacityEntry{
		{NodeId: "n1", Model: "m1", MaxConcurrent: 40, LockCount: 0},
	})
	require.False(t, cache.TryAcquireUnknown("ghost", "m1"), "poll prune must not clear live unknown in-flight")

	cache.ReleaseUnknown("ghost", "m1")
	require.True(t, cache.TryAcquireUnknown("ghost", "m1"))
}

func TestNodeCapacityCache_StaleDropsLockCount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := mlnode.NewCache(nil, mlnode.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return map[uint64]float64{}, now // fresh empty → floor divisor 4
		},
	})
	// Busy last poll: max=40, lockCount=30, divisor=4 → effectiveMax=10.
	cache.ApplyPollForTest([]*gen.NodeCapacityEntry{
		{NodeId: "n1", Model: "m1", MaxConcurrent: 40, LockCount: 30},
	})

	// While polls succeed, last-seen locks are charged: 10 - 30 → clamped to 0.
	assert.Equal(t, 10, cache.EffectiveMax("n1"))
	assert.Equal(t, 0, cache.AvailableSlots("n1"))
	require.False(t, cache.TryAcquire("n1", "m1"))

	// DAPI outage: poll fails → row goes stale → frozen lockCount is dropped,
	// so fallback gets its full effectiveMax slice instead of starving.
	cache.MarkStaleForTest()
	assert.Equal(t, 10, cache.AvailableSlots("n1"))
	assert.Equal(t, 10, cache.AvailableSlotsForModel("m1"))

	require.True(t, cache.TryAcquire("n1", "m1"))
	assert.Equal(t, 9, cache.AvailableSlots("n1")) // localInFlight still counts
}
