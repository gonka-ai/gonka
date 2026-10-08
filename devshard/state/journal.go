package state

import (
	"fmt"

	"devshard/types"
)

// mutationJournal saves the old value of each changed key.
// Trial applies capture new values, then undo; commit restores the new values.
type mutationJournal struct {
	pre, post journalScalars

	inferences journalMap[uint64, *types.InferenceRecord]
	committed  journalMap[uint64, []byte]
	sealed     journalMap[uint64, uint64]
	hostStats  journalMap[uint32, *types.HostStats]
	warmKeys   journalMap[uint32, string]

	// kept is set when the apply succeeded. closeJournalLocked undoes an
	// apply that returned without setting it.
	kept bool
}

// journalScalars is every non-map field an apply can write.
type journalScalars struct {
	balance       uint64
	fees          uint64
	phase         types.SessionPhase
	finalizeNonce uint64
	latestNonce   uint64
	sealedAcc     []byte
}

// SealedAcc is always replaced with a new slice and never written in place,
// so keeping the slice header is enough.
func (sm *StateMachine) readScalarsLocked() journalScalars {
	st := sm.state
	return journalScalars{
		balance:       st.Balance,
		fees:          st.Fees,
		phase:         st.Phase,
		finalizeNonce: st.FinalizeNonce,
		latestNonce:   st.LatestNonce,
		sealedAcc:     st.SealedAcc,
	}
}

func (sm *StateMachine) writeScalarsLocked(s journalScalars) {
	st := sm.state
	st.Balance = s.balance
	st.Fees = s.fees
	st.Phase = s.phase
	st.FinalizeNonce = s.finalizeNonce
	st.LatestNonce = s.latestNonce
	st.SealedAcc = s.sealedAcc
}

type journalSlot[V any] struct {
	v  V
	ok bool
}

// journalMap holds, per touched key, the value before the apply and, once
// detached, the value after it. ok=false means the key was absent.
type journalMap[K comparable, V any] struct {
	pre  map[K]journalSlot[V]
	post map[K]journalSlot[V]
}

func (m *journalMap[K, V]) seen(k K) bool {
	_, ok := m.pre[k]
	return ok
}

// touch saves live[k] the first time k is written in this apply.
func (m *journalMap[K, V]) touch(live map[K]V, k K) {
	if m.seen(k) {
		return
	}
	if m.pre == nil {
		m.pre = make(map[K]journalSlot[V])
	}
	v, ok := live[k]
	m.pre[k] = journalSlot[V]{v: v, ok: ok}
}

func (m *journalMap[K, V]) undo(live map[K]V) {
	for k, s := range m.pre {
		if s.ok {
			live[k] = s.v
		} else {
			delete(live, k)
		}
	}
}

func (m *journalMap[K, V]) capture(live map[K]V) {
	m.post = make(map[K]journalSlot[V], len(m.pre))
	for k := range m.pre {
		v, ok := live[k]
		m.post[k] = journalSlot[V]{v: v, ok: ok}
	}
}

func (m *journalMap[K, V]) redo(live map[K]V) {
	for k, s := range m.post {
		if s.ok {
			live[k] = s.v
		} else {
			delete(live, k)
		}
	}
}

// beginJournalLocked opens the journal for one apply. Every return after it
// must go through closeJournalLocked. Caller must hold sm.mu.
func (sm *StateMachine) beginJournalLocked() (*mutationJournal, error) {
	if sm.journal != nil {
		return nil, fmt.Errorf("state: apply journal already open")
	}
	j := &mutationJournal{pre: sm.readScalarsLocked()}
	sm.journal = j
	return j, nil
}

// closeJournalLocked stops recording. An apply that did not set kept is undone.
func (sm *StateMachine) closeJournalLocked(j *mutationJournal) {
	if sm.journal == j {
		sm.journal = nil
	}
	if !j.kept {
		sm.undoJournalLocked(j)
	}
}

