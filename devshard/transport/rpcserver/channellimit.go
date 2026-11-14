package rpcserver

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"devshard/observability"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const (
	channelLimiterIdleFor    = time.Minute
	channelLimiterEvictBatch = 32
	// maxWatchesPerPeer bounds concurrent Watch streams for one peer
	// address. Tokens from the same signer share the cap, so one peer
	// cannot fill MaxSessions by minting tokens. Eight is two HA replicas,
	// two generations during a rolling deploy, and two streams per process
	// while a reopen is still unwinding (2 × 2 × 2). A reconnect holds the
	// live Watch and the replacement at once. Replicas of one participant
	// share a signer, so they count as one peer.
	maxWatchesPerPeer = 8
	// renewalBurst and renewalPerMin size the per-peer bucket an Attach
	// with a live token spends before VerifyAttach. A client refreshes at
	// 75% of a two-minute TTL, so a healthy peer uses well under one per
	// minute. Each renewal adds a token, so this bucket is what bounds the
	// ECDSA work one peer can buy without the anonymous floor.
	renewalBurst  = 4
	renewalPerMin = 4
	// maxPendingChatsPerPeer and maxPendingChatsTotal bound Chats whose
	// envelope is still being read. Those hold an HTTP/2 stream and a
	// goroutine but no Chat slot yet, and ChatReadTimeout ends each one.
	// A gateway's burst only waits here for the envelope upload.
	maxPendingChatsPerPeer = 512
	maxPendingChatsTotal   = 2048
)

type rateLimitZone string

const (
	zoneShared       rateLimitZone = "shared"
	zoneStreams      rateLimitZone = "streams"
	zoneAttachFloor  rateLimitZone = "attach_floor"
	zoneAttachOrigin rateLimitZone = "attach_origin"
	zoneAttachRenew  rateLimitZone = "attach_renew"
)

type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	last       time.Time
	lastNano   atomic.Int64
	ratePerMin float64
	burst      float64
}

func newTokenBucket(ratePerMin, burst float64, now time.Time) *tokenBucket {
	if burst < 1 {
		burst = 1
	}
	b := &tokenBucket{tokens: burst, last: now, ratePerMin: ratePerMin, burst: burst}
	b.lastNano.Store(now.UnixNano())
	return b
}

func (b *tokenBucket) allow(now time.Time, cost float64) (ok bool, retry time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ratePerMin <= 0 {
		return false, time.Minute
	}
	elapsedMin := now.Sub(b.last).Minutes()
	if elapsedMin > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsedMin*b.ratePerMin)
		b.last = now
		b.lastNano.Store(now.UnixNano())
	}
	// Admit when tokens >= min(cost, burst), then subtract cost. cost > burst
	// overdrafts from a full bucket; refill still caps at burst (finding 12).
	need := transport.TokenAdmitThreshold(cost, b.burst) - b.tokens
	if need > 0 {
		retry = time.Duration(need / b.ratePerMin * float64(time.Minute))
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	b.tokens -= cost
	return true, 0
}

func (b *tokenBucket) idle(now time.Time) bool {
	if b == nil {
		return true
	}
	return now.UnixNano()-b.lastNano.Load() >= int64(channelLimiterIdleFor)
}

type channelLimiter struct {
	now func() time.Time
	cfg transport.ChannelLimitConfig

	sharedMu sync.Mutex
	shared   map[string]*tokenBucket
	renewMu  sync.Mutex
	renew    map[string]*tokenBucket
	streamMu sync.Mutex
	streams  map[string]peerStreamCount
	// procStreams / procChats are child-wide Chat occupancy. Watch does
	// not spend them. procWatches is capped at maxWatches (MaxSessions),
	// and peerWatches at maxWatchesPerPeer for each peer.
	// SETTINGS stays DefaultH2MaxConcurrentStreams.
	procStreams int
	procChats   int
	procWatches int
	maxWatches  int
	peerWatches map[string]int
	// pendingChats / procPendingChats count Chats still reading their
	// envelope. They do not spend procChats.
	pendingChats      map[string]int
	procPendingChats  int
	maxPendingPerPeer int
	maxPendingTotal   int
	// evictVisited is how many keys the last at-cap idle walk inspected.
	evictVisited int

	// warnByZone is one log per zone per minute. Origin and process-floor
	// refusals must not hide each other.
	warnByZone sync.Map // zone string -> *atomic.Int64
}

