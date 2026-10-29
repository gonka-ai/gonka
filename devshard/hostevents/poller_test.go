package hostevents_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
	"common/nodemanager/gen"
	"devshard/bridge"
	"devshard/chainoracle/blocks/headertime"
	"devshard/hostevents"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSubscribeSet(t *testing.T) {
	gw := hostevents.SubscribeSet(false)
	require.Equal(t, []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
	}, gw)
	host := hostevents.SubscribeSet(true)
	require.Contains(t, host, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED)
	require.Contains(t, host, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED)
	require.NotContains(t, host, gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED)
	for _, k := range gw {
		require.Contains(t, host, k)
	}
}

func TestTrustedWarmKeyMessage(t *testing.T) {
	msg := hostevents.TrustedWarmKeyMessage("api:9400")
	require.Contains(t, msg, "NODE_MANAGER_ADDR")
	require.Contains(t, msg, "trusted source of warm-key events and block times")
	unset := hostevents.TrustedWarmKeyMessage("")
	require.Contains(t, unset, "NODE_MANAGER_ADDR")
	require.Contains(t, unset, "never used")
}

type holeLog struct {
	mu        sync.Mutex
	headers   []int64
	batches   [][2]int64
	forbidden int
}

func (h *holeLog) Header(_ context.Context, height int64) (*blocks.Header, error) {
	h.mu.Lock()
	h.headers = append(h.headers, height)
	h.mu.Unlock()
	return blocks.HashOnlyHeader(height, time.UnixMilli(height*1000).UTC(), "gonka-test", []byte{byte(height)}), nil
}

func (h *holeLog) BlockchainInfo(_ context.Context, from, to int64) ([]*blocks.Header, error) {
	h.mu.Lock()
	h.batches = append(h.batches, [2]int64{from, to})
	h.mu.Unlock()
	var out []*blocks.Header
	for height := from; height <= to; height++ {
		out = append(out, blocks.HashOnlyHeader(height, time.UnixMilli(height*1000).UTC(), "gonka-test", []byte{byte(height)}))
	}
	return out, nil
}

func (h *holeLog) GetBlockHeader()   { h.forbidden++ }
func (h *holeLog) GetBlockByHeight() { h.forbidden++ }

func (h *holeLog) snapshot() (headers []int64, batches [][2]int64, forbidden int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.headers...), append([][2]int64(nil), h.batches...), h.forbidden
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *stepClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func blockKinds() []gen.HostEventKind {
	return []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK}
}

func TestPoller_OneLoopAndCursor(t *testing.T) {
	var inflight atomic.Int32
	var maxInflight atomic.Int32
	var mu sync.Mutex
	var reqs []*gen.GetHostEventsRequest
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		n := inflight.Add(1)
		for {
			old := maxInflight.Load()
			if n <= old || maxInflight.CompareAndSwap(old, n) {
				break
			}
		}
		mu.Lock()
		reqs = append(reqs, &gen.GetHostEventsRequest{
			Cursor: req.GetCursor(), Generation: req.GetGeneration(), AfterHeight: req.GetAfterHeight(),
			Subscribe: append([]gen.HostEventKind(nil), req.GetSubscribe()...),
		})
		mu.Unlock()
		timer := time.NewTimer(30 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		inflight.Add(-1)
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 4, SupportedKinds: blockKinds()}, nil
	})
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10, time.UnixMilli(10_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client:          dialFake(t, srv),
			ServerMaxWait:   50 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond,
			ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source:        src,
		IncludeEscrow: true,
		Escrow:        &recordingSink{},
		EpochFloor:    func() uint64 { return 25 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reqs) >= 2
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, int32(1), maxInflight.Load())
	require.Equal(t, uint64(0), reqs[0].GetCursor())
	require.Equal(t, uint64(0), reqs[0].GetGeneration())
	require.Equal(t, uint64(25), reqs[0].GetAfterHeight())
	require.Contains(t, reqs[0].GetSubscribe(), gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED)
	require.Equal(t, uint64(0), reqs[1].GetAfterHeight())
	require.Equal(t, uint64(4), reqs[1].GetCursor())
}

func TestPoller_StartAtHeadWhenMapEmpty(t *testing.T) {
	var mu sync.Mutex
	var reqs []*gen.GetHostEventsRequest
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		mu.Lock()
		reqs = append(reqs, &gen.GetHostEventsRequest{AfterHeight: req.GetAfterHeight(), Cursor: req.GetCursor()})
		mu.Unlock()
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client:          dialFake(t, srv),
			ServerMaxWait:   50 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond,
			ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source:      headertime.New(headertime.WithBlockInterval(time.Second)),
		StartAtHead: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reqs) >= 2
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, hostevents.AfterHeightHead, reqs[0].GetAfterHeight())
	require.Equal(t, uint64(0), reqs[1].GetAfterHeight())
}

