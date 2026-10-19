package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"devshard/logging"

	"golang.org/x/net/http2"
)

const (
	// envRPCH2Upgrade turns the HTTP/2 probe off. Unset is on: Attach
	// first tries prior-knowledge HTTP/2 (h2c on an HTTP InferenceUrl, TLS
	// with PeerRPCALPN on HTTPS) on InferenceUrl's own origin, and drops to
	// Connect over HTTP/1.1 on the same origin when that origin does not
	// speak it. "0" / "false" / "no" / "off" stay on HTTP/1.1.
	envRPCH2Upgrade = "DEVSHARD_RPC_H2_UPGRADE"
	envRPCH2GRPC    = "DEVSHARD_RPC_GRPC"

	// PeerRPCALPN is the ALPN token a peer offers on an HTTPS InferenceUrl.
	// The public proxy routes a ClientHello carrying it to the peer-RPC
	// listener on the same :443, which negotiates the token and speaks
	// HTTP/2. nginx behind an older wrapping negotiates nothing, so the
	// probe ends there and the peer uses HTTP/1.1. The token is not "h2":
	// browsers and the JSON clients offer h2 and must keep landing on nginx.
	PeerRPCALPN = "gonka-rpc/1"

	// DefaultRPCH2MissTTL is how long an InferenceUrl origin stays on
	// HTTP/1.1 after its origin did not speak HTTP/2. The next Attach or
	// session refresh after that probes HTTP/2 again, so a host whose
	// wrapping was updated in the meantime is picked up without a restart.
	DefaultRPCH2MissTTL = 30 * time.Minute

	// DefaultRPCH2MissTTLJitter is ± this fraction of DefaultRPCH2MissTTL so
	// distinct InferenceUrl hosts do not expire on the same tick.
	DefaultRPCH2MissTTLJitter = 0.10

	// DefaultRPCH2ProbeTimeout bounds dial and TLS for an h2 origin.
	// The Attach RPC uses DefaultAttachTimeout. A blackhole cannot hold
	// the handshake. A context deadline on Attach is not an h2 miss.
	DefaultRPCH2ProbeTimeout = 3 * time.Second

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

// PeerRPCDialSet is the client origin pair. Both are InferenceUrl's own
// origin: peer RPC shares the public port with the JSON API, and the two
// sides agree on HTTP/2 or HTTP/1.1 in band. H2URL is empty only when
// DEVSHARD_RPC_H2_UPGRADE is off.
type PeerRPCDialSet struct {
	InferenceURL string
	H2URL        string
}

// Origins returns InferenceURL, plus H2URL when the HTTP/2 probe is on.
// The client dial set is only these origins — never a child bind.
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

// RPCH2UpgradeEnabled reports whether the HTTP/2 probe is on. Unset is on.
// "0" / "false" / "no" / "off" stay on Connect over HTTP/1.1.
func RPCH2UpgradeEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// RPCH2GRPCEnabled reports whether native gRPC (connect.WithGRPC) is on.
// Empty / unset stays Connect. HTTP/1.1 fallback never uses gRPC.
func RPCH2GRPCEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// PeerRPCDialSetFromEnv reads InferenceURL plus the optional upgrade switch.
func PeerRPCDialSetFromEnv(inferenceURL string) (PeerRPCDialSet, error) {
	return PeerRPCDialSetFrom(inferenceURL, os.Getenv(envRPCH2Upgrade))
}

// PeerRPCDialSetFrom builds the origin pair. H2URL is InferenceURL's
// scheme and host with no path: the same listen, so the same SNI and
// certificate. Upgrade off leaves H2URL empty.
func PeerRPCDialSetFrom(inferenceURL, upgrade string) (PeerRPCDialSet, error) {
	out := PeerRPCDialSet{InferenceURL: strings.TrimSpace(inferenceURL)}
	if out.InferenceURL == "" {
		return PeerRPCDialSet{}, fmt.Errorf("inference URL is empty")
	}
	origin, err := inferenceOrigin(out.InferenceURL)
	if err != nil {
		return PeerRPCDialSet{}, err
	}
	if !RPCH2UpgradeEnabled(upgrade) {
		return out, nil
	}
	out.H2URL = origin
	return out, nil
}

// inferenceOrigin is the base URL's scheme and host, with no path.
func inferenceOrigin(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("inference URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("inference URL %q has no host", base)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("inference URL %q: scheme %q is not http or https", base, u.Scheme)
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// inferenceHostKey identifies a transport origin, not a hostname or RPC path.
// Explicit default ports, host case and IPv6 spellings share the same key.
func inferenceHostKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// rpcH2ServerName is TLS SNI and certificate verify: InferenceUrl's hostname.
func rpcH2ServerName(inferenceURL string) string {
	u, err := url.Parse(strings.TrimSpace(inferenceURL))
	if err != nil || u == nil {
		return ""
	}
	return u.Hostname()
}

// DefaultRPCH2CloseBackoff is the initial delay after ambiguous connection loss.
const DefaultRPCH2CloseBackoff = 30 * time.Second

// A probe lease serializes discovery per origin. Retain failure history across
// expiry, but discard it on a successful Attach. Generation checks prevent a
// late result from overwriting a newer decision.
type h2MissEntry struct {
	until      time.Time
	backoff    time.Duration
	generation uint64
	probing    bool
	done       chan struct{}
}
type h2MissCache struct {
	mu      sync.Mutex
	entries map[string]h2MissEntry
	serial  uint64
	now     func() time.Time
	ttl     time.Duration
	jitter  func(time.Duration) time.Duration
}

func newH2MissCache(ttl time.Duration, now func() time.Time) *h2MissCache {
	if ttl <= 0 {
		ttl = DefaultRPCH2MissTTL
	}
	if now == nil {
		now = time.Now
	}
	return &h2MissCache{entries: make(map[string]h2MissEntry), now: now, ttl: ttl, jitter: jitterRPCH2MissTTL}
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

func (c *h2MissCache) skip(key string) bool {
	if c == nil || key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	return e.probing || c.now().Before(e.until)
}

func (c *h2MissCache) begin(key string) (uint64, bool) {
	if c == nil || key == "" {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e.probing || c.now().Before(e.until) {
		return 0, false
	}
	// Bound retained history even when governance retires origins. Never evict
	// an active lease. An origin may be rediscovered after idle history eviction.
	if _, found := c.entries[key]; !found && len(c.entries) >= 4096 {
		oldestKey := ""
		var oldest time.Time
		for k, v := range c.entries {
			if !v.probing && (oldestKey == "" || v.until.Before(oldest)) {
				oldestKey, oldest = k, v.until
			}
		}
		if oldestKey == "" {
			return 0, false
		}
		delete(c.entries, oldestKey)
	}
	c.serial++
	e.generation, e.probing = c.serial, true
	e.done = make(chan struct{})
	c.entries[key] = e
	return e.generation, true
}

// Wait for an in-flight discovery rather than moving healthy siblings to H1.
func (c *h2MissCache) acquire(ctx context.Context, key string) (uint64, bool) {
	for {
		if generation, ok := c.begin(key); ok {
			return generation, true
		}
		c.mu.Lock()
		e := c.entries[key]
		c.mu.Unlock()
		if !e.probing {
			return 0, false
		}
		select {
		case <-e.done:
		case <-ctx.Done():
			return 0, false
		}
	}
}

func (c *h2MissCache) finish(key string, generation uint64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !e.probing || e.generation != generation {
		return
	}
	close(e.done)
	if err == nil {
		delete(c.entries, key)
		return
	}
	e.probing = false
	var wait time.Duration
	switch classifyRPCH2Miss(err) {
	case h2ProtocolMiss:
		wait = c.ttl
	case h2Closed:
		if e.backoff == 0 {
			e.backoff = min(DefaultRPCH2CloseBackoff, c.ttl)
		} else {
			e.backoff = min(e.backoff*2, c.ttl)
		}
		wait = e.backoff
	}
	if wait == 0 && e.backoff == 0 {
		delete(c.entries, key)
		return
	}
	if c.jitter != nil {
		wait = c.jitter(wait)
		if e.backoff > 0 {
			wait = min(wait, c.ttl)
		}
	}
	e.until = c.now().Add(wait)
	c.entries[key] = e
}

// remember is used by tests to model a sibling's confirmed protocol miss.
func (c *h2MissCache) remember(key string) {
	if gen, ok := c.begin(key); ok {
		c.finish(key, gen, errors.New("http2: unexpected ALPN protocol"))
	}
}

func (c *h2MissCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]h2MissEntry)
}

var rpch2Miss = newH2MissCache(DefaultRPCH2MissTTL, nil)

// skipRPCH2 is whether this InferenceUrl host missed HTTP/2 inside the
// miss TTL. Attach then goes straight to HTTP/1.1.
func skipRPCH2(inferenceURL string) bool {
	return rpch2Miss.skip(inferenceHostKey(inferenceURL))
}

// rememberRPCH2Miss pins this InferenceUrl host to HTTP/1.1 for the miss TTL.
func rememberRPCH2Miss(inferenceURL string) {
	rpch2Miss.remember(inferenceHostKey(inferenceURL))
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
	rpch2Miss.mu.Lock()
	defer rpch2Miss.mu.Unlock()
	return rpch2Miss.now().Before(rpch2Miss.entries[inferenceHostKey(inferenceURL)].until)
}

// h2MissLog is one Warn per InferenceUrl host per miss TTL. Later misses in
// that window are Debug. Several PeerConns can miss the same host together;
// only the first of those is Warn.
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
	kv = append(kv, "miss_kind", classifyRPCH2Miss(err))
	if rpch2MissLog.first(host) {
		logging.Warn("peer rpc HTTP/2 discovery deferred; using HTTP/1.1", kv...)
		return
	}
	logging.Debug("peer rpc HTTP/2 discovery deferred; using HTTP/1.1", kv...)
}

type h2MissKind uint8

const (
	h2NotMiss h2MissKind = iota
	h2ProtocolMiss
	h2Closed
)

// Application refusals, cancellation and failures common to both transports
// never change protocol capability. Only specific wire evidence gets a long
// pin; an ambiguous close gets a short, increasing discovery backoff.
func classifyRPCH2Miss(err error) h2MissKind {
	if err == nil || errors.Is(err, errAttachTTL) || isContextDone(err) || connect.IsWireError(err) {
		return h2NotMiss
	}
	var dns *net.DNSError
	var ne net.Error
	if errors.As(err, &dns) || errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &ne) && ne.Timeout()) {
		return h2NotMiss
	}
	var stream http2.StreamError
	if errors.As(err, &stream) {
		return h2NotMiss
	}
	msg := err.Error()
	if strings.Contains(msg, "http2: unexpected ALPN protocol") ||
		strings.Contains(msg, "remote error: tls: no application protocol") ||
		strings.Contains(msg, "frame header looked like an HTTP/1.1 header") ||
		strings.Contains(msg, "malformed HTTP response") {
		return h2ProtocolMiss
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return h2Closed
	}
	var conn http2.ConnectionError
	if errors.As(err, &conn) {
		return h2Closed
	}
	// These transport-generated errors do not prove protocol incompatibility.
	if msg == "http2: client conn could not be established" || strings.HasSuffix(msg, "http2: client connection lost") || strings.HasSuffix(msg, "http2: server sent GOAWAY and closed the connection") {
		return h2Closed
	}
	return h2NotMiss
}

func isRPCH2Miss(err error) bool { return classifyRPCH2Miss(err) != h2NotMiss }

func isContextDone(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// isRPCH2TransportMiss is a dead origin (RST, refuse, not h2c, preface).
// DeadlineExceeded / Canceled are not transport misses, and neither is a
// bare EOF: on a live session that is a clean Watch end.
func isRPCH2TransportMiss(err error) bool {
	if err == nil || errors.Is(err, errAttachTTL) || isContextDone(err) || connect.IsWireError(err) {
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
