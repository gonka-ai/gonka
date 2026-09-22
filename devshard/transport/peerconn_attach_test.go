package transport_test

import (
	"context"
	"errors"
	"io"
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
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	commonvalidation "common/validation"
	"devshard/heightsync"
	"devshard/host"
	devtest "devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"devshard/types"
)

func startPeerRPCServer(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, lookup rpcserver.SessionLookup, opts ...rpcserver.MuxOption) (*httptest.Server, *rpcserver.PeerAuthHandler) {
	t.Helper()
	return startPeerRPCServerObserved(t, hostAddr, authCfg, lookup, nil, opts...)
}

// startPeerRPCServerObserved records per-procedure wire encodings and the
// compressed request size in obs when it is non-nil.
func startPeerRPCServerObserved(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, lookup rpcserver.SessionLookup, obs *wireObserver, opts ...rpcserver.MuxOption) (*httptest.Server, *rpcserver.PeerAuthHandler) {
	t.Helper()
	return startPeerRPCServerMaybeH2C(t, hostAddr, authCfg, lookup, obs, false, opts...)
}

func startPeerRPCServerH2CObserved(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, lookup rpcserver.SessionLookup, obs *wireObserver, opts ...rpcserver.MuxOption) (*httptest.Server, *rpcserver.PeerAuthHandler) {
	t.Helper()
	return startPeerRPCServerMaybeH2C(t, hostAddr, authCfg, lookup, obs, true, opts...)
}

func startPeerRPCServerMaybeH2C(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, lookup rpcserver.SessionLookup, obs *wireObserver, h2cMode bool, opts ...rpcserver.MuxOption) (*httptest.Server, *rpcserver.PeerAuthHandler) {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(lookup), opts...)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1"))
		if obs == nil {
			mux.ServeHTTP(w, r)
			return
		}
		counted := &countingBody{ReadCloser: r.Body}
		r.Body = counted
		mux.ServeHTTP(w, r)
		obs.record(r.URL.Path, r.Header, w.Header(), counted.n)
	})
	var srv *httptest.Server
	if h2cMode {
		srv = httptest.NewServer(h2c.NewHandler(h, &http2.Server{}))
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	return srv, auth
}

type countingBody struct {
	io.ReadCloser
	n int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += n
	return n, err
}

// wireObserver is what actually crossed the wire, per procedure.
type wireObserver struct {
	mu      sync.Mutex
	reqEnc  map[string]string
	respEnc map[string]string
	reqCT   map[string]string
	wire    map[string]int
}

func newWireObserver() *wireObserver {
	return &wireObserver{
		reqEnc:  map[string]string{},
		respEnc: map[string]string{},
		reqCT:   map[string]string{},
		wire:    map[string]int{},
	}
}

func (o *wireObserver) record(procedure string, req, resp http.Header, wireBytes int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reqEnc[procedure] = headerEncoding(req)
	o.respEnc[procedure] = headerEncoding(resp)
	o.reqCT[procedure] = req.Get("Content-Type")
	o.wire[procedure] = wireBytes
}

func (o *wireObserver) requestEncoding(procedure string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reqEnc[procedure]
}

func (o *wireObserver) requestContentType(procedure string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reqCT[procedure]
}

func (o *wireObserver) responseEncoding(procedure string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.respEnc[procedure]
}

func (o *wireObserver) requestBytes(procedure string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.wire[procedure]
}

// headerEncoding is unary Content-Encoding or the Connect streaming
// equivalent, whichever the protocol used.
func headerEncoding(h http.Header) string {
	if enc := h.Get("Content-Encoding"); enc != "" {
		return enc
	}
	if enc := h.Get("Grpc-Encoding"); enc != "" {
		return enc
	}
	return h.Get("Connect-Content-Encoding")
}

func requireGRPCContentType(t *testing.T, ct string) {
	t.Helper()
	require.Contains(t, ct, "application/grpc", "native gRPC Content-Type")
	require.NotContains(t, ct, "connect")
}

