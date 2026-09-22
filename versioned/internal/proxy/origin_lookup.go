package proxy

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	originIPHeader                  = "X-Real-IP"
	headerDevshardError             = "X-Devshard-Error"
	errorEscrowNotFound             = "escrow_not_found"
	errorEscrowLookupLimited        = "escrow_lookup_limited"
	peerAuthAttachSuffix            = "/devshard.transport.v1.PeerAuthService/Attach"
	defaultUnknownEscrowPerIPPerMin = 2
	maxOriginLookupIPs              = 4096
)

// originLookupLimiter is the per origin-IP cap for unknown-escrow first bind
// (owner chat, height-sync seed, Attach). It keys on inbound X-Real-IP from
// versiond-router, not the child's RemoteAddr. Missing header skips the bucket
// so an old hop that does not forward the client IP cannot collapse the host.
// At maxOriginLookupIPs, idle IPs (no miss in the last minute) go first,
// then the oldest remaining IP — the table is never wiped wholesale.
type originLookupLimiter struct {
	mu   sync.Mutex
	byIP map[string][]time.Time
	now  func() time.Time
}

func newOriginLookupLimiter() *originLookupLimiter {
	return &originLookupLimiter{byIP: make(map[string][]time.Time)}
}

func (l *originLookupLimiter) clock() time.Time {
	if l != nil && l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *originLookupLimiter) blocked(r *http.Request, rest string) bool {
	if l == nil || r == nil || !isUnknownEscrowBindPath(r.Method, rest) {
		return false
	}
	ip := originIP(r.Header)
	if ip == "" {
		return false
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return countRecent(l.byIP[ip], now) >= defaultUnknownEscrowPerIPPerMin
}

func (l *originLookupLimiter) observe(r *http.Request, rest string, resp *http.Response) {
	if l == nil || r == nil || resp == nil || !isUnknownEscrowBindPath(r.Method, rest) {
		return
	}
	if !isUnknownEscrowMiss(resp.Header) {
		return
	}
	ip := originIP(r.Header)
	if ip == "" {
		return
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP == nil {
		l.byIP = make(map[string][]time.Time)
	}
	if _, ok := l.byIP[ip]; !ok {
		l.admitNewIPLocked(now)
	}
	l.byIP[ip] = appendRecent(l.byIP[ip], now)
}

func (l *originLookupLimiter) admitNewIPLocked(now time.Time) {
	if len(l.byIP) < maxOriginLookupIPs {
		return
	}
	l.evictIdleLocked(now)
	if len(l.byIP) < maxOriginLookupIPs {
		return
	}
	l.evictOldestLocked()
}

func (l *originLookupLimiter) evictIdleLocked(now time.Time) {
	cutoff := now.Add(-time.Minute)
	for ip, times := range l.byIP {
		keep := false
		for _, ts := range times {
			if ts.After(cutoff) {
				keep = true
				break
			}
		}
		if !keep {
			delete(l.byIP, ip)
		}
	}
}

func (l *originLookupLimiter) evictOldestLocked() {
	var oldest string
	var oldestLast time.Time
	found := false
	for ip, times := range l.byIP {
		if len(times) == 0 {
			delete(l.byIP, ip)
			continue
		}
		last := times[len(times)-1]
		if !found || last.Before(oldestLast) {
			oldest, oldestLast, found = ip, last, true
		}
	}
	if found {
		delete(l.byIP, oldest)
	}
}

func isUnknownEscrowBindPath(method, rest string) bool {
	if !strings.EqualFold(method, http.MethodPost) {
		return false
	}
	path := strings.Trim(rest, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "sessions" {
		return false
	}
	switch parts[2] {
	case "chat":
		return len(parts) == 4 && parts[3] == "completions"
	case "height-sync":
		return len(parts) == 3
	case "rpc":
		return strings.HasSuffix(rest, peerAuthAttachSuffix)
	}
	return false
}

func isUnknownEscrowMiss(h http.Header) bool {
	if h == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(h.Get(headerDevshardError))) {
	case errorEscrowNotFound, errorEscrowLookupLimited:
		return true
	}
	return false
}

func originIP(h http.Header) string {
	if h == nil {
		return ""
	}
	return parseOriginIP(h.Get(originIPHeader))
}

func parseOriginIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, ','); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	return addr.String()
}

func countRecent(times []time.Time, now time.Time) int {
	cutoff := now.Add(-time.Minute)
	n := 0
	for _, ts := range times {
		if ts.After(cutoff) {
			n++
		}
	}
	return n
}

func appendRecent(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	kept := times[:0]
	for _, ts := range times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	return append(kept, now)
}
