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

func TestPeerConn_SessionReplacedTwiceKeepsLiveConn(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	label := directMuxPeer(hostAddr)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	first := waitPeerReady(t, pc)

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-one-aaaa"))
	var second []byte
	require.Eventually(t, func() bool {
		second = pc.LiveToken()
		return pc.Ready() && len(second) > 0 && string(second) != string(first)
	}, 3*time.Second, 10*time.Millisecond, "the first session replaced is retried")

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-two-bbbb"))
	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(second)
	}, 3*time.Second, 10*time.Millisecond, "a live process Attaches again after a second replacement")
	require.Greater(t, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")), float64(2))
}

func TestPeerConn_ReleasedSessionReplacedTwiceDropsRegistry(t *testing.T) {
	transport.ResetOutboundPeerReleaseForTest()
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	httpClient := transport.NewHTTPClient(srv.URL, "escrow-1", peer)
	pc := transport.AcquirePeerConnForTest(transport.PeerConnConfig{
		BaseURL:      srv.URL,
		HostAddress:  hostAddr,
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	rpc := transport.NewRPCClient(httpClient, pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	t.Cleanup(rpc.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, rpc.WaitReady(ctx))
	require.True(t, transport.PeerConnRegistered(hostAddr, "direct"))

	transport.MarkOutboundPeersReleasedForTest()
	label := directMuxPeer(hostAddr)
	ok := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))
	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-one-aaaa"))
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")) > ok && pc.Ready()
	}, 3*time.Second, 10*time.Millisecond, "the first loss while retiring is retried")

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-two-bbbb"))
	require.Eventually(t, func() bool {
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer waitCancel()
		return rpc.WaitReady(waitCtx) != nil && !transport.PeerConnRegistered(hostAddr, "direct")
	}, 3*time.Second, 20*time.Millisecond, "the second loss cancels the conn and drops the registry entry")
}

func TestPeerConn_HealthyWatchResetsReplacedCount(t *testing.T) {
	transport.ResetOutboundPeerReleaseForTest()
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	label := directMuxPeer(hostAddr)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	// Longer than sessionReplacedHealthyWatch so this Watch clears the count.
	time.Sleep(2500 * time.Millisecond)

	transport.MarkOutboundPeersReleasedForTest()
	first := pc.LiveToken()
	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-after-healthy"))
	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond, "one loss after a healthy Watch is retried")

	ok := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))
	second := pc.LiveToken()
	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-second-quick"))
	require.Eventually(t, func() bool { return !pc.Ready() }, 3*time.Second, 10*time.Millisecond,
		"a second quick loss while retiring stops the conn")
	time.Sleep(400 * time.Millisecond)
	require.False(t, pc.Ready())
	require.Equal(t, ok, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"the retiring conn must not Attach again")
	require.NotEqual(t, string(second), "")
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
