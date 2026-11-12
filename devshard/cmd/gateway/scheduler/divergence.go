package scheduler

import (
	"sync"
	"time"
)

// replayCredit holds the one catch-up replay a participant gets on an escrow. See README, "Divergence and the replay credit".
type replayCredit struct {
	mu    sync.Mutex
	spent map[string]map[string]time.Time
}

// spend reports whether the participant still had its replay, and takes it if so.
func (c *replayCredit) spend(escrowID, participant string, at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	byParticipant, known := c.spent[escrowID]
	if !known {
		byParticipant = map[string]time.Time{}
		if c.spent == nil {
			c.spent = map[string]map[string]time.Time{}
		}
		c.spent[escrowID] = byParticipant
	}
	if _, taken := byParticipant[participant]; taken {
		return false
	}
	byParticipant[participant] = at
	return true
}

// restore returns the replay only for a send that started after it was taken: overlapping sends prove nothing.
func (c *replayCredit) restore(escrowID, participant string, sentAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	byParticipant := c.spent[escrowID]
	takenAt, taken := byParticipant[participant]
	if !taken || !sentAt.After(takenAt) {
		return
	}
	delete(byParticipant, participant)
	if len(byParticipant) == 0 {
		delete(c.spent, escrowID)
	}
}

func (c *replayCredit) forget(escrowID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.spent, escrowID)
}