func requireConnectContentType(t *testing.T, ct string) {
	t.Helper()
	lower := strings.ToLower(ct)
	require.NotContains(t, lower, "application/grpc", "HTTP/1.1 must not speak native gRPC, got %q", ct)
	require.True(t, strings.Contains(lower, "connect") || strings.Contains(lower, "application/proto"),
		"Connect Content-Type, got %q", ct)
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
	require.False(t, pc.UsingH2(), "unset h2 port stays HTTP/1.1")
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
	// MinTTL is lowered so this 4s session is not rejected by the 30s
	// production floor; refresh still uses a real timer.
	srv, auth := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 4 * time.Second,
		TokenGrace: 5 * time.Second,
	}, nil)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{
		WatchStale: time.Minute,
		MinTTL:     time.Second,
	})
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
		MinTTL:     time.Second,
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

func TestPeerConn_RefreshAttachTimeoutKeepsWatch(t *testing.T) {
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 20 * time.Second,
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
				// Exceed DefaultAttachTimeout, then 200 — a slow success,
				// not a RST, so the client error is DeadlineExceeded.
				time.Sleep(transport.DefaultAttachTimeout + time.Second)
				if r.Context().Err() != nil {
					return
				}
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
		MinTTL:     time.Second,
		Jitter:     func(time.Duration) time.Duration { return 50 * time.Millisecond },
	})
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	watchBefore := testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "watch"))
	ttlBefore := testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl"))

	require.Eventually(t, func() bool {
		return attachN.Load() >= 2
	}, 8*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return attachN.Load() >= 3
	}, transport.DefaultAttachTimeout+2*time.Second, 20*time.Millisecond)

	require.Equal(t, observability.PeerSessionReady, pc.State(), "refresh DeadlineExceeded must not drop Watch")
	require.True(t, pc.Ready())
	require.Equal(t, first, pc.LiveToken(), "token stays until a later refresh succeeds")
	require.Equal(t, watchBefore, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "watch")))
	require.Equal(t, ttlBefore, testutil.ToFloat64(observability.PeerReattachCounter(directMuxPeer(hostAddr), "ttl")))
	require.False(t, transport.RPCH2MissCachedForTest(srv.URL), "refresh timeout must not pin HTTP/1.1")

	release()
	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 8*time.Second, 20*time.Millisecond)
	require.False(t, transport.RPCH2MissCachedForTest(srv.URL))
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
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointSignatures))
	start := time.Now()
	_, err := rpc.GetSignatures(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Less(t, time.Since(start), time.Second, "oversize must not spend the 5s non-inference budget")
}

type sigLookup struct {
	sigs map[uint32][]byte
}

func (s sigLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return sigCore{sigs: s.sigs}, nil
}

func (s sigLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return s.SessionServerExisting(id)
}

func (s sigLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return s.SessionServerExisting(id)
}

func (s sigLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return s.SessionForParticipant(id, addr)
}

type sigCore struct {
	sigs map[uint32][]byte
}

func (s sigCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) { return s.sigs, nil }
func (s sigCore) AllowsSender(string) bool                             { return true }

func TestRPCClient_GetDiffsReadMaxBytes(t *testing.T) {
	t.Run("above 16KiB succeeds", func(t *testing.T) {
		n := transport.DefaultRPCReadMaxBytes + 1
		rpc := newQueryRPCClient(t, queryLookup{diffs: []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, UserSig: make([]byte, n)},
		}}}, transport.EndpointDiffs, 0)
		diffs, err := rpc.GetDiffs(context.Background(), 1, 1)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		require.Len(t, diffs[0].UserSig, n)
	})
	t.Run("above 1MiB succeeds", func(t *testing.T) {
		n := 1<<20 + 1
		rpc := newQueryRPCClient(t, queryLookup{diffs: []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, UserSig: make([]byte, n)},
		}}}, transport.EndpointDiffs, 0)
		diffs, err := rpc.GetDiffs(context.Background(), 1, 1)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		require.Len(t, diffs[0].UserSig, n)
	})
	t.Run("above 10MiB is ResourceExhausted", func(t *testing.T) {
		rpc := newQueryRPCClient(t, queryLookup{diffs: []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, UserSig: make([]byte, transport.DefaultRPCQueryReadMaxBytes+1)},
		}}}, transport.EndpointDiffs, 0)
		_, err := rpc.GetDiffs(context.Background(), 1, 1)
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	})
}

