package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/httpguard"
	devtest "devshard/internal/testutil"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

func TestRPCClient_GetSignaturesGRPCOnH2(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	want := map[uint32][]byte{1: []byte("sig-grpc")}
	obs := newWireObserver()
	h2, _ := startPeerRPCServerH2CObserved(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond},
		sigLookup{sigs: want}, obs)
	inf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
	}))
	t.Cleanup(inf.Close)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2.URL},
		GRPC:           true,
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	require.True(t, pc.UsingGRPC())

	rpc := transport.NewRPCClient(transport.NewHTTPClient(inf.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	got, err := rpc.GetSignatures(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, want, got)
	requireGRPCContentType(t, obs.requestContentType(rpcpbconnect.PeerAuthServiceAttachProcedure))
	requireGRPCContentType(t, obs.requestContentType(rpcpbconnect.SessionServiceGetSignaturesProcedure))
}

func TestRPCClient_GetSignaturesGRPCMissFailsClosed(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	obs := newWireObserver()
	inf, _ := startPeerRPCServerObserved(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond},
		sigLookup{sigs: map[uint32][]byte{1: []byte("sig-connect")}}, obs)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: dead.URL},
		GRPC:           true,
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc.Start()
	require.Never(t, func() bool { return pc.Ready() }, time.Second, 20*time.Millisecond)

	rpc := transport.NewRPCClient(transport.NewHTTPClient(inf.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := rpc.GetSignatures(ctx, 7)
	require.Error(t, err)
	require.Empty(t, obs.requestContentType(rpcpbconnect.PeerAuthServiceAttachProcedure))
}

func TestPeerConn_GRPCWithoutH2URLStaysConnect(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	obs := newWireObserver()
	srv, _ := startPeerRPCServerObserved(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond},
		sigLookup{sigs: map[uint32][]byte{1: []byte("x")}}, obs)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{GRPC: true})
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.False(t, pc.UsingGRPC())
	requireConnectContentType(t, obs.requestContentType(rpcpbconnect.PeerAuthServiceAttachProcedure))
}
