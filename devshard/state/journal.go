package state

import (
	"fmt"

	"github.com/gtank/ristretto255"

	"devshard/heightsync"
	"devshard/types"
)

// mutationJournal records the inverse of every write one apply makes. Apply
// mutates the live maps in place. Before the first write to a key, the journal
// saves what that key held, so undo restores only the keys the diff touched.
// Untouched records, committed blobs, and sealed nonces are never copied.
//
// Records and host stats are copied on first write: the journal keeps the old
// pointer and the live map gets a shallow copy. Undo puts the old pointer
// back. A record inserted in this apply is new, so later writes in the same
// diff go to it directly.
//
// A trial apply (ValidateDiff, PreviewLocalBestEffort) detaches the journal:
// it records the post value of every touched key, then undoes. CommitValidated
// redoes the journal by writing those post values back.
type mutationJournal struct {
	pre, post journalScalars

	inferences journalMap[uint64, *types.InferenceRecord]
	committed  journalMap[uint64, []byte]
	sealed     journalMap[uint64, uint64]
	// owed is the host-local validation-obligation set. It is not part of
	// post_state_root; it is journaled so a rejected or trial apply cannot
	// leave a stale id behind.
	owed      journalMap[uint64, struct{}]
	hostStats journalMap[uint32, *types.HostStats]
	warmKeys  journalMap[uint32, string]

	trackerCopied bool
	floorCopied   bool
	// kept is set when the apply succeeded. closeJournalLocked undoes an
	// apply that returned without setting it.
	kept bool
}

// journalScalars is every non-map field an apply can write. The tracker and
// floor are pointers: observeHeightSyncLocked replaces them with a clone
// before the first write, so the saved pointer is the pre-state.
type journalScalars struct {
	balance       uint64
	fees          uint64
	phase         types.SessionPhase
	finalizeNonce uint64
	latestNonce   uint64
	liveEntrySum  ristretto255.Element
	sealedAcc     []byte

	hsForcedStart         uint64
	hsForcedEnd           uint64
	hsCadenceSwallowUntil uint64
	hsSwallowFe           uint64
	hsTurnK               uint64
	hsTurnSlots           uint64
	hsTurnReason          string
	hsLastCompletedHeight uint64
	hsLatestTurnStart     uint64

	turnTracker     *heightsync.TurnTracker
	heightSyncFloor *heightsync.FloorIndex
}

// SealedAcc is always replaced with a new slice and never written in place,
// so keeping the slice header is enough.
func (sm *StateMachine) readScalarsLocked() journalScalars {
	st := sm.state
	return journalScalars{
		balance:               st.Balance,
		fees:                  st.Fees,
		phase:                 st.Phase,
		finalizeNonce:         st.FinalizeNonce,
		latestNonce:           st.LatestNonce,
		liveEntrySum:          sm.liveEntrySum,
		sealedAcc:             st.SealedAcc,
		hsForcedStart:         st.HeightSyncForcedStart,
		hsForcedEnd:           st.HeightSyncForcedEnd,
		hsCadenceSwallowUntil: st.HeightSyncCadenceSwallowUntil,
		hsSwallowFe:           st.HeightSyncSwallowFe,
		hsTurnK:               st.HeightSyncTurnK,
		hsTurnSlots:           st.HeightSyncTurnSlots,
		hsTurnReason:          st.HeightSyncTurnReason,
		hsLastCompletedHeight: st.HeightSyncLastCompletedHeight,
		hsLatestTurnStart:     st.HeightSyncLatestTurnStart,
		turnTracker:           sm.turnTracker,
		heightSyncFloor:       sm.heightSyncFloor,
	}
}

func (sm *StateMachine) writeScalarsLocked(s journalScalars) {
	st := sm.state
	st.Balance = s.balance
	st.Fees = s.fees
	st.Phase = s.phase
	st.FinalizeNonce = s.finalizeNonce
	st.LatestNonce = s.latestNonce
	sm.liveEntrySum = s.liveEntrySum
	st.SealedAcc = s.sealedAcc
	st.HeightSyncForcedStart = s.hsForcedStart
	st.HeightSyncForcedEnd = s.hsForcedEnd
	st.HeightSyncCadenceSwallowUntil = s.hsCadenceSwallowUntil
	st.HeightSyncSwallowFe = s.hsSwallowFe
	st.HeightSyncTurnK = s.hsTurnK
	st.HeightSyncTurnSlots = s.hsTurnSlots
	st.HeightSyncTurnReason = s.hsTurnReason
	st.HeightSyncLastCompletedHeight = s.hsLastCompletedHeight
	st.HeightSyncLatestTurnStart = s.hsLatestTurnStart
	sm.turnTracker = s.turnTracker
	sm.heightSyncFloor = s.heightSyncFloor
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
	sm.ensureOwedMapLocked(j.owed.pre)
	j.owed.undo(sm.owed)
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
	j.owed.capture(sm.owed)
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
	sm.ensureOwedMapLocked(j.owed.post)
	j.owed.redo(sm.owed)
	j.hostStats.redo(sm.state.HostStats)
	j.warmKeys.redo(sm.state.WarmKeys)
	sm.writeScalarsLocked(j.post)
	sm.publishOwedIfTouchedLocked(j)
}

// ensureOwedMapLocked allocates the owed set before undo or redo writes a key
// that was present. delete on a nil map is a no-op; assigning to one panics.
func (sm *StateMachine) ensureOwedMapLocked(slots map[uint64]journalSlot[struct{}]) {
	if sm.owed != nil {
		return
	}
	for _, s := range slots {
		if s.ok {
			sm.owed = make(map[uint64]struct{}, len(slots))
			return
		}
	}
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
	sm.syncOwedLocked(id)
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

// journalHeightSyncLocked clones the tracker before Observe writes it, and
// the floor when this diff carries a claim that can raise it. FloorIndex
// ignores a diff with no claims.
func (sm *StateMachine) journalHeightSyncLocked(txs []*types.DevshardTx) {
	j := sm.journal
	if j == nil {
		return
	}
	if !j.trackerCopied {
		sm.turnTracker = sm.turnTracker.Clone()
		j.trackerCopied = true
	}
	if !j.floorCopied && len(floorClaims(sm.state, txs)) > 0 {
		sm.heightSyncFloor = sm.heightSyncFloor.Clone()
		j.floorCopied = true
	}
}