func TestPoller_EscrowFlagDropsKindsAndLoadMap(t *testing.T) {
	var mu sync.Mutex
	var subs [][]gen.HostEventKind
	load := hostevents.NewLoadMap()
	load.Replace([]*gen.EscrowLoad{{EscrowId: 3, RequestsPerMin: 9}}, time.Unix(1, 0))
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		mu.Lock()
		subs = append(subs, append([]gen.HostEventKind(nil), req.GetSubscribe()...))
		mu.Unlock()
		return &gen.GetHostEventsResponse{
			Unchanged: true, Generation: 1, NextCursor: 1,
			SupportedKinds: blockKinds(),
			EscrowLoad:     []*gen.EscrowLoad{{EscrowId: 1, RequestsPerMin: 1}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.NewPoller(hostevents.PollerConfig{
			Config: hostevents.Config{
				Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond, LoadMap: load,
				ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
			},
			IncludeEscrow: false,
		}).Run(ctx)
	}()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(subs) > 0
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	require.NotContains(t, subs[0], gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED)
	require.Contains(t, subs[0], gen.HostEventKind_HOST_EVENT_KIND_BLOCK)
	got, _ := load.Snapshot()
	require.Equal(t, map[uint64]float64{3: 9}, got)
}

func TestPoller_LoadMapOnSuccessOnly(t *testing.T) {
	load := hostevents.NewLoadMap()
	stamp := time.Unix(1, 0)
	load.Replace([]*gen.EscrowLoad{{EscrowId: 3, RequestsPerMin: 9}}, stamp)
	var kept atomic.Bool
	kept.Store(true)
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(
		func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
			return nil, status.Error(codes.Unavailable, "stalled")
		},
		func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
			got, at := load.Snapshot()
			if at != stamp || got[3] != 9 || len(got) != 1 {
				kept.Store(false)
			}
			return &gen.GetHostEventsResponse{
				Unchanged: true, Generation: 2, NextCursor: 8,
				SupportedKinds: blockKinds(),
				EscrowLoad:     []*gen.EscrowLoad{{EscrowId: 4, RequestsPerMin: 2}},
			}, nil
		},
	)
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Unchanged: true, Generation: 2, NextCursor: 8, SupportedKinds: blockKinds(),
			EscrowLoad: []*gen.EscrowLoad{{EscrowId: 4, RequestsPerMin: 2}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond, LoadMap: load,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		IncludeEscrow: true,
		Escrow:        &recordingSink{},
	}).Run(ctx)
	require.Eventually(t, func() bool {
		m, at := load.Snapshot()
		return !at.IsZero() && m[4] == 2
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	require.True(t, kept.Load())
}

func TestPoller_ResetOnceThenFillsTheGap(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10, time.UnixMilli(10_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	sink := &recordingSink{}
	var resets atomic.Int32
	var firstAfter atomic.Uint64
	var prePoll atomic.Int32
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		firstAfter.Store(req.GetAfterHeight())
		_, _, n := sink.snapshot()
		prePoll.Store(int32(n))
		resets.Add(1)
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 3, NextCursor: 100, SupportedKinds: blockKinds()}, nil
	})
	var follow atomic.Value
	srv.SetSticky(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		if follow.Load() == nil {
			follow.Store(&gen.GetHostEventsRequest{
				Cursor: req.GetCursor(), Generation: req.GetGeneration(), AfterHeight: req.GetAfterHeight(),
			})
		}
		return &gen.GetHostEventsResponse{
			Generation: 3, NextCursor: 101, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 101, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 15, Hash: []byte{15}, HeaderTimeUnixMs: 15_000},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, IncludeEscrow: true, BlocksVerified: true, Escrow: sink, Holes: holes,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(15)
		if !ok || rec.Origin != headertime.OriginDAPI || !rec.Verified {
			return false
		}
		for h := uint64(11); h <= 14; h++ {
			if _, ok := src.StoredAt(h); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	_, batches, forbidden := holes.snapshot()
	require.Equal(t, [][2]int64{{11, 14}}, batches)
	require.Zero(t, forbidden)
	require.Equal(t, int32(1), resets.Load())
	got := follow.Load().(*gen.GetHostEventsRequest)
	require.Equal(t, uint64(100), got.GetCursor())
	require.Equal(t, uint64(3), got.GetGeneration())
	require.Equal(t, uint64(0), got.GetAfterHeight())
	require.Equal(t, uint64(10), firstAfter.Load())
	require.Equal(t, int32(1), prePoll.Load())
	_, _, n := sink.snapshot()
	require.GreaterOrEqual(t, n, 2, "rehydrate before the first poll and again on reset")
}

func TestPoller_ResetOnceThenFillsTheGapAtHeight10000(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10000, time.UnixMilli(10_000_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	const floor uint64 = 9390
	sink := &recordingSink{}
	var resets atomic.Int32
	var firstAfter atomic.Uint64
	var prePoll atomic.Int32
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		firstAfter.Store(req.GetAfterHeight())
		_, _, n := sink.snapshot()
		prePoll.Store(int32(n))
		resets.Add(1)
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 3, NextCursor: 100, SupportedKinds: blockKinds()}, nil
	})
	var follow atomic.Value
	srv.SetSticky(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		if follow.Load() == nil {
			follow.Store(&gen.GetHostEventsRequest{
				Cursor: req.GetCursor(), Generation: req.GetGeneration(), AfterHeight: req.GetAfterHeight(),
			})
		}
		return &gen.GetHostEventsResponse{
			Generation: 3, NextCursor: 101, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 101, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 10005, Hash: []byte{15}, HeaderTimeUnixMs: 10_005_000},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, IncludeEscrow: true, BlocksVerified: true, Escrow: sink, Holes: holes,
		EpochFloor: func() uint64 { return floor },
	}).Run(ctx)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(10005)
		if !ok || rec.Origin != headertime.OriginDAPI || !rec.Verified {
			return false
		}
		for h := uint64(10001); h <= 10004; h++ {
			if _, ok := src.StoredAt(h); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	_, batches, forbidden := holes.snapshot()
	require.Equal(t, [][2]int64{{10001, 10004}}, batches)
	for _, b := range batches {
		require.GreaterOrEqual(t, b[0], int64(floor), "catch-up does not read below the epoch floor")
	}
	require.Zero(t, forbidden)
	require.Equal(t, int32(1), resets.Load())
	got := follow.Load().(*gen.GetHostEventsRequest)
	require.Equal(t, uint64(100), got.GetCursor())
	require.Equal(t, uint64(3), got.GetGeneration())
	require.Equal(t, uint64(0), got.GetAfterHeight())
	require.Equal(t, uint64(10000), firstAfter.Load())
	require.Equal(t, int32(1), prePoll.Load())
	_, _, n := sink.snapshot()
	require.GreaterOrEqual(t, n, 2, "rehydrate before the first poll and again on reset")
}

