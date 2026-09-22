package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"common/httpguard"
)

func TestNewPeerConnTransportsH2ServerName(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	origin, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "https://join.example.com:443",
		DialSet: PeerRPCDialSet{
			InferenceURL: "https://join.example.com:443",
			H2URL:        "https://127.0.0.1:8443",
		},
	}, 4)
	require.Equal(t, "127.0.0.1:8443", origin.h2URL.Host)
	h2, ok := origin.h2.(*http2.Transport)
	require.True(t, ok)
	require.NotNil(t, h2.TLSClientConfig)
	require.Equal(t, "join.example.com", h2.TLSClientConfig.ServerName)
	require.Equal(t, DefaultRPCH2ReadIdleTimeout, h2.ReadIdleTimeout)
	require.Equal(t, DefaultRPCH2PingTimeout, h2.PingTimeout)
	require.Equal(t, DefaultRPCH2IdleConnTimeout, h2.IdleConnTimeout)

	again, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "https://join.example.com:443",
		DialSet: PeerRPCDialSet{
			InferenceURL: "https://join.example.com:443",
			H2URL:        "https://127.0.0.1:8443",
		},
	}, 4)
	require.Same(t, origin.h2, again.h2, "same H2URL+SNI must reuse the process pool")
}

func TestPeerConnTransportsShareH2ByH2URL(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)

	overlayA, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "http://peer-a.example:8080",
		DialSet: PeerRPCDialSet{
			InferenceURL: "http://peer-a.example:8080",
			H2URL:        "http://proxy:8443",
		},
	}, 4)
	overlayB, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "http://peer-b.example:8080",
		DialSet: PeerRPCDialSet{
			InferenceURL: "http://peer-b.example:8080",
			H2URL:        "http://proxy:8443",
		},
	}, 4)
	require.Same(t, overlayA.h2, overlayB.h2, "h2c overlay: distinct InferenceUrl hosts share one client to proxy")

	joinB, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "http://host-b.example:8080",
		DialSet: PeerRPCDialSet{
			InferenceURL: "http://host-b.example:8080",
			H2URL:        "http://host-b.example:8443",
		},
	}, 4)
	require.NotSame(t, overlayA.h2, joinB.h2, "distinct H2URL remotes must not share a client")

	tlsA, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "https://a.example.com",
		DialSet: PeerRPCDialSet{
			InferenceURL: "https://a.example.com",
			H2URL:        "https://proxy:8443",
		},
	}, 4)
	tlsB, _ := newPeerConnTransports(PeerConnConfig{
		BaseURL: "https://b.example.com",
		DialSet: PeerRPCDialSet{
			InferenceURL: "https://b.example.com",
			H2URL:        "https://proxy:8443",
		},
	}, 4)
	require.NotSame(t, tlsA.h2, tlsB.h2, "HTTPS distinct SNI must not share a TLS client")
}

func TestOriginSwitch_SetH2FalseDoesNotCloseIdleH2(t *testing.T) {
	h2URL, err := url.Parse("http://proxy:8443")
	require.NoError(t, err)
	h2 := &closeIdleRT{}
	origin := newOriginSwitchTransport(&closeIdleRT{}, h2, h2URL)
	origin.setH2(true)
	require.True(t, origin.usingH2())
	origin.setH2(false)
	require.False(t, origin.usingH2())
	require.Equal(t, int32(0), h2.n.Load(), "setH2(false) must not CloseIdle the pooled h2 client")
}

func TestOriginSwitch_CloseIdleConnectionsSkipsPooledH2(t *testing.T) {
	h2URL, err := url.Parse("http://proxy:8443")
	require.NoError(t, err)
	h1 := &closeIdleRT{}
	h2 := &closeIdleRT{}
	origin := newOriginSwitchTransport(h1, h2, h2URL)
	origin.CloseIdleConnections()
	require.Equal(t, int32(1), h1.n.Load(), "CloseIdle must still drain this PeerConn's HTTP/1.1 pool")
	require.Equal(t, int32(0), h2.n.Load(), "CloseIdle must not drain the process h2 mux")
}

func TestPeerConn_CloseDoesNotClosePooledH2(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL: "http://127.0.0.1:1",
		DialSet: PeerRPCDialSet{
			InferenceURL: "http://127.0.0.1:1",
			H2URL:        "http://proxy:8443",
		},
		DoorEscrowID: "1",
	})
	require.NotNil(t, pc.origin.h2)
	spy := &closeIdleRT{}
	pc.origin.h2 = spy
	pc.Close()
	require.Equal(t, int32(0), spy.n.Load(), "PeerConn.Close must not CloseIdle the pooled h2 client")
}

type h2AcceptCounter struct {
	net.Listener
	n *atomic.Int32
}

func (l *h2AcceptCounter) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.n.Add(1)
	return c, nil
}

func startCountingH2CServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	httpguard.SetAllowPrivate(true)
	accepts := new(atomic.Int32)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewUnstartedServer(h2c.NewHandler(h, &http2.Server{}))
	srv.Listener = &h2AcceptCounter{Listener: ln, n: accepts}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, accepts
}

func h2OriginRoundTrip(t *testing.T, origin *originSwitchTransport, rawURL string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL+"/", nil)
	require.NoError(t, err)
	resp, err := origin.RoundTrip(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
}

