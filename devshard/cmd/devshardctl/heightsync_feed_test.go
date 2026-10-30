package main

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks/observer"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/headertime"
	"devshard/hostevents"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type feedHostEvents struct {
	gen.UnimplementedNodeManagerServer
	mu       sync.Mutex
	handlers []func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)
	sticky   func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)
}

func (s *feedHostEvents) SetHandlers(hs ...func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append([]func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)(nil), hs...)
}

func (s *feedHostEvents) SetSticky(h func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sticky = h
}

func (s *feedHostEvents) GetHostEvents(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
	s.mu.Lock()
	var h func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)
	if len(s.handlers) > 0 {
		h = s.handlers[0]
		s.handlers = s.handlers[1:]
	} else if s.sticky != nil {
		h = s.sticky
	}
	s.mu.Unlock()
	if h != nil {
		return h(ctx, req)
	}
	return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1}, nil
}

func dialFeed(t *testing.T, srv *feedHostEvents) gen.NodeManagerClient {
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

func dapiBlockEvents(seq uint64, page []observer.DAPIBlock) []*gen.HostEvent {
	out := make([]*gen.HostEvent, len(page))
	for i, b := range page {
		out[i] = &gen.HostEvent{
			Seq:  seq + uint64(i),
			Kind: gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
			Block: &gen.BlockPayload{
				Height:           b.Height,
				Hash:             append([]byte(nil), b.Hash...),
				HeaderTimeUnixMs: b.TimeMs,
				Header:           b.Header,
			},
		}
	}
	return out
}

func rejectedBlocks(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "devshard_hostevents_dapi_blocks_rejected_total" {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	return 0
}

func startGatewayVerifierPoller(t *testing.T, rpcURL string, nm gen.NodeManagerClient) (*headertime.Source, *hostevents.Poller, context.CancelFunc) {
	t.Helper()
	src, verifier := gatewayHeaderTimeSource(rpcURL, nil)
	require.NotNil(t, verifier, "NewTendermint against the stub RPC builds the commit verifier")
	require.True(t, seedGatewayVerified(context.Background(), verifier), "Comet seed must succeed so Verify is the production path")
	ctx, cancel := context.WithCancel(context.Background())
	p := hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client:          nm,
			ServerMaxWait:   20 * time.Millisecond,
			ErrorBackoffMin: time.Millisecond,
			ErrorBackoffMax: 2 * time.Millisecond,
		},
		Source:      src,
		StartAtHead: true,
		Verify:      gatewayBlockVerifier(verifier),
	})
	verifier.SetOnDAPIFailure(p.DAPIBlockRejected)
	go p.Run(ctx)
	return src, p, cancel
}

func TestGateway_SetOnDAPIFailureSuppressesForgedPage(t *testing.T) {
	c := observer.NewSignedChain(100)
	rpc := c.ServeJSONRPC()
	t.Cleanup(rpc.Close)

	page := c.Page(95, 99)
	page[2] = observer.BlockOf(c.Forged(97))
	srv := &feedHostEvents{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 5, SupportedKinds: []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
			Events: dapiBlockEvents(1, page),
		}, nil
	})
	var livePages atomic.Int32
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		livePages.Add(1)
		c.Grow(101)
		return &gen.GetHostEventsResponse{
			Generation: 1, NextCursor: 6, SupportedKinds: []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
			Events: dapiBlockEvents(6, c.Page(101, 101)),
		}, nil
	})

	before := rejectedBlocks(t)
	src, p, cancel := startGatewayVerifierPoller(t, rpc.URL, dialFeed(t, srv))
	defer cancel()

	require.Eventually(t, func() bool {
		return rejectedBlocks(t) >= before+1 && !p.BlockFeedLive()
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return livePages.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	_, stored := src.StoredAt(101)
	require.False(t, stored, "blocks are suppressed after the forged page")
	require.Equal(t, before+1, rejectedBlocks(t))
}

func TestGateway_PollerVerifierSourceCatchUpLiveAndForged(t *testing.T) {
	c := observer.NewSignedChain(100)
	rpc := c.ServeJSONRPC()
	t.Cleanup(rpc.Close)

	blockKind := []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK}
	unchanged := &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: 1, SupportedKinds: blockKind}
	var current atomic.Pointer[gen.GetHostEventsResponse]
	current.Store(unchanged)
	srv := &feedHostEvents{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return current.Load(), nil
	})

	before := rejectedBlocks(t)
	src, p, cancel := startGatewayVerifierPoller(t, rpc.URL, dialFeed(t, srv))
	defer cancel()

	current.Store(&gen.GetHostEventsResponse{
		Generation: 1, NextCursor: 5, SupportedKinds: blockKind,
		Events: dapiBlockEvents(1, c.Page(95, 99)),
	})
	require.Eventually(t, func() bool {
		for h := uint64(95); h <= 99; h++ {
			rec, ok := src.StoredAt(h)
			if !ok || rec.Origin != headertime.OriginDAPI || !rec.Verified {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond, "catch-up below the trusted tip is stored from dapi")

	c.Grow(103)
	current.Store(&gen.GetHostEventsResponse{
		Generation: 1, NextCursor: 8, SupportedKinds: blockKind,
		Events: dapiBlockEvents(6, c.Page(101, 103)),
	})
	require.Eventually(t, func() bool {
		for h := uint64(101); h <= 103; h++ {
			rec, ok := src.StoredAt(h)
			if !ok || !rec.Verified {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "a live page above the trusted tip is stored verified")

	c.Grow(104)
	forged := c.Page(104, 104)
	forged[0] = observer.BlockOf(c.Forged(104))
	current.Store(&gen.GetHostEventsResponse{
		Generation: 1, NextCursor: 9, SupportedKinds: blockKind,
		Events: dapiBlockEvents(9, forged),
	})
	require.Eventually(t, func() bool {
		return rejectedBlocks(t) >= before+1 && !p.BlockFeedLive()
	}, 5*time.Second, 10*time.Millisecond)
	if rec, ok := src.StoredAt(104); ok {
		require.NotEqual(t, headertime.OriginDAPI, rec.Origin, "the forged dapi header does not win")
	}
	require.Equal(t, before+1, rejectedBlocks(t))
}
