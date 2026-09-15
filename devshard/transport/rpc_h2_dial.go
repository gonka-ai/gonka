package transport

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

const (
	envRPCH2Port = "DEVSHARD_RPC_H2_PORT"
	// envRPCH2Host is the TCP dial hostname for the h2 origin (overlay
	// "proxy"). TLS SNI and certificate verify always use InferenceUrl's
	// hostname, never this value.
	envRPCH2Host    = "DEVSHARD_RPC_H2_HOST"
	envRPCH2Upgrade = "DEVSHARD_RPC_H2_UPGRADE"
	envRPCH2GRPC    = "DEVSHARD_RPC_GRPC"

	// DefaultRPCH2Port is the in-network overlay / join RPC HTTP/2 listen.
	// JSON and catalog stay on InferenceUrl (testenv :8080). Unset env still
	// means no h2 dial; this is only the number operators set when opting in.
	DefaultRPCH2Port = 8443

	// DefaultRPCH2MissTTL is how long a host stays on HTTP/1.1 after an h2
	// origin miss. Reconnects skip h2 until this elapses, then probe again.
	DefaultRPCH2MissTTL = 30 * time.Minute

	// DefaultRPCH2MissTTLJitter is ± this fraction of DefaultRPCH2MissTTL so
	// distinct InferenceUrl hosts do not expire on the same tick.
	DefaultRPCH2MissTTLJitter = 0.10

	// DefaultRPCH2ProbeTimeout bounds the h2 Attach probe inside
	// DefaultAttachTimeout. A blackhole cannot consume the whole handshake;
	// HTTP/1.1 gets the remainder.
	DefaultRPCH2ProbeTimeout = time.Second

	// DefaultRPCH2IdleConnTimeout matches HTTP/1.1 IdleConnTimeout so a
	// quiet mux does not last until process exit.
	DefaultRPCH2IdleConnTimeout = 120 * time.Second

	// DefaultRPCH2ReadIdleTimeout PINGs if no HTTP/2 frame arrives. Under
	// WatchStale (90s) so a half-open mux does not wait for three missed
	// heartbeats. Watch beats still reset the idle timer.
	DefaultRPCH2ReadIdleTimeout = 15 * time.Second

	// DefaultRPCH2PingTimeout is how long a health-check PING may wait.
	DefaultRPCH2PingTimeout = 5 * time.Second
)

// PeerRPCDialSet is the Phase 6 client origin pair. Upgrade stays off unless
// DEVSHARD_RPC_H2_UPGRADE is on and DEVSHARD_RPC_H2_PORT is set.
type PeerRPCDialSet struct {
	InferenceURL string
	H2URL        string
}

// Origins returns InferenceURL, plus H2URL when upgrade is on. The client
// dial set is only these origins — never a child bind.
func (s PeerRPCDialSet) Origins() []string {
	out := make([]string, 0, 2)
	if s.InferenceURL != "" {
		out = append(out, s.InferenceURL)
	}
	if s.H2URL != "" {
		out = append(out, s.H2URL)
	}
	return out
}

// RPCH2UpgradeEnabled reports whether DEVSHARD_RPC_H2_UPGRADE is on.
// Empty / unset / "0" / "false" / "off" stay HTTP/1.1 on InferenceUrl.
func RPCH2UpgradeEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// RPCH2GRPCEnabled reports whether native gRPC (connect.WithGRPC) is on.
// Empty / unset stays Connect. HTTP/1.1 fallback never uses gRPC.
func RPCH2GRPCEnabled(raw string) bool {
	return RPCH2UpgradeEnabled(raw)
}

// PeerRPCDialSetFromEnv reads InferenceURL plus optional H2 host/port/upgrade.
func PeerRPCDialSetFromEnv(inferenceURL string) (PeerRPCDialSet, error) {
	return PeerRPCDialSetFrom(
		inferenceURL,
		os.Getenv(envRPCH2Host),
		os.Getenv(envRPCH2Port),
		os.Getenv(envRPCH2Upgrade),
	)
}

