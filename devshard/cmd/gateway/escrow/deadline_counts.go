package escrow

import (
	"maps"
	"sync"
)

// DeadlineUnsettled is one model and one reason the deadline rule narrated an escrow unsettled for; the metrics labels carry it.
type DeadlineUnsettled struct {
	Model  string
	Reason string
}

type deadlineCounter struct {
	mu        sync.Mutex
	unsettled map[DeadlineUnsettled]uint64
}

func (counter *deadlineCounter) add(model, reason string) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.unsettled == nil {
		counter.unsettled = map[DeadlineUnsettled]uint64{}
	}
	counter.unsettled[DeadlineUnsettled{Model: model, Reason: reason}]++
}

// DeadlineUnsettledCounts is how many escrows the deadline rule narrated unsettled, by model and reason, for the metrics collector.
func (m *Manager) DeadlineUnsettledCounts() map[DeadlineUnsettled]uint64 {
	m.deadlines.counts.mu.Lock()
	defer m.deadlines.counts.mu.Unlock()
	return maps.Clone(m.deadlines.counts.unsettled)
}
