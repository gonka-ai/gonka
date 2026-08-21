package heightsync

import (
	"context"
	"sync"
	"time"
)

// RepairSkip is why a planned probe was not sent. Empty means send.
type RepairSkip string

const (
	RepairSkipNone      RepairSkip = ""
	RepairSkipArmed     RepairSkip = "armed"
	RepairSkipProbed    RepairSkip = "already_probed"
	RepairSkipBudget    RepairSkip = "budget_exhausted"
	RepairSkipAckLanded RepairSkip = "skipped_ack_landed"
	RepairSkipBackoff   RepairSkip = "backoff"
	RepairSkipOwnSlot   RepairSkip = "own_slot"
)

type turnSlot struct {
	turn uint64
	slot uint32
}

// RepairBudget is the per-prober §11.4 state machine: one probe per
// (turn, slot), R_max per cadence window, stagger, exponential backoff.
// Counters are local (gateway /metrics is a separate operator surface).
type RepairBudget struct {
	mu sync.Mutex

	cfg      RepairConfig
	slotsNum uint32
	vSlot    uint32
	window   time.Duration

	probed         map[turnSlot]struct{}
	windowStart    time.Time
	probesInWindow int
	failCount      map[uint32]int
	backoffUntil   map[uint32]time.Time
	counts         map[string]int

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// NewRepairBudget constructs a prober budget. MaxProbesPerWindow ≤ 0 uses
// slotsNum. Stagger is used as-is (zero means no wait; tests rely on that).
// window is the cadence interval R_max is counted over; a prober that is
// missing acks is by definition not learning heights, so the window is elapsed
// time rather than a span of blocks it cannot see.
func NewRepairBudget(cfg RepairConfig, slotsNum, vSlot uint32, window time.Duration) *RepairBudget {
	if slotsNum == 0 {
		slotsNum = 1
	}
	if window <= 0 {
		window = DefaultHeartbeatInterval
	}
	if cfg.MaxProbesPerWindow <= 0 {
		cfg.MaxProbesPerWindow = int(slotsNum)
	}
	return &RepairBudget{
		cfg:          cfg,
		slotsNum:     slotsNum,
		vSlot:        vSlot,
		window:       window,
		probed:       make(map[turnSlot]struct{}),
		failCount:    make(map[uint32]int),
		backoffUntil: make(map[uint32]time.Time),
		counts:       make(map[string]int),
		now:          time.Now,
		sleep:        sleepCtx,
	}
}

// SetClock replaces now/sleep (tests).
func (b *RepairBudget) SetClock(now func() time.Time, sleep func(time.Duration)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if now != nil {
		b.now = now
	}
	if sleep != nil {
		b.sleep = func(ctx context.Context, d time.Duration) error {
			sleep(d)
			if ctx != nil {
				return ctx.Err()
			}
			return nil
		}
	}
}

// Count returns the local counter for an outcome / skip reason.
func (b *RepairBudget) Count(outcome string) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[outcome]
}

// InBackoff reports whether slot j is still backing off.
func (b *RepairBudget) InBackoff(slot uint32) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.backoffUntil[slot]
	if !ok {
		return false
	}
	return b.now().Before(until)
}

// FailCount is the UNREACHABLE count for slot j (tests).
func (b *RepairBudget) FailCount(slot uint32) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failCount[slot]
}

// ProbeStagger is ((V_slot − j) mod slots_num) · δ_probe.
func ProbeStagger(vSlot, j, slotsNum uint32, d time.Duration) time.Duration {
	if slotsNum == 0 || d <= 0 {
		return 0
	}
	diff := (int64(vSlot) - int64(j)) % int64(slotsNum)
	if diff < 0 {
		diff += int64(slotsNum)
	}
	return time.Duration(diff) * d
}

