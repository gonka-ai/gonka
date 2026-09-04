package hostevents_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"common/nodemanager/gen"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"devshard/hostevents"
)

type recordingSink struct {
	mu        sync.Mutex
	warmed    []string
	settled   []string
	rehydrate int
}

func (s *recordingSink) WarmEscrow(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warmed = append(s.warmed, id)
	return nil
}

func (s *recordingSink) OnEscrowSettled(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settled = append(s.settled, id)
	return nil
}

func (s *recordingSink) RehydrateOpenEscrows() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rehydrate++
}

func (s *recordingSink) snapshot() (warmed, settled []string, rehydrate int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.warmed...), append([]string(nil), s.settled...), s.rehydrate
}

type fakeHostEventsServer struct {
	gen.UnimplementedNodeManagerServer
	mu       sync.Mutex
	handlers []func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)
	sticky   func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)
}

func (s *fakeHostEventsServer) SetHandlers(hs ...func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append([]func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)(nil), hs...)
}

func (s *fakeHostEventsServer) SetSticky(h func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sticky = h
}

func (s *fakeHostEventsServer) GetHostEvents(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
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

func dialFake(t *testing.T, srv *fakeHostEventsServer) gen.NodeManagerClient {
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

func TestRun_RoutesCreatedAndSettled(t *testing.T) {
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		require.NotEmpty(t, req.GetSubscribe())
		return &gen.GetHostEventsResponse{
			Generation: 1,
			NextCursor: 2,
			Events: []*gen.HostEvent{
				{
					Seq:    1,
					Kind:   gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
					Escrow: &gen.EscrowPayload{EscrowId: 42},
				},
				{
					Seq:    2,
					Kind:   gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
					Escrow: &gen.EscrowPayload{EscrowId: 42},
				},
			},
		}, nil
	})
	client := dialFake(t, srv)
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.Run(ctx, hostevents.Config{
			Client:          client,
			ServerMaxWait:   100 * time.Millisecond,
			ErrorBackoffMin: 10 * time.Millisecond,
			ErrorBackoffMax: 20 * time.Millisecond,
		}, sink)
	}()

	require.Eventually(t, func() bool {
		w, s, _ := sink.snapshot()
		return len(w) == 1 && len(s) == 1 && w[0] == "42" && s[0] == "42"
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	<-done
}

func TestRun_NeedsResetRehydrates(t *testing.T) {
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			NeedsReset: true,
			Generation: 9,
			NextCursor: 0,
		}, nil
	})
	client := dialFake(t, srv)
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.Run(ctx, hostevents.Config{
			Client:        client,
			ServerMaxWait: 50 * time.Millisecond,
		}, sink)
	}()

	require.Eventually(t, func() bool {
		_, _, n := sink.snapshot()
		return n >= 1
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	<-done
}

func TestRun_UnimplementedStops(t *testing.T) {
	srv := &fakeHostEventsServer{}
	srv.SetHandlers(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return nil, status.Error(codes.Unimplemented, "method GetHostEvents not implemented")
	})
	client := dialFake(t, srv)
	sink := &recordingSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	hostevents.Run(ctx, hostevents.Config{
		Client:        client,
		ServerMaxWait: time.Second,
	}, sink)
	require.Less(t, time.Since(start), 1500*time.Millisecond)
	w, s, r := sink.snapshot()
	require.Empty(t, w)
	require.Empty(t, s)
	require.Zero(t, r)
}

