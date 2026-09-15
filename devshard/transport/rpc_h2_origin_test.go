package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
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
