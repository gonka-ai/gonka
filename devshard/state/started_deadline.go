package state

import (
	"time"

	"devshard/types"
)

// StartedInferencesPastDeadline returns the ids of records the chain still settles and no race has
// claimed: started and past their own execution deadline by buffer, measured in block time from the
// record's ConfirmedAtTimeMs as TimeoutRemaining does. A record without one, and every record
// while the block time of the session's floor is unknown, is not due. A record from racesFrom on also
// waits out raceGrace, the time its own race may still take to vote; one below it has no race left to
// wait for. At most budget ids come back, the longest overdue first, so a backlog larger than the budget
// drains oldest first instead of leaving an unlucky record behind; a scan that finds nothing allocates
// nothing.
func (sm *StateMachine) StartedInferencesPastDeadline(buffer, raceGrace time.Duration, racesFrom uint64, budget int) []uint64 {
	if budget <= 0 {
		return nil
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	floor, err := sm.floorPointLocked(sm.state.LatestNonce + 1)
	if err != nil {
		return nil
	}
	nowMs, err := sm.floorTimeMsLocked(floor)
	if err != nil {
		return nil
	}
	var due []overdueRecord
	for id, record := range sm.state.Inferences {
		if record.Status != types.StatusStarted {
			continue
		}
		anchorMs, windowMs, err := sm.timeoutWindowLocked(record, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
		if err != nil {
			continue
		}
		deadlineMs := anchorMs + windowMs + buffer.Milliseconds()
		if id >= racesFrom {
			deadlineMs += raceGrace.Milliseconds()
		}
		if nowMs < deadlineMs {
			continue
		}
		if due == nil {
			due = make([]overdueRecord, 0, budget)
		}
		due = keepMostOverdue(due, overdueRecord{id: id, deadlineMs: deadlineMs}, budget)
	}
	if due == nil {
		return nil
	}
	ids := make([]uint64, len(due))
	for index, record := range due {
		ids[index] = record.id
	}
	return ids
}

type overdueRecord struct {
	id         uint64
	deadlineMs int64
}

func (r overdueRecord) before(other overdueRecord) bool {
	if r.deadlineMs == other.deadlineMs {
		return r.id < other.id
	}
	return r.deadlineMs < other.deadlineMs
}

// keepMostOverdue inserts candidate into kept, sorted by deadline, and drops whatever falls past budget.
func keepMostOverdue(kept []overdueRecord, candidate overdueRecord, budget int) []overdueRecord {
	if len(kept) == budget && !candidate.before(kept[len(kept)-1]) {
		return kept
	}
	position := len(kept)
	for position > 0 && candidate.before(kept[position-1]) {
		position--
	}
	if len(kept) < budget {
		kept = append(kept, overdueRecord{})
	}
	copy(kept[position+1:], kept[position:len(kept)-1])
	kept[position] = candidate
	return kept
}
