package limits

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	reasonTooManyConcurrentRequests = "too many concurrent requests"
	reasonTooManyInputTokens        = "too many input tokens in flight"
	reasonQueueTooDeep              = "queue is deeper than the wait budget can clear"
	admissionOpen                   = ""

	maxQueueHeadPasses       = 64
	queueHeadPatienceDivisor = 2
)

type RateLimitError struct {
	Reason     string
	RetryAfter time.Duration
}

// Label is the stable name a counter and a log line group by; Reason is the sentence a caller reads.
func (e *RateLimitError) Label() string {
	switch {
	case e == nil:
		return ""
	case e.Reason == reasonTooManyConcurrentRequests:
		return RejectionConcurrentRequests
	case e.Reason == reasonTooManyInputTokens:
		return RejectionInputTokens
	case e.Reason == reasonQueueTooDeep:
		return RejectionQueueDepth
	}
	return RejectionUnnamed
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "rate limit exceeded"
	}
	return fmt.Sprintf("rate limit exceeded: %s", e.Reason)
}

// ModelCapacity must arrive already availability- and block-filtered: Acquire never re-reads the snapshot.
type ModelCapacity struct {
	ScaleFactor                 float64
	CurrentWeight               float64
	BaselineWeight              float64
	MaxConcurrentPer10000Weight float64
	HostWindowRequests          int64
}

type ModelOverride struct {
	MaxConcurrent  *int64
	MaxInputTokens *int64
}

type GatewayConfig struct {
	MaxConcurrent         int64
	MaxInputTokens        int64
	AcquireWait           time.Duration
	AdmissionQueuePerSlot int64
	ModelLimits           map[string]ModelOverride
}

type modelCounter struct {
	inFlight    int64
	inputTokens int64
	enforced    admission
}

func (c *modelCounter) take(inputTokens int64) {
	c.inFlight++
	c.inputTokens += inputTokens
}

func (c *modelCounter) give(inputTokens int64) {
	c.inFlight = max(c.inFlight-1, 0)
	c.inputTokens = max(c.inputTokens-inputTokens, 0)
}

// waiter is one blocked Acquire; its reason is what a timed-out wait reports. See capacity.md, "The queue".
type waiter struct {
	model    string
	tokens   int64
	capacity ModelCapacity
	reason   string
	ready    chan struct{}
	queuedAt time.Time
	passedBy int
}

// GatewayLimiter is the gateway-wide admission limiter, queued in arrival order and passed by what fits; each model records the cap it was last judged against.
type GatewayLimiter struct {
	mu     sync.Mutex
	cfg    GatewayConfig
	models map[string]*modelCounter
	total  modelCounter
	queue  []*waiter
	now    func() time.Time
}

// InFlight is what one scope of the limiter currently holds.
type InFlight struct {
	Requests    int64
	InputTokens int64
	QueueDepth  int
}

// Enforced is per model because there is no gateway-wide answer: two models rarely share one cap.
type Enforced struct {
	MaxConcurrentRequests  int64
	MaxInputTokensInFlight int64
}

type LimiterSnapshot struct {
	Total                            InFlight
	ByModel                          map[string]InFlight
	EnforcedByModel                  map[string]Enforced
	ConfiguredMaxConcurrentRequests  int64
	ConfiguredMaxInputTokensInFlight int64
}

func NewGatewayLimiter(cfg GatewayConfig) *GatewayLimiter {
	return &GatewayLimiter{cfg: cfg, models: map[string]*modelCounter{}, now: time.Now}
}

// Reconfigure replaces the caps and sweeps the queue. See capacity.md, "The queue".
func (l *GatewayLimiter) Reconfigure(cfg GatewayConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = cfg
	l.promoteLocked()
}

func (l *GatewayLimiter) Snapshot() LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot := LimiterSnapshot{
		Total:                            InFlight{Requests: l.total.inFlight, InputTokens: l.total.inputTokens},
		ByModel:                          make(map[string]InFlight, len(l.models)),
		EnforcedByModel:                  make(map[string]Enforced, len(l.models)),
		ConfiguredMaxConcurrentRequests:  l.cfg.MaxConcurrent,
		ConfiguredMaxInputTokensInFlight: l.cfg.MaxInputTokens,
	}
	for model, counter := range l.models {
		snapshot.ByModel[model] = InFlight{Requests: counter.inFlight, InputTokens: counter.inputTokens}
		snapshot.EnforcedByModel[model] = Enforced{
			MaxConcurrentRequests:  counter.enforced.concurrencyLimit,
			MaxInputTokensInFlight: counter.enforced.inputTokenLimit,
		}
	}
	for _, blocked := range l.queue {
		queued := snapshot.ByModel[blocked.model]
		queued.QueueDepth++
		snapshot.ByModel[blocked.model] = queued
		snapshot.Total.QueueDepth++
	}
	return snapshot
}