func TestRPCClient_GetMempoolReadMaxBytes(t *testing.T) {
	t.Run("above 16KiB succeeds", func(t *testing.T) {
		n := transport.DefaultRPCReadMaxBytes + 1
		rpc := newQueryRPCClient(t, queryLookup{mempool: largeHeartbeat(n)}, transport.EndpointMempool, 0)
		txs, err := rpc.GetMempool(context.Background())
		require.NoError(t, err)
		require.Len(t, txs, 1)
		require.Len(t, txs[0].GetHeartbeat().GetObservedBlockHash(), n)
	})
	t.Run("above 1MiB succeeds", func(t *testing.T) {
		n := 1<<20 + 1
		rpc := newQueryRPCClient(t, queryLookup{mempool: largeHeartbeat(n)}, transport.EndpointMempool, 0)
		txs, err := rpc.GetMempool(context.Background())
		require.NoError(t, err)
		require.Len(t, txs, 1)
		require.Len(t, txs[0].GetHeartbeat().GetObservedBlockHash(), n)
	})
	t.Run("above 10MiB is ResourceExhausted", func(t *testing.T) {
		rpc := newQueryRPCClient(t, queryLookup{mempool: largeHeartbeat(transport.DefaultRPCQueryReadMaxBytes + 1)},
			transport.EndpointMempool, 0)
		_, err := rpc.GetMempool(context.Background())
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	})
}

func TestRPCClient_QueriesGzipRequestAndResponse(t *testing.T) {
	obs := newWireObserver()
	lookup := queryLookup{
		diffs:   []types.DiffRecord{{Diff: types.Diff{Nonce: 1, UserSig: make([]byte, 8<<10)}}},
		mempool: largeHeartbeat(8 << 10),
	}
	rpc := newQueryRPCClientObserved(t, lookup, transport.EndpointDiffs+","+transport.EndpointMempool,
		0, rpcserver.PeerAuthConfig{}, obs)

	diffs, err := rpc.GetDiffs(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	txs, err := rpc.GetMempool(context.Background())
	require.NoError(t, err)
	require.Len(t, txs, 1)

	for _, procedure := range []string{
		rpcpbconnect.SessionServiceGetDiffsProcedure,
		rpcpbconnect.SessionServiceGetMempoolProcedure,
	} {
		require.Equal(t, "gzip", obs.requestEncoding(procedure), procedure)
		require.Equal(t, "gzip", obs.responseEncoding(procedure), procedure)
	}
}

func newQueryRPCClient(t *testing.T, lookup queryLookup, endpoints string, queryTimeout time.Duration) *transport.RPCClient {
	t.Helper()
	return newQueryRPCClientAuth(t, lookup, endpoints, queryTimeout, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
}

func newQueryRPCClientAuth(t *testing.T, lookup queryLookup, endpoints string, queryTimeout time.Duration, authCfg rpcserver.PeerAuthConfig) *transport.RPCClient {
	t.Helper()
	return newQueryRPCClientObserved(t, lookup, endpoints, queryTimeout, authCfg, nil)
}

func newQueryRPCClientObserved(t *testing.T, lookup queryLookup, endpoints string, queryTimeout time.Duration, authCfg rpcserver.PeerAuthConfig, obs *wireObserver) *transport.RPCClient {
	t.Helper()
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	if authCfg.Heartbeat <= 0 {
		authCfg.Heartbeat = 50 * time.Millisecond
	}
	srv, _ := startPeerRPCServerObserved(t, hostAddr, authCfg, lookup, obs)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	cfg := transport.DefaultClientConfig()
	if queryTimeout > 0 {
		cfg.QueryTimeout = queryTimeout
	}
	return transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer, cfg), pc, transport.ParseRPCEndpoints(endpoints))
}