// PeerRPCDialSetFrom builds the origin pair. Upgrade off or port unset →
// H2URL empty. Host empty rewrites InferenceURL's hostname to h2Port
// (join same-host). Overlay sets host to "proxy" as the TCP dial name
// only; TLS SNI/verify stay on InferenceURL's hostname.
func PeerRPCDialSetFrom(inferenceURL, h2Host, h2Port, upgrade string) (PeerRPCDialSet, error) {
	out := PeerRPCDialSet{InferenceURL: strings.TrimSpace(inferenceURL)}
	if out.InferenceURL == "" {
		return PeerRPCDialSet{}, fmt.Errorf("inference URL is empty")
	}
	if _, err := url.Parse(out.InferenceURL); err != nil {
		return PeerRPCDialSet{}, fmt.Errorf("inference URL: %w", err)
	}
	if !RPCH2UpgradeEnabled(upgrade) {
		return out, nil
	}
	raw := strings.TrimSpace(h2Port)
	if raw == "" {
		return out, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > 65535 {
		return PeerRPCDialSet{}, fmt.Errorf("DEVSHARD_RPC_H2_PORT %q is not a valid port", raw)
	}
	h2, err := originWithHostPort(out.InferenceURL, strings.TrimSpace(h2Host), n)
	if err != nil {
		return PeerRPCDialSet{}, err
	}
	out.H2URL = h2
	return out, nil
}

func originWithHostPort(raw, host string, port int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("inference URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("inference URL %q has no host", raw)
	}
	if host == "" {
		host = u.Hostname()
	}
	if host == "" {
		return "", fmt.Errorf("inference URL %q has no hostname", raw)
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func inferenceHostKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return host
}

// rpcH2ServerName is TLS SNI and certificate verify: InferenceUrl's hostname.
// DEVSHARD_RPC_H2_HOST is a dial name only and must not appear here.
func rpcH2ServerName(inferenceURL string) string {
	u, err := url.Parse(strings.TrimSpace(inferenceURL))
	if err != nil || u == nil {
		return ""
	}
	return u.Hostname()
}

type h2MissCache struct {
	mu     sync.Mutex
	until  map[string]time.Time
	now    func() time.Time
	ttl    time.Duration
	jitter func(time.Duration) time.Duration
}

func newH2MissCache(ttl time.Duration, now func() time.Time) *h2MissCache {
	if ttl <= 0 {
		ttl = DefaultRPCH2MissTTL
	}
	if now == nil {
		now = time.Now
	}
	return &h2MissCache{until: make(map[string]time.Time), now: now, ttl: ttl, jitter: jitterRPCH2MissTTL}
}

// jitterRPCH2MissTTL returns ttl ± 10% so unique hosts do not retry together.
func jitterRPCH2MissTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	span := time.Duration(float64(ttl) * DefaultRPCH2MissTTLJitter)
	if span <= 0 {
		return ttl
	}
	off := time.Duration(rand.Int64N(int64(2*span+1))) - span
	return ttl + off
}

func (c *h2MissCache) skip(host string) bool {
	if c == nil || host == "" {
		return false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.until[host]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(c.until, host)
		return false
	}
	return true
}

func (c *h2MissCache) remember(host string) {
	if c == nil || host == "" {
		return
	}
	now := c.now()
	ttl := c.ttl
	if c.jitter != nil {
		ttl = c.jitter(ttl)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[host] = now.Add(ttl)
}

func (c *h2MissCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = make(map[string]time.Time)
}

var rpch2Miss = newH2MissCache(DefaultRPCH2MissTTL, nil)

func rememberRPCH2Miss(inferenceURL string) {
	rpch2Miss.remember(inferenceHostKey(inferenceURL))
}

func skipRPCH2(inferenceURL string) bool {
	return rpch2Miss.skip(inferenceHostKey(inferenceURL))
}

// ResetRPCH2MissCacheForTest clears the process-wide h2-miss map and restores
// the 30-minute TTL and wall clock.
func ResetRPCH2MissCacheForTest() {
	rpch2Miss = newH2MissCache(DefaultRPCH2MissTTL, nil)
}

// InstallRPCH2MissCacheForTest replaces the process cache clock and TTL.
// Jitter is off so tests can expire at exactly ttl.
func InstallRPCH2MissCacheForTest(ttl time.Duration, now func() time.Time) {
	c := newH2MissCache(ttl, now)
	c.jitter = func(d time.Duration) time.Duration { return d }
	rpch2Miss = c
}

// RPCH2MissCachedForTest is whether this InferenceUrl host is skipping h2.
func RPCH2MissCachedForTest(inferenceURL string) bool {
	return skipRPCH2(inferenceURL)
}

// isRPCH2Miss is a failed h2 origin (closed, not h2c, timeout, refuse,
// preface). Connect status from a working HTTP/2 server — including
// Unavailable / Unknown after an RPC — must not pin HTTP/1.1.
func isRPCH2Miss(err error) bool {
	if err == nil || errors.Is(err, errAttachTTL) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return true
	}
	var connErr http2.ConnectionError
	if errors.As(err, &connErr) {
		return true
	}
	return http2TransportMessage(err)
}

func http2TransportMessage(err error) bool {
	for err != nil {
		msg := err.Error()
		if strings.Contains(msg, "http2:") ||
			strings.Contains(msg, "unencrypted HTTP/2") ||
			strings.Contains(msg, "malformed HTTP response") {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
