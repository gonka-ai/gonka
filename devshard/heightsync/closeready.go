package heightsync

import (
	"sync"
	"time"
)

// CloseReadyView is the producer half of spec §12. Finalization consumes it;
// this package never emits a vote, round, or mainnet tx on arming.
type CloseReadyView interface {
	Armed() (armed bool, armedAtHeight uint64)
	TimeoutEvidence() UserTimeoutEvidence
}

// UserTimeoutEvidence is retained silence toward this host. DegradedTurns
// are context, not fraud.
type UserTimeoutEvidence struct {
	Slot                  uint32
	LastSignalHeight      uint64
	ArmedAtHeight         uint64
	LastUserHeightClaim   uint64
	LastCompleteTurnStart uint64
	DegradedTurns         []uint64

	// SilentFor is the measured silence that armed this host, and LastSignalAt
	// its start. Arming is a local decision on a local clock, so these are
	// this host's account of the gap, not a value other hosts can recompute.
	LastSignalAt time.Time
	ArmedAt      time.Time
	SilentFor    time.Duration
}

// ArmedInterval is one [armed_at, disarmed_at) gap retained after contact.
// Times are the arming clock; heights are evidence for finalization.
type ArmedInterval struct {
	ArmedAt        uint64
	DisarmedAt     uint64
	ArmedAtTime    time.Time
	DisarmedAtTime time.Time
	SilentFor      time.Duration
}

// CloseReady is the level-triggered arming machine (spec §12).
//
// Silence is measured on this host's wall clock: a host that has heard nothing
// from the user cannot observe mainnet height advancing either, so it cannot
// count blocks of silence. It can only count elapsed time. Arming stays
// silence-only — a missing ack is never a reason — and still emits nothing.
type CloseReady struct {
	mu sync.Mutex

	cfg  HeartbeatConfig
	slot uint32
	now  func() time.Time

	contacted             bool
	lastSignalAt          time.Time
	lastSignalHeight      uint64
	lastKnownHeight       uint64
	lastUserHeightClaim   uint64
	lastCompleteTurnStart uint64
	degradedTurns         []uint64

	armed         bool
	armedAt       time.Time
	armedAtHeight uint64
	intervals     []ArmedInterval

	forceArmed bool // test injection
}

// NewCloseReady constructs an unarmed tracker for slot.
func NewCloseReady(slot uint32, cfg HeartbeatConfig) *CloseReady {
	return &CloseReady{cfg: cfg.withDefaults(), slot: slot, now: time.Now}
}

// SetClock replaces the arming clock (tests).
func (c *CloseReady) SetClock(now func() time.Time) {
	if c == nil || now == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// NoteContact records user-signed traffic (applied diff, heartbeat, or this
// host's mempool tx included). Any contact disarms and restarts the silence
// budget. hNow is recorded as evidence only.
func (c *CloseReady) NoteContact(hNow, userClaim uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.nowLocked()
	c.contacted = true
	if userClaim > c.lastUserHeightClaim {
		c.lastUserHeightClaim = userClaim
	}
	if hNow > c.lastKnownHeight {
		c.lastKnownHeight = hNow
	}
	if c.armed {
		c.intervals = append(c.intervals, ArmedInterval{
			ArmedAt:        c.armedAtHeight,
			DisarmedAt:     hNow,
			ArmedAtTime:    c.armedAt,
			DisarmedAtTime: at,
			SilentFor:      at.Sub(c.lastSignalAt),
		})
		c.armed = false
		c.armedAt = time.Time{}
		c.armedAtHeight = 0
	}
	c.lastSignalAt = at
	if hNow > c.lastSignalHeight {
		c.lastSignalHeight = hNow
	}
}

// SetTurnContext copies turn-tracker facts into TimeoutEvidence.
func (c *CloseReady) SetTurnContext(lastCompleteTurnStart uint64, degraded []uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastCompleteTurnStart = lastCompleteTurnStart
	c.degradedTurns = append([]uint64(nil), degraded...)
}

// Evaluate refreshes arming and records hNow as the height to attach to
// evidence if this host arms. Arming itself keys on elapsed silence, so callers
// need no oracle and no tick to stay correct — Armed re-evaluates on read.
func (c *CloseReady) Evaluate(hNow uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hNow > c.lastKnownHeight {
		c.lastKnownHeight = hNow
	}
	c.evaluateLocked()
}

// evaluateLocked arms when silence has exceeded T_idle after at least one
// contact. A missing ack is never a reason. Emits nothing.
func (c *CloseReady) evaluateLocked() {
	if c.forceArmed {
		c.armed = true
		if c.armedAt.IsZero() {
			c.armedAt = c.nowLocked()
		}
		if c.armedAtHeight == 0 {
			c.armedAtHeight = c.lastKnownHeight
		}
		return
	}
	if !c.contacted || c.armed {
		return
	}
	now := c.nowLocked()
	if now.Sub(c.lastSignalAt) > c.cfg.IdleTimeout {
		c.armed = true
		c.armedAt = now
		c.armedAtHeight = c.lastKnownHeight
	}
}

func (c *CloseReady) nowLocked() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// Armed implements CloseReadyView. It re-evaluates the silence level on read so
// a host arms from elapsed time alone, with no ticker and no inbound event.
func (c *CloseReady) Armed() (bool, uint64) {
	if c == nil {
		return false, 0
	}
	c.mu.Lock()
	c.evaluateLocked()
	armed := c.armed
	at := c.armedAtHeight
	c.mu.Unlock()
	SetCloseReadyArmed(armed)
	return armed, at
}

// SilentFor is how long this host has gone without user contact.
func (c *CloseReady) SilentFor() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.contacted {
		return 0
	}
	return c.nowLocked().Sub(c.lastSignalAt)
}

// TimeoutEvidence implements CloseReadyView.
func (c *CloseReady) TimeoutEvidence() UserTimeoutEvidence {
	if c == nil {
		return UserTimeoutEvidence{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evaluateLocked()
	var silent time.Duration
	if c.contacted {
		end := c.armedAt
		if end.IsZero() {
			end = c.nowLocked()
		}
		silent = end.Sub(c.lastSignalAt)
	}
	return UserTimeoutEvidence{
		Slot:                  c.slot,
		LastSignalHeight:      c.lastSignalHeight,
		ArmedAtHeight:         c.armedAtHeight,
		LastUserHeightClaim:   c.lastUserHeightClaim,
		LastCompleteTurnStart: c.lastCompleteTurnStart,
		DegradedTurns:         append([]uint64(nil), c.degradedTurns...),
		LastSignalAt:          c.lastSignalAt,
		ArmedAt:               c.armedAt,
		SilentFor:             silent,
	}
}

// Intervals returns retained [armed_at, disarmed_at) gaps (spec §12).
func (c *CloseReady) Intervals() []ArmedInterval {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ArmedInterval(nil), c.intervals...)
}

// ForceArmed is a test seam. Production never calls it.
func (c *CloseReady) ForceArmed(armed bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forceArmed = armed
	if !armed {
		c.armed = false
		c.armedAt = time.Time{}
		c.armedAtHeight = 0
	}
}
