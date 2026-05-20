package devshard

import (
	"log/slog"
	"sync"

	"devshard/storage"
)

type AvailabilityStatus struct {
	Enabled bool
	Time    int64
	EpochID uint64
}

type AvailabilityProvider interface {
	CurrentAvailability() AvailabilityStatus
	WasUnavailableAt(timestamp int64) bool
	WasUnavailableAtWithGrace(timestamp int64, earlyGraceSeconds int64) bool
}

type prunedCursorProvider interface {
	PrunedUpTo() uint64
}

type AvailabilityTracker struct {
	mu      sync.RWMutex
	current AvailabilityStatus
	periods []storage.AvailabilityPeriod
	store   storage.Storage
}

func NewAvailabilityTracker(enabled bool, timestamp int64, epochID uint64) *AvailabilityTracker {
	return &AvailabilityTracker{
		current: AvailabilityStatus{Enabled: enabled, Time: timestamp, EpochID: epochID},
	}
}

func (t *AvailabilityTracker) SetStore(store storage.Storage) {
	if t == nil || store == nil {
		return
	}
	periods, err := store.ListAvailabilityPeriods()
	if err != nil {
		slog.Warn("devshard availability: failed to load persisted periods", "error", err)
	}

	t.mu.Lock()
	t.store = store
	if err == nil {
		t.periods = append([]storage.AvailabilityPeriod(nil), periods...)
	}
	current := t.current
	t.mu.Unlock()

	if !current.Enabled {
		t.persistPeriod(storage.AvailabilityPeriod{
			EpochID:   current.EpochID,
			StartTime: current.Time,
			EndTime:   0,
		})
	}
	t.pruneToStoreCursor()
}

func (t *AvailabilityTracker) Record(enabled bool, timestamp int64, epochID uint64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	if t.current.Enabled == enabled {
		if enabled && timestamp > t.current.Time {
			t.current.Time = timestamp
			t.current.EpochID = epochID
			var persist []storage.AvailabilityPeriod
			for i, period := range t.periods {
				if period.EndTime == 0 && timestamp > period.StartTime {
					t.periods[i].EndTime = timestamp
					persist = append(persist, t.periods[i])
				}
			}
			t.mu.Unlock()
			for _, period := range persist {
				t.persistPeriod(period)
			}
			t.pruneToStoreCursor()
			return
		}
		if !enabled && epochID != t.current.EpochID && timestamp > t.current.Time {
			closed := storage.AvailabilityPeriod{
				EpochID:   t.current.EpochID,
				StartTime: t.current.Time,
				EndTime:   timestamp,
			}
			open := storage.AvailabilityPeriod{
				EpochID:   epochID,
				StartTime: timestamp,
				EndTime:   0,
			}
			t.upsertPeriodLocked(closed)
			t.current = AvailabilityStatus{Enabled: enabled, Time: timestamp, EpochID: epochID}
			t.mu.Unlock()
			t.persistPeriod(closed)
			t.persistPeriod(open)
			t.pruneToStoreCursor()
			return
		}
		t.mu.Unlock()
		return
	}

	var persist []storage.AvailabilityPeriod
	if !t.current.Enabled {
		closed := storage.AvailabilityPeriod{
			EpochID:   t.current.EpochID,
			StartTime: t.current.Time,
			EndTime:   timestamp,
		}
		t.upsertPeriodLocked(closed)
		persist = append(persist, closed)
	}

	t.current = AvailabilityStatus{Enabled: enabled, Time: timestamp, EpochID: epochID}
	if !enabled {
		open := storage.AvailabilityPeriod{EpochID: epochID, StartTime: timestamp}
		persist = append(persist, open)
	}
	t.mu.Unlock()

	for _, period := range persist {
		t.persistPeriod(period)
	}
	t.pruneToStoreCursor()
}

func (t *AvailabilityTracker) upsertPeriodLocked(period storage.AvailabilityPeriod) {
	for i, existing := range t.periods {
		if existing.EpochID == period.EpochID && existing.StartTime == period.StartTime {
			t.periods[i] = period
			return
		}
	}
	t.periods = append(t.periods, period)
}

func (t *AvailabilityTracker) PruneBefore(cutoffEpochID uint64) {
	if t == nil || cutoffEpochID == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.periods[:0]
	for _, period := range t.periods {
		if period.EpochID >= cutoffEpochID {
			out = append(out, period)
		}
	}
	t.periods = out
	if t.current.EpochID < cutoffEpochID {
		t.current = AvailabilityStatus{Enabled: true}
	}
}

func (t *AvailabilityTracker) CurrentAvailability() AvailabilityStatus {
	if t == nil {
		return AvailabilityStatus{Enabled: true}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current
}

func (t *AvailabilityTracker) WasUnavailableAt(timestamp int64) bool {
	return t.WasUnavailableAtWithGrace(timestamp, 0)
}

func (t *AvailabilityTracker) WasUnavailableAtWithGrace(timestamp int64, earlyGraceSeconds int64) bool {
	if t == nil {
		return false
	}
	if earlyGraceSeconds < 0 {
		earlyGraceSeconds = 0
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	if !t.current.Enabled && timestamp >= t.current.Time-earlyGraceSeconds {
		return true
	}
	for _, period := range t.periods {
		if timestamp >= period.StartTime-earlyGraceSeconds && (period.EndTime == 0 || timestamp < period.EndTime) {
			return true
		}
	}
	return false
}

func (t *AvailabilityTracker) persistPeriod(period storage.AvailabilityPeriod) {
	t.mu.RLock()
	store := t.store
	t.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.RecordAvailabilityPeriod(period); err != nil {
		slog.Warn("devshard availability: failed to persist period",
			"epoch_id", period.EpochID, "start_time", period.StartTime, "end_time", period.EndTime, "error", err)
	}
}

func (t *AvailabilityTracker) pruneToStoreCursor() {
	t.mu.RLock()
	store := t.store
	t.mu.RUnlock()
	if cursor, ok := store.(prunedCursorProvider); ok {
		t.PruneBefore(cursor.PrunedUpTo())
	}
}