func TestRPCClient_IgnoresPacingWhenWaitExceedsDeadline(t *testing.T) {
	var nDiffs atomic.Int32
	rpc := newQueryRPCClientAuth(t, queryLookup{nDiffs: &nDiffs}, transport.EndpointDiffs, 200*time.Millisecond,
		rpcserver.PeerAuthConfig{
			Heartbeat: 50 * time.Millisecond,
			Limits: &transport.ChannelLimitConfig{
				MessagesPerMin: 60,
				MessagesBurst:  60,
			},
		})
	_, err := rpc.GetDiffs(context.Background(), 0, 1)
	require.NoError(t, err)
	require.Equal(t, int32(1), nDiffs.Load())

	_, err = rpc.GetDiffs(context.Background(), 0, 1)
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"wait of 1 min exceeds 200ms deadline; pacing is skipped so the RPC hits the interceptor")
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, int32(1), nDiffs.Load())
}

func TestRPCClient_ClonesSharePeerBudget(t *testing.T) {
	var nSigs atomic.Int32
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat: 50 * time.Millisecond,
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 60,
			MessagesBurst:  1,
		},
	}, queryLookup{nSigs: &nSigs})
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	cfg := transport.DefaultClientConfig()
	cfg.QueryTimeout = 200 * time.Millisecond
	set := transport.ParseRPCEndpoints(transport.EndpointSignatures)
	rpc1 := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer, cfg), pc, set)
	rpc2 := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-2", peer, cfg), pc, set)
	_, err := rpc1.GetSignatures(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, int32(1), nSigs.Load())
	_, err = rpc2.GetSignatures(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"client wait (1s) exceeds QueryTimeout so pacing is skipped; the interceptor still has one burst token")
	require.Equal(t, int32(1), nSigs.Load(), "clones must share the PeerConn session (server burst)")
}

func largeHeartbeat(n int) []*types.DevshardTx {
	return []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{
			Heartbeat: &types.MsgHeartbeat{ObservedBlockHash: make([]byte, n)},
		},
	}}
}

type queryLookup struct {
	diffs   []types.DiffRecord
	mempool []*types.DevshardTx
	nDiffs  *atomic.Int32
	nSigs   *atomic.Int32
}

func (q queryLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return queryCore(q), nil
}

func (q queryLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return q.SessionServerExisting(id)
}

func (q queryLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return q.SessionServerExisting(id)
}

func (q queryLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return q.SessionForParticipant(id, addr)
}

type queryCore struct {
	diffs   []types.DiffRecord
	mempool []*types.DevshardTx
	nDiffs  *atomic.Int32
	nSigs   *atomic.Int32
}

func (q queryCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	if q.nSigs != nil {
		q.nSigs.Add(1)
	}
	return map[uint32][]byte{}, nil
}
func (q queryCore) AllowsSender(string) bool { return true }
func (q queryCore) ServeGetDiffs(uint64, uint64) ([]types.DiffRecord, error) {
	if q.nDiffs != nil {
		q.nDiffs.Add(1)
	}
	return q.diffs, nil
}
func (q queryCore) ServeGetMempool(context.Context) ([]*types.DevshardTx, error) {
	return q.mempool, nil
}

