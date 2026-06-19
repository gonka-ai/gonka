package runtimeconfig

import (
	"log/slog"
	"sync"
	"sync/atomic"

	devshardpkg "devshard"
)

// baseProvider holds the snapshot, listener machinery, and apply/fireEpoch
// logic shared by every Provider implementation (gRPC long-poll, chain poll).
// Provider implementations embed *baseProvider so they all expose Snapshot /
// LogprobsMode / CurrentEpochID / Availability / OnEpochChange with identical
// semantics, and feed updates through a single apply() that writes the same
// AvailabilityTracker and fires the same epoch listeners.
type baseProvider struct {
	log          *slog.Logger
	availability *devshardpkg.AvailabilityTracker

	snap atomic.Pointer[Snapshot]

	listenersMu sync.Mutex
	listeners   map[uint64]EpochChangeListener
	nextID      uint64
}

// newBase returns a baseProvider with `defaults` stored as the initial
// snapshot so Snapshot()/LogprobsMode()/etc never return zero values.
func newBase(log *slog.Logger, availability *devshardpkg.AvailabilityTracker, defaults Snapshot) *baseProvider {
	if log == nil {
		log = slog.Default()
	}
	b := &baseProvider{
		log:          log,
		availability: availability,
		listeners:    make(map[uint64]EpochChangeListener),
	}
	initial := defaults
	b.snap.Store(&initial)
	return b
}

// Snapshot returns the latest applied snapshot. Safe for concurrent reads.
func (b *baseProvider) Snapshot() Snapshot {
	s := b.snap.Load()
	if s == nil {
		return Snapshot{}
	}
	return *s
}

func (b *baseProvider) LogprobsMode() string   { return b.Snapshot().LogprobsMode }
func (b *baseProvider) CurrentEpochID() uint64 { return b.Snapshot().CurrentEpochID }

// Availability derives the live AvailabilityStatus from the latest snapshot.
// Callers normally read the AvailabilityTracker directly (host gate), this is
// here for completeness so a Provider value can be queried without holding
// the tracker reference.
func (b *baseProvider) Availability() devshardpkg.AvailabilityStatus {
	s := b.Snapshot()
	var ts int64
	if !s.ServedAt.IsZero() {
		ts = s.ServedAt.Unix()
	}
	return devshardpkg.AvailabilityStatus{
		Enabled: s.DevshardRequestsEnabled,
		Time:    ts,
		EpochID: s.CurrentEpochID,
	}
}

// OnEpochChange registers a callback fired (in its own goroutine) on every
// CurrentEpochID transition observed *after* the first successful apply with
// a non-zero ParamsBlockHeight. The cancel func detaches the listener.
func (b *baseProvider) OnEpochChange(fn EpochChangeListener) (cancel func()) {
	b.listenersMu.Lock()
	defer b.listenersMu.Unlock()
	id := b.nextID
	b.nextID++
	b.listeners[id] = fn
	return func() {
		b.listenersMu.Lock()
		delete(b.listeners, id)
		b.listenersMu.Unlock()
	}
}

// apply atomically swaps in the new snapshot, records availability into the
// shared tracker, and dispatches OnEpochChange listeners on a real epoch
// transition (prev.ParamsBlockHeight > 0 gates the initial-apply case).
func (b *baseProvider) apply(next Snapshot) {
	prev := b.snap.Load()
	prevEnabled := curDevshardEnabled(prev)
	nextCopy := next
	b.snap.Store(&nextCopy)

	if prev == nil || prevEnabled != next.DevshardRequestsEnabled {
		b.log.Info("runtime_config: devshard_requests_enabled applied",
			"previous", prevEnabled,
			"current", next.DevshardRequestsEnabled,
			"paramsBlockHeight", next.ParamsBlockHeight,
			"epochID", next.CurrentEpochID,
		)
	} else {
		b.log.Debug("runtime_config: snapshot applied",
			"devshardRequestsEnabled", next.DevshardRequestsEnabled,
			"paramsBlockHeight", next.ParamsBlockHeight,
			"epochID", next.CurrentEpochID,
		)
	}

	if b.availability != nil {
		var ts int64
		if !next.ServedAt.IsZero() {
			ts = next.ServedAt.Unix()
		}
		b.availability.Record(next.DevshardRequestsEnabled, ts, next.CurrentEpochID)
	}

	if prev != nil && prev.ParamsBlockHeight > 0 && prev.CurrentEpochID != next.CurrentEpochID {
		b.fireEpoch(prev.CurrentEpochID, next.CurrentEpochID)
	}
}

func (b *baseProvider) fireEpoch(oldE, newE uint64) {
	b.listenersMu.Lock()
	snap := make([]EpochChangeListener, 0, len(b.listeners))
	for _, fn := range b.listeners {
		snap = append(snap, fn)
	}
	b.listenersMu.Unlock()

	for _, fn := range snap {
		fn := fn
		go func() {
			defer func() {
				if r := recover(); r != nil {
					b.log.Error("epoch listener panic", "panic", r)
				}
			}()
			fn(oldE, newE)
		}()
	}
}

func curDevshardEnabled(s *Snapshot) bool {
	if s == nil {
		return false
	}
	return s.DevshardRequestsEnabled
}
