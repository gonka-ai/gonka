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

	"devshard/logging"

	"golang.org/x/net/http2"
)

const (
	envRPCH2Port = "DEVSHARD_RPC_H2_PORT"
	// envRPCH2Host is the TCP dial hostname for the h2 origin (overlay
	// "proxy"). TLS SNI and certificate verify always use InferenceUrl's
	// hostname, never this value.
	envRPCH2Host    = "DEVSHARD_RPC_H2_HOST"
	envRPCH2Upgrade = "DEVSHARD_RPC_H2_UPGRADE"
	// envRPCH2FrontHost lists shared front doors (comma-separated hostnames,
	// testenv: versiond-router). The overlay dial host replaces only those.
	// Any other InferenceUrl is a direct participant and stays on h2c at its
	// own origin. Unset applies the overlay host to every base.
	envRPCH2FrontHost = "DEVSHARD_RPC_H2_FRONT_HOST"
	envRPCH2GRPC      = "DEVSHARD_RPC_GRPC"

	// DefaultRPCH2Port is the in-network overlay / join RPC HTTP/2 listen.
	// JSON and catalog stay on InferenceUrl (testenv :8080). Unset env still
	// means no h2 dial; this is only the number operators set when opting in.
	DefaultRPCH2Port = 8443

	// DefaultRPCH2MissTTL is retained for the miss-cache unit tests.
	// Attach does not record a miss: a current hop fails closed and the
	// next attempt probes h2 again. HTTP/1.1 is only the dial when H2URL
	// is empty (upgrade off or port unset), which is the 0.2.15-v5 pin.
	DefaultRPCH2MissTTL = 30 * time.Minute

	// DefaultRPCH2MissTTLJitter is ± this fraction of DefaultRPCH2MissTTL so
	// distinct InferenceUrl hosts do not expire on the same tick.
	DefaultRPCH2MissTTLJitter = 0.10

	// DefaultRPCH2ProbeTimeout bounds the h2 Attach probe inside
	// DefaultAttachTimeout. A blackhole cannot consume the whole handshake.
	// A miss returns; it does not spend the remainder on InferenceUrl.
	DefaultRPCH2ProbeTimeout = time.Second

	// DefaultRPCH2IdleConnTimeout matches HTTP/1.1 IdleConnTimeout so a
	// quiet mux does not last until process exit. versiond's child reverse
	// proxy copies this as DefaultChildH2IdleConnTimeout.
	DefaultRPCH2IdleConnTimeout = 120 * time.Second

	// DefaultRPCH2ReadIdleTimeout PINGs if no HTTP/2 frame arrives. Under
	// WatchStale (90s) so a half-open mux does not wait for three missed
	// heartbeats. Watch beats still reset the idle timer. versiond's child
	// reverse proxy copies this as DefaultChildH2ReadIdleTimeout.
	DefaultRPCH2ReadIdleTimeout = 15 * time.Second

	// DefaultRPCH2PingTimeout is how long a health-check PING may wait.
	// versiond's child reverse proxy copies this as DefaultChildH2PingTimeout.
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

// keepDirectH2Origin is true when DEVSHARD_RPC_H2_FRONT_HOST is set and base
// names some other host. Catch-up and gossip to a solo must not be rewritten
// onto the overlay proxy: the router hashes by escrow and the solo never
// sees the diff.
func keepDirectH2Origin(base string) bool {
	raw := strings.TrimSpace(os.Getenv(envRPCH2FrontHost))
	if raw == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	for _, part := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(part), host) {
			return false
		}
	}
	return true
}

// directH2Origin is the base URL's scheme and host, with no path. versiond
// accepts h2c on that public listen.
func directH2Origin(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("inference URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("inference URL %q has no host", base)
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
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

func skipRPCH2(inferenceURL string) bool {
	return rpch2Miss.skip(inferenceHostKey(inferenceURL))
}

// ResetRPCH2MissCacheForTest clears the process-wide h2-miss map and restores
// the 30-minute TTL and wall clock.
func ResetRPCH2MissCacheForTest() {
	rpch2Miss = newH2MissCache(DefaultRPCH2MissTTL, nil)
	rpch2MissLog = newH2MissLog(DefaultRPCH2MissTTL, nil)
}

// InstallRPCH2MissCacheForTest replaces the process cache clock and TTL.
// Jitter is off so tests can expire at exactly ttl.
func InstallRPCH2MissCacheForTest(ttl time.Duration, now func() time.Time) {
	c := newH2MissCache(ttl, now)
	c.jitter = func(d time.Duration) time.Duration { return d }
	rpch2Miss = c
	rpch2MissLog = newH2MissLog(ttl, now)
}

// RPCH2MissCachedForTest is whether this InferenceUrl host is skipping h2.
func RPCH2MissCachedForTest(inferenceURL string) bool {
	return skipRPCH2(inferenceURL)
}

// h2MissLog is one Warn per InferenceUrl host per miss TTL. Later misses in
// that window are Debug. The attach loop retries a dead hop, and several
// PeerConns can miss the same host together; only the first of those is Warn.
type h2MissLog struct {
	mu    sync.Mutex
	until map[string]time.Time
	now   func() time.Time
	ttl   time.Duration
}

func newH2MissLog(ttl time.Duration, now func() time.Time) *h2MissLog {
	if ttl <= 0 {
		ttl = DefaultRPCH2MissTTL
	}
	if now == nil {
		now = time.Now
	}
	return &h2MissLog{until: make(map[string]time.Time), now: now, ttl: ttl}
}

var rpch2MissLog = newH2MissLog(DefaultRPCH2MissTTL, nil)

// first reports whether this key should Warn. An empty key always Warns.
// The check and the TTL stamp are one critical section, so concurrent
// misses of the same host produce one Warn.
func (l *h2MissLog) first(key string) bool {
	if l == nil || key == "" {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.until[key]; ok && now.Before(until) {
		return false
	}
	l.until[key] = now.Add(l.ttl)
	return true
}

func noteRPCH2Miss(host, h2URL string, err error) {
	kv := []any{
		"subsystem", "transport",
		"host", host,
		"h2_url", h2URL,
		"error", err,
	}
	if rpch2MissLog.first(host) {
		logging.Warn("peer rpc h2 origin missed; failing closed", kv...)
		return
	}
	logging.Debug("peer rpc h2 origin missed; failing closed", kv...)
}

// isRPCH2Miss is a failed h2 origin (closed, not h2c, timeout, refuse,
// preface). Connect status from a working HTTP/2 server — including
// Unavailable / Unknown after an RPC — must not pin HTTP/1.1.
// DeadlineExceeded / Canceled are probe misses only (1s blackhole). A
// live TTL refresh uses isRPCH2TransportMiss so a slow Attach does not
// drop Watch or rememberRPCH2Miss.
func isRPCH2Miss(err error) bool {
	if err == nil || errors.Is(err, errAttachTTL) {
		return false
	}
	if isContextDone(err) {
		return true
	}
	return isRPCH2TransportMiss(err)
}

func isContextDone(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// isRPCH2TransportMiss is a dead origin (RST, refuse, not h2c, preface).
// DeadlineExceeded / Canceled are not transport misses.
func isRPCH2TransportMiss(err error) bool {
	if err == nil || errors.Is(err, errAttachTTL) || isContextDone(err) {
		return false
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