func TestRPCClient_VerifyTimeoutReadMaxBytes(t *testing.T) {
	t.Run("prompt above 16KiB succeeds", func(t *testing.T) {
		var ran atomic.Bool
		rpc := newLargeRPCClient(t, largeRPCLookup{core: largeRPCCore{verifyRan: &ran}},
			transport.EndpointVerifyTimeout, transport.DefaultClientConfig())
		n := transport.DefaultRPCReadMaxBytes + 1
		resp, err := rpc.SendVerifyTimeout(context.Background(), transport.VerifyTimeoutRequest{
			InferenceID: 1,
			Reason:      "refused",
			Payload:     &transport.PayloadJSON{Prompt: make([]byte, n)},
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.True(t, ran.Load(), "16KiB+1 prompt must reach ServeVerifyTimeout")
	})
	t.Run("request over 10MiB is ResourceExhausted and does not reach ServeX", func(t *testing.T) {
		var ran atomic.Bool
		rpc := newLargeRPCClient(t, largeRPCLookup{core: largeRPCCore{verifyRan: &ran}},
			transport.EndpointVerifyTimeout, transport.DefaultClientConfig())
		start := time.Now()
		_, err := rpc.SendVerifyTimeout(context.Background(), transport.VerifyTimeoutRequest{
			InferenceID: 1,
			Reason:      "refused",
			Payload:     &transport.PayloadJSON{Prompt: make([]byte, transport.DefaultRPCLargeReadMaxBytes+1)},
		})
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.False(t, ran.Load(), "oversize VerifyTimeout must not reach ServeVerifyTimeout")
		require.Less(t, time.Since(start), time.Second, "oversize must not spend the 5s non-inference budget")
	})
	t.Run("recovery mempool response over 16KiB succeeds", func(t *testing.T) {
		n := transport.DefaultRPCReadMaxBytes + 1
		raw, err := transport.DevshardTxsToBytes(largeHeartbeat(n))
		require.NoError(t, err)
		rpc := newLargeRPCClient(t, largeRPCLookup{core: largeRPCCore{
			verifyResp: &transport.VerifyTimeoutResponse{Mempool: raw},
		}}, transport.EndpointVerifyTimeout, transport.DefaultClientConfig())
		resp, err := rpc.SendVerifyTimeout(context.Background(), transport.VerifyTimeoutRequest{
			InferenceID: 1,
			Reason:      "refused",
		})
		require.NoError(t, err)
		require.Len(t, resp.Mempool, 1)
		require.Greater(t, len(resp.Mempool[0]), n)
	})
}

func TestRPCClient_ChallengeReceiptReadMaxBytes(t *testing.T) {
	t.Run("prompt above 16KiB succeeds", func(t *testing.T) {
		var ran atomic.Bool
		rpc := newLargeRPCClient(t, largeRPCLookup{core: largeRPCCore{challengeRan: &ran}},
			transport.EndpointChallengeReceipt, transport.DefaultClientConfig())
		n := transport.DefaultRPCReadMaxBytes + 1
		_, _, err := rpc.ChallengeReceipt(context.Background(), 1, &host.InferencePayload{Prompt: make([]byte, n)}, nil)
		require.NoError(t, err)
		require.True(t, ran.Load(), "16KiB+1 prompt must reach ServeChallengeReceipt")
	})
	t.Run("request over 10MiB is ResourceExhausted and does not reach ServeX", func(t *testing.T) {
		var ran atomic.Bool
		rpc := newLargeRPCClient(t, largeRPCLookup{core: largeRPCCore{challengeRan: &ran}},
			transport.EndpointChallengeReceipt, transport.DefaultClientConfig())
		start := time.Now()
		_, _, err := rpc.ChallengeReceipt(context.Background(), 1,
			&host.InferencePayload{Prompt: make([]byte, transport.DefaultRPCLargeReadMaxBytes+1)}, nil)
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.False(t, ran.Load(), "oversize ChallengeReceipt must not reach ServeChallengeReceipt")
		require.Less(t, time.Since(start), time.Second, "oversize must not spend the 5s non-inference budget")
	})
}

func TestRPCClient_GossipTxsReadMaxBytes(t *testing.T) {
	t.Run("batch above 16KiB succeeds", func(t *testing.T) {
		var ran atomic.Bool
		lookup := largeRPCLookup{core: largeRPCCore{gossipTxsRan: &ran}}
		rpc := newLargeRPCClient(t, lookup, transport.EndpointGossip, transport.DefaultClientConfig(),
			rpcserver.WithGossipService(rpcserver.NewGossipHandler(lookup)))
		err := rpc.GossipTxs(context.Background(), largeHeartbeat(transport.DefaultRPCReadMaxBytes+1))
		require.NoError(t, err)
		require.True(t, ran.Load(), "16KiB+1 Gossip Txs must reach ServeGossipTxs")
	})
	t.Run("batch over 10MiB is ResourceExhausted and does not reach ServeX", func(t *testing.T) {
		var ran atomic.Bool
		lookup := largeRPCLookup{core: largeRPCCore{gossipTxsRan: &ran}}
		rpc := newLargeRPCClient(t, lookup, transport.EndpointGossip, transport.DefaultClientConfig(),
			rpcserver.WithGossipService(rpcserver.NewGossipHandler(lookup)))
		start := time.Now()
		err := rpc.GossipTxs(context.Background(), largeHeartbeat(transport.DefaultRPCLargeReadMaxBytes+1))
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.False(t, ran.Load(), "oversize Gossip Txs must not reach ServeGossipTxs")
		require.Less(t, time.Since(start), time.Second, "oversize must not spend the 5s non-inference budget")
	})
}

func TestRPCClient_GetPayloadRoundTrip(t *testing.T) {
	const authz = "dGVzdC1zaWduYXR1cmU=" // base64 header text, not raw ECDSA
	var sawSig atomic.Value
	lookup := largeRPCLookup{core: largeRPCCore{}}
	h := rpcserver.NewPayloadHandler(lookup, func(_ context.Context, _ rpcserver.SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		sawSig.Store(string(req.GetSignature()))
		return &rpcpb.GetPayloadResponse{
			InferenceId:       req.GetInferenceId(),
			PromptPayload:     []byte("prompt"),
			ResponsePayload:   []byte("response"),
			ExecutorSignature: "executor-sig",
		}, nil
	})
	rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
		transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
	resp, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{
		InferenceId:      "42",
		ValidatorAddress: "gonka1validator",
		Timestamp:        1,
		EpochId:          7,
		Signature:        []byte(authz),
	}, 0)
	require.NoError(t, err)
	require.Equal(t, "42", resp.GetInferenceId())
	require.Equal(t, []byte("prompt"), resp.GetPromptPayload())
	require.Equal(t, []byte("response"), resp.GetResponsePayload())
	require.Equal(t, "executor-sig", resp.GetExecutorSignature())
	require.Equal(t, authz, sawSig.Load())
}

func TestRPCPayloadMaxBytesMatchesHTTPGet(t *testing.T) {
	require.Equal(t, int(commonvalidation.MaxPayloadResponseBytes), transport.DefaultRPCPayloadMaxBytes)
	require.Equal(t, int(commonvalidation.MaxPayloadResponseBytesHard), transport.DefaultRPCPayloadSendMaxBytes)
}

func TestRPCQueryReadMaxBytesMatchesMaxBodySize(t *testing.T) {
	require.Equal(t, int(transport.DefaultMaxBodySize), transport.DefaultRPCQueryReadMaxBytes)
	require.Equal(t, transport.DefaultRPCLargeReadMaxBytes, transport.DefaultRPCQueryReadMaxBytes)
}

func TestRPCClient_GetPayloadResponseCap(t *testing.T) {
	t.Run("10MiB+1 response succeeds", func(t *testing.T) {
		body := make([]byte, int(transport.DefaultMaxBodySize)+1)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		resp, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 0)
		require.NoError(t, err)
		require.Equal(t, len(body), len(resp.GetResponsePayload()))
	})
	t.Run("over 64MiB default is ResourceExhausted", func(t *testing.T) {
		var n atomic.Int32
		body := make([]byte, transport.DefaultRPCPayloadMaxBytes+1)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			n.Add(1)
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		_, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 0)
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.Equal(t, int32(1), n.Load(), "oversize GetPayload must not retry")
	})
	t.Run("512-byte pin rejects 2KiB decoded body", func(t *testing.T) {
		var n atomic.Int32
		body := make([]byte, 2048)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			n.Add(1)
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		_, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 512)
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.Equal(t, int32(1), n.Load(), "decoded pin must not retry")
	})
	t.Run("512-byte pin allows 400-byte body", func(t *testing.T) {
		body := make([]byte, 400)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		resp, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 512)
		require.NoError(t, err)
		require.Equal(t, len(body), len(resp.GetResponsePayload()))
	})
	t.Run("over 32MiB with 32MiB bucket is ResourceExhausted", func(t *testing.T) {
		var n atomic.Int32
		body := make([]byte, 32<<20+1)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			n.Add(1)
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		_, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 512)
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.Equal(t, int32(1), n.Load(), "oversize GetPayload must not retry")
	})
	t.Run("cap above 64MiB allows 64MiB+1", func(t *testing.T) {
		body := make([]byte, transport.DefaultRPCPayloadMaxBytes+1)
		lookup := largeRPCLookup{core: largeRPCCore{}}
		h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
			transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))
		resp, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"},
			int64(commonvalidation.MaxPayloadResponseBytesHard))
		require.NoError(t, err)
		require.Equal(t, len(body), len(resp.GetResponsePayload()))
	})
}