// peerStreamCount is Chat occupancy. Chat uses at most max-1 when max>1
// so a Watch can still be held beside Chat (finding 3). Watch itself is
// not stored here. max==1 is one Chat.
type peerStreamCount struct {
	total int
	chat  int
}

func newChannelLimiter(cfg transport.ChannelLimitConfig, now func() time.Time) *channelLimiter {
	if now == nil {
		now = time.Now
	}
	return &channelLimiter{
		now:         now,
		cfg:         cfg.WithDefaults(),
		shared:      make(map[string]*tokenBucket),
		renew:       make(map[string]*tokenBucket),
		streams:     make(map[string]peerStreamCount),
		maxWatches:  defaultMaxSessions,
		peerWatches: make(map[string]int),

		pendingChats:      make(map[string]int),
		maxPendingPerPeer: maxPendingChatsPerPeer,
		maxPendingTotal:   maxPendingChatsTotal,
	}
}

func (l *channelLimiter) advertised() *rpcpb.RateLimits {
	if l == nil {
		return advertisedRateLimits(transport.ChannelLimitConfig{}.WithDefaults())
	}
	return advertisedRateLimits(l.cfg)
}

func advertisedRateLimits(cfg transport.ChannelLimitConfig) *rpcpb.RateLimits {
	cfg = cfg.WithDefaults()
	if cfg.Disabled {
		return &rpcpb.RateLimits{
			MessagesPerMin: transport.UnlimitedRPCLimit,
			MessagesBurst:  transport.UnlimitedRPCLimit,
			MaxStreams:     transport.UnlimitedRPCLimit,
			IpWeightPerMin: transport.UnlimitedRPCLimit,
			IpBurst:        transport.UnlimitedRPCLimit,
		}
	}
	return &rpcpb.RateLimits{
		MessagesPerMin: cfg.MessagesPerMin,
		MessagesBurst:  cfg.MessagesBurst,
		MaxStreams:     cfg.EffectiveMaxStreams(),
		// The origin Attach bucket is an admission gate, not this budget.
		// Advertise unlimited so clients do not pace themselves to it.
		IpWeightPerMin: transport.UnlimitedRPCLimit,
		IpBurst:        transport.UnlimitedRPCLimit,
	}
}

func (l *channelLimiter) charge(ctx context.Context, peer, procedure string) error {
	if l == nil || l.cfg.Disabled || peer == "" {
		return nil
	}
	weight := transport.RPCProcedureWeight(procedure)
	if weight <= 0 {
		return nil
	}
	now := l.now()
	ok, retry := l.take(&l.sharedMu, l.shared, peer, float64(l.cfg.MessagesPerMin), float64(l.cfg.MessagesBurst), now, float64(weight), transport.IsUnlimitedRPCLimit(l.cfg.MessagesPerMin))
	if ok {
		return nil
	}
	l.warnBanned(ctx, procedure, zoneShared, peer)
	if retry <= 0 {
		return rateLimitExhausted("too many peers", time.Second)
	}
	err := rateLimitExhausted("rate limit exceeded", retry)
	if procedure == rpcpbconnect.SessionServiceChatProcedure {
		return observability.FailNoReceipt(ctx, EscrowIDFromContext(ctx),
			observability.ReasonRateLimited, observability.WhereTransportRateLimit,
			"rate limit exceeded", err, "sender", peer)
	}
	return err
}

// takeRenewal charges one Attach that presented a live token for peer.
// peer comes from a valid HMAC tag, so it is not caller-chosen.
func (l *channelLimiter) takeRenewal(ctx context.Context, peer string) error {
	if l == nil || l.cfg.Disabled || peer == "" {
		return nil
	}
	ok, retry := l.take(&l.renewMu, l.renew, peer, renewalPerMin, renewalBurst, l.now(), 1, false)
	if ok {
		return nil
	}
	l.warnBanned(ctx, rpcpbconnect.PeerAuthServiceAttachProcedure, zoneAttachRenew, peer)
	if retry <= 0 {
		retry = time.Second
	}
	return rateLimitExhausted("too many attach renewals", retry)
}

