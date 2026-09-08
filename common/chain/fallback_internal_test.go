package chain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"testing"
	"time"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
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

// onRPC reports whether queries are currently served over CometBFT RPC. An
// aborted probe re-probes immediately, so call counts alone cannot tell a
// retried probe apart from a restored gRPC transport.
func onRPC(c *fallbackConn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usingRPC
}

func TestFallback_CanceledProbeDoesNotRestoreGRPC(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))
	require.Equal(t, 1, direct.calls())
	require.True(t, onRPC(fb))

	// The probe is cut short by its caller, which says nothing about whether
	// gRPC is reachable.
	direct.setErr(status.Error(codes.Canceled, "context canceled"))
	clock.advance(testProbeInterval)
	require.Error(t, invoke(fb))

	assert.Equal(t, 2, direct.calls())
	assert.Equal(t, 1, rpc.calls(), "a probe with no verdict must not retry on RPC")
	assert.True(t, onRPC(fb), "an aborted probe must not restore gRPC")
}

func TestFallback_ProbeWithDoneContextDoesNotRestoreGRPC(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))

	// A bare context error rather than a gRPC status must read the same way.
	direct.setErr(context.Canceled)
	clock.advance(testProbeInterval)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, fb.Invoke(ctx, "/test.Service/Query", &struct{}{}, &struct{}{}))

	assert.Equal(t, 2, direct.calls())
	assert.True(t, onRPC(fb), "an aborted probe must not restore gRPC")
}

func TestFallback_AbortedProbeDoesNotDelayTheNextProbe(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))

	direct.setErr(status.Error(codes.DeadlineExceeded, "timeout"))
	clock.advance(testProbeInterval)
	require.Error(t, invoke(fb))
	require.Equal(t, 2, direct.calls())
	require.True(t, onRPC(fb))

	// The aborted attempt did not consume the probe window, so the next request
	// probes again straight away rather than waiting another whole interval.
	direct.setErr(nil)
	require.NoError(t, invoke(fb))
	assert.Equal(t, 3, direct.calls())
	assert.False(t, onRPC(fb), "a probe that answered restores gRPC")
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

func TestFallback_BothTransportsDownReportsBothErrors(t *testing.T) {
	rpcErr := status.Error(codes.Internal, "abci query failed")
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{err: rpcErr}
	fb, _ := newTestFallback(direct, rpc)

	err := invoke(fb)
	require.Error(t, err)

	// Reporting only the RPC failure hides why the fallback engaged at all, so
	// both causes must stay reachable.
	assert.ErrorIs(t, err, errUnavailable, "the original gRPC failure must survive")
	assert.ErrorIs(t, err, rpcErr, "the RPC failure must survive")
	assert.Contains(t, err.Error(), "/test.Service/Query")

	// status.FromError unwraps to the first cause, so a node that answers on
	// neither transport reports Unavailable rather than the RPC-side code.
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, err.Error(), "abci query failed")
}

func TestFallback_FailedProbeWithDeadRPCReportsBothErrors(t *testing.T) {
	rpcErr := status.Error(codes.Internal, "abci query failed")
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))

	rpc.setErr(rpcErr)
	clock.advance(testProbeInterval)
	err := invoke(fb)
	require.Error(t, err)

	assert.ErrorIs(t, err, errUnavailable)
	assert.ErrorIs(t, err, rpcErr)
}

func TestFallback_SuccessfulRPCRetryHidesTheGRPCError(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	// Callers must not see an error for a request the fallback served.
	require.NoError(t, invoke(fb))
}

// logCounter counts records emitted at a given level, to assert that a
// transition is announced once rather than once per racing request.
type logCounter struct {
	mu    sync.Mutex
	level slog.Level
	count int
}

func (h *logCounter) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCounter) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level == h.level {
		h.count++
	}
	return nil
}

func (h *logCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCounter) WithGroup(string) slog.Handler      { return h }

