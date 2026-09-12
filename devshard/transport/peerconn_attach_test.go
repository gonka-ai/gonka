package transport_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func startPeerRPCServer(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, lookup rpcserver.SessionLookup) (*httptest.Server, *rpcserver.PeerAuthHandler) {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(lookup))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	return srv, auth
}

func newTestPeerConn(t *testing.T, srv *httptest.Server, hostAddr string, signer signing.Signer, cfg transport.PeerConnConfig) *transport.PeerConn {
	t.Helper()
	cfg.BaseURL = srv.URL
	cfg.HostAddress = hostAddr
	cfg.Signer = signer
	cfg.DirectMux = true
	cfg.DoorEscrowID = "escrow-1"
	if cfg.WatchStale == 0 {
		cfg.WatchStale = time.Minute
	}
	pc := transport.NewPeerConn(cfg)
	t.Cleanup(pc.Close)
	return pc
}

func waitPeerReady(t *testing.T, pc *transport.PeerConn) []byte {
	t.Helper()
	var tok []byte
	require.Eventually(t, func() bool {
		tok = pc.LiveToken()
		return pc.Ready() && len(tok) > 0
	}, 3*time.Second, 10*time.Millisecond)
	return tok
}

// directMuxPeer is the Prometheus peer label for DirectMux tests (addr@direct).
func directMuxPeer(hostAddr string) string {
	return hostAddr + "@direct"
}

func stealSession(t *testing.T, srv *httptest.Server, hostAddr string, signer signing.Signer) {
	t.Helper()
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("stolen-attach-nonce-012345")
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

func TestPeerConn_AttachLoopHappyPath(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

	before := testutil.ToFloat64(observability.PeerAttachCounter(directMuxPeer(hostAddr), "ok"))
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	tok := waitPeerReady(t, pc)
	require.NotEmpty(t, tok)
	require.Equal(t, observability.PeerSessionReady, pc.State())
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerAttachCounter(directMuxPeer(hostAddr), "ok"))-before)
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerSessionStateGauge(directMuxPeer(hostAddr), observability.PeerSessionReady)))
}

func TestPeerConn_ReconnectOnWatchClose(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond, SessionTTL: time.Minute}, nil)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	before := testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "watch"))

	stealSession(t, srv, hostAddr, peer)

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "watch")), before)
}

func TestPeerConn_TokenRefresh(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	// expires_at is unix seconds. A sub-second SessionTTL truncates remaining
	// TTL to 0 on the client and re-attaches immediately, which drops the
	// grace token on the second replace. Keep TTL in whole seconds so 75%
	// refresh happens while the predecessor is still inside TokenGrace.
	srv, auth := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 4 * time.Second,
		TokenGrace: 5 * time.Second,
	}, nil)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{WatchStale: time.Minute})
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 8*time.Second, 20*time.Millisecond)
	second := pc.LiveToken()
	require.NotEqual(t, first, second)
	_, ok := auth.LookupToken(first)
	require.True(t, ok, "old token must still admit for TokenGrace")
	_, ok = auth.LookupToken(second)
	require.True(t, ok)
	require.Greater(t, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl")), 0.0)
}

func TestPeerConn_RefreshAttachFailureKeepsWatch(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 4 * time.Second,
		TokenGrace: 5 * time.Second,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	var attachN atomic.Int32
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PeerAuthService/Attach") {
			n := attachN.Add(1)
			if n == 2 {
				ew := connect.NewErrorWriter()
				_ = ew.Write(w, r, connect.NewError(connect.CodeUnavailable, errors.New("refresh down")))
				return
			}
			if n >= 3 {
				select {
				case <-released:
				case <-r.Context().Done():
					return
				}
			}
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	t.Cleanup(release)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{
		WatchStale: time.Minute,
		BackoffMin: 50 * time.Millisecond,
	})
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	ttlBefore := testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl"))

	require.Eventually(t, func() bool {
		return attachN.Load() >= 2
	}, 8*time.Second, 10*time.Millisecond)
	require.True(t, pc.Ready(), "failed refresh must not drop to unauthenticated")
	require.Equal(t, first, pc.LiveToken(), "token A stays until B is published")
	require.Equal(t, ttlBefore, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl")),
		"failed refresh is incAttach only, not reattach_total{ttl}")
	release()

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 8*time.Second, 20*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl")), ttlBefore)
}