func (l *channelLimiter) acquireStream(ctx context.Context, peer, procedure string) error {
	if l == nil || l.cfg.Disabled || peer == "" {
		return nil
	}
	if isWatchPath(procedure) {
		return l.acquireWatch(ctx, peer, procedure)
	}
	perPeerUnlimited := transport.IsUnlimitedRPCLimit(l.cfg.EffectiveMaxStreams())
	procStreams, procChats, procUnlimited := l.cfg.ProcessStreamCaps()
	if perPeerUnlimited && procUnlimited {
		return nil
	}
	chat := isChatPath(procedure)
	l.streamMu.Lock()
	n, exists := l.streams[peer]
	if !perPeerUnlimited {
		if !exists && len(l.streams) >= l.cfg.MaxEntries {
			l.streamMu.Unlock()
			l.warnBanned(ctx, procedure, zoneStreams, peer)
			return rateLimitExhausted("too many concurrent streams", time.Second)
		}
		max := int(l.cfg.EffectiveMaxStreams())
		if chat && max > 1 && n.chat >= max-1 {
			l.streamMu.Unlock()
			l.warnBanned(ctx, procedure, zoneStreams, peer)
			return rateLimitExhausted("too many concurrent streams", time.Second)
		}
		if n.total >= max {
			l.streamMu.Unlock()
			l.warnBanned(ctx, procedure, zoneStreams, peer)
			return rateLimitExhausted("too many concurrent streams", time.Second)
		}
	} else if !exists && len(l.streams) >= l.cfg.MaxEntries {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, peer)
		return rateLimitExhausted("too many concurrent streams", time.Second)
	}
	if !procUnlimited {
		if chat && l.procChats >= int(procChats) {
			l.streamMu.Unlock()
			l.warnBanned(ctx, procedure, zoneStreams, "process")
			return rateLimitExhausted("too many concurrent chats", time.Second)
		}
		if l.procStreams >= int(procStreams) {
			l.streamMu.Unlock()
			l.warnBanned(ctx, procedure, zoneStreams, "process")
			return rateLimitExhausted("too many concurrent streams", time.Second)
		}
		l.procStreams++
		if chat {
			l.procChats++
		}
	}
	if chat {
		n.chat++
	}
	n.total++
	l.streams[peer] = n
	l.streamMu.Unlock()
	return nil
}

// acquireWatch counts a Watch against maxWatchesPerPeer and MaxSessions.
// It does not take a Chat stream slot, so a full Chat roster cannot refuse
// Watch and a full Watch roster cannot refuse Chat.
func (l *channelLimiter) acquireWatch(ctx context.Context, peer, procedure string) error {
	l.streamMu.Lock()
	if l.peerWatches[peer] >= maxWatchesPerPeer {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, peer)
		return rateLimitExhausted("too many concurrent watches", time.Second)
	}
	if l.maxWatches > 0 && l.procWatches >= l.maxWatches {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, "process")
		return rateLimitExhausted("too many concurrent watches", time.Second)
	}
	l.peerWatches[peer]++
	l.procWatches++
	l.streamMu.Unlock()
	return nil
}

// acquirePendingChat counts one Chat whose envelope is not read yet. The
// count is released when that Chat takes its stream slot or the request
// ends, whichever is first.
func (l *channelLimiter) acquirePendingChat(ctx context.Context, peer string) error {
	if l == nil || l.cfg.Disabled || peer == "" {
		return nil
	}
	procedure := rpcpbconnect.SessionServiceChatProcedure
	perPeerUnlimited := transport.IsUnlimitedRPCLimit(l.cfg.EffectiveMaxStreams())
	_, _, procUnlimited := l.cfg.ProcessStreamCaps()
	l.streamMu.Lock()
	if !perPeerUnlimited && l.pendingChats[peer] >= l.maxPendingPerPeer {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, peer)
		return rateLimitExhausted("too many pending chats", time.Second)
	}
	if !procUnlimited && l.procPendingChats >= l.maxPendingTotal {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, "process")
		return rateLimitExhausted("too many pending chats", time.Second)
	}
	l.pendingChats[peer]++
	l.procPendingChats++
	l.streamMu.Unlock()
	return nil
}

