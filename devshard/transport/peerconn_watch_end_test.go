package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// beatThenReplacedAuth sends two beats and then ends Watch with session
// replaced, the order a stolen Attach produces on a live stream.
type beatThenReplacedAuth struct{}

func (beatThenReplacedAuth) Attach(context.Context, *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("attach"))
}

func (beatThenReplacedAuth) Watch(_ context.Context, _ *connect.Request[rpcpb.WatchRequest], stream *connect.ServerStream[rpcpb.SessionEvent]) error {
	for i := 0; i < 2; i++ {
		if err := stream.Send(&rpcpb.SessionEvent{
			Event: &rpcpb.SessionEvent_Beat{Beat: &rpcpb.Heartbeat{UnixSeconds: time.Now().Unix()}},
		}); err != nil {
			return err
		}
	}
	return connect.NewError(connect.CodeUnauthenticated, errors.New("session replaced"))
}

func TestWatch_EndBehindQueuedBeatIsDelivered(t *testing.T) {
	path, h := rpcpbconnect.NewPeerAuthServiceHandler(beatThenReplacedAuth{})
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     srv.URL,
		HostAddress: "host",
		DirectMux:   true,
		WatchStale:  time.Minute,
	})
	t.Cleanup(pc.Close)

	// The reader holds the first beat until the receive goroutine has
	// queued the second beat and handed off the stream end behind it.
	recvExited := make(chan struct{})
	testWatchRecvExit = func(c *PeerConn) {
		if c == pc {
			close(recvExited)
		}
	}
	t.Cleanup(func() { testWatchRecvExit = nil })
	var first sync.Once
	onBeat := func() { first.Do(func() { <-recvExited }) }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- pc.watch(ctx, []byte("token-bytes-012345"), onBeat) }()

	select {
	case err := <-done:
		require.True(t, peerSessionReplaced(err), "watch returned %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Watch lost session replaced behind a queued beat and waits for WatchStale")
	}
}
