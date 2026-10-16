package transport_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"common/httpguard"
	devtest "devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcserver"
)

func startPeerRPCServerH2C(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig) *httptest.Server {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1"))
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h2c.NewHandler(h, &http2.Server{}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	return srv
}

func h2PeerConn(t *testing.T, inf *httptest.Server, hostAddr string, peer signing.Signer, h2URL string) *transport.PeerConn {
	t.Helper()
	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	return newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
	})
}

func TestPeerConn_H2Success(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2 := startPeerRPCServerH2C(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	var infHits atomic.Int32
	counted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infHits.Add(1)
		http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
	}))
	t.Cleanup(counted.Close)

	pc := h2PeerConn(t, counted, hostAddr, peer, h2.URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	require.Zero(t, infHits.Load(), "Connect must not fall back to InferenceUrl when h2 works")
}

func TestPeerConn_SlowAttachUsesFullHandshakeTimeout(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat: 50 * time.Millisecond,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	h2 := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PeerAuthService/Attach") {
			time.Sleep(400 * time.Millisecond)
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}), &http2.Server{}))
	t.Cleanup(h2.Close)
	t.Cleanup(auth.Close)

	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)
	pc := h2PeerConn(t, inf, hostAddr, peer, h2.URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	require.Zero(t, infHits.Load(), "a slow Attach must stay on h2")
}

func TestPeerConn_H2PortClosedFallsBackToHTTP11(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	pc := h2PeerConn(t, inf, hostAddr, peer, dead.URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2(), "a closed h2 origin must Attach over Connect/HTTP/1.1 on InferenceUrl")
	require.Greater(t, infHits.Load(), int32(0))
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL), "the miss pins this host to HTTP/1.1 for the miss TTL")
}

