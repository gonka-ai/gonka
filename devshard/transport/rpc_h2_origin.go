package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

// originSwitchTransport sends Connect on InferenceUrl (HTTP/1.1) or rewrites
// to H2URL on a prior-knowledge http2.Transport. Never uses http.Transport
// against the h2 origin (that would silently speak HTTP/1.1).
type originSwitchTransport struct {
	h1    http.RoundTripper
	h2    http.RoundTripper
	h2URL *url.URL
	h2On  atomic.Bool
}

func newOriginSwitchTransport(h1, h2 http.RoundTripper, h2URL *url.URL) *originSwitchTransport {
	return &originSwitchTransport{h1: h1, h2: h2, h2URL: h2URL}
}

func (t *originSwitchTransport) setH2(on bool) {
	if t == nil || t.h2 == nil || t.h2URL == nil {
		return
	}
	// This PeerConn only. The h2 RoundTripper is process-pooled; closing
	// idle muxes here would tear down every other peer's connection to the
	// same origin.
	t.h2On.Store(on)
}

func (t *originSwitchTransport) usingH2() bool {
	return t != nil && t.h2On.Load()
}

func (t *originSwitchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.h2On.Load() && t.h2 != nil && t.h2URL != nil {
		return t.h2.RoundTrip(rewriteOrigin(req, t.h2URL))
	}
	return t.h1.RoundTrip(req)
}

func (t *originSwitchTransport) CloseIdleConnections() {
	if t == nil {
		return
	}
	// h1 is owned by this PeerConn. h2 is the process pool (rpch2Clients).
	closeIdleConnections(t.h1)
}

func rewriteOrigin(req *http.Request, origin *url.URL) *http.Request {
	out := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = origin.Scheme
	u.Host = origin.Host
	out.URL = &u
	out.Host = origin.Host
	return out
}

// rpch2ClientPool is the process-wide h2 client. Overlay (same H2URL) and
// join-same-host mux every PeerConn onto one TCP. Distinct remotes (distinct
// H2URL) stay separate. TLS also keys on SNI so two InferenceUrl hostnames
// do not share a handshake. h2c ignores SNI so overlay through proxy muxes.
type rpch2ClientPool struct {
	mu sync.Mutex
	m  map[string]*http2.Transport
}

func newRPCH2ClientPool() *rpch2ClientPool {
	return &rpch2ClientPool{m: make(map[string]*http2.Transport)}
}

func rpch2ClientKey(h2URL *url.URL, serverName string) string {
	if h2URL == nil {
		return ""
	}
	scheme := strings.ToLower(h2URL.Scheme)
	host := strings.ToLower(h2URL.Host)
	if h2cOrigin(h2URL) {
		return scheme + "://" + host
	}
	return scheme + "://" + host + "\x00" + strings.ToLower(serverName)
}

func (p *rpch2ClientPool) get(h2URL *url.URL, serverName string, dial func(context.Context, string, string) (net.Conn, error), readIdle, ping time.Duration) *http2.Transport {
	if p == nil || h2URL == nil {
		return nil
	}
	key := rpch2ClientKey(h2URL, serverName)
	p.mu.Lock()
	defer p.mu.Unlock()
	if tr := p.m[key]; tr != nil {
		return tr
	}
	tr := newRPCH2Transport(dial, h2cOrigin(h2URL), serverName, readIdle, ping)
	p.m[key] = tr
	return tr
}

func (p *rpch2ClientPool) reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, tr := range p.m {
		tr.CloseIdleConnections()
	}
	p.m = make(map[string]*http2.Transport)
}

var rpch2Clients = newRPCH2ClientPool()

// ResetRPCH2ClientPoolForTest drops pooled h2 clients. Tests that pin
// transport identity or timeouts must call this first.
func ResetRPCH2ClientPoolForTest() {
	rpch2Clients.reset()
}

func newRPCH2Transport(dial func(context.Context, string, string) (net.Conn, error), h2c bool, serverName string, readIdle, ping time.Duration) *http2.Transport {
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	if readIdle <= 0 {
		readIdle = DefaultRPCH2ReadIdleTimeout
	}
	if ping <= 0 {
		ping = DefaultRPCH2PingTimeout
	}
	tlsCfg := &tls.Config{}
	if serverName != "" {
		tlsCfg.ServerName = serverName
	}
	return &http2.Transport{
		AllowHTTP:       h2c,
		TLSClientConfig: tlsCfg,
		IdleConnTimeout: DefaultRPCH2IdleConnTimeout,
		ReadIdleTimeout: readIdle,
		PingTimeout:     ping,
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if h2c {
				return conn, nil
			}
			cfg = rpch2TLSConfig(cfg, serverName)
			tlsConn := tls.Client(conn, cfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			if err := rpch2RequireALPN(tlsConn.ConnectionState()); err != nil {
				_ = tlsConn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
}

// rpch2TLSConfig clones cfg and sets ServerName to InferenceUrl's hostname.
// x/net's newTLSConfig would otherwise use the dial address (H2_HOST / proxy).
func rpch2TLSConfig(cfg *tls.Config, serverName string) *tls.Config {
	if cfg != nil {
		cfg = cfg.Clone()
	} else {
		cfg = &tls.Config{}
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	return cfg
}

// rpch2RequireALPN is x/net's post-handshake check. DialTLSContext skips it,
// so a TLS listener that only offers http/1.1 would otherwise get an HTTP/2
// preface. Empty ALPN (cert-only proxy) is the same miss.
func rpch2RequireALPN(state tls.ConnectionState) error {
	if p := state.NegotiatedProtocol; p != http2.NextProtoTLS {
		return fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", p, http2.NextProtoTLS)
	}
	return nil
}

// SetH2TLSRootCAsForTest installs RootCAs on the h2 TLS client so tests can
// handshake a httptest TLS origin. Production uses the system pool.
func (p *PeerConn) SetH2TLSRootCAsForTest(pool *x509.CertPool) {
	if p == nil || p.origin == nil || p.origin.h2 == nil {
		return
	}
	h2, ok := p.origin.h2.(*http2.Transport)
	if !ok || h2.TLSClientConfig == nil {
		return
	}
	h2.TLSClientConfig.RootCAs = pool
}

func parseH2Origin(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

func h2cOrigin(u *url.URL) bool {
	return u != nil && strings.EqualFold(u.Scheme, "http")
}
