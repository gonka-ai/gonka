package transport

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"devshard/observability"
	"devshard/transport/rpcpb"
)

const (
	// minAdvertisedPeerMessagesPerMin is the lowest messages/min the client
	// will pace to. Below that (including a MITM rewrite to 1) is treated
	// as not advertised: no local wait, server still enforces (finding 20).
	minAdvertisedPeerMessagesPerMin uint32 = 10
	// maxAdvertisedPeerMessagesPerMin is the highest finite messages/min
	// (and burst) the client will pace to. UnlimitedRPCLimit is already
	// "not advertised". Values above this are the same.
	maxAdvertisedPeerMessagesPerMin uint32 = 1_000_000
)

// peerRPCBudget is the outbound peer-weight bucket for one Attach session.
// RPCClient clones share the PeerConn that owns this, matching the server's
// per-peer key. The child does not pace Attach by origin IP.
type peerRPCBudget struct {
	mu     sync.Mutex
	bucket *peerBucket
}

type peerBucket struct {
	tokens     float64
	last       time.Time
	ratePerMin float64
	burst      float64
}

// applyAttachLimits stores advertised peer messages/min and burst.
// Protobuf 0 / nil means the peer did not advertise (old child) — do not
// pace. UnlimitedRPCLimit and out-of-range values skip pacing (finding 20).
// Re-Attach updates rate and burst without resetting remaining tokens.
func (b *peerRPCBudget) apply(limits *rpcpb.RateLimits, now time.Time) {
	rate, burst, unlimited := advertisedPeerMessages(limits)
	b.mu.Lock()
	defer b.mu.Unlock()
	if unlimited {
		b.bucket = nil
		return
	}
	if b.bucket == nil {
		b.bucket = &peerBucket{
			tokens:     burst,
			last:       now,
			ratePerMin: rate,
			burst:      burst,
		}
		return
	}
	b.bucket.refill(now)
	b.bucket.ratePerMin = rate
	b.bucket.burst = burst
	if b.bucket.tokens > burst {
		b.bucket.tokens = burst
	}
}

func advertisedPeerMessages(limits *rpcpb.RateLimits) (rate, burst float64, unlimited bool) {
	if limits == nil {
		return 0, 0, true
	}
	r := limits.GetMessagesPerMin()
	if r == 0 || IsUnlimitedRPCLimit(r) {
		return 0, 0, true
	}
	if r < minAdvertisedPeerMessagesPerMin || r > maxAdvertisedPeerMessagesPerMin {
		return 0, 0, true
	}
	bu := limits.GetMessagesBurst()
	if IsUnlimitedRPCLimit(bu) {
		return 0, 0, true
	}
	if bu == 0 {
		bu = r / 10
		if bu == 0 {
			bu = 1
		}
	}
	if bu < 1 || bu > maxAdvertisedPeerMessagesPerMin {
		return 0, 0, true
	}
	return float64(r), float64(bu), false
}

func peerBudgetWaitCap(ctx context.Context) time.Duration {
	capWait := nonInferenceRetryBudget
	if dl, ok := ctx.Deadline(); ok {
		rem := time.Until(dl)
		if rem < capWait {
			if rem < 0 {
				return 0
			}
			return rem
		}
	}
	return capWait
}

func (b *peerRPCBudget) take(ctx context.Context, procedure string, nowFn func() time.Time, sleep func(context.Context, time.Duration) error) error {
	cost := float64(RPCProcedureWeight(procedure))
	if cost <= 0 {
		return nil
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait := b.try(nowFn(), cost)
		if wait <= 0 {
			return nil
		}
		// A wait that cannot fit in the retry budget (or the RPC
		// deadline) would park until context.DeadlineExceeded with no
		// byte on the wire. Skip pacing instead (finding 20).
		endpoint := rpcBudgetEndpoint(procedure)
		if wait >= peerBudgetWaitCap(ctx) {
			observability.IncPeerRPCBudgetWaitSkipped(endpoint)
			return nil
		}
		err := sleep(ctx, wait)
		observability.ObservePeerRPCBudgetWait(endpoint, wait)
		if err != nil {
			return err
		}
	}
}

func (b *peerRPCBudget) try(now time.Time, cost float64) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bucket == nil {
		return 0
	}
	ok, wait := b.bucket.take(now, cost)
	if ok {
		return 0
	}
	return wait
}

func (b *peerRPCBudget) refund(now time.Time, procedure string) {
	cost := float64(RPCProcedureWeight(procedure))
	if cost <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bucket == nil {
		return
	}
	b.bucket.refund(now, cost)
}

func (bk *peerBucket) refill(now time.Time) {
	if bk.ratePerMin <= 0 {
		return
	}
	elapsedMin := now.Sub(bk.last).Minutes()
	if elapsedMin > 0 {
		bk.tokens = math.Min(bk.burst, bk.tokens+elapsedMin*bk.ratePerMin)
		bk.last = now
	}
}

func (bk *peerBucket) take(now time.Time, cost float64) (ok bool, wait time.Duration) {
	bk.refill(now)
	need := TokenAdmitThreshold(cost, bk.burst) - bk.tokens
	if need <= 0 {
		bk.tokens -= cost
		return true, 0
	}
	if bk.ratePerMin <= 0 {
		return false, time.Minute
	}
	wait = time.Duration(need / bk.ratePerMin * float64(time.Minute))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return false, wait
}

func (bk *peerBucket) refund(now time.Time, cost float64) {
	bk.refill(now)
	bk.tokens = math.Min(bk.burst, bk.tokens+cost)
}

// RPCEndpointName is the snapshot / metric label for a Connect procedure
// (the last path segment: GetDiffs, Attach, Chat, …).
func RPCEndpointName(procedure string) string {
	return rpcBudgetEndpoint(procedure)
}

func rpcBudgetEndpoint(procedure string) string {
	if i := strings.LastIndex(procedure, "/"); i >= 0 && i+1 < len(procedure) {
		return procedure[i+1:]
	}
	if procedure == "" {
		return "other"
	}
	return procedure
}

const (
	minAdvertisedMaxStreams uint32 = 1
	maxAdvertisedMaxStreams uint32 = 1_000_000
)

// peerStreamBudget is the client's advertised max_streams cap (Watch + Chat).
type peerStreamBudget struct {
	mu    sync.Mutex
	max   uint32 // 0 = not advertised
	inUse int
}

func advertisedMaxStreams(limits *rpcpb.RateLimits) (max uint32, advertised bool) {
	if limits == nil {
		return 0, false
	}
	m := limits.GetMaxStreams()
	if m == 0 || IsUnlimitedRPCLimit(m) {
		return 0, false
	}
	if m < minAdvertisedMaxStreams || m > maxAdvertisedMaxStreams {
		return 0, false
	}
	return m, true
}

func (b *peerStreamBudget) apply(limits *rpcpb.RateLimits) {
	max, advertised := advertisedMaxStreams(limits)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !advertised {
		b.max = 0
		return
	}
	b.max = max
}

func (b *peerStreamBudget) acquire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max == 0 {
		return true
	}
	if b.inUse >= int(b.max) {
		return false
	}
	b.inUse++
	return true
}

func (b *peerStreamBudget) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inUse > 0 {
		b.inUse--
	}
}