func (h *logCounter) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func TestFallback_ConcurrentFailuresAnnounceFallbackOnce(t *testing.T) {
	counter := &logCounter{level: slog.LevelWarn}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })

	release := make(chan struct{})
	direct := &stubConn{err: errUnavailable, block: release}
	rpc := &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	const concurrent = 8
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			_ = invoke(fb)
		}()
	}

	// Hold every request inside the gRPC call so they all routed while
	// usingRPC was still false, then let them fail together and race on the
	// transition.
	require.Eventually(t, func() bool { return direct.calls() == concurrent },
		time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, concurrent, direct.calls(), "every request tries gRPC before the switch")
	assert.Equal(t, concurrent, rpc.calls(), "and every request is then served over RPC")
	assert.Equal(t, 1, counter.total(), "the transition must be logged once, not once per request")
}

func TestFallback_EveryProbeIntervalReannouncesTheDegradation(t *testing.T) {
	counter := &logCounter{level: slog.LevelWarn}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })

	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, clock := newTestFallback(direct, rpc)

	require.NoError(t, invoke(fb))

	// Announcing only the transition would leave a service that has been on
	// RPC for a week looking exactly like one that never fell back at all.
	for range 2 {
		clock.advance(testProbeInterval)
		require.NoError(t, invoke(fb))
	}

	assert.Equal(t, 3, counter.total(), "the transition plus one line per failed probe")
}

// txServiceClient is the client any caller would build on QueryConn if they
// mistook it for a general-purpose conn. Driving the test through it rather than
// the method string checks the guard against the name the SDK actually emits.
func txServiceClient(conn grpc.ClientConnInterface) txtypes.ServiceClient {
	return txtypes.NewServiceClient(conn)
}

func TestFallback_RejectsTxBroadcastOnBothTransports(t *testing.T) {
	direct, rpc := &stubConn{}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	_, err := txServiceClient(fb).BroadcastTx(context.Background(), &txtypes.BroadcastTxRequest{})
	require.Error(t, err)

	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "Client.Conn()")
	assert.Zero(t, direct.calls(), "a broadcast must not reach either transport")
	assert.Zero(t, rpc.calls())
}

func TestUnorderedTxConn_BroadcastUsesSelectedRPCTransport(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)
	client := &Client{conn: direct, queryConn: fb}
	conn := client.UnorderedTxConn()

	// Account/GetTx queries select RPC before the unordered broadcast.
	require.NoError(t, conn.Invoke(context.Background(), "/cosmos.auth.v1beta1.Query/Account", &struct{}{}, &struct{}{}))
	_, err := txServiceClient(conn).BroadcastTx(context.Background(), &txtypes.BroadcastTxRequest{})
	require.NoError(t, err)

	assert.Equal(t, 1, direct.calls(), "broadcast must not retry the unavailable gRPC transport")
	assert.Equal(t, 2, rpc.calls(), "one query and one broadcast must use RPC")
}

func TestUnorderedTxConn_DoesNotRetryFailedBroadcast(t *testing.T) {
	direct, rpc := &stubConn{err: errUnavailable}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)
	client := &Client{conn: direct, queryConn: fb}

	_, err := txServiceClient(client.UnorderedTxConn()).BroadcastTx(
		context.Background(), &txtypes.BroadcastTxRequest{})
	require.ErrorIs(t, err, errUnavailable)
	assert.Equal(t, 1, direct.calls())
	assert.Zero(t, rpc.calls(), "an ambiguous broadcast error must never trigger a second submission")
}

func TestFallback_TxServiceQueriesStillRoute(t *testing.T) {
	direct, rpc := &stubConn{}, &stubConn{}
	fb, _ := newTestFallback(direct, rpc)

	// Only broadcasting is barred. GetTx and friends are ordinary queries and
	// must keep working, fallback included.
	_, err := txServiceClient(fb).GetTx(context.Background(), &txtypes.GetTxRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, direct.calls())
}

func TestQueryConn_RejectsBroadcastEvenWhenGRPCIsHealthy(t *testing.T) {
	c, err := NewWithRPCFallback("localhost:9090", "http://localhost:26657")
	require.NoError(t, err)

	// Failing closed from the first call is the point: a broadcast that worked
	// until the day gRPC dropped would submit the same signed transaction on
	// both transports exactly when nobody is watching.
	_, err = txServiceClient(c.QueryConn()).BroadcastTx(context.Background(), &txtypes.BroadcastTxRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
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
	// observability.ObservedConn is a struct value rather than a pointer, so
	// equality is the identity check available here.
	assert.Equal(t, c.Conn(), fallback.direct, "the fallback must probe the conn txs use")
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
