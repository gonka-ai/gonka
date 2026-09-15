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

const channelLimiterIdleFor = time.Minute

type rateLimitZone string

const (
	zoneShared      rateLimitZone = "shared"
	zoneStreams     rateLimitZone = "streams"
	zoneAttachFloor rateLimitZone = "attach_floor"
)

type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	last       time.Time
	ratePerMin float64
	burst      float64
}

func newTokenBucket(ratePerMin, burst float64, now time.Time) *tokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &tokenBucket{tokens: burst, last: now, ratePerMin: ratePerMin, burst: burst}
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
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Sub(b.last) >= channelLimiterIdleFor
}

type channelLimiter struct {
	now func() time.Time
	cfg transport.ChannelLimitConfig

	sharedMu sync.Mutex
	shared   map[string]*tokenBucket
	streamMu sync.Mutex
	streams  map[string]int

	warnMinute atomic.Int64
}

func newChannelLimiter(cfg transport.ChannelLimitConfig, now func() time.Time) *channelLimiter {
	if now == nil {
		now = time.Now
	}
	return &channelLimiter{
		now:     now,
		cfg:     cfg.WithDefaults(),
		shared:  make(map[string]*tokenBucket),
		streams: make(map[string]int),
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
		MaxStreams:     cfg.MaxStreams,
		// Child does not key Attach on IP. Per-IP bounds live on versiond /
		// Phase 6 proxy. Advertise unlimited so mixed-fleet clients do not
		// self-throttle to a bucket this process never enforces.
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

func (l *channelLimiter) acquireStream(ctx context.Context, peer, procedure string) error {
	if l == nil || l.cfg.Disabled || peer == "" || transport.IsUnlimitedRPCLimit(l.cfg.MaxStreams) {
		return nil
	}
	l.streamMu.Lock()
	n, exists := l.streams[peer]
	if !exists && len(l.streams) >= l.cfg.MaxEntries {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, peer)
		return rateLimitExhausted("too many concurrent streams", time.Second)
	}
	if n >= int(l.cfg.MaxStreams) {
		l.streamMu.Unlock()
		l.warnBanned(ctx, procedure, zoneStreams, peer)
		return rateLimitExhausted("too many concurrent streams", time.Second)
	}
	l.streams[peer] = n + 1
	l.streamMu.Unlock()
	return nil
}

func (l *channelLimiter) releaseStream(peer string) {
	if l == nil || peer == "" {
		return
	}
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	n := l.streams[peer]
	if n <= 1 {
		delete(l.streams, peer)
		return
	}
	l.streams[peer] = n - 1
}

// take looks up or creates a bucket under the map lock, then charges under
// the bucket lock so peers do not serialize on token math. At MaxEntries
// idle keys are evicted; a new peer is refused so named peers keep the
// advertised rate (finding 18).
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
	for k, b := range buckets {
		if b == nil {
			continue
		}
		if b.idle(now) {
			delete(buckets, k)
		}
	}
}

func (l *channelLimiter) warnBanned(ctx context.Context, procedure string, zone rateLimitZone, key string) {
	minute := l.now().Unix() / 60
	for {
		prev := l.warnMinute.Load()
		if prev == minute {
			return
		}
		if l.warnMinute.CompareAndSwap(prev, minute) {
			break
		}
	}
	observability.Log(ctx, observability.LevelWarn, "rate limit exceeded",
		observability.StageReceived, observability.WhereTransportRateLimit,
		EscrowIDFromContext(ctx), observability.ReasonRateLimited, nil,
		"endpoint", procedure, "zone", string(zone), "key", key)
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
