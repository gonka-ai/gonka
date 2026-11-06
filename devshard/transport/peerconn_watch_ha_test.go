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

func TestPeerConn_SecondAttachKeepsBothLive(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	label := directMuxPeer(hostAddr)
	before := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))

	firstConn := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	firstConn.Start()
	first := append([]byte(nil), waitPeerReady(t, firstConn)...)
	secondConn := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	secondConn.Start()
	second := append([]byte(nil), waitPeerReady(t, secondConn)...)
	require.NotEqual(t, string(first), string(second))
	require.Equal(t, before+2, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")))

	stealNonce(t, srv, hostAddr, peer, []byte("steal-nonce-one-aaaa"))
	time.Sleep(400 * time.Millisecond)
	require.True(t, firstConn.Ready())
	require.True(t, secondConn.Ready())
	require.Equal(t, string(first), string(firstConn.LiveToken()))
	require.Equal(t, string(second), string(secondConn.LiveToken()))
	require.Equal(t, before+2, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"a second Attach must not make a live process Attach again")
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
	require.Eventually(t, func() bool {
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer waitCancel()
		return rpc.WaitReady(waitCtx) != nil && !transport.PeerConnRegistered(hostAddr, "direct")
	}, 3*time.Second, 20*time.Millisecond, "the release flag cancels the conn and drops the registry entry")
	time.Sleep(400 * time.Millisecond)
	require.Equal(t, ok, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"the retiring generation must not Attach again")
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

	ok := testutil.ToFloat64(observability.PeerAttachCounter(label, "ok"))
	transport.MarkOutboundPeersReleasedForTest()
	require.Eventually(t, func() bool { return !pc.Ready() }, 3*time.Second, 10*time.Millisecond,
		"the release flag stops the conn")
	time.Sleep(400 * time.Millisecond)
	require.False(t, pc.Ready())
	require.Equal(t, ok, testutil.ToFloat64(observability.PeerAttachCounter(label, "ok")),
		"the retiring conn must not Attach again")
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
