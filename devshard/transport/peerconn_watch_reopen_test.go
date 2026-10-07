package transport

import (
	"errors"
	"io"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/transport/rpcpb"
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
