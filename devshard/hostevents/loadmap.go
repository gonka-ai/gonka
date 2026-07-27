package hostevents

import (
	"sync"
	"time"

	"devshard/nodemanager/gen"
)

// LoadMap stores the latest escrow_load snapshot from GetHostEvents.
// Every successful long-poll response (event change or timeout) replaces the
// map; idle escrows are already omitted by dapi.
type LoadMap struct {
	mu          sync.RWMutex
	byEscrow    map[uint64]float64
	deliveredAt time.Time
}

// NewLoadMap returns an empty load map (no delivery yet).
func NewLoadMap() *LoadMap {
	return &LoadMap{byEscrow: make(map[uint64]float64)}
}

// Replace stores a full snapshot from a GetHostEvents response.
func (l *LoadMap) Replace(loads []*gen.EscrowLoad, at time.Time) {
	if l == nil {
		return
	}
	next := make(map[uint64]float64, len(loads))
	for _, e := range loads {
		if e == nil {
			continue
		}
		next[e.GetEscrowId()] = e.GetRequestsPerMin()
	}
	l.mu.Lock()
	l.byEscrow = next
	l.deliveredAt = at
	l.mu.Unlock()
}

// Snapshot returns a copy of the last load map and its delivery time.
// deliveredAt is zero when no successful GetHostEvents response has been seen.
func (l *LoadMap) Snapshot() (byEscrow map[uint64]float64, deliveredAt time.Time) {
	if l == nil {
		return nil, time.Time{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.byEscrow) == 0 {
		return map[uint64]float64{}, l.deliveredAt
	}
	out := make(map[uint64]float64, len(l.byEscrow))
	for id, rate := range l.byEscrow {
		out[id] = rate
	}
	return out, l.deliveredAt
}