func (sm *StateMachine) undoJournalLocked(j *mutationJournal) {
	j.inferences.undo(sm.state.Inferences)
	j.committed.undo(sm.committedEntries)
	j.sealed.undo(sm.sealedNonces)
	j.hostStats.undo(sm.state.HostStats)
	j.warmKeys.undo(sm.state.WarmKeys)
	sm.writeScalarsLocked(j.pre)
}

// detachJournalLocked records the post-state of every touched key and then
// undoes the apply. The post records stay referenced by the journal only.
func (sm *StateMachine) detachJournalLocked(j *mutationJournal) {
	j.post = sm.readScalarsLocked()
	j.inferences.capture(sm.state.Inferences)
	j.committed.capture(sm.committedEntries)
	j.sealed.capture(sm.sealedNonces)
	j.hostStats.capture(sm.state.HostStats)
	j.warmKeys.capture(sm.state.WarmKeys)
	sm.undoJournalLocked(j)
}

// redoJournalLocked installs a detached journal's post-state. The live state
// must be the pre-state the journal was recorded against.
func (sm *StateMachine) redoJournalLocked(j *mutationJournal) {
	if len(j.sealed.post) > 0 && sm.sealedNonces == nil {
		sm.sealedNonces = make(map[uint64]uint64, len(j.sealed.post))
	}
	j.inferences.redo(sm.state.Inferences)
	j.committed.redo(sm.committedEntries)
	j.sealed.redo(sm.sealedNonces)
	j.hostStats.redo(sm.state.HostStats)
	j.warmKeys.redo(sm.state.WarmKeys)
	sm.writeScalarsLocked(j.post)
}

// inferenceForWriteLocked returns the live record for id, ready to mutate. On
// the first write in an apply the journal keeps the old record and the live
// map gets a shallow copy. PromptHash and ResponseHash stay shared: writes
// replace those slices and never write into them.
func (sm *StateMachine) inferenceForWriteLocked(id uint64) (*types.InferenceRecord, bool) {
	rec, ok := sm.state.Inferences[id]
	if !ok {
		return nil, false
	}
	j := sm.journal
	if j == nil || j.inferences.seen(id) {
		return rec, true
	}
	j.inferences.touch(sm.state.Inferences, id)
	cp := *rec
	sm.state.Inferences[id] = &cp
	return &cp, true
}

func (sm *StateMachine) putInferenceLocked(id uint64, rec *types.InferenceRecord) {
	if j := sm.journal; j != nil {
		j.inferences.touch(sm.state.Inferences, id)
	}
	sm.state.Inferences[id] = rec
}

func (sm *StateMachine) deleteInferenceLocked(id uint64) {
	if j := sm.journal; j != nil {
		j.inferences.touch(sm.state.Inferences, id)
	}
	delete(sm.state.Inferences, id)
}

// hostStatsForWriteLocked is inferenceForWriteLocked for one host-stats slot.
func (sm *StateMachine) hostStatsForWriteLocked(slot uint32) *types.HostStats {
	hs := sm.state.HostStats[slot]
	j := sm.journal
	if j == nil || hs == nil || j.hostStats.seen(slot) {
		return hs
	}
	j.hostStats.touch(sm.state.HostStats, slot)
	cp := *hs
	sm.state.HostStats[slot] = &cp
	return &cp
}

func (sm *StateMachine) setSealedNonceLocked(id, nonce uint64) {
	if sm.sealedNonces == nil {
		sm.sealedNonces = make(map[uint64]uint64)
	}
	if j := sm.journal; j != nil {
		j.sealed.touch(sm.sealedNonces, id)
	}
	sm.sealedNonces[id] = nonce
}

func (sm *StateMachine) setWarmKeyLocked(slot uint32, addr string) {
	if j := sm.journal; j != nil {
		j.warmKeys.touch(sm.state.WarmKeys, slot)
	}
	sm.state.WarmKeys[slot] = addr
}