func TestPeerConn_PastExpiresAtDoesNotTightLoop(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := &pastExpiryAuth{}
	path, h := rpcpbconnect.NewPeerAuthServiceHandler(auth)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{
		BackoffMin: 50 * time.Millisecond,
		BackoffMax: 5 * time.Second,
		Jitter:     func(d time.Duration) time.Duration { return d },
	})
	pc.Start()
	require.Never(t, pc.Ready, 200*time.Millisecond, 10*time.Millisecond)
	n := auth.n.Load()
	require.Greater(t, n, int32(0))
	require.Less(t, n, int32(8), "bogus expires_at must backoff, not tight-loop Attach")
}

type pastExpiryAuth struct {
	n atomic.Int32
}

func (a *pastExpiryAuth) Attach(_ context.Context, req *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	a.n.Add(1)
	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: append([]byte(nil), req.Msg.GetAttachNonce()...),
		ExpiresAt:    time.Now().Unix() - 60,
	}), nil
}

func (a *pastExpiryAuth) Watch(context.Context, *connect.Request[rpcpb.WatchRequest], *connect.ServerStream[rpcpb.SessionEvent]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("watch"))
}

func TestPeerConn_AttachReadMaxBytes(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := &oversizedAttachAuth{}
	path, h := rpcpbconnect.NewPeerAuthServiceHandler(auth)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{
		BackoffMin: 50 * time.Millisecond,
		Jitter:     func(d time.Duration) time.Duration { return d },
	})
	pc.Start()
	require.Never(t, pc.Ready, 200*time.Millisecond, 10*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(observability.PeerAttachCounter(directMuxPeer(hostAddr), connect.CodeResourceExhausted.String())), 0.0)
}

type oversizedAttachAuth struct{}

func (a *oversizedAttachAuth) Attach(_ context.Context, _ *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: make([]byte, transport.DefaultRPCReadMaxBytes+1),
		ExpiresAt:    time.Now().Add(time.Minute).Unix(),
	}), nil
}

func (a *oversizedAttachAuth) Watch(context.Context, *connect.Request[rpcpb.WatchRequest], *connect.ServerStream[rpcpb.SessionEvent]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("watch"))
}

func TestRPCClient_GetSignaturesRoundTrip(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	want := map[uint32][]byte{1: []byte("sig-a")}
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond},
		sigLookup{sigs: want})
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	httpClient := transport.NewHTTPClient(srv.URL, "escrow-1", peer)
	rpc := transport.NewRPCClient(httpClient, pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	got, err := rpc.GetSignatures(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestRPCClient_GetSignaturesUnauthenticatedFailsFast(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(sigLookup{sigs: map[uint32][]byte{1: []byte("x")}}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "GetSignatures") {
			ew := connect.NewErrorWriter()
			_ = ew.Write(w, r, connect.NewError(connect.CodeUnauthenticated, errors.New("stale")))
			return
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	start := time.Now()
	_, err := rpc.GetSignatures(context.Background(), 1)
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second, "stable Unauthenticated must not spend the 5s budget")
}

func TestPeerConn_DoesNotFollowRedirect(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var hitDest atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitDest.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(dest.Close)

	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(sigLookup{sigs: map[uint32][]byte{1: []byte("x")}}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "GetSignatures") {
			http.Redirect(w, r, dest.URL, http.StatusFound)
			return
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)

	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	_, err := rpc.GetSignatures(context.Background(), 1)
	require.Error(t, err)
	require.False(t, hitDest.Load(), "X-Devshard-Session must not follow a 302")
}

func TestRPCClient_GetSignaturesReadMaxBytes(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	oversized := map[uint32][]byte{1: make([]byte, transport.DefaultRPCReadMaxBytes+1)}
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond},
		sigLookup{sigs: oversized})
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	cfg := transport.DefaultClientConfig()
	cfg.QueryTimeout = 300 * time.Millisecond
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer, cfg), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	_, err := rpc.GetSignatures(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

type sigLookup struct {
	sigs map[uint32][]byte
}

func (s sigLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return sigCore{sigs: s.sigs}, nil
}

type sigCore struct {
	sigs map[uint32][]byte
}

func (s sigCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) { return s.sigs, nil }
func (s sigCore) AllowsSender(string) bool                             { return true }
