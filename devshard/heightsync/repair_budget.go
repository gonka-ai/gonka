package heightsync

import (
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
// (turn, slot), R_max per K_hb window, stagger, exponential backoff.
// Counters are local (Prometheus waits for E8).
type RepairBudget struct {
	mu sync.Mutex

	cfg      RepairConfig
	slotsNum uint32
	vSlot    uint32
	kHb      uint64

	probed         map[turnSlot]struct{}
	windowKey      uint64
	probesInWindow int
	failCount      map[uint32]int
	backoffUntil   map[uint32]time.Time
	counts         map[string]int

	now   func() time.Time
	sleep func(time.Duration)
}

// NewRepairBudget constructs a prober budget. MaxProbesPerWindow ≤ 0 uses
// slotsNum. Stagger is used as-is (zero means no wait; tests rely on that).
func NewRepairBudget(cfg RepairConfig, slotsNum, vSlot uint32, kHb uint64) *RepairBudget {
	if slotsNum == 0 {
		slotsNum = 1
	}
	if kHb == 0 {
		kHb = DefaultHeartbeatIntervalBlocks
	}
	if cfg.MaxProbesPerWindow <= 0 {
		cfg.MaxProbesPerWindow = int(slotsNum)
	}
	return &RepairBudget{
		cfg:          cfg,
		slotsNum:     slotsNum,
		vSlot:        vSlot,
		kHb:          kHb,
		probed:       make(map[turnSlot]struct{}),
		failCount:    make(map[uint32]int),
		backoffUntil: make(map[uint32]time.Time),
		counts:       make(map[string]int),
		now:          time.Now,
		sleep:        time.Sleep,
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
		b.sleep = sleep
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
func (b *RepairBudget) Begin(turnSeq uint64, slot uint32, hNow uint64, armed bool) (delay time.Duration, skip RepairSkip) {
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
	b.rollWindowLocked(hNow)
	if b.probesInWindow >= b.cfg.MaxProbesPerWindow {
		b.counts[string(RepairSkipBudget)]++
		return 0, RepairSkipBudget
	}
	return ProbeStagger(b.vSlot, slot, b.slotsNum, b.cfg.Stagger), RepairSkipNone
}

// Sleep waits delay using the injected clock. No-op when delay ≤ 0.
func (b *RepairBudget) Sleep(delay time.Duration) {
	if b == nil || delay <= 0 {
		return
	}
	b.mu.Lock()
	sleep := b.sleep
	b.mu.Unlock()
	if sleep != nil {
		sleep(delay)
	}
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

func (b *RepairBudget) rollWindowLocked(hNow uint64) {
	key := hNow / b.kHb
	if key != b.windowKey {
		b.windowKey = key
		b.probesInWindow = 0
	}
}