func TestRun_UpdatesLoadMapOnUnchanged(t *testing.T) {
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(context.Context, *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		return &gen.GetHostEventsResponse{
			Unchanged:  true,
			Generation: 1,
			NextCursor: 0,
			EscrowLoad: []*gen.EscrowLoad{
				{EscrowId: 7, RequestsPerMin: 0.5},
				{EscrowId: 8, RequestsPerMin: 1.25},
			},
		}, nil
	})
	client := dialFake(t, srv)
	loadMap := hostevents.NewLoadMap()
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.Run(ctx, hostevents.Config{
			Client:        client,
			ServerMaxWait: 50 * time.Millisecond,
			LoadMap:       loadMap,
		}, sink)
	}()

	require.Eventually(t, func() bool {
		m, at := loadMap.Snapshot()
		return !at.IsZero() && len(m) == 2 && m[7] == 0.5 && m[8] == 1.25
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	<-done
}

// flakySettleSink fails OnEscrowSettled failures times before succeeding.
type flakySettleSink struct {
	recordingSink
	failMu   sync.Mutex
	failures int
	attempts int
}

func (s *flakySettleSink) OnEscrowSettled(id string) error {
	s.failMu.Lock()
	s.attempts++
	fail := s.attempts <= s.failures
	s.failMu.Unlock()
	if fail {
		return errors.New("mark settled failed")
	}
	return s.recordingSink.OnEscrowSettled(id)
}

func (s *flakySettleSink) attemptCount() int {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.attempts
}

// settledEventsServer serves one settled event to any cursor below its seq,
// mimicking the ring's "seq > cursor" contract so a rewound cursor redelivers.
// The second return value reports whether the loop has ever polled from a
// cursor at or past the event, i.e. whether it moved on.
func settledEventsServer(escrowID, seq uint64) (*fakeHostEventsServer, func() bool) {
	var (
		mu       sync.Mutex
		advanced bool
	)
	srv := &fakeHostEventsServer{}
	srv.SetSticky(func(_ context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
		cursor := req.GetCursor()
		if cursor >= seq {
			mu.Lock()
			advanced = true
			mu.Unlock()
			return &gen.GetHostEventsResponse{Unchanged: true, Generation: 1, NextCursor: seq}, nil
		}
		return &gen.GetHostEventsResponse{
			Generation: 1,
			NextCursor: seq,
			Events: []*gen.HostEvent{{
				Seq:    seq,
				Kind:   gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
				Escrow: &gen.EscrowPayload{EscrowId: escrowID},
			}},
		}, nil
	})
	return srv, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return advanced
	}
}

// A settlement whose sink call fails must be redelivered, not dropped: the
// long-poll is the only settlement signal a host gets when it missed the chain
// event, so advancing the cursor past a failed write loses it permanently.
func TestRun_RedeliversFailedDispatch(t *testing.T) {
	srv, _ := settledEventsServer(42, 5)
	client := dialFake(t, srv)
	sink := &flakySettleSink{failures: 3}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.Run(ctx, hostevents.Config{
			Client:              client,
			ServerMaxWait:       50 * time.Millisecond,
			ErrorBackoffMin:     5 * time.Millisecond,
			ErrorBackoffMax:     10 * time.Millisecond,
			MaxDispatchAttempts: 10,
		}, sink)
	}()

	require.Eventually(t, func() bool {
		_, settled, _ := sink.snapshot()
		return len(settled) == 1 && settled[0] == "42"
	}, 3*time.Second, 20*time.Millisecond)
	cancel()
	<-done

	require.Equal(t, 4, sink.attemptCount(), "three failures then one success")
}

// One permanently wedged event must not stall the stream forever.
func TestRun_SkipsDispatchAfterMaxAttempts(t *testing.T) {
	srv, movedOn := settledEventsServer(42, 5)
	client := dialFake(t, srv)
	sink := &flakySettleSink{failures: 1 << 30}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostevents.Run(ctx, hostevents.Config{
			Client:              client,
			ServerMaxWait:       50 * time.Millisecond,
			ErrorBackoffMin:     time.Millisecond,
			ErrorBackoffMax:     2 * time.Millisecond,
			MaxDispatchAttempts: 3,
		}, sink)
	}()

	require.Eventually(t, func() bool {
		return sink.attemptCount() >= 3
	}, 3*time.Second, 10*time.Millisecond)

	// Once the bound is hit the loop advances past the event, so the server
	// starts seeing the post-event cursor instead of the rewound one.
	require.Eventually(t, movedOn, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	_, settled, rehydrate := sink.snapshot()
	require.Empty(t, settled, "the event never applied; it was skipped, not silently recorded")
	require.NotZero(t, rehydrate,
		"a skipped settlement is unrecoverable from the ring, so the skip must fall back to a chain sweep")
}