// A listen that accepts and never answers the preface is not a miss: it is
// indistinguishable from a slow host, so the Attach deadline governs and
// the next attempt probes h2 again. The probe timeout covers dial and TLS.
func TestPeerConn_H2AcceptHoldIsNotAMiss(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)
	h2URL, accepts := startAcceptHold(t)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc.Start()
	require.Never(t, func() bool { return pc.Ready() }, time.Second, 20*time.Millisecond,
		"a held h2 listen is a slow Attach, not a miss")
	require.Greater(t, accepts.Load(), int32(0), "blackhole hop must consume the h2 probe")
	require.Zero(t, infHits.Load())
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2CloseDoesNotFINSharedMux(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2URL, live := startPeerRPCServerH2CLive(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	var infHits atomic.Int32
	inf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infHits.Add(1)
		http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
	}))
	t.Cleanup(inf.Close)

	pc := h2PeerConn(t, inf, hostAddr, peer, h2URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	require.Equal(t, int32(1), live.Load())

	pc.Close()
	require.Never(t, func() bool { return live.Load() == 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"PeerConn.Close must not CloseIdle the pooled h2 mux")
	require.Equal(t, int32(1), live.Load())
	require.Zero(t, infHits.Load())
}

// A listen that accepts the preface and then closes without a word (a TLS
// terminator dropping an unknown ALPN, a TCP-mode reject on the preface)
// is a miss, not a slow host.
func TestPeerConn_H2SilentCloseFallsBackToHTTP11(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var accepts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func(c net.Conn) {
				buf := make([]byte, 24)
				_, _ = io.ReadFull(c, buf)
				_ = c.Close()
			}(c)
		}
	}()

	pc := h2PeerConn(t, inf, hostAddr, peer, "http://"+ln.Addr().String())
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.Greater(t, accepts.Load(), int32(0))
	require.Greater(t, infHits.Load(), int32(0))
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2BaselineNotH2CFallsBackNoMixedStream(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	var sawH1Attach atomic.Bool
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 1 {
			sawH1Attach.Store(true)
		}
		http.Error(w, "no h2c", http.StatusNotImplemented)
	}))
	t.Cleanup(plain.Close)

	pc := h2PeerConn(t, inf, hostAddr, peer, plain.URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, sawH1Attach.Load(), "http2.Transport must not complete HTTP/1.1 on the h2 origin")
	require.False(t, pc.UsingH2())
	require.Greater(t, infHits.Load(), int32(0), "the fallback is Connect over HTTP/1.1 on InferenceUrl")
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2MissPinsHTTP11UntilTheMissTTL(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	now := time.Now()
	var nowMu sync.Mutex
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	transport.InstallRPCH2MissCacheForTest(time.Minute, clock)
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	// An HTTP/1.1-only origin: every h2 probe is one accepted connection
	// whose preface is refused (an older wrapping's nginx).
	var accepts atomic.Int32
	plain := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no h2c", http.StatusNotImplemented)
	}))
	plain.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			accepts.Add(1)
		}
	}
	plain.Start()
	t.Cleanup(plain.Close)
	h2URL := plain.URL

	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		BackoffMin:     50 * time.Millisecond,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.Equal(t, int32(1), accepts.Load(), "one h2 probe, then HTTP/1.1")
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
	transport.ResetRPCH2ClientPoolForTest()

	// Another PeerConn to the same InferenceUrl host inside the TTL goes
	// straight to HTTP/1.1: it must not pay the probe timeout again.
	pc2 := newTestPeerConn(t, inf, hostAddr, devtest.MustGenerateKey(t), transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		BackoffMin:     50 * time.Millisecond,
	})
	pc2.Start()
	waitPeerReady(t, pc2)
	require.False(t, pc2.UsingH2())
	require.Equal(t, int32(1), accepts.Load(), "a cached miss must not probe h2 again")
	pc2.Close()

	// After the TTL the next Attach probes h2 again.
	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL))
	pc3 := newTestPeerConn(t, inf, hostAddr, devtest.MustGenerateKey(t), transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		BackoffMin:     50 * time.Millisecond,
	})
	pc3.Start()
	waitPeerReady(t, pc3)
	require.Equal(t, int32(2), accepts.Load(), "the first Attach after the TTL must probe h2 again")
	require.False(t, pc3.UsingH2())
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_HTTP11FallbackReturnsToH2OnRefreshAfterTTL(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	now := time.Now()
	var nowMu sync.Mutex
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	transport.InstallRPCH2MissCacheForTest(time.Minute, clock)
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 2 * time.Second,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	t.Cleanup(auth.Close)
	var h2Attach, h1Attach atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PeerAuthService/Attach") {
			if r.ProtoMajor >= 2 {
				h2Attach.Add(1)
			} else {
				h1Attach.Add(1)
			}
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	})
	// One origin that first refuses the HTTP/2 preface (an older wrapping)
	// and later speaks h2c (the wrapping was updated under the same session).
	var h2cOn atomic.Bool
	h2cHandler := h2c.NewHandler(handler, &http2.Server{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h2cOn.Load() {
			h2cHandler.ServeHTTP(w, r)
			return
		}
		if r.Method == "PRI" {
			// httptest's server sees the preface as an HTTP/1.1 PRI
			// request when h2c is off; refuse it like a plain server.
			http.Error(w, "no h2c", http.StatusNotImplemented)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(origin.Close)

	pc := newTestPeerConn(t, origin, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{InferenceURL: origin.URL, H2URL: origin.URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		BackoffMin:     50 * time.Millisecond,
		MinTTL:         time.Second,
		WatchStale:     time.Minute,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.True(t, transport.RPCH2MissCachedForTest(origin.URL))
	require.Zero(t, h2Attach.Load())
	require.GreaterOrEqual(t, h1Attach.Load(), int32(1))

	// The session is refreshed on HTTP/1.1 while the miss is cached.
	require.Eventually(t, func() bool { return h1Attach.Load() >= 2 }, 8*time.Second, 20*time.Millisecond)
	require.Zero(t, h2Attach.Load(), "a cached miss must not probe h2 on refresh")
	require.False(t, pc.UsingH2())

	// The wrapping now speaks h2c and the miss TTL has passed: the next
	// refresh probes h2, succeeds with the live token, and this PeerConn
	// moves onto the mux without a restart or a new session.
	h2cOn.Store(true)
	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()
	require.Eventually(t, func() bool { return pc.Ready() && pc.UsingH2() }, 8*time.Second, 20*time.Millisecond,
		"the first refresh after the miss TTL must move the session onto HTTP/2")
	require.GreaterOrEqual(t, h2Attach.Load(), int32(1))
	require.False(t, transport.RPCH2MissCachedForTest(origin.URL))
}

func startPeerRPCServerCounted(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	return srv
}

func TestPeerConn_H2UnavailableDoesNotFallback(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2 := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ew := connect.NewErrorWriter()
		_ = ew.Write(w, r, connect.NewError(connect.CodeUnavailable, errors.New("host initializing")))
	}), &http2.Server{}))
	t.Cleanup(h2.Close)

	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	pc := h2PeerConn(t, inf, hostAddr, peer, h2.URL)
	pc.Start()
	require.Never(t, func() bool { return pc.Ready() }, 500*time.Millisecond, 20*time.Millisecond,
		"Connect Unavailable from a working h2 origin must not pin HTTP/1.1")
	require.True(t, pc.UsingH2())
	require.Zero(t, infHits.Load())
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2BadTTLDoesNotFallback(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2 := startPeerRPCServerH2C(t, hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: time.Second,
	})
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	pc := h2PeerConn(t, inf, hostAddr, peer, h2.URL)
	pc.Start()
	require.Never(t, func() bool { return pc.Ready() }, 500*time.Millisecond, 20*time.Millisecond,
		"errAttachTTL after a completed h2 Attach must not fall back to HTTP/1.1")
	require.True(t, pc.UsingH2())
	require.Zero(t, infHits.Load())
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func startPeerRPCServerTLS11(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig, sawH1, sawH2 *atomic.Bool) *httptest.Server {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor >= 2 {
			sawH2.Store(true)
		} else {
			sawH1.Store(true)
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	return srv
}

func TestPeerConn_HTTPS11ALPNFallsBackToHTTP11(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	var sawH1, sawH2 atomic.Bool
	tls11 := startPeerRPCServerTLS11(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &sawH1, &sawH2)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

	pc := h2PeerConn(t, inf, hostAddr, peer, tls11.URL)
	pool := x509.NewCertPool()
	pool.AddCert(tls11.Certificate())
	pc.SetH2TLSRootCAsForTest(pool)
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2(), "a TLS listen without the peer-RPC ALPN is nginx: Connect goes HTTP/1.1 on InferenceUrl")
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
	require.False(t, sawH2.Load(), "must not complete HTTP/2 on a 1.1-only TLS listen")
	require.False(t, sawH1.Load(), "must not speak HTTP/1.1 on the h2 origin")
}

func TestPeerConn_H2RefreshTransportMissCancelsWatch(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 4 * time.Second,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	var attachN atomic.Int32
	h2 := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PeerAuthService/Attach") {
			if attachN.Add(1) > 1 {
				panic(http.ErrAbortHandler)
			}
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}), &http2.Server{}))
	t.Cleanup(h2.Close)
	t.Cleanup(auth.Close)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2.URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		WatchStale:     90 * time.Second,
		MinTTL:         time.Second,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	start := time.Now()
	require.Eventually(t, func() bool { return !pc.Ready() }, 8*time.Second, 20*time.Millisecond,
		"transport miss on TTL refresh must cancel Watch")
	require.Less(t, time.Since(start), 90*time.Second)
	// The outer loop probes h2 again (the origin aborts every later
	// Attach), records the miss, and Attaches on InferenceUrl over HTTP/1.1.
	require.Eventually(t, func() bool { return pc.Ready() && !pc.UsingH2() }, 8*time.Second, 20*time.Millisecond,
		"after the refresh miss the peer must come back on HTTP/1.1")
	require.Greater(t, infHits.Load(), int32(0))
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_RefreshAttachTimeoutDoesNotPinHTTP11(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat:  50 * time.Millisecond,
		SessionTTL: 20 * time.Second,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	var attachN atomic.Int32
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }

	h2 := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PeerAuthService/Attach") {
			n := attachN.Add(1)
			if n == 2 {
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
	}), &http2.Server{}))
	t.Cleanup(h2.Close)
	t.Cleanup(auth.Close)
	t.Cleanup(release)

	inf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "must not fall back to HTTP/1.1 on refresh timeout", http.StatusTeapot)
	}))
	t.Cleanup(inf.Close)

	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2.URL},
		H2ProbeTimeout: 200 * time.Millisecond,
		WatchStale:     time.Minute,
		BackoffMin:     50 * time.Millisecond,
		MinTTL:         time.Second,
		Jitter:         func(time.Duration) time.Duration { return 50 * time.Millisecond },
	})
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	require.True(t, pc.UsingH2())

	require.Eventually(t, func() bool {
		return attachN.Load() >= 2
	}, 8*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return attachN.Load() >= 3
	}, transport.DefaultAttachTimeout+2*time.Second, 20*time.Millisecond)

	require.Equal(t, observability.PeerSessionReady, pc.State(), "refresh DeadlineExceeded must not drop Watch")
	require.True(t, pc.Ready())
	require.True(t, pc.UsingH2(), "refresh timeout must not flip this PeerConn off h2")
	require.Equal(t, first, pc.LiveToken(), "token stays until a later refresh succeeds")
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL), "refresh timeout must not pin HTTP/1.1")

	release()
	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && pc.UsingH2() && len(tok) > 0 && string(tok) != string(first)
	}, 8*time.Second, 20*time.Millisecond)
	require.False(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2HalfOpenFallsBackWithoutWatchStale(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2 := startPeerRPCServerH2C(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	backend, err := url.Parse(h2.URL)
	require.NoError(t, err)
	proxyURL, hold := startTCPHoldProxy(t, backend.Host)
	var infHits atomic.Int32
	inf := startPeerRPCServerCounted(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, &infHits)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:           transport.PeerRPCDialSet{H2URL: proxyURL},
		H2ProbeTimeout:    200 * time.Millisecond,
		H2ReadIdleTimeout: 200 * time.Millisecond,
		H2PingTimeout:     200 * time.Millisecond,
		WatchStale:        90 * time.Second,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())

	hold()
	h2.Close()
	start := time.Now()
	require.Eventually(t, func() bool { return !pc.Ready() }, 5*time.Second, 20*time.Millisecond,
		"half-open mux must PING-fail, not wait WatchStale")
	require.Less(t, time.Since(start), 5*time.Second)
	// The held hop still eats the h2 probe; the peer falls back to
	// HTTP/1.1 on InferenceUrl and is ready again.
	require.Eventually(t, func() bool { return pc.Ready() && !pc.UsingH2() }, 8*time.Second, 20*time.Millisecond)
	require.Greater(t, infHits.Load(), int32(0))
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func startTCPHoldProxy(t *testing.T, backendHost string) (proxyURL string, hold func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var backends []net.Conn
	var frozen atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if frozen.Load() {
				continue
			}
			b, err := net.Dial("tcp", backendHost)
			if err != nil {
				_ = c.Close()
				continue
			}
			mu.Lock()
			backends = append(backends, b)
			mu.Unlock()
			go func(c, b net.Conn) {
				go func() { _, _ = io.Copy(b, c) }()
				_, _ = io.Copy(c, b)
			}(c, b)
		}
	}()
	hold = func() {
		frozen.Store(true)
		mu.Lock()
		defer mu.Unlock()
		for _, b := range backends {
			_ = b.Close()
		}
	}
	return "http://" + ln.Addr().String(), hold
}

func startAcceptHold(t *testing.T) (h2URL string, accepts *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	accepts = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func(c net.Conn) {
				time.Sleep(time.Minute)
				_ = c.Close()
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), accepts
}

func startPeerRPCServerH2CLive(t *testing.T, hostAddr string, authCfg rpcserver.PeerAuthConfig) (h2URL string, live *atomic.Int32) {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, authCfg)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	t.Cleanup(auth.Close)
	live = new(atomic.Int32)
	go func() {
		h2s := &http2.Server{}
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				live.Add(1)
				defer live.Add(-1)
				h2s.ServeConn(c, &http2.ServeConnOpts{Handler: h})
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), live
}
