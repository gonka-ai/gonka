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

func TestPeerAuth_EarlierTokenKeepsWatching(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: time.Hour})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("late-watch-nonce-aaaaaaaaa"))
	second := attach(t, client, signer, []byte("late-watch-nonce-bbbbbbbbb"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), first.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), "an earlier token stays a live Watch: %v", stream.Err())
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}
