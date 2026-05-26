package devshard

import (
	"errors"
	"sync"
)

var ErrRequestsDisabled = errors.New("devshard completion and timeout requests are disabled")

type AvailabilityStatus struct {
	Enabled bool
	Time    int64
	EpochID uint64
}

type AvailabilityProvider interface {
	CurrentAvailability() AvailabilityStatus
}

// AvailabilityTracker is a lightweight process-local snapshot used by the host
// hot path. Long-term standalone devshardd should receive this state from a
// devshard-owned mainnet params provider rather than dapi-specific wiring.
type AvailabilityTracker struct {
	mu      sync.RWMutex
	current AvailabilityStatus
}

func NewAvailabilityTracker(enabled bool, timestamp int64, epochID uint64) *AvailabilityTracker {
	return &AvailabilityTracker{
		current: AvailabilityStatus{Enabled: enabled, Time: timestamp, EpochID: epochID},
	}
}

func (t *AvailabilityTracker) Record(enabled bool, timestamp int64, epochID uint64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	t.current = AvailabilityStatus{Enabled: enabled, Time: timestamp, EpochID: epochID}
	t.mu.Unlock()
}

func (t *AvailabilityTracker) CurrentAvailability() AvailabilityStatus {
	if t == nil {
		return AvailabilityStatus{Enabled: true}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current
}
