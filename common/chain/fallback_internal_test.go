package chain

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testProbeInterval = 30 * time.Minute

// stubConn records the methods invoked on it and returns a scripted error.
type stubConn struct {
	mu      sync.Mutex
	methods []string
	err     error
	// block, when non-nil, holds every Invoke until it is closed.
	block chan struct{}
}

func (s *stubConn) Invoke(_ context.Context, method string, _, _ any, _ ...grpc.CallOption) error {
	s.mu.Lock()
	s.methods = append(s.methods, method)
	block := s.block
	err := s.err
	s.mu.Unlock()

	if block != nil {
		<-block
	}
	return err
}

func (s *stubConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	s.mu.Lock()
	s.methods = append(s.methods, "stream")
	s.mu.Unlock()
	return nil, s.err
}

func (s *stubConn) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *stubConn) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.methods)
}

// fakeClock is a manually advanced clock for probe-interval assertions.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestFallback(direct, rpc *stubConn) (*fallbackConn, *fakeClock) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	return newFallbackConn(direct, rpc, testProbeInterval, clock.Now), clock
}

func invoke(c *fallbackConn) error {
	return c.Invoke(context.Background(), "/test.Service/Query", &struct{}{}, &struct{}{})
}

var errUnavailable = status.Error(codes.Unavailable, "connection refused")

func TestFallback_HealthyGRPCNeverTouchesRPC(t *testing.T) {
	direct, rpc := &stubConn{}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	for range 3 {
		require.NoError(t, invoke(fb))
	}

	assert.Equal(t, 3, direct.calls())
	assert.Zero(t, rpc.calls())
}

func TestFallback_ApplicationErrorsStayOnGRPC(t *testing.T) {
	appErr := status.Error(codes.NotFound, "no such participant")
	direct, rpc := &stubConn{err: appErr}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	require.ErrorIs(t, invoke(fb), appErr)
	require.ErrorIs(t, invoke(fb), appErr)

	assert.Equal(t, 2, direct.calls())
	assert.Zero(t, rpc.calls(), "application errors must not trigger fallback")
}

func TestFallback_DisconnectRetriesSameRequestOnRPC(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb), "the failed request must be retried on RPC")
	assert.Equal(t, 1, direct.calls())
	assert.Equal(t, 1, rpc.calls())
}

func TestFallback_RPCModeIsStickyUntilProbeInterval(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))
	require.Equal(t, 1, direct.calls())

	// Well inside the probe interval: nothing should reach gRPC again.
	clock.advance(testProbeInterval - time.Second)
	for range 5 {
		require.NoError(t, invoke(fb))
	}
	assert.Equal(t, 1, direct.calls())
	assert.Equal(t, 6, rpc.calls())
}

func TestFallback_ProbeRestoresGRPCAfterInterval(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))
	require.Equal(t, 1, direct.calls())

	// gRPC comes back (e.g. the node enabled :9090) and the interval elapses.
	direct.setErr(nil)
	clock.advance(testProbeInterval)

	require.NoError(t, invoke(fb))
	assert.Equal(t, 2, direct.calls())
	assert.Equal(t, 1, rpc.calls(), "the probe succeeded, so no RPC retry")

	// Subsequent requests go straight to gRPC.
	require.NoError(t, invoke(fb))
	assert.Equal(t, 3, direct.calls())
	assert.Equal(t, 1, rpc.calls())
}

func TestFallback_FailedProbeRetriesOnRPCAndResetsInterval(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))
	clock.advance(testProbeInterval)

	// Probe fails: the request still succeeds via RPC.
	require.NoError(t, invoke(fb))
	assert.Equal(t, 2, direct.calls())
	assert.Equal(t, 2, rpc.calls())

	// A fresh interval started, so the next request does not probe again.
	require.NoError(t, invoke(fb))
	assert.Equal(t, 2, direct.calls())
	assert.Equal(t, 3, rpc.calls())
}