func TestPoller_DAPIBlockBeforeHeaderRead(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New()
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 1, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 1, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 8, Hash: []byte{8}, HeaderTimeUnixMs: 8_000},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true, Holes: holes,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(8)
		return ok && rec.Origin == headertime.OriginDAPI
	}, 2*time.Second, 10*time.Millisecond)
	ms, err := src.HeaderTimeMs(8, []byte{8})
	require.NoError(t, err)
	require.Equal(t, int64(8_000), ms)
	headers, batches, forbidden := holes.snapshot()
	require.Empty(t, headers)
	require.Empty(t, batches)
	require.Zero(t, forbidden)
	cancel()
}

func TestPoller_OldDAPIStillAppliesWarmKeys(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 1,
			Events: []*gen.HostEvent{{
				Seq: 1, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 1, Height: 4},
			}},
		}, nil
	})
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 1)
		return err == nil && found
	}, 2*time.Second, 10*time.Millisecond)
	require.False(t, p.BlockFeedLive())
	cancel()
}

func TestPoller_UnavailableKeepsCursor(t *testing.T) {
	var mu sync.Mutex
	var cursors []uint64
	srv := &fakeHostEventsServer{}
	block := &gen.GetHostEventsResponse{
		Generation: 1, NextCursor: 7, SupportedKinds: blockKinds(),
		Events: []*gen.HostEvent{{
			Seq: 7, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
			Block: &gen.BlockPayload{Height: 3, Hash: []byte{3}, HeaderTimeUnixMs: 3_000},
		}},
	}
	srv.SetHandlers(
		func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
			mu.Lock()
			cursors = append(cursors, req.GetCursor())
			mu.Unlock()
			return &gen.GetHostEventsResponse{Generation: 1, NextCursor: 6, SupportedKinds: blockKinds()}, nil
		},
		func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
			mu.Lock()
			cursors = append(cursors, req.GetCursor())
			mu.Unlock()
			return nil, status.Error(codes.Unavailable, "stalled")
		},
		func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
			mu.Lock()
			cursors = append(cursors, req.GetCursor())
			mu.Unlock()
			return block, nil
		},
	)
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return block, nil
	})
	src := headertime.New()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, ok := src.StoredAt(3)
		return ok && p.BlockFeedLive()
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []uint64{0, 6, 6}, cursors)
}

