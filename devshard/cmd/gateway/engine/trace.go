package engine

import "time"

// RaceStepKind names the lifecycle step a race reports to its journal.
type RaceStepKind uint8

const (
	RaceStepNonceCommitted RaceStepKind = iota + 1
	RaceStepEscalationUnfilled
	RaceStepAttemptCrowned
	RaceStepAttemptFinished
	RaceStepNonceStranded
	RaceStepHostBlocked
	RaceStepHostRewound
	RaceStepWinnerFinishLanded
)

// RaceStep is one lifecycle step copied at emit on the coordinator goroutine. See README, "Boundaries".
type RaceStep struct {
	Kind          RaceStepKind
	RequestID     string
	EscrowID      string
	Nonce         uint64
	Participant   string
	Slot          int
	Role          string
	Reason        string
	Attempts      int
	Err           error
	Rewound       bool
	Terminal      Terminal
	NonceFinished bool
	HasOutcome    bool
	PhaseAborted  bool
	Outcome       AttemptOutcome
	FinishLag     time.Duration
}

// raceJournal is satisfied by *journal.Journal; RecordStep returns without waiting on a sink.
type raceJournal interface {
	RecordStep(step RaceStep)
	crownNarrator
}

func (c *raceCoordinator) traceStep(step RaceStep) {
	if c.deps.Journal != nil {
		c.deps.Journal.RecordStep(step)
	}
}