func (l *GatewayLimiter) AcquireForModel(ctx context.Context, model string, inputTokens int64, capacity ModelCapacity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inputTokens <= 0 {
		inputTokens = 1
	}
	model = strings.TrimSpace(model)

	l.mu.Lock()
	acquireWait := l.cfg.AcquireWait
	admitted := l.admissionFor(model, inputTokens, capacity)
	counter := l.counterLocked(model)
	counter.enforced = admitted
	if reason := admitted.impossible(); reason != "" {
		l.mu.Unlock()
		return &RateLimitError{Reason: reason, RetryAfter: acquireWait}
	}
	// A freed slot goes to the queue under this same lock; a request that fits passes only waiters whose own tokens do not, and a starving head not at all.
	reason := admitted.blockedReason(counter.inFlight, counter.inputTokens)
	if reason == admissionOpen {
		reason = l.passQueueHeadLocked(model)
	}
	if reason == admissionOpen {
		l.takeLocked(counter, inputTokens)
		l.mu.Unlock()
		return nil
	}
	if acquireWait <= 0 {
		l.mu.Unlock()
		return &RateLimitError{Reason: reason, RetryAfter: acquireWait}
	}

	if l.queueTooDeepLocked(model, admitted) {
		l.mu.Unlock()
		return &RateLimitError{Reason: reasonQueueTooDeep, RetryAfter: acquireWait}
	}

	blocked := &waiter{model: model, tokens: inputTokens, capacity: capacity, reason: reason, ready: make(chan struct{}), queuedAt: l.now()}
	l.queue = append(l.queue, blocked)
	l.promoteLocked() // capacity may already be free; the queue only enforces order, it is not a delay
	l.mu.Unlock()

	timer := time.NewTimer(acquireWait)
	defer timer.Stop()
	select {
	case <-blocked.ready:
		return nil
	case <-timer.C:
		if !l.dequeue(blocked) {
			return nil // promoted as the deadline fired: the slot is already ours
		}
		return &RateLimitError{Reason: blocked.reason, RetryAfter: acquireWait}
	case <-ctx.Done():
		if !l.dequeue(blocked) {
			l.ReleaseForModel(model, inputTokens) // promoted for a caller that is already gone
		}
		return ctx.Err()
	}
}

// dequeue reports whether the waiter was still queued, meaning it never received a slot.
func (l *GatewayLimiter) dequeue(w *waiter) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, queued := range l.queue {
		if queued == w {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			return true
		}
	}
	return false
}

// promoteLocked skips — rather than stops at — a waiter its own model cannot serve, until that model's head starves. See capacity.md, "The queue".
func (l *GatewayLimiter) promoteLocked() {
	heads := map[string]*waiter{}
	for i := 0; i < len(l.queue); {
		waiting := l.queue[i]
		head, behindHead := heads[waiting.model]
		if behindHead && l.starvingLocked(head) {
			i++
			continue
		}
		admitted := l.admissionFor(waiting.model, waiting.tokens, waiting.capacity)
		counter := l.counterLocked(waiting.model)
		counter.enforced = admitted
		if admitted.blockedReason(counter.inFlight, counter.inputTokens) != admissionOpen {
			if !behindHead {
				heads[waiting.model] = waiting
			}
			i++
			continue
		}
		if behindHead {
			head.passedBy++
		}
		l.queue = append(l.queue[:i], l.queue[i+1:]...)
		l.takeLocked(counter, waiting.tokens)
		close(waiting.ready)
	}
}

// passQueueHeadLocked counts a pass of the model's queue head, or names its cap when the head is starving and may not be passed.
func (l *GatewayLimiter) passQueueHeadLocked(model string) string {
	for _, head := range l.queue {
		if head.model != model {
			continue
		}
		if l.starvingLocked(head) {
			return head.reason
		}
		head.passedBy++
		return admissionOpen
	}
	return admissionOpen
}

// starvingLocked reports a head passed as often as the ceiling allows, or waiting longer than its share of the admission wait.
func (l *GatewayLimiter) starvingLocked(head *waiter) bool {
	patience := l.cfg.AcquireWait / queueHeadPatienceDivisor
	return head.passedBy >= maxQueueHeadPasses || !l.now().Before(head.queuedAt.Add(patience))
}

// queueTooDeepLocked reports a queue this request cannot reach the front of in time. See capacity.md, "The wait budget".
func (l *GatewayLimiter) queueTooDeepLocked(model string, admitted admission) bool {
	perSlot := l.cfg.AdmissionQueuePerSlot
	if perSlot <= 0 || admitted.concurrencyLimit <= 0 {
		return false
	}
	queued := int64(0)
	for _, waiting := range l.queue {
		if waiting.model == model {
			queued++
		}
	}
	return queued >= admitted.concurrencyLimit*perSlot
}

