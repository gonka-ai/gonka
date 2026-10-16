package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	devtest "devshard/internal/testutil"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestWatchReopen(t *testing.T) {
	require.True(t, watchReopen(nil))
	require.True(t, watchReopen(io.EOF))
	require.True(t, watchReopen(connect.NewError(connect.CodeFailedPrecondition, errors.New("host shutting down"))))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session token"))))
	require.False(t, watchReopen(errWatchStale))
	require.False(t, watchReopen(connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))))
	require.False(t, watchReopen(errWatchStreamCap))
}

func TestServeWatch_LocalStreamCapWaits(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		HostAddress: "host",
		BackoffMin:  15 * time.Millisecond,
		BackoffMax:  time.Second,
		WatchStale:  time.Minute,
	})
	t.Cleanup(pc.Close)
	pc.streams.apply(&rpcpb.RateLimits{MaxStreams: 1})
	require.True(t, pc.acquireChatStream())

	done := make(chan error, 1)
	go func() {
		_, err := pc.serveWatch([]byte("token-bytes-012345"), time.Now().Add(time.Hour))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a full local stream slot must wait, not end Watch: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	pc.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveWatch did not return after Close")
	}
}

type watchCapAuth struct {
	rpcpbconnect.UnimplementedPeerAuthServiceHandler
	watches    *atomic.Int32
	attaches   *atomic.Int32
	retryAfter string
	mu         *sync.Mutex
	watchAt    *[]time.Time
	attachGate *atomic.Int32
}

func (a watchCapAuth) Attach(context.Context, *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	if a.attaches != nil {
		a.attaches.Add(1)
	}
	if a.attachGate != nil {
		for a.attachGate.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: []byte("token-bytes-renewed"),
		ExpiresAt:    time.Now().Add(5 * time.Second).Unix(),
	}), nil
}

func (a watchCapAuth) Watch(context.Context, *connect.Request[rpcpb.WatchRequest], *connect.ServerStream[rpcpb.SessionEvent]) error {
	a.watches.Add(1)
	if a.mu != nil {
		a.mu.Lock()
		*a.watchAt = append(*a.watchAt, time.Now())
		a.mu.Unlock()
	}
	if a.attachGate != nil && a.attachGate.Load() == 0 {
		time.Sleep(50 * time.Millisecond)
		defer a.attachGate.Add(1)
	}
	err := connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent watches"))
	if a.retryAfter != "" {
		err.Meta().Set("Retry-After", a.retryAfter)
	}
	return err
}

func startWatchCapServer(t *testing.T, auth watchCapAuth) *httptest.Server {
	t.Helper()
	path, h := rpcpbconnect.NewPeerAuthServiceHandler(auth)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func secondWatchGap(t *testing.T, mu *sync.Mutex, at *[]time.Time) time.Duration {
	t.Helper()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*at) >= 2
	}, 3*time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return (*at)[1].Sub((*at)[0])
}

func TestIsServerWatchCap(t *testing.T) {
	require.True(t, isServerWatchCap(connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent watches"))))
	require.False(t, isServerWatchCap(connect.NewError(connect.CodeResourceExhausted, errors.New("too many attach attempts"))))
	require.False(t, isServerWatchCap(connect.NewError(connect.CodeUnauthenticated, errors.New("too many concurrent watches"))))
	require.False(t, isServerWatchCap(nil))
}

func TestServeWatch_ServerWatchCapKeepsSession(t *testing.T) {
	var watches atomic.Int32
	srv := startWatchCapServer(t, watchCapAuth{watches: &watches})

	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     srv.URL,
		HostAddress: "host",
		DirectMux:   true,
		BackoffMin:  10 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		WatchStale:  time.Minute,
	})
	t.Cleanup(pc.Close)

	done := make(chan error, 1)
	go func() {
		_, err := pc.serveWatch([]byte("token-bytes-012345"), time.Now().Add(time.Hour))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a server Watch cap must keep the session and retry Watch, got %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	require.GreaterOrEqual(t, watches.Load(), int32(2))
	pc.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveWatch did not return after Close")
	}
}

func TestServeWatch_ServerWatchCapHonorsRetryAfterAcrossJitter(t *testing.T) {
	var watches atomic.Int32
	var mu sync.Mutex
	var at []time.Time
	srv := startWatchCapServer(t, watchCapAuth{watches: &watches, retryAfter: "1", mu: &mu, watchAt: &at})

	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     srv.URL,
		HostAddress: "host",
		DirectMux:   true,
		BackoffMin:  10 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		WatchStale:  time.Minute,
		Jitter:      func(d time.Duration) time.Duration { return d / 2 },
	})
	t.Cleanup(pc.Close)
	go func() { _, _ = pc.serveWatch([]byte("token-bytes-012345"), time.Now().Add(time.Hour)) }()

	require.GreaterOrEqual(t, secondWatchGap(t, &mu, &at), 950*time.Millisecond)
}

func TestServeWatch_RenewalWaitsForWatchRetryAfter(t *testing.T) {
	var watches, attaches, refused atomic.Int32
	var mu sync.Mutex
	var at []time.Time
	srv := startWatchCapServer(t, watchCapAuth{watches: &watches, attaches: &attaches, retryAfter: "1", mu: &mu, watchAt: &at, attachGate: &refused})

	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      srv.URL,
		HostAddress:  "host",
		DoorEscrowID: "9801",
		Signer:       devtest.MustGenerateKey(t),
		DirectMux:    true,
		BackoffMin:   10 * time.Millisecond,
		BackoffMax:   20 * time.Millisecond,
		WatchStale:   time.Minute,
		MinTTL:       time.Second,
	})
	t.Cleanup(pc.Close)
	go func() { _, _ = pc.serveWatch([]byte("token-bytes-012345"), time.Now()) }()

	require.Eventually(t, func() bool { return attaches.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, secondWatchGap(t, &mu, &at), 950*time.Millisecond)
}
