package broker

import (
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

// EscrowLoadTracker records AcquireMLNode hits per escrow and exposes a
// rolling requests-per-minute snapshot. Idle escrows (no acquires in the
// window) are omitted from Snapshot.
type EscrowLoadTracker struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	// escrowID -> acquire timestamps (unix nano), kept pruned to the window.
	events map[uint64][]int64
}

// NewEscrowLoadTracker returns a tracker with the given window (DefaultEscrowLoadWindow when <= 0).
func NewEscrowLoadTracker(window time.Duration) *EscrowLoadTracker {
	if window <= 0 {
		window = DefaultEscrowLoadWindow
	}
	return &EscrowLoadTracker{
		window: window,
		now:    time.Now,
		events: make(map[uint64][]int64),
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
	cutoff := now - t.window.Nanoseconds()

	t.mu.Lock()
	defer t.mu.Unlock()
	ts := append(t.events[id], now)
	t.events[id] = pruneTimestamps(ts, cutoff)
}

// Snapshot returns active escrows with requests_per_min = count / windowMinutes.
// Escrows with zero events in the window are omitted.
func (t *EscrowLoadTracker) Snapshot() []EscrowLoad {
	if t == nil {
		return nil
	}
	now := t.now().UnixNano()
	cutoff := now - t.window.Nanoseconds()
	mins := t.window.Minutes()
	if mins <= 0 {
		mins = DefaultEscrowLoadWindow.Minutes()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]EscrowLoad, 0, len(t.events))
	for id, ts := range t.events {
		kept := pruneTimestamps(ts, cutoff)
		if len(kept) == 0 {
			delete(t.events, id)
			continue
		}
		t.events[id] = kept
		out = append(out, EscrowLoad{
			EscrowID:       id,
			RequestsPerMin: float64(len(kept)) / mins,
		})
	}
	return out
}

func pruneTimestamps(ts []int64, cutoff int64) []int64 {
	i := 0
	for i < len(ts) && ts[i] < cutoff {
		i++
	}
	if i == 0 {
		return ts
	}
	if i >= len(ts) {
		return nil
	}
	return append([]int64(nil), ts[i:]...)
}
