package devshard

import (
	"log/slog"
	"sync"

	"devshard/storage"
)

type PoCActivityProvider interface {
	WasPoCActiveAt(timestamp int64) bool
	WasPoCActiveAtWithGrace(timestamp int64, earlyGraceSeconds int64) bool
	WasPoCActiveBetween(startTime int64, endTime int64, earlyGraceSeconds int64) bool
}

type PoCActivityTracker struct {
	mu      sync.RWMutex
	active  bool
	time    int64
	epochID uint64
	periods []storage.PoCActivityPeriod
	store   storage.Storage
}

func NewPoCActivityTracker(active bool, timestamp int64, epochID uint64) *PoCActivityTracker {
	return &PoCActivityTracker{active: active, time: timestamp, epochID: epochID}
}

func (t *PoCActivityTracker) SetStore(store storage.Storage) {
	if t == nil || store == nil {
		return
	}
	periods, err := store.ListPoCActivityPeriods()
	if err != nil {
		slog.Warn("devshard poc activity: failed to load persisted periods", "error", err)
	}

	t.mu.Lock()
	t.store = store
	if err == nil {
		t.periods = append([]storage.PoCActivityPeriod(nil), periods...)
	}
	active := t.active
	epochID := t.epochID
	start := t.time
	t.mu.Unlock()

	if active {
		t.persistPeriod(storage.PoCActivityPeriod{EpochID: epochID, StartTime: start})
	}
	t.pruneToStoreCursor()
}

func (t *PoCActivityTracker) Record(active bool, timestamp int64, epochID uint64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	if t.active == active {
		if active && epochID != t.epochID && timestamp > t.time {
			closed := storage.PoCActivityPeriod{EpochID: t.epochID, StartTime: t.time, EndTime: timestamp}
			open := storage.PoCActivityPeriod{EpochID: epochID, StartTime: timestamp}
			t.upsertPeriodLocked(closed)
			t.active = active
			t.time = timestamp
			t.epochID = epochID
			t.mu.Unlock()
			t.persistPeriod(closed)
			t.persistPeriod(open)
			t.pruneToStoreCursor()
			return
		}
		if !active && timestamp > t.time {
			t.time = timestamp
			t.epochID = epochID
		}
		t.mu.Unlock()
		return
	}

	var persist []storage.PoCActivityPeriod
	if t.active {
		closed := storage.PoCActivityPeriod{EpochID: t.epochID, StartTime: t.time, EndTime: timestamp}
		t.upsertPeriodLocked(closed)
		persist = append(persist, closed)
	}

	t.active = active
	t.time = timestamp
	t.epochID = epochID
	if active {
		open := storage.PoCActivityPeriod{EpochID: epochID, StartTime: timestamp}
		t.upsertPeriodLocked(open)
		persist = append(persist, open)
	}
	t.mu.Unlock()

	for _, period := range persist {
		t.persistPeriod(period)
	}
	t.pruneToStoreCursor()
}

func (t *PoCActivityTracker) upsertPeriodLocked(period storage.PoCActivityPeriod) {
	for i, existing := range t.periods {
		if existing.EpochID == period.EpochID && existing.StartTime == period.StartTime {
			t.periods[i] = period
			return
		}
	}
	t.periods = append(t.periods, period)
}

func (t *PoCActivityTracker) PruneBefore(cutoffEpochID uint64) {
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
	if t.epochID < cutoffEpochID {
		t.active = false
	}
}

func (t *PoCActivityTracker) WasPoCActiveAt(timestamp int64) bool {
	return t.WasPoCActiveAtWithGrace(timestamp, 0)
}

func (t *PoCActivityTracker) WasPoCActiveAtWithGrace(timestamp int64, earlyGraceSeconds int64) bool {
	return t.WasPoCActiveBetween(timestamp, timestamp, earlyGraceSeconds)
}

func (t *PoCActivityTracker) WasPoCActiveBetween(startTime int64, endTime int64, earlyGraceSeconds int64) bool {
	if t == nil {
		return false
	}
	if endTime < startTime {
		return false
	}
	if earlyGraceSeconds < 0 {
		earlyGraceSeconds = 0
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.active && endTime >= t.time-earlyGraceSeconds {
		return true
	}
	for _, period := range t.periods {
		start := period.StartTime - earlyGraceSeconds
		end := period.EndTime
		if end == 0 {
			end = endTime + 1
		}
		if startTime < end && endTime >= start {
			return true
		}
	}
	return false
}

func (t *PoCActivityTracker) persistPeriod(period storage.PoCActivityPeriod) {
	t.mu.RLock()
	store := t.store
	t.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.RecordPoCActivityPeriod(period); err != nil {
		slog.Warn("devshard poc activity: failed to persist period",
			"epoch_id", period.EpochID, "start_time", period.StartTime, "end_time", period.EndTime, "error", err)
	}
}

func (t *PoCActivityTracker) pruneToStoreCursor() {
	t.mu.RLock()
	store := t.store
	t.mu.RUnlock()
	if cursor, ok := store.(prunedCursorProvider); ok {
		t.PruneBefore(cursor.PrunedUpTo())
	}
}
