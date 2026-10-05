package proxy

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	originIPHeader                  = "X-Real-IP"
	sessionHeader                   = "X-Devshard-Session"
	headerDevshardError             = "X-Devshard-Error"
	errorEscrowNotFound             = "escrow_not_found"
	errorEscrowLookupLimited        = "escrow_lookup_limited"
	errorInvalidSessionToken        = "invalid_session_token"
	invalidSessionTokenBody         = "handshake required"
	peerAuthAttachSuffix            = "/devshard.transport.v1.PeerAuthService/Attach"
	defaultUnknownEscrowPerIPPerMin = 2
	maxOriginLookupIPs              = 4096
	originLookupEvictBatch          = 32
	// staleSessionTokenTTL coalesces a burst of calls that still carry one
	// dead token. The first invalid_session_token starts the entry and is
	// one miss. Repeats in the window are rejected locally and are not
	// charged. After the window a later use of that token is a new miss.
	staleSessionTokenTTL = 15 * time.Second
	// maxStaleSessionTokens bounds the remembered set. A full table drops
	// one entry so a new dead token can still be coalesced.
	maxStaleSessionTokens = 4096
	// sessionTokenKeyMax is above the 64-char hex Attach token. Longer
	// values are hashed so a huge header cannot sit in the map.
	sessionTokenKeyMax = 128
	// maxAttachInFlightPerIP caps concurrent Attach per origin IP. A miss is
	// recorded only when the child answers, so without this cap one parallel
	// burst from a fresh IP reaches the child before the 2/min budget sees it.
	maxAttachInFlightPerIP = 16
	// attachInFlightWait is how long an Attach over the cap queues for a
	// slot. A queued call re-checks the miss budget when it wakes, so a burst
	// of misses stops at the cap while a burst of real binds just queues.
	attachInFlightWait = 5 * time.Second
)

// originLookupLimiter is the per origin-IP cap for unknown-escrow first bind
// (owner chat, height-sync seed, Attach) and for a presented session token
// the child rejects. Both misses share the 2/min budget. It keys on inbound
// X-Real-IP from versiond-router, not the child's RemoteAddr. Missing header
// skips the bucket so an old hop that does not forward the client IP cannot
// collapse the host.
//
// A burst of invalid_session_token for one X-Devshard-Session counts once
// for staleSessionTokenTTL. Unknown-escrow misses are not part of that cache.
// Attach also holds an in-flight slot per IP (attaching) until the child's
// headers arrive.
// order is last-miss time, front = oldest. At cap, idle IPs (a prefix of
// that list) go first; the oldest under-budget IP is then O(1). The table
// is never replaced, and an IP at its 2/min budget is not evicted.
type originLookupLimiter struct {
	mu           sync.Mutex
	byIP         map[string]*originIPNode
	order        *list.List
	staleTokens  map[string]time.Time
	attaching    map[string]*attachGate
	evictVisited int
	now          func() time.Time
}

type originIPNode struct {
	ip    string
	times []time.Time
	el    *list.Element
}

// attachGate is the in-flight Attach count for one origin IP. wake is closed
// on every release. An entry exists only while n > 0.
type attachGate struct {
	n    int
	wake chan struct{}
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

// cachedInvalidToken reports a session token already rejected inside
// staleSessionTokenTTL. The caller rejects it locally and does not charge.
func (l *originLookupLimiter) cachedInvalidToken(r *http.Request, rest string) bool {
	if l == nil || r == nil || !isSessionTokenPath(r.Method, rest) {
		return false
	}
	token := sessionTokenKey(r.Header)
	if token == "" {
		return false
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tokenCachedLocked(token, now)
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

// admit is blocked plus the Attach in-flight slot. ok=false is the 429.
// release is safe to call more than once; the slot must be released after
// observe so a woken caller sees the miss that call recorded.
func (l *originLookupLimiter) admit(r *http.Request, rest string) (release func(), ok bool) {
	release = func() {}
	if l.blocked(r, rest) {
		return release, false
	}
	if l == nil || r == nil || !isUnknownEscrowBindPath(r.Method, rest) || !isAttachPath(rest) {
		return release, true
	}
	ip := originIP(r.Header)
	if ip == "" {
		return release, true
	}
	timer := time.NewTimer(attachInFlightWait)
	defer timer.Stop()
	for {
		l.mu.Lock()
		if node := l.byIP[ip]; node != nil && countRecent(node.times, l.clock()) >= defaultUnknownEscrowPerIPPerMin {
			l.mu.Unlock()
			return release, false
		}
		if l.attaching == nil {
			l.attaching = make(map[string]*attachGate)
		}
		gate := l.attaching[ip]
		if gate == nil {
			gate = &attachGate{wake: make(chan struct{})}
			l.attaching[ip] = gate
		}
		if gate.n < maxAttachInFlightPerIP {
			gate.n++
			l.mu.Unlock()
			return l.attachRelease(ip), true
		}
		wake := gate.wake
		l.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return release, false
		case <-r.Context().Done():
			return release, false
		}
	}
}

func (l *originLookupLimiter) attachRelease(ip string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			gate := l.attaching[ip]
			if gate == nil {
				return
			}
			gate.n--
			close(gate.wake)
			if gate.n <= 0 {
				delete(l.attaching, ip)
				return
			}
			gate.wake = make(chan struct{})
		})
	}
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
	token := ""
	if isSessionTokenPath(r.Method, rest) && isInvalidSessionToken(resp.Header) {
		token = sessionTokenKey(r.Header)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if token != "" && l.noteStaleTokenLocked(token, now) {
		return
	}
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

// noteStaleTokenLocked returns true when the token is already inside the
// window, so the caller must not charge again. The first sight records the
// window and returns false.
func (l *originLookupLimiter) noteStaleTokenLocked(token string, now time.Time) bool {
	if l.tokenCachedLocked(token, now) {
		return true
	}
	if l.staleTokens == nil {
		l.staleTokens = make(map[string]time.Time)
	}
	l.evictExpiredTokensLocked(now)
	if len(l.staleTokens) >= maxStaleSessionTokens {
		for key := range l.staleTokens {
			delete(l.staleTokens, key)
			break
		}
	}
	l.staleTokens[token] = now.Add(staleSessionTokenTTL)
	return false
}

func (l *originLookupLimiter) tokenCachedLocked(token string, now time.Time) bool {
	if l.staleTokens == nil {
		return false
	}
	until, ok := l.staleTokens[token]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(l.staleTokens, token)
		return false
	}
	return true
}

func (l *originLookupLimiter) evictExpiredTokensLocked(now time.Time) {
	for token, until := range l.staleTokens {
		if !until.After(now) {
			delete(l.staleTokens, token)
		}
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

func sessionTokenKey(h http.Header) string {
	if h == nil {
		return ""
	}
	token := strings.TrimSpace(h.Get(sessionHeader))
	if token == "" {
		return ""
	}
	if len(token) <= sessionTokenKeyMax {
		return token
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeInvalidSessionToken(w http.ResponseWriter) {
	w.Header().Set(headerDevshardError, errorInvalidSessionToken)
	http.Error(w, invalidSessionTokenBody, http.StatusUnauthorized)
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
		return isAttachPath(rest)
	}
	return false
}

func isAttachPath(rest string) bool {
	return strings.HasSuffix(rest, peerAuthAttachSuffix)
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
