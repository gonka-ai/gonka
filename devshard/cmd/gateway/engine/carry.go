package engine

import (
	"bytes"
	"sync"
	"sync/atomic"

	"devshard/cmd/gateway/config"
)

// carryBudget bounds bytes held per attempt, participant and gateway: the SSE reassembly tail and the crown prefix each have one. See race.md, "Classification and reassembly".
type carryBudget struct {
	attemptLimit     int64
	participantLimit int64
	globalLimit      int64

	global       atomic.Int64
	mu           sync.Mutex
	participants map[string]*atomic.Int64
}

func newCarryBudget(stream config.Stream) *carryBudget {
	return &carryBudget{
		attemptLimit:     stream.ClassifyMaxAttemptBytes,
		participantLimit: stream.ClassifyMaxParticipantBytes,
		globalLimit:      stream.ClassifyMaxGlobalBytes,
		participants:     map[string]*atomic.Int64{},
	}
}

// counterFor creates a participant's counter on first use and never removes it. See rules.md, "9. Bounded by construction".
func (b *carryBudget) counterFor(participant string) *atomic.Int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	counter := b.participants[participant]
	if counter == nil {
		counter = &atomic.Int64{}
		b.participants[participant] = counter
	}
	return counter
}

type budgetReservation int

const (
	budgetGranted budgetReservation = iota
	budgetParticipantFull
	budgetGlobalFull
)

// reserve charges the participant then the global pool, undoing the first when the second trips. See rules.md, "9. Bounded by construction".
func (b *carryBudget) reserve(participant *atomic.Int64, n int64) budgetReservation {
	if participant.Add(n) > b.participantLimit {
		participant.Add(-n)
		return budgetParticipantFull
	}
	if b.global.Add(n) > b.globalLimit {
		b.global.Add(-n)
		participant.Add(-n)
		return budgetGlobalFull
	}
	return budgetGranted
}

func (b *carryBudget) refund(participant *atomic.Int64, n int64) {
	if n <= 0 {
		return
	}
	participant.Add(-n)
	b.global.Add(-n)
}

// carryBuffer reassembles SSE events for one attempt and belongs to that attempt's goroutine alone.
type carryBuffer struct {
	budget      *carryBudget
	participant *atomic.Int64
	held        []byte
	dropped     bool
}

func newCarryBuffer(budget *carryBudget, participant string) *carryBuffer {
	return &carryBuffer{budget: budget, participant: budget.counterFor(participant)}
}

// Take returns every newline-terminated event and retains the rest; firstDrop is true only on the first cap trip. See README, "Classification and reassembly".
func (c *carryBuffer) Take(chunk []byte) (parseable []byte, firstDrop bool) {
	lastLineFeed := bytes.LastIndexByte(chunk, '\n')
	if lastLineFeed < 0 {
		if c.grow(chunk) {
			return nil, false
		}
		return chunk, c.noteDrop()
	}
	parseable = chunk[:lastLineFeed+1]
	if len(c.held) > 0 {
		joined := make([]byte, 0, len(c.held)+lastLineFeed+1)
		joined = append(joined, c.held...)
		joined = append(joined, chunk[:lastLineFeed+1]...)
		parseable = joined
	}
	if c.replace(chunk[lastLineFeed+1:]) {
		return parseable, false
	}
	return parseable, c.noteDrop()
}

// Tail is the unterminated final fragment, from which a newline-less last event is classified.
func (c *carryBuffer) Tail() []byte { return c.held }

func (c *carryBuffer) Release() {
	c.budget.refund(c.participant, int64(len(c.held)))
	c.held = nil
}

func (c *carryBuffer) grow(tail []byte) bool {
	if !c.charge(int64(len(tail))) {
		return false
	}
	c.held = append(c.held, tail...)
	return true
}

func (c *carryBuffer) replace(tail []byte) bool {
	delta := int64(len(tail) - len(c.held))
	if delta > 0 && !c.charge(delta) {
		return false
	}
	if delta < 0 {
		c.budget.refund(c.participant, -delta)
	}
	c.held = append(c.held[:0], tail...)
	return true
}

func (c *carryBuffer) charge(n int64) bool {
	if int64(len(c.held))+n > c.budget.attemptLimit || c.budget.reserve(c.participant, n) != budgetGranted {
		c.Release()
		return false
	}
	return true
}

func (c *carryBuffer) noteDrop() bool {
	if c.dropped {
		return false
	}
	c.dropped = true
	return true
}