func (l *GatewayLimiter) takeLocked(counter *modelCounter, inputTokens int64) {
	counter.take(inputTokens)
	l.total.take(inputTokens)
}

// admissionFor treats each model's configured maximum as its own budget. See capacity.md, "The gateway limiter".
func (l *GatewayLimiter) admissionFor(model string, inputTokens int64, capacity ModelCapacity) admission {
	maxConcurrent, maxInputTokens := l.cfg.MaxConcurrent, l.cfg.MaxInputTokens
	if override, ok := l.cfg.ModelLimits[model]; ok {
		if override.MaxConcurrent != nil {
			maxConcurrent = *override.MaxConcurrent
		}
		if override.MaxInputTokens != nil {
			maxInputTokens = *override.MaxInputTokens
		}
	}
	admitted := admission{requestedTokens: inputTokens}
	admitted.concurrencyLimit, admitted.concurrencyLimited = effectiveConcurrencyLimit(maxConcurrent, capacity)
	admitted.inputTokenLimit, admitted.inputTokenLimited = effectiveInputTokenLimit(maxInputTokens, capacity)
	return admitted
}

func (l *GatewayLimiter) ReleaseForModel(model string, inputTokens int64) {
	if inputTokens <= 0 {
		inputTokens = 1
	}
	model = strings.TrimSpace(model)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(model, inputTokens)
	l.promoteLocked()
}

// releaseLocked leaves an idle counter in place so a quiet model reads zero instead of disappearing.
func (l *GatewayLimiter) releaseLocked(model string, inputTokens int64) {
	counter, ok := l.models[model]
	if !ok {
		return
	}
	counter.give(inputTokens)
	l.total.give(inputTokens)
}

func (l *GatewayLimiter) counterLocked(model string) *modelCounter {
	counter, ok := l.models[model]
	if !ok {
		counter = &modelCounter{}
		l.models[model] = counter
	}
	return counter
}

type admission struct {
	concurrencyLimit   int64
	concurrencyLimited bool
	inputTokenLimit    int64
	inputTokenLimited  bool
	requestedTokens    int64
}

func (a admission) blockedReason(inFlight, inFlightTokens int64) string {
	if a.concurrencyLimited && inFlight+1 > a.concurrencyLimit {
		return reasonTooManyConcurrentRequests
	}
	if a.inputTokenLimited && inFlightTokens+a.requestedTokens > a.inputTokenLimit {
		return reasonTooManyInputTokens
	}
	return admissionOpen
}

func (a admission) impossible() string {
	return a.blockedReason(0, 0)
}

func effectiveConcurrencyLimit(baseMaxConcurrent int64, capacity ModelCapacity) (limit int64, limited bool) {
	if capacity.MaxConcurrentPer10000Weight > 0 && capacity.BaselineWeight > 0 {
		current := weightConcurrencyLimit(capacity.CurrentWeight, capacity.MaxConcurrentPer10000Weight)
		baseline := weightConcurrencyLimit(capacity.BaselineWeight, capacity.MaxConcurrentPer10000Weight)
		return backstopped(max(min(current, baseline), capacity.HostWindowRequests), baseMaxConcurrent), true
	}
	if capacity.HostWindowRequests > 0 {
		return backstopped(capacity.HostWindowRequests, baseMaxConcurrent), true
	}
	if baseMaxConcurrent <= 0 {
		return 0, false
	}
	return scaleClamp(baseMaxConcurrent, capacity.ScaleFactor), true
}

func backstopped(limit, baseMaxConcurrent int64) int64 {
	if baseMaxConcurrent <= 0 {
		return limit
	}
	return min(limit, baseMaxConcurrent)
}

func effectiveInputTokenLimit(baseMaxInputTokens int64, capacity ModelCapacity) (limit int64, limited bool) {
	if baseMaxInputTokens <= 0 {
		return 0, false
	}
	return scaleClamp(baseMaxInputTokens, capacity.ScaleFactor), true
}

// scaleClamp rounds to nearest rather than flooring. See capacity.md, "The gateway limiter".
func scaleClamp(base int64, scale float64) int64 {
	if base <= 0 {
		return 0
	}
	return min(int64(math.Round(float64(base)*clampUnit(scale))), base)
}

func clampUnit(scale float64) float64 {
	switch {
	case math.IsNaN(scale): // fail closed: a corrupted scale must not grant unlimited capacity
		return 0
	case scale < 0:
		return 0
	case scale > 1:
		return 1
	default:
		return scale
	}
}
