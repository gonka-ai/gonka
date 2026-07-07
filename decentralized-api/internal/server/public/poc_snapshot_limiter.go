package public

import (
	"fmt"
	"sync"
	"time"
)

const (
	// maxDistinctSnapshotCountsPerValidator caps how many distinct snapshot
	// counts one validator may request proofs for, per (stage, model) store.
	// Honest validation needs exactly two: the early checkpoint count and the
	// final commitment count. The third slot absorbs a capture that straddled
	// a commit boundary. Repeat requests for an already-seen count are always
	// allowed (they are snapshot-cache hits and cost nothing to serve).
	maxDistinctSnapshotCountsPerValidator = 3

	// snapshotLimiterIdleTTL evicts idle per-validator entries. It comfortably
	// covers the validation retry window (~14 minutes).
	snapshotLimiterIdleTTL = 2 * time.Hour

	// snapshotLimiterMaxEntries bounds limiter memory against many validators
	// and stages. Oldest entries are evicted first.
	snapshotLimiterMaxEntries = 8192
)

type snapshotLimiterEntry struct {
	counts   map[uint32]struct{}
	lastSeen time.Time
}

// snapshotCountLimiter tracks the distinct snapshot counts each validator has
// requested per (stage, model) store. It exists to stop a registered validator
// from forcing repeated snapshot-tree rebuilds by cycling through many valid
// flush-boundary counts. It deliberately does not limit request volume for
// counts already seen: those are served from the snapshot cache.
type snapshotCountLimiter struct {
	mu      sync.Mutex
	entries map[string]*snapshotLimiterEntry
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func newSnapshotCountLimiter() *snapshotCountLimiter {
	return &snapshotCountLimiter{
		entries: make(map[string]*snapshotLimiterEntry),
		max:     maxDistinctSnapshotCountsPerValidator,
		ttl:     snapshotLimiterIdleTTL,
		now:     time.Now,
	}
}

// Allow reports whether the validator may request proofs against the given
// snapshot count of the (stage, model) store, recording the count if allowed.
// distinct is the number of distinct counts the validator has used after this
// request (including the rejected one when allowed is false), so callers can
// surface validators approaching the quota before it trips.
func (l *snapshotCountLimiter) Allow(validatorAddress string, stageHeight int64, modelID string, count uint32) (allowed bool, distinct int) {
	key := fmt.Sprintf("%s|%d|%s", validatorAddress, stageHeight, modelID)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneLocked(now)

	entry, ok := l.entries[key]
	if !ok {
		entry = &snapshotLimiterEntry{counts: make(map[uint32]struct{}, l.max)}
		l.entries[key] = entry
	}
	entry.lastSeen = now

	if _, seen := entry.counts[count]; seen {
		return true, len(entry.counts)
	}
	if len(entry.counts) >= l.max {
		return false, len(entry.counts) + 1
	}
	entry.counts[count] = struct{}{}
	return true, len(entry.counts)
}

func (l *snapshotCountLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.lastSeen) > l.ttl {
			delete(l.entries, key)
		}
	}
	// Hard cap as a memory backstop; evict oldest entries first.
	for len(l.entries) >= snapshotLimiterMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range l.entries {
			if oldestKey == "" || entry.lastSeen.Before(oldest) {
				oldestKey = key
				oldest = entry.lastSeen
			}
		}
		delete(l.entries, oldestKey)
	}
}
