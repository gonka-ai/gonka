package state

import (
	"devshard/observability"
	"devshard/types"
)

// OwedValidationPredicate reports whether id is still a validation obligation
// for the host attached to this state machine. Called under sm.mu; it must
// not lock the machine. validationRate is the session config value.
type OwedValidationPredicate func(id uint64, rec *types.InferenceRecord, validationRate uint32) bool

// SetOwedValidationPredicate installs the host's obligation rule and rebuilds
// the set from the live inference map. A nil predicate clears the set. One
// host owns a machine: a later call replaces the rule.
func (sm *StateMachine) SetOwedValidationPredicate(p OwedValidationPredicate) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.owedPredicate = p
	sm.rebuildOwedLocked()
}

// OwedValidationIDs returns a copy of the obligation set. The order is the
// map order.
func (sm *StateMachine) OwedValidationIDs() []uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.owed) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(sm.owed))
	for id := range sm.owed {
		ids = append(ids, id)
	}
	return ids
}

// syncOwedLocked inserts or drops id to match the predicate. Caller must hold
// sm.mu. A membership change during an apply is journaled with the inference
// write, so undo and redo restore the set.
func (sm *StateMachine) syncOwedLocked(id uint64) {
	if sm.owedPredicate == nil {
		return
	}
	rec := sm.state.Inferences[id]
	want := rec != nil && sm.owedPredicate(id, rec, sm.state.Config.ValidationRate)
	_, have := sm.owed[id]
	if want == have {
		return
	}
	if j := sm.journal; j != nil {
		j.owed.touch(sm.owed, id)
	}
	if sm.owed == nil {
		sm.owed = make(map[uint64]struct{})
	}
	if want {
		sm.owed[id] = struct{}{}
	} else {
		delete(sm.owed, id)
	}
	// Inside an apply the size is published once the apply is kept or redone,
	// so trial and rejected applies never move the gauge.
	if sm.journal == nil {
		sm.publishOwedLocked()
	}
}

// publishOwedIfTouchedLocked publishes the set size after a kept apply that
// changed membership.
func (sm *StateMachine) publishOwedIfTouchedLocked(j *mutationJournal) {
	if j != nil && len(j.owed.pre) > 0 {
		sm.publishOwedLocked()
	}
}

// rebuildOwedLocked replaces the set with a scan of the live map. Used when
// the predicate is installed and when restore swaps the inference map in
// whole. Caller must hold sm.mu. Not for use while a journal is open: it
// replaces the map the journal would undo into.
func (sm *StateMachine) rebuildOwedLocked() {
	if sm.owedPredicate == nil {
		sm.owed = nil
		sm.publishOwedLocked()
		return
	}
	rate := sm.state.Config.ValidationRate
	next := make(map[uint64]struct{})
	for id, rec := range sm.state.Inferences {
		if rec != nil && sm.owedPredicate(id, rec, rate) {
			next[id] = struct{}{}
		}
	}
	sm.owed = next
	sm.publishOwedLocked()
}

func (sm *StateMachine) publishOwedLocked() {
	if sm.owedPredicate == nil || sm.state == nil || sm.state.EscrowID == "" {
		return
	}
	observability.SetValidationOwed(sm.state.EscrowID, len(sm.owed))
}
