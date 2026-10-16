package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
// to H2URL on a prior-knowledge http2.Transport. Both are the same public
// origin; the switch is which protocol this PeerConn speaks there. Never
// uses http.Transport against the h2 origin (that would silently speak
// HTTP/1.1 on a mux).
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

// rpch2ClientPool is the process-wide h2 client. PeerConns to the same
// origin (one host's escrows, or several hosts behind one public proxy)
// mux onto one TCP. Distinct origins stay separate. TLS also keys on SNI
// so two InferenceUrl hostnames do not share a handshake.
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

func (p *rpch2ClientPool) get(h2URL *url.URL, serverName string, dial func(context.Context, string, string) (net.Conn, error), readIdle, ping, probe time.Duration) *http2.Transport {
	if p == nil || h2URL == nil {
		return nil
	}
	key := rpch2ClientKey(h2URL, serverName)
	p.mu.Lock()
	defer p.mu.Unlock()
	if tr := p.m[key]; tr != nil {
		return tr
	}
	tr := newRPCH2Transport(dial, h2cOrigin(h2URL), serverName, readIdle, ping, probe)
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

func newRPCH2Transport(dial func(context.Context, string, string) (net.Conn, error), h2c bool, serverName string, readIdle, ping, probe time.Duration) *http2.Transport {
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
	if probe <= 0 {
		probe = DefaultRPCH2ProbeTimeout
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
			// Probe covers dial and TLS only. The Attach RPC keeps the
			// caller's deadline (DefaultAttachTimeout).
			dialCtx, cancel := context.WithTimeout(ctx, probe)
			defer cancel()
			conn, err := dial(dialCtx, network, addr)
			if err != nil {
				return nil, rpch2ProbeTimeoutError(err)
			}
			if h2c {
				return conn, nil
			}
			cfg = rpch2TLSConfig(cfg, serverName)
			tlsConn := tls.Client(conn, cfg)
			if err := tlsConn.HandshakeContext(dialCtx); err != nil {
				_ = conn.Close()
				return nil, rpch2ProbeTimeoutError(err)
			}
			if err := rpch2RequireALPN(tlsConn.ConnectionState()); err != nil {
				_ = tlsConn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
}

// rpch2ProbeTimeoutError turns a probe-context deadline into a dial error.
// The Attach RPC's own context deadline must stay a deadline so it is not
// an h2 miss. Parent cancel is left unchanged.
func rpch2ProbeTimeoutError(err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("h2 probe timeout")}
}

// rpch2TLSConfig clones cfg, sets ServerName to InferenceUrl's hostname,
// and offers only PeerRPCALPN. x/net's newTLSConfig would otherwise offer
// h2 and http/1.1, which the public proxy sends to nginx.
func rpch2TLSConfig(cfg *tls.Config, serverName string) *tls.Config {
	if cfg != nil {
		cfg = cfg.Clone()
	} else {
		cfg = &tls.Config{}
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	cfg.NextProtos = []string{PeerRPCALPN}
	return cfg
}

// rpch2RequireALPN replaces x/net's post-handshake check, which
// DialTLSContext skips. The peer-RPC listener negotiates PeerRPCALPN.
// A listener that offers something else, or nothing (nginx, a cert-only
// proxy, an older wrapping), must not get an HTTP/2 preface: that is the
// miss that sends this peer to HTTP/1.1.
func rpch2RequireALPN(state tls.ConnectionState) error {
	if p := state.NegotiatedProtocol; p != PeerRPCALPN {
		return fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", p, PeerRPCALPN)
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
