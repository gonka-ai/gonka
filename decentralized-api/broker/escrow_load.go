package broker

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// DefaultEscrowLoadWindow is the rolling window used for per-escrow acquire rates.
const DefaultEscrowLoadWindow = 30 * time.Minute

// EscrowLoad is one escrow's average acquire rate over the tracker window.
type EscrowLoad struct {
	EscrowID       uint64
	RequestsPerMin float64
}

// escrowStat is the O(1) per-escrow state: an exponentially-weighted acquire
// count (decayed count "C" as of lastNano) plus the last acquire time. No raw
// timestamps are retained, so memory/CPU scale with the number of escrows, not
// with request volume.
type escrowStat struct {
	count    float64 // EWMA acquire count as of lastNano
	lastNano int64   // unix-nano of the most recent Record
}

// EscrowLoadTracker records AcquireMLNode hits per escrow and exposes a rolling
// requests-per-minute snapshot. Instead of storing a timestamp per acquire, it
// keeps a time-decayed exponentially-weighted count per escrow (decay time
// constant = window). At a steady rate r req/min the weighted count converges
// to r * windowMinutes, so requests_per_min = count / windowMinutes ≈ r.
//
// An escrow is considered idle (and omitted/evicted from Snapshot) once its
// last acquire is older than the window — matching the previous "no events in
// window" semantics that drives the capacity divisor's active-escrow count.
type EscrowLoadTracker struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	stats  map[uint64]escrowStat
}

// NewEscrowLoadTracker returns a tracker with the given window (DefaultEscrowLoadWindow when <= 0).
func NewEscrowLoadTracker(window time.Duration) *EscrowLoadTracker {
	if window <= 0 {
		window = DefaultEscrowLoadWindow
	}
	return &EscrowLoadTracker{
		window: window,
		now:    time.Now,
		stats:  make(map[uint64]escrowStat),
	}
}

// SetNowForTest overrides the clock (tests only).
func (t *EscrowLoadTracker) SetNowForTest(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// Record attributes one acquire to escrowID. Empty / non-numeric IDs are ignored.
func (t *EscrowLoadTracker) Record(escrowID string) {
	if t == nil || escrowID == "" {
		return
	}
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return
	}
	now := t.now().UnixNano()

	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats[id]
	if s.lastNano != 0 {
		s.count *= t.decayFactor(now - s.lastNano)
	}
	s.count += 1
	s.lastNano = now
	t.stats[id] = s
}

// Snapshot returns active escrows with requests_per_min = decayedCount / windowMinutes.
// Escrows whose last acquire is older than the window are evicted and omitted.
func (t *EscrowLoadTracker) Snapshot() []EscrowLoad {
	if t == nil {
		return nil
	}
	now := t.now().UnixNano()
	windowNanos := t.window.Nanoseconds()
	mins := t.window.Minutes()
	if mins <= 0 {
		mins = DefaultEscrowLoadWindow.Minutes()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]EscrowLoad, 0, len(t.stats))
	for id, s := range t.stats {
		elapsed := now - s.lastNano
		if elapsed > windowNanos {
			delete(t.stats, id)
			continue
		}
		decayed := s.count * t.decayFactor(elapsed)
		out = append(out, EscrowLoad{
			EscrowID:       id,
			RequestsPerMin: decayed / mins,
		})
	}
	return out
}

// decayFactor is exp(-elapsed / window): the exponential decay applied to the
// weighted count over elapsedNano nanoseconds (decay time constant = window).
func (t *EscrowLoadTracker) decayFactor(elapsedNano int64) float64 {
	if elapsedNano <= 0 {
		return 1
	}
	return math.Exp(-float64(elapsedNano) / float64(t.window.Nanoseconds()))
}