// Begin decides whether to wait-then-probe slot j of turnSeq.
// Armed stops the whole prober: callers should not continue the loop.
func (b *RepairBudget) Begin(turnSeq uint64, slot uint32, armed bool) (delay time.Duration, skip RepairSkip) {
	if b == nil {
		return 0, RepairSkipBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if armed {
		b.counts[string(RepairSkipArmed)]++
		return 0, RepairSkipArmed
	}
	if slot == b.vSlot {
		b.counts[string(RepairSkipOwnSlot)]++
		return 0, RepairSkipOwnSlot
	}
	key := turnSlot{turn: turnSeq, slot: slot}
	if _, ok := b.probed[key]; ok {
		b.counts[string(RepairSkipProbed)]++
		return 0, RepairSkipProbed
	}
	if until, ok := b.backoffUntil[slot]; ok && b.now().Before(until) {
		b.counts[string(RepairSkipBackoff)]++
		return 0, RepairSkipBackoff
	}
	b.rollWindowLocked()
	if b.probesInWindow >= b.cfg.MaxProbesPerWindow {
		b.counts[string(RepairSkipBudget)]++
		return 0, RepairSkipBudget
	}
	b.pruneLocked(turnSeq)
	return ProbeStagger(b.vSlot, slot, b.slotsNum, b.cfg.Stagger), RepairSkipNone
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	if ctx == nil {
		<-t.C
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Sleep waits delay using the injected clock. No-op when delay ≤ 0.
// Returns ctx.Err() if the context is cancelled while waiting.
func (b *RepairBudget) Sleep(ctx context.Context, delay time.Duration) error {
	if b == nil || delay <= 0 {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	b.mu.Lock()
	sleep := b.sleep
	b.mu.Unlock()
	if sleep == nil {
		return sleepCtx(ctx, delay)
	}
	return sleep(ctx, delay)
}

// AfterWait re-checks ack-in-Diff after the stagger. A landed ack spends no
// unicast and still consumes the (turn, slot) probe slot so we do not retry.
func (b *RepairBudget) AfterWait(turnSeq uint64, slot uint32, ackLanded bool) RepairSkip {
	if b == nil {
		return RepairSkipBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if ackLanded {
		b.probed[turnSlot{turn: turnSeq, slot: slot}] = struct{}{}
		b.counts[string(RepairSkipAckLanded)]++
		return RepairSkipAckLanded
	}
	return RepairSkipNone
}

// Record stores a unicast outcome. HEIGHT / UNREACHABLE consume R_max.
// UNREACHABLE starts exponential backoff for slot j.
func (b *RepairBudget) Record(turnSeq uint64, slot uint32, outcome string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probed[turnSlot{turn: turnSeq, slot: slot}] = struct{}{}
	b.probesInWindow++
	b.counts[outcome]++
	b.pruneLocked(turnSeq)
	if outcome != RepairOutcomeUnreachable {
		return
	}
	n := b.failCount[slot] + 1
	b.failCount[slot] = n
	if n > 5 {
		n = 5
	}
	stagger := b.cfg.Stagger
	if stagger <= 0 {
		stagger = time.Millisecond
	}
	b.backoffUntil[slot] = b.now().Add(stagger << n)
}

func (b *RepairBudget) rollWindowLocked() {
	now := b.now()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= b.window {
		b.windowStart = now
		b.probesInWindow = 0
	}
}

// Prune drops (turn, slot) entries older than DefaultTurnRetain behind maxTurnSeq.
func (b *RepairBudget) Prune(maxTurnSeq uint64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(maxTurnSeq)
}

func (b *RepairBudget) pruneLocked(maxTurnSeq uint64) {
	for k := range b.probed {
		if k.turn > maxTurnSeq {
			maxTurnSeq = k.turn
		}
	}
	var cutoff uint64
	if maxTurnSeq > DefaultTurnRetain {
		cutoff = maxTurnSeq - DefaultTurnRetain
	}
	for k := range b.probed {
		if k.turn < cutoff {
			delete(b.probed, k)
		}
	}
}

// ProbedCount is the size of the (turn, slot) map.
func (b *RepairBudget) ProbedCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.probed)
}

// RepairResponderBudget is the target-side mirror of RepairBudget: one HEIGHT
// build per (turn, requester slot), R_max per Interval window. It does not
// assign blame and does not consult close-ready — answering a probe is not
// probing.
type RepairResponderBudget struct {
	mu sync.Mutex

	cfg               RepairConfig
	window            time.Duration
	served            map[turnSlot]struct{}
	windowStart       time.Time
	responsesInWindow int
	counts            map[string]int
	now               func() time.Time
}

// NewRepairResponderBudget constructs the incoming-probe budget. Defaults
// match NewRepairBudget: MaxProbesPerWindow ≤ 0 uses slotsNum; window ≤ 0
// uses DefaultHeartbeatInterval.
func NewRepairResponderBudget(cfg RepairConfig, slotsNum uint32, window time.Duration) *RepairResponderBudget {
	if slotsNum == 0 {
		slotsNum = 1
	}
	if window <= 0 {
		window = DefaultHeartbeatInterval
	}
	if cfg.MaxProbesPerWindow <= 0 {
		cfg.MaxProbesPerWindow = int(slotsNum)
	}
	return &RepairResponderBudget{
		cfg:    cfg,
		window: window,
		served: make(map[turnSlot]struct{}),
		counts: make(map[string]int),
		now:    time.Now,
	}
}

// SetClock replaces now (tests).
func (b *RepairResponderBudget) SetClock(now func() time.Time) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if now != nil {
		b.now = now
	}
}

// Count returns the local counter for an outcome / skip reason.
func (b *RepairResponderBudget) Count(outcome string) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[outcome]
}

// Allow reports whether this (turn, requester) may spend an oracle read.
// Unknown-turn rejection is the caller's job and must happen first so a
// flood of invented turn_seqs never reaches here.
func (b *RepairResponderBudget) Allow(turnSeq uint64, requesterSlot uint32) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := turnSlot{turn: turnSeq, slot: requesterSlot}
	if _, ok := b.served[key]; ok {
		b.counts[string(RepairSkipProbed)]++
		return false
	}
	b.rollWindowLocked()
	if b.responsesInWindow >= b.cfg.MaxProbesPerWindow {
		b.counts[string(RepairSkipBudget)]++
		return false
	}
	b.served[key] = struct{}{}
	b.responsesInWindow++
	b.counts[RepairOutcomeHeight]++
	b.pruneLocked(turnSeq)
	return true
}

func (b *RepairResponderBudget) rollWindowLocked() {
	now := b.now()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= b.window {
		b.windowStart = now
		b.responsesInWindow = 0
	}
}

func (b *RepairResponderBudget) pruneLocked(maxTurnSeq uint64) {
	for k := range b.served {
		if k.turn > maxTurnSeq {
			maxTurnSeq = k.turn
		}
	}
	var cutoff uint64
	if maxTurnSeq > DefaultTurnRetain {
		cutoff = maxTurnSeq - DefaultTurnRetain
	}
	for k := range b.served {
		if k.turn < cutoff {
			delete(b.served, k)
		}
	}
}

// ServedCount is the size of the (turn, requester) map.
func (b *RepairResponderBudget) ServedCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.served)
}
