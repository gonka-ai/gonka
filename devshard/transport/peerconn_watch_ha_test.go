package transport_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	devtest "devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

func TestPeerConn_WatchShutdownReopensWithoutAttach(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, auth := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	label := directMuxPeer(hostAddr)
	before := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	tok := waitPeerReady(t, pc)
	require.Equal(t, before+1, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")))

	auth.Close()
	time.Sleep(400 * time.Millisecond)

	require.True(t, pc.Ready(), "shutting down must keep the token")
	require.Equal(t, string(tok), string(pc.LiveToken()))
	require.Equal(t, before+1, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"reopening Watch must not Attach")
}

func TestPeerConn_RefreshWhileWatchDown(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, auth := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 4 * time.Second,
		TokenGrace: 5 * time.Second,
	}, nil)
	label := directMuxPeer(hostAddr)
	failedBefore := testutil.ToFloat64(observability.PeerAttachCounter(label, "failed_precondition"))

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{
		MinTTL: time.Second,
	})
	pc.Start()
	tok := waitPeerReady(t, pc)
	auth.Close()

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(observability.PeerAttachCounter(label, "failed_precondition")) > failedBefore
	}, 8*time.Second, 20*time.Millisecond, "refresh must fire while Watch is down")
	require.True(t, pc.Ready())
	require.Equal(t, string(tok), string(pc.LiveToken()), "a failed refresh keeps the token")
}

func TestPeerConn_SessionReplacedTwiceReleases(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	label := directMuxPeer(hostAddr)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	first := waitPeerReady(t, pc)

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-one-aaaa"))
	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond, "the first session replaced is retried")
	time.Sleep(150 * time.Millisecond)

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-two-bbbb"))
	require.Eventually(t, func() bool {
		return !pc.Ready()
	}, 3*time.Second, 10*time.Millisecond, "the second session replaced releases the conn")

	ok := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))
	time.Sleep(400 * time.Millisecond)
	require.False(t, pc.Ready())
	require.Equal(t, ok, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"release must not Attach again")
}

func stealNonce(t *testing.T, srv *httptest.Server, hostAddr string, signer signing.Signer, nonce []byte) {
	t.Helper()
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, hostAddr, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     hostAddr,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
}