func TestSharedH2IdleMuxSurvivesSetH2FalseAndCloseIdle(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	srv, accepts := startCountingH2CServer(t)
	cfg := PeerConnConfig{
		BaseURL: srv.URL,
		DialSet: PeerRPCDialSet{InferenceURL: srv.URL, H2URL: srv.URL},
	}
	a, _ := newPeerConnTransports(cfg, 4)
	b, _ := newPeerConnTransports(cfg, 4)
	require.Same(t, a.h2, b.h2)

	a.setH2(true)
	h2OriginRoundTrip(t, a, srv.URL)
	require.Equal(t, int32(1), accepts.Load())

	b.setH2(true)
	b.setH2(false)
	require.False(t, b.usingH2())
	require.True(t, a.usingH2())
	h2OriginRoundTrip(t, a, srv.URL)
	require.Equal(t, int32(1), accepts.Load(), "setH2(false) must not CloseIdle the shared mux")

	a.CloseIdleConnections()
	h2OriginRoundTrip(t, a, srv.URL)
	require.Equal(t, int32(1), accepts.Load(), "owned HTTP/1.1 CloseIdle must not drain pooled h2")
}

func TestPeerConn_CloseDoesNotRedialSharedH2(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	srv, accepts := startCountingH2CServer(t)
	cfg := PeerConnConfig{
		BaseURL:      srv.URL,
		DoorEscrowID: "1",
		DialSet:      PeerRPCDialSet{InferenceURL: srv.URL, H2URL: srv.URL},
	}
	keep := NewPeerConn(cfg)
	drop := NewPeerConn(cfg)
	t.Cleanup(keep.Close)

	keep.origin.setH2(true)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	require.NoError(t, err)
	resp, err := keep.http.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), accepts.Load())

	drop.Close()
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	require.NoError(t, err)
	resp, err = keep.http.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), accepts.Load(), "PeerConn.Close must not CloseIdle a sibling's h2 mux")
}

func TestAcquirePeerConn_LoserCloseDoesNotRedialSharedH2(t *testing.T) {
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	srv, accepts := startCountingH2CServer(t)
	cfg := PeerConnConfig{
		BaseURL:      srv.URL,
		HostAddress:  "gonka1h2loser",
		DirectMux:    true,
		DoorEscrowID: "1",
		DialSet:      PeerRPCDialSet{InferenceURL: srv.URL, H2URL: srv.URL},
	}
	keep := acquirePeerConn(cfg)
	t.Cleanup(keep.Release)

	keep.origin.setH2(true)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	require.NoError(t, err)
	resp, err := keep.http.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), accepts.Load())

	// Same as acquirePeerConn's discarded NewPeerConn: Close without Start.
	fresh := NewPeerConn(cfg)
	fresh.Close()

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	require.NoError(t, err)
	resp, err = keep.http.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), accepts.Load(), "discarded acquire Close must not drain the winner's h2 mux")
}

func TestRPCH2DialTLSServerNameUsesInferenceHost(t *testing.T) {
	const want = "join.example.com"
	leaf, parsed := testHostnameCert(t, want)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	var gotSNI atomic.Value
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			gotSNI.Store(hello.ServerName)
			return &tls.Config{
				Certificates: []tls.Certificate{leaf},
				NextProtos:   []string{http2.NextProtoTLS},
			}, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
			}(c)
		}
	}()

	tr := newRPCH2Transport(nil, false, want, 0, 0)
	tr.TLSClientConfig.RootCAs = pool
	// x/net would pass the H2URL dial host here (127.0.0.1 / proxy).
	cfg := &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    pool,
		NextProtos: []string{http2.NextProtoTLS},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := tr.DialTLSContext(ctx, "tcp", ln.Addr().String(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.Equal(t, want, gotSNI.Load())
	tlsConn, ok := conn.(*tls.Conn)
	require.True(t, ok)
	require.Equal(t, want, tlsConn.ConnectionState().ServerName)
}

func TestRPCH2DialTLSRejectsHTTP11ALPN(t *testing.T) {
	err := dialRPCH2TLS(t, []string{"http/1.1"}, []string{http2.NextProtoTLS, "http/1.1"})
	require.Error(t, err)
	require.True(t, isRPCH2Miss(err), "http/1.1 ALPN must be an h2 miss, not Connect")
	require.Contains(t, err.Error(), `unexpected ALPN protocol "http/1.1"`)
}

func TestRPCH2DialTLSRejectsEmptyALPN(t *testing.T) {
	err := dialRPCH2TLS(t, nil, []string{http2.NextProtoTLS})
	require.Error(t, err)
	require.True(t, isRPCH2Miss(err), "cert-only TLS (no ALPN) must be an h2 miss")
	require.Contains(t, err.Error(), `unexpected ALPN protocol ""`)
}

func dialRPCH2TLS(t *testing.T, serverProtos, clientProtos []string) error {
	t.Helper()
	const name = "join.example.com"
	leaf, parsed := testHostnameCert(t, name)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{leaf},
		NextProtos:   serverProtos,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
			}(c)
		}
	}()

	tr := newRPCH2Transport(nil, false, name, 0, 0)
	tr.TLSClientConfig.RootCAs = pool
	cfg := &tls.Config{
		ServerName: name,
		RootCAs:    pool,
		NextProtos: clientProtos,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := tr.DialTLSContext(ctx, "tcp", ln.Addr().String(), cfg)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return err
}

func testHostnameCert(t *testing.T, dnsName string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        parsed,
	}, parsed
}
