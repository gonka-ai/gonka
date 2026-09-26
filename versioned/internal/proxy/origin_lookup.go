package proxy

import (
	"container/list"
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
	errorInvalidSessionToken        = "invalid_session_token"
	peerAuthAttachSuffix            = "/devshard.transport.v1.PeerAuthService/Attach"
	defaultUnknownEscrowPerIPPerMin = 2
	maxOriginLookupIPs              = 4096
	originLookupEvictBatch          = 32
)

// originLookupLimiter is the per origin-IP cap for unknown-escrow first bind
// (owner chat, height-sync seed, Attach) and for a presented session token
// the child rejects. Both misses share the 2/min budget. It keys on inbound
// X-Real-IP from versiond-router, not the child's RemoteAddr. Missing header
// skips the bucket so an old hop that does not forward the client IP cannot
// collapse the host.
// order is last-miss time, front = oldest. At cap, idle IPs (a prefix of
// that list) go first; the oldest under-budget IP is then O(1). The table
// is never replaced, and an IP at its 2/min budget is not evicted.
type originLookupLimiter struct {
	mu           sync.Mutex
	byIP         map[string]*originIPNode
	order        *list.List
	evictVisited int
	now          func() time.Time
}

type originIPNode struct {
	ip    string
	times []time.Time
	el    *list.Element
}

func newOriginLookupLimiter() *originLookupLimiter {
	return &originLookupLimiter{
		byIP:  make(map[string]*originIPNode),
		order: list.New(),
	}
}

func (l *originLookupLimiter) clock() time.Time {
	if l != nil && l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *originLookupLimiter) blocked(r *http.Request, rest string) bool {
	if l == nil || r == nil || !isOriginLimitedPath(r.Method, rest) {
		return false
	}
	ip := originIP(r.Header)
	if ip == "" {
		return false
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	node := l.byIP[ip]
	if node == nil {
		return false
	}
	return countRecent(node.times, now) >= defaultUnknownEscrowPerIPPerMin
}

func (l *originLookupLimiter) observe(r *http.Request, rest string, resp *http.Response) {
	if l == nil || r == nil || resp == nil || !isOriginLimitedMiss(r.Method, rest, resp.Header) {
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
		l.byIP = make(map[string]*originIPNode)
	}
	if l.order == nil {
		l.order = list.New()
	}
	node := l.byIP[ip]
	if node == nil {
		l.admitNewIPLocked(now)
		if len(l.byIP) >= maxOriginLookupIPs {
			return
		}
		node = &originIPNode{ip: ip}
		node.el = l.order.PushBack(node)
		l.byIP[ip] = node
	}
	node.times = appendRecent(node.times, now)
	if node.el != nil {
		l.order.MoveToBack(node.el)
	}
}

func (l *originLookupLimiter) admitNewIPLocked(now time.Time) {
	l.evictVisited = 0
	if len(l.byIP) < maxOriginLookupIPs {
		return
	}
	l.evictIdleLocked(now)
	if len(l.byIP) < maxOriginLookupIPs {
		return
	}
	l.evictOldestLocked(now)
}

// evictIdleLocked drops the idle prefix of the last-miss list. Each batch
// examines at most originLookupEvictBatch entries. A live IP ends the walk,
// so a hot table costs one visit. An all-idle table repeats until it is clear.
func (l *originLookupLimiter) evictIdleLocked(now time.Time) {
	for {
		n := 0
		removed := 0
		el := l.order.Front()
		for el != nil && n < originLookupEvictBatch {
			node := el.Value.(*originIPNode)
			next := el.Next()
			n++
			l.evictVisited++
			if countRecent(node.times, now) > 0 {
				return
			}
			l.removeLocked(node)
			removed++
			el = next
		}
		if removed == 0 || el == nil {
			return
		}
	}
}

// evictOldestLocked drops the oldest last-miss IP that is under the 2/min
// budget. Over-budget IPs stay. The walk is capped at one batch.
func (l *originLookupLimiter) evictOldestLocked(now time.Time) {
	n := 0
	for el := l.order.Front(); el != nil && n < originLookupEvictBatch; {
		node := el.Value.(*originIPNode)
		next := el.Next()
		n++
		l.evictVisited++
		if len(node.times) == 0 || countRecent(node.times, now) < defaultUnknownEscrowPerIPPerMin {
			l.removeLocked(node)
			return
		}
		el = next
	}
}

func (l *originLookupLimiter) removeLocked(node *originIPNode) {
	if node == nil {
		return
	}
	if node.el != nil {
		l.order.Remove(node.el)
	}
	delete(l.byIP, node.ip)
}

func isOriginLimitedPath(method, rest string) bool {
	return isUnknownEscrowBindPath(method, rest) || isSessionTokenPath(method, rest)
}

func isOriginLimitedMiss(method, rest string, h http.Header) bool {
	if isUnknownEscrowBindPath(method, rest) && isUnknownEscrowMiss(h) {
		return true
	}
	return isSessionTokenPath(method, rest) && isInvalidSessionToken(h)
}

// isSessionTokenPath is a peer RPC that presents X-Devshard-Session.
// Attach is the handshake and stays on the unknown-escrow path.
func isSessionTokenPath(method, rest string) bool {
	if !strings.EqualFold(method, http.MethodPost) {
		return false
	}
	path := strings.Trim(rest, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] != "sessions" || parts[2] != "rpc" {
		return false
	}
	return !strings.HasSuffix(rest, peerAuthAttachSuffix)
}

func isInvalidSessionToken(h http.Header) bool {
	if h == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(h.Get(headerDevshardError)), errorInvalidSessionToken)
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