func TestPoller_LogsHeaderTimeApplied(t *testing.T) {
	logs := &syncBuffer{}
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 7, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 7, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 3, Hash: []byte{3}, HeaderTimeUnixMs: 3_000},
			}},
		}, nil
	})
	src := headertime.New()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
			Log: slog.New(slog.NewTextHandler(logs, nil)),
		},
		Source: src, BlocksVerified: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, ok := src.StoredAt(3)
		return ok && strings.Contains(logs.String(), "header_time: applied")
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	got := logs.String()
	require.Contains(t, got, "height=3")
	require.Contains(t, got, "origin=dapi")
	require.Contains(t, got, "verified=true")
	require.Contains(t, got, "high_water=3")
	require.Contains(t, got, "tip_height=3")
	require.Contains(t, got, "tip_origin=dapi")
	require.Contains(t, got, "tip_verified=true")
}

func TestPoller_LogsAfterHeightOnceAndResetFollowUp(t *testing.T) {
	logs := &syncBuffer{}
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			NeedsReset: true, Generation: 2, NextCursor: 9, SupportedKinds: blockKinds(),
		}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Unchanged: true, Generation: 2, NextCursor: 9, SupportedKinds: blockKinds(),
		}, nil
	})
	src := headertime.New()
	src.Observe(blocks.HashOnlyHeader(10, time.UnixMilli(10_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
			Log: slog.New(slog.NewTextHandler(logs, nil)),
		},
		Source: src,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		got := logs.String()
		return strings.Contains(got, "after_height=10") &&
			strings.Contains(got, "hostevents: needs_reset") &&
			strings.Contains(got, "after_height=0 ")
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	require.Equal(t, 1, strings.Count(logs.String(), "hostevents: needs_reset"))
}

func TestPoller_LogsWarmKey(t *testing.T) {
	logs := &syncBuffer{}
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 4, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 4, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 7, Height: 3},
			}},
		}, nil
	})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
			Log: slog.New(slog.NewTextHandler(logs, nil)),
		},
		Warm: warm,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 7)
		return err == nil && found && strings.Contains(logs.String(), "hostevents: warm-key")
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	got := logs.String()
	require.Contains(t, got, "grantee=warm")
	require.Contains(t, got, "grant_id=7")
}

func TestPoller_QuietFeedThenBlockResumes(t *testing.T) {
	clock := &stepClock{now: time.Unix(1_000, 0)}
	var n atomic.Int32
	release := make(chan struct{})
	logs := &syncBuffer{}
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(ctx context.Context, _ *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		switch n.Add(1) {
		case 1:
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
		case 2:
			clock.Advance(3 * time.Second)
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
		default:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
			return &gen.GetHostEventsResponse{
				Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
				Events: []*gen.HostEvent{{
					Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
					Block: &gen.BlockPayload{Height: 1, Hash: []byte{1}, HeaderTimeUnixMs: 1_000},
				}},
			}, nil
		}
	})
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond, Clock: clock,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
			Log: slog.New(slog.NewTextHandler(logs, nil)),
		},
		Source: src, BlocksVerified: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		return n.Load() >= 2 && !p.BlockFeedLive()
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, float64(0), liveGauge(t))
	require.Contains(t, logs.String(), "hostevents: block feed not live")
	close(release)
	require.Eventually(t, func() bool { return p.BlockFeedLive() }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, float64(1), liveGauge(t))
	require.Contains(t, logs.String(), "hostevents: block feed live")
	cancel()
}