func TestFallback_ProbeSucceedsOnApplicationError(t *testing.T) {
	appErr := status.Error(codes.InvalidArgument, "bad epoch")
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))

	// An application error proves the transport is up, so gRPC becomes primary
	// again and the error surfaces to the caller.
	direct.setErr(appErr)
	clock.advance(testProbeInterval)
	require.ErrorIs(t, invoke(fb), appErr)

	require.ErrorIs(t, invoke(fb), appErr)
	assert.Equal(t, 3, direct.calls())
	assert.Equal(t, 1, rpc.calls())
}

func TestFallback_OnlyOneRequestProbesGRPC(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))
	require.Equal(t, 1, direct.calls())
	clock.advance(testProbeInterval)

	// Hold the probe in flight so concurrent callers see probing == true.
	release := make(chan struct{})
	direct.mu.Lock()
	direct.block = release
	direct.mu.Unlock()

	const concurrent = 8
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			_ = invoke(fb)
		}()
	}

	// Give the goroutines time to route before releasing the probe.
	assert.Eventually(t, func() bool { return rpc.calls() >= concurrent-1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	// Exactly one of the concurrent requests probed gRPC; it failed and fell
	// back, so every request still hit RPC once.
	assert.Equal(t, 2, direct.calls())
	assert.Equal(t, concurrent+1, rpc.calls())
}

func TestFallback_StreamsAlwaysUseDirectGRPC(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	// Force RPC mode first.
	require.NoError(t, invoke(fb))

	_, err := fb.NewStream(context.Background(), &grpc.StreamDesc{}, "/test.Service/Stream")
	require.Error(t, err, "RPC/ABCI cannot serve streams, so the direct error surfaces")
	assert.Equal(t, 1, rpc.calls(), "the stream must not be attempted over RPC")
}

func TestIsTransportDown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", errUnavailable, true},
		{"not found", status.Error(codes.NotFound, "missing"), false},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad request"), false},
		{"canceled", status.Error(codes.Canceled, "context canceled"), false},
		{"deadline exceeded", status.Error(codes.DeadlineExceeded, "timeout"), false},
		{"eof", io.EOF, true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"wrapped connection refused", errors.New("dial: " + syscall.ECONNREFUSED.Error()), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransportDown(tc.err))
		})
	}
}

func TestNewWithRPCFallback_QueriesUseFallbackTxUsesDirect(t *testing.T) {
	c, err := NewWithRPCFallback("localhost:9090", "http://localhost:26657")
	require.NoError(t, err)

	assert.NotNil(t, c.InferenceQueryClient())
	assert.NotNil(t, c.BLSQueryClient())
	assert.NotNil(t, c.RestrictionsQueryClient())
	assert.NotNil(t, c.CometServiceClient())

	fallback, isFallback := c.QueryConn().(*fallbackConn)
	require.True(t, isFallback, "queries must go through the fallback conn")
	_, txIsFallback := c.Conn().(*fallbackConn)
	assert.False(t, txIsFallback, "transactions must stay on direct gRPC")
	assert.Equal(t, c.Conn(), fallback.direct)
	assert.Equal(t, DefaultRPCProbeInterval, fallback.probeInterval)
}

func TestNewWithRPCFallback_RejectsMissingRPCURL(t *testing.T) {
	_, err := NewWithRPCFallback("localhost:9090", "  ")
	require.ErrorContains(t, err, "comet rpc url is required")
}

func TestNewWithRPCFallback_RejectsUnparsableRPCURL(t *testing.T) {
	_, err := NewWithRPCFallback("localhost:9090", "http://[::1")
	require.ErrorContains(t, err, "comet rpc client")
}

func TestNew_QueryConnIsDirectConn(t *testing.T) {
	c, err := New("localhost:9090")
	require.NoError(t, err)
	assert.Equal(t, c.Conn(), c.QueryConn())
}
