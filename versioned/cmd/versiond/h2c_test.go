package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"versioned/internal/config"
	"versioned/internal/host"
	"versioned/internal/process"
	"versioned/internal/proxy"
)

func TestPublicListen_HTTP1AndH2C(t *testing.T) {
	srv := httptest.NewServer(proxy.H2CHandler(publicHandler(
		process.NewManager(config.Config{BasePort: 5000}),
		host.NewController(),
		nil,
	)))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.ProtoMajor != 1 {
		t.Fatalf("HTTP/1.1 client proto major = %d, want 1", resp.ProtoMajor)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d", resp.StatusCode)
	}

	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	h2resp, err := client.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h2resp.Body.Close() })
	_, _ = io.Copy(io.Discard, h2resp.Body)
	if h2resp.ProtoMajor != 2 {
		t.Fatalf("h2c client proto major = %d, want 2", h2resp.ProtoMajor)
	}
	if h2resp.StatusCode != http.StatusOK {
		t.Fatalf("h2c /healthz status = %d", h2resp.StatusCode)
	}
}