func TestPoller_SuppressBlocksDoesNotStoreVerified(t *testing.T) {
	clock := &stepClock{now: time.Unix(1_000, 0)}
	var n atomic.Int32
	src := headertime.New()
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		i := n.Add(1)
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: uint64(i), SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{blockEvent(uint64(i), 9+int64(i))},
		}, nil
	})
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond, Clock: clock,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool {
		_, ok := src.StoredAt(10)
		return ok && p.BlockFeedLive()
	}, 2*time.Second, 10*time.Millisecond)
	p.SuppressBlocks(hostevents.DAPIBlockPenalty)
	require.False(t, p.BlockFeedLive())
	hw := src.HighWater()
	at := n.Load()
	require.Eventually(t, func() bool { return n.Load() >= at+3 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, hw, src.HighWater(), "suppressed BLOCK pages must not reach Source")
	clock.Advance(hostevents.DAPIBlockPenalty)
	require.Eventually(t, func() bool {
		if !p.BlockFeedLive() {
			return false
		}
		top := uint64(9 + n.Load())
		for h := hw + 1; h <= top; h++ {
			if _, ok := src.StoredAt(h); ok {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
}

func TestPoller_HeaderTimeView(t *testing.T) {
	src := headertime.New()
	at := time.Unix(1_700_000_000, 0).UTC()
	src.Observe(blocks.HashOnlyHeader(10, at, "gonka-test", []byte{10}), headertime.OriginDAPI, true)
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config:         hostevents.Config{Client: dialFake(t, &fakeHostEventsServer{})},
		Source:         src,
		BlocksVerified: true,
	})
	v := p.HeaderTimeView()
	require.True(t, v.BlockFeedLive)
	require.Equal(t, uint64(10), v.HighWater)
	require.Equal(t, uint64(10), v.TipHeight)
	require.Equal(t, string(headertime.OriginDAPI), v.TipOrigin)
	require.True(t, v.TipVerified)
	require.False(t, (*hostevents.Poller)(nil).HeaderTimeView().BlockFeedLive)
}

func TestPoller_NoClientStillInstallsHeaderFiller(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithMissWait(50 * time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Source: src,
		Holes:  holes,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		_, err := src.HeaderTimeMs(4, []byte{4})
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	headers, _, forbidden := holes.snapshot()
	require.Equal(t, []int64{4}, headers)
	require.Zero(t, forbidden)
}

func TestPoller_ReplaysWarmKeyOnce(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	ev := &gen.HostEvent{
		Seq: 1, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 9, Height: 2, ExpiresAtUnix: 50},
	}
	page := &gen.GetHostEventsResponse{Generation: 1, NextCursor: 1, SupportedKinds: blockKinds(), Events: []*gen.HostEvent{ev}}
	var n atomic.Int32
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		n.Add(1)
		return page, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		n.Load()
		e, found, err := warm.WarmKeyEntry("cold", 9)
		return err == nil && found && e.Grantee == "warm"
	}, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return n.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	e, found, err := warm.WarmKeyEntry("cold", 9)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "warm", e.Grantee)
	cancel()
}

func TestPoller_ResetReloadsWarmKeys(t *testing.T) {
	fetch := &failThenWarm{entry: bridge.WarmKeyEntry{
		Granter: "cold", ID: 1, Grantee: "warm",
		ExpiresAtUnix: time.Now().Add(time.Hour).Unix(),
	}}
	warm := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	got, err := warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, got, 1)
	before := fetch.calls.Load()
	reset := make(chan struct{})
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		select {
		case <-reset:
		default:
			close(reset)
		}
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm,
	}).Run(ctx)
	select {
	case <-reset:
	case <-time.After(2 * time.Second):
		t.Fatal("needs_reset did not arrive")
	}
	require.Equal(t, before, fetch.calls.Load(), "needs_reset does not read every granter")
	got, err = warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "warm", got[0].Grantee)
	require.Greater(t, fetch.calls.Load(), before)
	cancel()
}

type failThenWarm struct {
	mu    sync.Mutex
	err   error
	entry bridge.WarmKeyEntry
	calls atomic.Int32
}

func (f *failThenWarm) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *failThenWarm) setEntry(e bridge.WarmKeyEntry) {
	f.mu.Lock()
	f.entry = e
	f.mu.Unlock()
}

func (f *failThenWarm) FetchWarmKeyLog(context.Context, string) ([]bridge.WarmKeyEntry, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.entry.ID == 0 {
		return nil, nil
	}
	return []bridge.WarmKeyEntry{f.entry}, nil
}

func (f *failThenWarm) FetchWarmKeyEntry(context.Context, string, uint64) (bridge.WarmKeyEntry, bool, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return bridge.WarmKeyEntry{}, false, f.err
	}
	if f.entry.ID == 0 {
		return bridge.WarmKeyEntry{}, false, nil
	}
	return f.entry, true, nil
}

func TestPoller_HoleBatchesOf20(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(1, time.UnixMilli(1_000).UTC(), "gonka-test", []byte{1}), headertime.OriginComet, true)
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 42, Hash: []byte{42}, HeaderTimeUnixMs: 42_000},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true, Holes: holes,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		if _, ok := src.StoredAt(42); !ok {
			return false
		}
		for h := uint64(2); h <= 41; h++ {
			if _, ok := src.StoredAt(h); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	_, batches, forbidden := holes.snapshot()
	require.Equal(t, [][2]int64{{2, 21}, {22, 41}}, batches)
	require.Zero(t, forbidden)
	rec, ok := src.StoredAt(42)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
}

