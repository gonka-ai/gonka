package rpcserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestPeerAuth_WatchOnReplacedTokenIsRefused(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: time.Hour})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("late-watch-nonce-aaaaaaaaa"))
	attach(t, client, signer, []byte("late-watch-nonce-bbbbbbbbb"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), first.SessionToken))
	require.NoError(t, err)
	require.False(t, stream.Receive(), "a Watch opened after its token was replaced must not beat")
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(stream.Err()))
	require.Contains(t, stream.Err().Error(), "session replaced")
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "grace token still admits unaries")
}
