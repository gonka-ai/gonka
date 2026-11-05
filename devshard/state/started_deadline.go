package state

import (
	"time"

	"devshard/types"
)

// StartedInferencesPastDeadline returns the ids of records the chain still settles and no race has
// claimed: started and past their own execution deadline by buffer, counted from the executor's stamp, or
// from the start when the executor stamped none. A record from racesFrom on also waits out raceGrace, the
// time its own race may still take to vote; one below it has no race left to wait for. At most budget ids
// come back, the longest overdue first, so a backlog larger than the budget drains oldest first instead of
// leaving an unlucky record behind; a scan that finds nothing allocates nothing.
func (sm *StateMachine) StartedInferencesPastDeadline(now time.Time, buffer, raceGrace time.Duration, racesFrom uint64, budget int) []uint64 {
	if budget <= 0 {
		return nil
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	window := time.Duration(sm.state.Config.ExecutionTimeout)*time.Second + buffer
	var due []overdueRecord
	for id, record := range sm.state.Inferences {
		if record.Status != types.StatusStarted {
			continue
		}
		anchor := record.ConfirmedAt
		if anchor <= 0 {
			anchor = record.StartedAt
		}
		deadline := time.Unix(anchor, 0).Add(window)
		if id >= racesFrom {
			deadline = deadline.Add(raceGrace)
		}
		if now.Before(deadline) {
			continue
		}
		if due == nil {
			due = make([]overdueRecord, 0, budget)
		}
		due = keepMostOverdue(due, overdueRecord{id: id, deadline: deadline}, budget)
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
	id       uint64
	deadline time.Time
}

func (r overdueRecord) before(other overdueRecord) bool {
	if r.deadline.Equal(other.deadline) {
		return r.id < other.id
	}
	return r.deadline.Before(other.deadline)
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