func TestPoller_GatewayDoesNotStoreUnverifiedBlock(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10, time.UnixMilli(10_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, false)
	var polls atomic.Int32
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		polls.Add(1)
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		polls.Add(1)
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
				Block: &gen.BlockPayload{Height: 12, Hash: []byte{99}, HeaderTimeUnixMs: 12_000},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: false, Holes: holes,
	}).Run(ctx)
	require.Eventually(t, func() bool { return polls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	_, ok := src.StoredAt(12)
	require.False(t, ok, "the gateway does not store an unverified dapi BLOCK")
	_, batches, forbidden := holes.snapshot()
	require.Empty(t, batches)
	require.Zero(t, forbidden)
}

func TestPoller_ResetCatchUpSkipsBelowFloorAndDoesNotBlockPoll(t *testing.T) {
	holes := &holeLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10000, time.UnixMilli(10_000_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	const floor uint64 = 9390
	var mu sync.Mutex
	var afterReset []time.Time
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		mu.Lock()
		afterReset = append(afterReset, time.Now())
		n := len(afterReset)
		mu.Unlock()
		if n == 1 {
			return &gen.GetHostEventsResponse{
				Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
				Events: []*gen.HostEvent{{
					Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
					Block: &gen.BlockPayload{Height: 10050, Hash: []byte{50}, HeaderTimeUnixMs: 10_050_000},
				}},
			}, nil
		}
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 2, SupportedKinds: blockKinds()}, nil
	})
	pollInterval := time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: pollInterval,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true, Holes: holes,
		EpochFloor: func() uint64 { return floor },
	}).Run(ctx)
	require.Eventually(t, func() bool {
		if _, ok := src.StoredAt(10050); !ok {
			return false
		}
		for h := uint64(10001); h <= 10049; h++ {
			if _, ok := src.StoredAt(h); !ok {
				return false
			}
		}
		mu.Lock()
		n := len(afterReset)
		mu.Unlock()
		return n >= 2
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	_, batches, forbidden := holes.snapshot()
	require.Zero(t, forbidden)
	for _, b := range batches {
		require.GreaterOrEqual(t, b[0], int64(floor), "BlockchainInfo from %d is below the floor", b[0])
	}
	coverExactly(t, batches, 10001, 10049)
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(afterReset), 2)
	require.LessOrEqual(t, afterReset[1].Sub(afterReset[0]), pollInterval)
}

func TestPoller_BlockingHoleReaderDoesNotStallWarmKeys(t *testing.T) {
	holes := &blockingHoles{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(holes.release) })
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	src.Observe(blocks.HashOnlyHeader(10000, time.UnixMilli(10_000_000).UTC(), "gonka-test", []byte{10}), headertime.OriginComet, true)
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	var polls atomic.Int32
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		polls.Add(1)
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		n := polls.Add(1)
		if n == 2 {
			return &gen.GetHostEventsResponse{
				Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
				Events: []*gen.HostEvent{{
					Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
					Block: &gen.BlockPayload{Height: 10050, Hash: []byte{50}, HeaderTimeUnixMs: 10_050_000},
				}},
			}, nil
		}
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 3, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{{
				Seq: 3, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 9, Height: 10050},
			}},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true, Holes: holes, Warm: warm,
	}).Run(ctx)
	select {
	case <-holes.started:
	case <-time.After(2 * time.Second):
		t.Fatal("catch-up did not start")
	}
	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 9)
		return err == nil && found
	}, 2*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, polls.Load(), int32(3), "the poll loop ran while BlockchainInfo was blocked")
	cancel()
}

func TestPoller_EpochAdvancePrunesOlderThanPrevious(t *testing.T) {
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	at := time.UnixMilli(1_000).UTC()
	src.Observe(blocks.HashOnlyHeader(50, at, "gonka-test", []byte{50}), headertime.OriginComet, true)
	src.Observe(blocks.HashOnlyHeader(100, at, "gonka-test", []byte{100}), headertime.OriginComet, true)
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 2, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{
				{
					Seq: 1, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
					Block: &gen.BlockPayload{Height: 100, Hash: []byte{100}, HeaderTimeUnixMs: 100_000, EpochIndex: 2},
				},
				{
					Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
					Block: &gen.BlockPayload{Height: 200, Hash: []byte{200}, HeaderTimeUnixMs: 200_000, EpochIndex: 3},
				},
			},
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		_, fifty := src.StoredAt(50)
		_, hundred := src.StoredAt(100)
		_, twoHundred := src.StoredAt(200)
		return !fifty && hundred && twoHundred
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
}

