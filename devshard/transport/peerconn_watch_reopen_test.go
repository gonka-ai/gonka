package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

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
	watches *atomic.Int32
}

func (a watchCapAuth) Watch(context.Context, *connect.Request[rpcpb.WatchRequest], *connect.ServerStream[rpcpb.SessionEvent]) error {
	a.watches.Add(1)
	return connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent watches"))
}

func TestIsServerWatchCap(t *testing.T) {
	require.True(t, isServerWatchCap(connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent watches"))))
	require.False(t, isServerWatchCap(connect.NewError(connect.CodeResourceExhausted, errors.New("too many attach attempts"))))
	require.False(t, isServerWatchCap(connect.NewError(connect.CodeUnauthenticated, errors.New("too many concurrent watches"))))
	require.False(t, isServerWatchCap(nil))
}

func TestServeWatch_ServerWatchCapKeepsSession(t *testing.T) {
	var watches atomic.Int32
	path, h := rpcpbconnect.NewPeerAuthServiceHandler(watchCapAuth{watches: &watches})
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

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