func TestRPCClient_CloneWithSignerRepairEnvelope(t *testing.T) {
	req := &heightsync.RepairRequest{RequesterSlot: 0, RequesterSig: []byte{1}}
	t.Run("same signer reaches ServeHeightSyncRepair", func(t *testing.T) {
		var ran atomic.Bool
		rpc, peer := newRepairRPCClient(t, largeRPCLookup{core: largeRPCCore{repairRan: &ran}})
		clone := rpc.CloneWithSigner(peer, time.Second)
		t.Cleanup(clone.Close)
		_, err := clone.HeightSyncRepair(context.Background(), req)
		require.NoError(t, err)
		require.True(t, ran.Load(), "same-signer clone must keep the Attach identity")
	})
	t.Run("different signer is envelope mismatch", func(t *testing.T) {
		var ran atomic.Bool
		rpc, _ := newRepairRPCClient(t, largeRPCLookup{core: largeRPCCore{repairRan: &ran}})
		clone := rpc.CloneWithSigner(devtest.MustGenerateKey(t), time.Second)
		t.Cleanup(clone.Close)
		_, err := clone.HeightSyncRepair(context.Background(), req)
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Contains(t, err.Error(), "envelope signer does not match handshake")
		require.False(t, ran.Load(), "mismatch must not reach ServeHeightSyncRepair")
	})
}

