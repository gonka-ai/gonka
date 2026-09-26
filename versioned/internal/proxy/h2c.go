package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// DefaultH2MaxConcurrentStreams is SETTINGS_MAX_CONCURRENT_STREAMS on
// versiond's public listen and the child h2c listen. One ingress TCP
// (HAProxy proto h2, then this process, then one mux to the child)
// carries many peers' Watch streams. Not the per-peer interceptor cap
// (256). Keep lockstep with transport.DefaultH2MaxConcurrentStreams and
// HAProxy tune.h2.max-concurrent-streams (versioned cannot import
// devshard). HAProxy's default is 100; below that, golang's
// http2.Transport silently dials another TCP.
const DefaultH2MaxConcurrentStreams = 4096

// Child h2c reverse-proxy idle / PING — keep in lockstep with
// transport.DefaultRPCH2ReadIdleTimeout / PingTimeout / IdleConnTimeout
// (versioned cannot import the devshard module).
const (
	DefaultChildH2ReadIdleTimeout = 15 * time.Second
	DefaultChildH2PingTimeout     = 5 * time.Second
	DefaultChildH2IdleConnTimeout = 120 * time.Second
)

// H2CServer is the HTTP/2 settings for versiond's public listen.
func H2CServer() *http2.Server {
	return &http2.Server{MaxConcurrentStreams: DefaultH2MaxConcurrentStreams}
}

// ConfigureCleartextHTTP2 enables HTTP/1.1 and prior-knowledge HTTP/2 on srv.
// http.Server tracks those connections, so Shutdown sends GOAWAY and waits
// for the handler. h2c.NewHandler hijacks the conn without that tracking,
// and Shutdown returns while the stream is still running.
// MaxConcurrentStreams is applied by ConfigureServer (4096).
func ConfigureCleartextHTTP2(srv *http.Server) error {
	if srv == nil {
		return errors.New("nil http server")
	}
	if srv.Protocols == nil {
		srv.Protocols = new(http.Protocols)
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	return http2.ConfigureServer(srv, H2CServer())
}

// childTransport is shared across ReverseProxy instances for h2c children
// (one ReverseProxy is built per request). A new http2.Transport per request
// would be one TCP conn per stream; this one multiplexes to each child
// host:port. childHTTP1Transport is the dial for a child with no h2c listen.
var (
	childTransport      http.RoundTripper = newChildH2Transport()
	childHTTP1Transport http.RoundTripper = newChildHTTP1Transport()
)

func newChildHTTP1Transport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2: false,
		IdleConnTimeout:   DefaultChildH2IdleConnTimeout,
	}
}

func newChildH2Transport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP:       true,
		IdleConnTimeout: DefaultChildH2IdleConnTimeout,
		ReadIdleTimeout: DefaultChildH2ReadIdleTimeout,
		PingTimeout:     DefaultChildH2PingTimeout,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}