func coverExactly(t *testing.T, batches [][2]int64, from, to int64) {
	t.Helper()
	seen := make(map[int64]bool)
	for _, b := range batches {
		require.GreaterOrEqual(t, b[0], from)
		require.LessOrEqual(t, b[1], to)
		for h := b[0]; h <= b[1]; h++ {
			seen[h] = true
		}
	}
	for h := from; h <= to; h++ {
		require.Truef(t, seen[h], "height %d missing from catch-up reads", h)
	}
}

type blockingHoles struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingHoles) Header(_ context.Context, height int64) (*blocks.Header, error) {
	return blocks.HashOnlyHeader(height, time.UnixMilli(height*1000).UTC(), "gonka-test", []byte{byte(height)}), nil
}

func (h *blockingHoles) BlockchainInfo(ctx context.Context, from, to int64) ([]*blocks.Header, error) {
	h.once.Do(func() { close(h.started) })
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var out []*blocks.Header
	for height := from; height <= to; height++ {
		out = append(out, blocks.HashOnlyHeader(height, time.UnixMilli(height*1000).UTC(), "gonka-test", []byte{byte(height)}))
	}
	return out, nil
}

type pageLog struct {
	mu      sync.Mutex
	pages   [][]observer.DAPIBlock
	onOffer func()
	onLink  func()
}

func (l *pageLog) OfferDAPI(page []observer.DAPIBlock) error {
	l.mu.Lock()
	l.pages = append(l.pages, append([]observer.DAPIBlock(nil), page...))
	fn := l.onOffer
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (l *pageLog) LinkBuffered() {
	l.mu.Lock()
	fn := l.onLink
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (l *pageLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pages)
}

func (l *pageLog) first() []observer.DAPIBlock {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pages) == 0 {
		return nil
	}
	return l.pages[0]
}

func blockEvent(seq uint64, height int64) *gen.HostEvent {
	return &gen.HostEvent{
		Seq: seq, Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		Block: &gen.BlockPayload{Height: height, Hash: []byte{byte(height)}, HeaderTimeUnixMs: height * 1_000, Header: []byte{0xa, byte(height)}},
	}
}

func TestPoller_GatewayOffersEachPageOnce(t *testing.T) {
	verify := &pageLog{}
	src := headertime.New(headertime.WithBlockInterval(time.Second))
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 3, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{blockEvent(1, 7), blockEvent(2, 8), {
				Seq: 3, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 4, Height: 8, ExpiresAtUnix: 50},
			}},
		}, nil
	})
	var polls atomic.Int32
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		polls.Add(1)
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 3, SupportedKinds: blockKinds()}, nil
	})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, Warm: warm, Verify: verify,
	}).Run(ctx)
	require.Eventually(t, func() bool { return polls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	cancel()

	require.Equal(t, 1, verify.count(), "one response is one offer")
	page := verify.first()
	require.Len(t, page, 2)
	require.Equal(t, observer.DAPIBlock{Height: 7, Hash: []byte{7}, TimeMs: 7_000, Header: []byte{0xa, 7}}, page[0])
	require.Equal(t, int64(8), page[1].Height)
	_, stored := src.StoredAt(8)
	require.False(t, stored, "the verifier writes the map, the poller does not")
	_, found, err := warm.WarmKeyEntry("cold", 4)
	require.NoError(t, err)
	require.True(t, found)
}

func TestPoller_SlowOfferDAPIDoesNotDelayWarmKeys(t *testing.T) {
	var offerAt, foundAt atomic.Int64
	verify := &pageLog{onOffer: func() {
		offerAt.CompareAndSwap(0, time.Now().UnixNano())
		time.Sleep(2 * time.Second)
	}}
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 3, SupportedKinds: blockKinds(),
			Events: []*gen.HostEvent{blockEvent(1, 7), {
				Seq: 2, Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: 4, Height: 7, ExpiresAtUnix: 50},
			}},
		}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 3, SupportedKinds: blockKinds()}, nil
	})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Warm: warm, Verify: verify,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 4)
		if err == nil && found {
			foundAt.CompareAndSwap(0, time.Now().UnixNano())
		}
		return foundAt.Load() != 0 && offerAt.Load() != 0
	}, 3*time.Second, time.Millisecond)
	require.LessOrEqual(t, foundAt.Load(), offerAt.Load()+int64(10*time.Millisecond),
		"the warm-key is applied after dispatch, no later than the offer plus 10 ms")
	cancel()
}

