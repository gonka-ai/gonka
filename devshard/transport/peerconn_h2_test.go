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

func TestPeerConn_H2PortClosedFallsBackWithinBound(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	start := time.Now()
	pc := h2PeerConn(t, inf, hostAddr, peer, dead.URL)
	pc.Start()
	waitPeerReady(t, pc)
	require.Less(t, time.Since(start), 5*time.Second)
	require.False(t, pc.UsingH2())
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2AcceptHoldFallsBackWithinAttachBudget(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)
	h2URL, accepts := startAcceptHold(t)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	start := time.Now()
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet: transport.PeerRPCDialSet{H2URL: h2URL},
	})
	pc.Start()
	require.Eventually(t, func() bool {
		return pc.Ready() && len(pc.LiveToken()) > 0
	}, transport.DefaultAttachTimeout+200*time.Millisecond, 10*time.Millisecond)
	require.Less(t, time.Since(start), transport.DefaultAttachTimeout+200*time.Millisecond,
		"probe+fallback must share the 5s Attach budget, not 1s+5s")
	require.False(t, pc.UsingH2())
	require.Greater(t, accepts.Load(), int32(0), "blackhole hop must consume the h2 probe")
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2CloseFINsMux(t *testing.T) {
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
	require.Eventually(t, func() bool { return live.Load() == 0 }, 5*time.Second, 10*time.Millisecond,
		"PeerConn.Close must FIN the idle h2 mux")
	require.Zero(t, infHits.Load())
}

func TestPeerConn_H2BaselineNotH2CFallsBackNoMixedStream(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

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
	require.False(t, pc.UsingH2())
	require.False(t, sawH1Attach.Load(), "http2.Transport must not complete HTTP/1.1 on the h2 origin")
}

func TestPeerConn_H2MissSkipsUntilTTL(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

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
				time.Sleep(time.Minute)
				_ = c.Close()
			}(c)
		}
	}()
	h2URL := "http://" + ln.Addr().String()

	now := time.Now()
	var cur atomic.Pointer[time.Time]
	cur.Store(&now)
	transport.InstallRPCH2MissCacheForTest(30*time.Minute, func() time.Time { return *cur.Load() })
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)

	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.Greater(t, accepts.Load(), int32(0))
	first := accepts.Load()

	pc2 := newTestPeerConn(t, inf, hostAddr, devtest.MustGenerateKey(t), transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2URL},
		H2ProbeTimeout: 2 * time.Second,
	})
	start := time.Now()
	pc2.Start()
	waitPeerReady(t, pc2)
	require.Less(t, time.Since(start), time.Second, "cached miss must skip the h2 probe")
	require.False(t, pc2.UsingH2())
	require.Equal(t, first, accepts.Load(), "reconnect must not retry h2 while cached")

	later := now.Add(30 * time.Minute)
	cur.Store(&later)
	h2 := startPeerRPCServerH2C(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	pc3 := newTestPeerConn(t, inf, hostAddr, devtest.MustGenerateKey(t), transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2.URL},
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc3.Start()
	waitPeerReady(t, pc3)
	require.True(t, pc3.UsingH2(), "after 30 min the h2 origin must be tried again")
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

func TestPeerConn_HTTPS11ALPNFallsBackNotH2Attach(t *testing.T) {
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
	require.False(t, pc.UsingH2(), "TLS http/1.1 (no h2 ALPN) must not count as h2")
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
		TokenGrace: 5 * time.Second,
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
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

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
	require.Eventually(t, func() bool {
		return pc.Ready() && !pc.UsingH2()
	}, 8*time.Second, 20*time.Millisecond, "transport miss on TTL refresh must cancel Watch")
	require.Less(t, time.Since(start), 90*time.Second)
	require.True(t, transport.RPCH2MissCachedForTest(inf.URL))
}

func TestPeerConn_H2HalfOpenFallsBackWithoutWatchStale(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h2 := startPeerRPCServerH2C(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	backend, err := url.Parse(h2.URL)
	require.NoError(t, err)
	proxyURL, hold := startTCPHoldProxy(t, backend.Host)
	inf, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, nil)

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
	require.Eventually(t, func() bool {
		return pc.Ready() && !pc.UsingH2()
	}, 5*time.Second, 20*time.Millisecond, "half-open mux must PING-fail, not wait WatchStale")
	require.Less(t, time.Since(start), 5*time.Second)
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