func newRepairRPCClient(t *testing.T, lookup rpcserver.SessionLookup) (*transport.RPCClient, *signing.Secp256k1Signer) {
	t.Helper()
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, lookup)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer, transport.DefaultClientConfig()), pc, transport.ParseRPCEndpoints(transport.EndpointRepair))
	return rpc, peer
}

func newLargeRPCClient(t *testing.T, lookup rpcserver.SessionLookup, endpoints string, cfg transport.ClientConfig, muxOpts ...rpcserver.MuxOption) *transport.RPCClient {
	t.Helper()
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, lookup, muxOpts...)
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	return transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer, cfg), pc, transport.ParseRPCEndpoints(endpoints))
}

type largeRPCLookup struct {
	core largeRPCCore
}

func (l largeRPCLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}

func (l largeRPCLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}

func (l largeRPCLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}

func (l largeRPCLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return l.SessionForParticipant(id, addr)
}

type largeRPCCore struct {
	verifyRan     *atomic.Bool
	challengeRan  *atomic.Bool
	gossipTxsRan  *atomic.Bool
	repairRan     *atomic.Bool
	verifyResp    *transport.VerifyTimeoutResponse
	challengeResp *transport.ChallengeReceiptResponse
}

func (c largeRPCCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}
func (c largeRPCCore) AllowsSender(string) bool  { return true }
func (c largeRPCCore) IsOwner(string) bool       { return true }
func (c largeRPCCore) IsGroupMember(string) bool { return true }

func (c largeRPCCore) ServeVerifyTimeout(context.Context, transport.VerifyTimeoutRequest) (*transport.VerifyTimeoutResponse, error) {
	if c.verifyRan != nil {
		c.verifyRan.Store(true)
	}
	if c.verifyResp != nil {
		return c.verifyResp, nil
	}
	return &transport.VerifyTimeoutResponse{}, nil
}

func (c largeRPCCore) ServeChallengeReceipt(context.Context, transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	if c.challengeRan != nil {
		c.challengeRan.Store(true)
	}
	if c.challengeResp != nil {
		return c.challengeResp, nil
	}
	return &transport.ChallengeReceiptResponse{}, nil
}

func (c largeRPCCore) ServeGossipTxs([]*types.DevshardTx) {
	if c.gossipTxsRan != nil {
		c.gossipTxsRan.Store(true)
	}
}

func (c largeRPCCore) ServeHeightSyncRepair(context.Context, string, *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	if c.repairRan != nil {
		c.repairRan.Store(true)
	}
	return &heightsync.RepairResponse{}, nil
}