func TestPoller_NeedsResetAndSuppressLinkTheBuffer(t *testing.T) {
	var links atomic.Int32
	verify := &pageLog{onLink: func() { links.Add(1) }}
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{NeedsReset: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKinds()}, nil
	})
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Verify: verify,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	require.Eventually(t, func() bool { return links.Load() >= 1 }, 2*time.Second, 10*time.Millisecond, "needs_reset links the buffer")
	before := links.Load()
	p.SuppressBlocks(time.Minute)
	require.Greater(t, links.Load(), before, "SuppressBlocks links the buffer")
	cancel()
}

func TestPoller_FailedDAPIHeaderSuppressesBlocksForFiveMinutes(t *testing.T) {
	clock := &stepClock{now: time.Unix(1_000, 0)}
	verify := &pageLog{}
	var n atomic.Int64
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		i := n.Add(1)
		events := []*gen.HostEvent{blockEvent(uint64(2*i), 10+i)}
		if i > 1 {
			events = append(events, &gen.HostEvent{
				Seq: uint64(2*i + 1), Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
				WarmKey: &gen.WarmKeyPayload{Granter: "cold", Grantee: "warm", GrantId: uint64(i), Height: 10 + i, ExpiresAtUnix: 50},
			})
		}
		return &gen.GetHostEventsResponse{Generation: 1, NextCursor: uint64(2*i + 1), SupportedKinds: blockKinds(), Events: events}, nil
	})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond, Clock: clock,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: headertime.New(headertime.WithBlockInterval(time.Second)),
		Warm:   warm,
		Verify: verify,
	})
	var once sync.Once
	verify.onOffer = func() { once.Do(func() { p.DAPIBlockRejected(errors.New("broken link")) }) }
	rejected := rejectedTotal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		_, found, err := warm.WarmKeyEntry("cold", 4)
		return err == nil && found
	}, 2*time.Second, 10*time.Millisecond, "warm-key events still apply")
	require.Equal(t, 1, verify.count(), "no page is offered while suppressed")
	require.False(t, p.BlockFeedLive())
	require.Equal(t, rejected+1, rejectedTotal(t))

	clock.Advance(hostevents.DAPIBlockPenalty - time.Second)
	at := n.Load()
	require.Eventually(t, func() bool { return n.Load() >= at+3 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, verify.count())

	clock.Advance(time.Second)
	require.Eventually(t, func() bool { return verify.count() >= 2 && p.BlockFeedLive() }, 2*time.Second, 10*time.Millisecond)
	cancel()
}

func rejectedTotal(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "devshard_hostevents_dapi_blocks_rejected_total" {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatal("devshard_hostevents_dapi_blocks_rejected_total is not registered")
	return 0
}

func liveGauge(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == hostevents.BlockFeedLiveMetric {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal(hostevents.BlockFeedLiveMetric + " is not registered")
	return 0
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPoller_DevsharddNeverCallsVerify(t *testing.T) {
	verify := &pageLog{}
	src := headertime.New()
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{Generation: 1, NextCursor: 1, SupportedKinds: blockKinds(), Events: []*gen.HostEvent{blockEvent(1, 5)}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src, BlocksVerified: true, Verify: verify,
	}).Run(ctx)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(5)
		return ok && rec.Verified && rec.Origin == headertime.OriginDAPI
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	require.Zero(t, verify.count())
}

func TestPoller_GatewayWithoutVerifierHasNoBlockFeed(t *testing.T) {
	src := headertime.New()
	srv := &fakeHostEventsServer{}
	var polls atomic.Int32
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		polls.Add(1)
		return &gen.GetHostEventsResponse{Generation: 1, NextCursor: 1, SupportedKinds: blockKinds(), Events: []*gen.HostEvent{blockEvent(1, 5)}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client: dialFake(t, srv), ServerMaxWait: 20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond, ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source: src,
	})
	go p.Run(ctx)
	require.Eventually(t, func() bool { return polls.Load() >= 3 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	require.False(t, p.BlockFeedLive())
	_, stored := src.StoredAt(5)
	require.False(t, stored)
}

func TestSignAndTimeoutPackagesDoNotPoll(t *testing.T) {
	roots := []string{"../state", "../host"}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(b)
			if strings.Contains(text, "GetHostEvents") || strings.Contains(text, "GetBlockHeader") {
				t.Errorf("%s dials from the sign or timeout packages", path)
			}
			return nil
		})
		require.NoError(t, err)
	}
}