func (l *channelLimiter) releasePendingChat(peer string) {
	if l == nil || peer == "" {
		return
	}
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	n, ok := l.pendingChats[peer]
	if !ok {
		return
	}
	if l.procPendingChats > 0 {
		l.procPendingChats--
	}
	if n <= 1 {
		delete(l.pendingChats, peer)
		return
	}
	l.pendingChats[peer] = n - 1
}

func (l *channelLimiter) releaseStream(peer, procedure string) {
	if l == nil || peer == "" {
		return
	}
	if isWatchPath(procedure) {
		l.streamMu.Lock()
		if l.procWatches > 0 {
			l.procWatches--
		}
		if n := l.peerWatches[peer]; n <= 1 {
			delete(l.peerWatches, peer)
		} else {
			l.peerWatches[peer] = n - 1
		}
		l.streamMu.Unlock()
		return
	}
	chat := isChatPath(procedure)
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	n, ok := l.streams[peer]
	if !ok || n.total == 0 {
		return
	}
	if chat && n.chat > 0 {
		n.chat--
		if l.procChats > 0 {
			l.procChats--
		}
	}
	if l.procStreams > 0 {
		l.procStreams--
	}
	if n.total <= 1 {
		delete(l.streams, peer)
		return
	}
	n.total--
	l.streams[peer] = n
}

// take looks up or creates a bucket under the map lock, then charges under
// the bucket lock so peers do not serialize on token math. At MaxEntries
// idle keys are evicted a batch at a time; a new peer is refused so named
// peers keep the advertised rate (finding 18).
func (l *channelLimiter) take(mu *sync.Mutex, buckets map[string]*tokenBucket, key string, rate, burst float64, now time.Time, cost float64, unlimited bool) (bool, time.Duration) {
	if unlimited {
		return true, 0
	}
	mu.Lock()
	b := l.lookupOrCreateLocked(buckets, key, rate, burst, now)
	mu.Unlock()
	if b == nil {
		return false, 0
	}
	return b.allow(now, cost)
}

func (l *channelLimiter) lookupOrCreateLocked(buckets map[string]*tokenBucket, key string, rate, burst float64, now time.Time) *tokenBucket {
	if b, ok := buckets[key]; ok {
		return b
	}
	max := l.cfg.MaxEntries
	if len(buckets) >= max {
		l.evictIdleLocked(buckets, now)
	}
	if len(buckets) >= max {
		return nil
	}
	b := newTokenBucket(rate, burst, now)
	buckets[key] = b
	return b
}

func (l *channelLimiter) evictIdleLocked(buckets map[string]*tokenBucket, now time.Time) {
	n := 0
	l.evictVisited = 0
	for k, b := range buckets {
		if n >= channelLimiterEvictBatch {
			return
		}
		n++
		l.evictVisited++
		if b == nil || b.idle(now) {
			delete(buckets, k)
		}
	}
}

func (l *channelLimiter) warnBanned(ctx context.Context, procedure string, zone rateLimitZone, key string) {
	if !l.reserveWarn(zone) {
		return
	}
	observability.Log(ctx, observability.LevelWarn, "rate limit exceeded",
		observability.StageReceived, observability.WhereTransportRateLimit,
		EscrowIDFromContext(ctx), observability.ReasonRateLimited, nil,
		"endpoint", procedure, "zone", string(zone), "key", key)
}

// reserveWarn is true the first time this zone warns in the current minute.
func (l *channelLimiter) reserveWarn(zone rateLimitZone) bool {
	minute := l.now().Unix() / 60
	v, _ := l.warnByZone.LoadOrStore(string(zone), &atomic.Int64{})
	slot := v.(*atomic.Int64)
	for {
		prev := slot.Load()
		if prev == minute {
			return false
		}
		if slot.CompareAndSwap(prev, minute) {
			return true
		}
	}
}

func rateLimitExhausted(msg string, retry time.Duration) error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New(msg))
	err.Meta().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
	return err
}

func isStreamPath(path string) bool {
	return isWatchPath(path) || isChatPath(path)
}

func isChatPath(path string) bool {
	return path == rpcpbconnect.SessionServiceChatProcedure
}

type rateLimitChargedKey struct{}

func withRateLimitCharged(ctx context.Context) context.Context {
	return context.WithValue(ctx, rateLimitChargedKey{}, true)
}

func rateLimitCharged(ctx context.Context) bool {
	v, _ := ctx.Value(rateLimitChargedKey{}).(bool)
	return v
}
