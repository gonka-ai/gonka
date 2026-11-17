package registry

import (
	"errors"
	"fmt"
	"maps"
	"sync/atomic"
	"time"

	"devshard/types"
)

// escrowEntry is one escrow's live state; inFlight is shared by every published set the entry appears in.
type escrowEntry struct {
	id           string
	sessionID    uint64
	model        string
	session      EscrowSession
	slots        map[string]int
	participants []string
	stream       nonceStream
	inFlight     atomic.Int64
	holds        atomic.Uint64
	isReserve    atomic.Bool
	hold         func() (func(), bool)
	rescueHold   func() (func(), bool)
	openRecords  openRecordIndex

	settlementHolds atomic.Int64
	sweepHolds      atomic.Int64
	closeClaimed    bool
	// nonceCapped is set under the registry lock as the entry leaves routing for its nonce cap.
	nonceCapped bool
}

func newEscrowEntry(escrowID, model string, sessionID uint64, session EscrowSession, now func() time.Time) *escrowEntry {
	slots := slotCounts(session.HostParticipantKeyList())
	return &escrowEntry{
		id:           escrowID,
		sessionID:    sessionID,
		model:        model,
		session:      session,
		slots:        slots,
		participants: sortedKeys(slots),
		stream:       newNonceStream(session, model, now),
	}
}

func (e *escrowEntry) accepting() bool { return e.session.Phase() == types.PhaseActive }

func (e *escrowEntry) busy() bool { return e.inFlight.Load() > 0 }

func (e *escrowEntry) busyOrSwept() bool { return e.busy() || e.sweepHolds.Load() > 0 }

// close flushes before releasing storage and reports whether the store was released. See routing.md, "The escrow registry".
func (e *escrowEntry) close() (bool, error) {
	var flushErr error
	if err := e.session.FlushSnapshot(); err != nil {
		flushErr = fmt.Errorf("flushing escrow %s: %w", e.id, err)
	}
	closeErr := e.session.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("closing escrow %s: %w", e.id, closeErr)
	}
	return closeErr == nil, errors.Join(flushErr, closeErr)
}

// liveSet is replaced rather than mutated, so readers take it with one atomic load. See routing.md, "The escrow registry".
type liveSet struct {
	byID    map[string]*escrowEntry
	byModel map[string][]*escrowEntry
	ordered []*escrowEntry
}

func emptyLiveSet() *liveSet { return newLiveSet(map[string]*escrowEntry{}) }

func (s *liveSet) with(entry *escrowEntry) *liveSet {
	byID := maps.Clone(s.byID)
	byID[entry.id] = entry
	return newLiveSet(byID)
}

func (s *liveSet) without(escrowID string) *liveSet {
	byID := maps.Clone(s.byID)
	delete(byID, escrowID)
	return newLiveSet(byID)
}

// newLiveSet orders each model's candidates, and the full set, by escrow id: the stable order the tie-break assumes.
func newLiveSet(byID map[string]*escrowEntry) *liveSet {
	set := &liveSet{
		byID:    byID,
		byModel: make(map[string][]*escrowEntry, len(byID)),
		ordered: make([]*escrowEntry, 0, len(byID)),
	}
	for _, id := range sortedKeys(byID) {
		entry := byID[id]
		set.byModel[entry.model] = append(set.byModel[entry.model], entry)
		set.ordered = append(set.ordered, entry)
	}
	return set
}
