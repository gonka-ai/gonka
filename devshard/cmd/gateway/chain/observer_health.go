package chain

import "sync"

// snapshotHealth remembers the last publish's health so a five-second poll speaks only on the turn.
type snapshotHealth struct {
	mu       sync.Mutex
	degraded bool
}

// healthChange separates the decision from the narration, so narrating nothing while a failure persists is testable.
type healthChange struct {
	degraded  bool
	recovered bool
}

func (h *snapshotHealth) advance(lastError string) healthChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	failed := lastError != ""
	change := healthChange{degraded: failed && !h.degraded, recovered: !failed && h.degraded}
	h.degraded = failed
	return change
}

// joinSnapshotError keeps every failed read of one poll. See README.md, "The poll loop".
func joinSnapshotError(existing, added string) string {
	switch {
	case added == "":
		return existing
	case existing == "":
		return added
	}
	return existing + "; " + added
}

// narrateHealth carries the cause the health gauge cannot: LastError names which of four reads failed.
func (o *PhaseObserver) narrateHealth(snapshot PhaseSnapshot) {
	change := o.health.advance(snapshot.LastError)
	if o.narrator == nil {
		return
	}
	switch {
	case change.degraded:
		o.narrator.ChainSnapshotStale(snapshot.LastError, snapshot.EpochIndex, snapshot.BlockHeight)
	case change.recovered:
		o.narrator.ChainSnapshotRecovered(snapshot.EpochIndex, snapshot.BlockHeight)
	}
}
