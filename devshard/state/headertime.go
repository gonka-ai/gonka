package state

import (
	"bytes"
	"time"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

type HeaderTimeSource interface {
	HeaderTimeMs(height uint64, hash []byte) (int64, error)
}

// HeaderTimeWatch is a header map that reports when a time is stored.
// The chat gate waits on that signal instead of polling the sequencer lock.
type HeaderTimeWatch interface {
	Watch(fn func()) (cancel func())
}

// HeaderAckView is the polled map as a height ack sees it.
// StoredHash is a row already stored. A miss means this process has not
// polled that height yet. HostAckHash is a hash already accepted from a host.
// NoteHostAck records an accepted host hash. A later poll with a different
// hash is logged; a later version should open a dispute.
type HeaderAckView interface {
	StoredHash(height uint64) ([]byte, bool)
	HostAckHash(height uint64) ([]byte, bool)
	NoteHostAck(height uint64, hash []byte)
}

var fallbackHeaderTimes HeaderTimeSource

func SetFallbackHeaderTimes(src HeaderTimeSource) {
	fallbackHeaderTimes = src
}

// VerifierNow is the wall clock a timeout verifier compares with the map.
func (sm *StateMachine) VerifierNow() time.Time { return time.Now() }

// HasHeaderFeed reports whether this machine reads a stored header-time map.
// A fallback clock that only answers HeaderTimeMs is not that map.
func (sm *StateMachine) HasHeaderFeed() bool {
	if sm == nil {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.storedHeadersLocked()
	return ok
}

// StampHeaderTime is the stored time for a stamp. A miss does not call out.
// An empty hash matches the row at that height. A hash that differs is a miss.
func (sm *StateMachine) StampHeaderTime(height uint64, hash []byte) (int64, bool) {
	if sm == nil || height == 0 {
		return 0, false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	src, ok := sm.storedHeadersLocked()
	if !ok {
		return 0, false
	}
	row, ok := src.StoredAt(height)
	if !ok || row.TimeMs <= 0 {
		return 0, false
	}
	if len(hash) > 0 && !bytes.Equal(row.Hash, hash) {
		return 0, false
	}
	return row.TimeMs, true
}

// TimeoutInterval is the refusal or execution window from the session config.
func (sm *StateMachine) TimeoutInterval(reason types.TimeoutReason) time.Duration {
	if sm == nil {
		return 0
	}
	seconds := sm.Config().RefusalTimeout
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		seconds = sm.Config().ExecutionTimeout
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

type storedHeaderReader interface {
	StoredAt(height uint64) (headertime.Record, bool)
}

// storedHeadersLocked returns the non-fetching map reader. Caller holds sm.mu
// (or accepts a torn pointer on SetHeaderTimes); do not call HeaderTimes here —
// that takes RLock and deadlocks under apply.
func (sm *StateMachine) storedHeadersLocked() (storedHeaderReader, bool) {
	if sm == nil {
		return nil, false
	}
	src, ok := sm.headerTimes.(storedHeaderReader)
	if !ok || src == nil {
		return nil, false
	}
	return src, true
}

// headerCatchUp is a map that can fill a gap from a height toward the tip
// without blocking the caller. *headertime.Source implements it.
type headerCatchUp interface {
	EnsureFrom(height uint64)
}

// LowestUnfinishedStartHeight is the smallest StartedAtHeight among live
// inferences that are still Pending, Started, or Challenged. Sealed records
// are already gone from Inferences. Zero means none, or every live record
// carried no start height.
func (sm *StateMachine) LowestUnfinishedStartHeight() uint64 {
	if sm == nil {
		return 0
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var lowest uint64
	for _, rec := range sm.state.Inferences {
		if rec == nil {
			continue
		}
		switch rec.Status {
		case types.StatusPending, types.StatusStarted, types.StatusChallenged:
		default:
			continue
		}
		if rec.StartedAtHeight == 0 {
			continue
		}
		if lowest == 0 || rec.StartedAtHeight < lowest {
			lowest = rec.StartedAtHeight
		}
	}
	return lowest
}

// WarmUnfinishedHeaders asks the header map to catch up from the lowest
// unfinished start height. It does not wait. A map that cannot catch up is
// a no-op.
func (sm *StateMachine) WarmUnfinishedHeaders() {
	height := sm.LowestUnfinishedStartHeight()
	if height == 0 {
		return
	}
	sm.mu.RLock()
	src, ok := sm.headerTimes.(headerCatchUp)
	sm.mu.RUnlock()
	if !ok || src == nil {
		return
	}
	src.EnsureFrom(height)
}

