package proxy

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// DefaultH2MaxConcurrentStreams is SETTINGS_MAX_CONCURRENT_STREAMS on
// versiond's public listen. One ingress TCP connection (later HAProxy proto h2)
// carries many peer /rpc/ streams; this is not the per-peer child cap (256).
const DefaultH2MaxConcurrentStreams = 4096

// H2CServer is the HTTP/2 settings for versiond's public listen.
func H2CServer() *http2.Server {
	return &http2.Server{MaxConcurrentStreams: DefaultH2MaxConcurrentStreams}
}

// H2CHandler wraps h so one TCP connection can carry HTTP/1.1 and h2c
// (prior-knowledge HTTP/2). A listen without this wrapper cannot complete an
// HTTP/2 client (fail closed; a default http.Client would silently speak 1.1).
func H2CHandler(h http.Handler) http.Handler {
	return h2c.NewHandler(h, H2CServer())
}

// childTransport is shared across ReverseProxy instances (one is built per
// request). A new http2.Transport per request would be one TCP conn per
// stream; this one multiplexes to each child host:port.
var childTransport http.RoundTripper = newChildH2Transport()

func newChildH2Transport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}
