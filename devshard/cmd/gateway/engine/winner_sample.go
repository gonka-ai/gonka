package engine

import "sync"

type heldWinnerKey struct {
	escrowID string
	nonce    uint64
}

type heldWinner struct {
	outcome RaceOutcome
	attempt AttemptOutcome
}

type heldWinners struct {
	mu      sync.Mutex
	winners map[heldWinnerKey]heldWinner
}

func newHeldWinners() *heldWinners {
	return &heldWinners{winners: map[heldWinnerKey]heldWinner{}}
}

func (h *heldWinners) hold(key heldWinnerKey, winner heldWinner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.winners[key] = winner
}

func (h *heldWinners) take(key heldWinnerKey) (heldWinner, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	winner, held := h.winners[key]
	delete(h.winners, key)
	return winner, held
}

// OnFinishApplied decides the served winner held for this nonce's finish as finished. See race.md, "A served winner's sample waits for its finish".
func (e *Engine) OnFinishApplied(escrowID string, nonce uint64) {
	if winner, held := e.heldWinners.take(heldWinnerKey{escrowID: escrowID, nonce: nonce}); held {
		e.decideWinner(winner, true)
	}
}

func (e *Engine) recordSample(outcome RaceOutcome, attempt AttemptOutcome, registration *raceRegistration) {
	if !outcome.FinishAwaited(attempt) {
		e.recordJudgedSample(outcome, attempt)
		return
	}
	e.heldWinners.hold(heldWinnerKey{escrowID: outcome.EscrowID, nonce: attempt.Nonce}, heldWinner{outcome: outcome, attempt: attempt})
	if registration.nonceFinished(attempt.Nonce) {
		e.OnFinishApplied(outcome.EscrowID, attempt.Nonce)
	}
}

func (e *Engine) recordJudgedSample(outcome RaceOutcome, attempt AttemptOutcome) {
	if sample, exemption := outcome.Sample(attempt); exemption == SampleRecorded {
		e.deps.Perf.RecordSample(sample)
	}
}

func (e *Engine) decideHeldWinners(escrowID string, steps []TimeoutStep, registration *raceRegistration) {
	for _, step := range steps {
		if winner, held := e.heldWinners.take(heldWinnerKey{escrowID: escrowID, nonce: step.Nonce}); held {
			e.decideWinner(winner, registration.nonceFinished(step.Nonce))
		}
	}
}

func (e *Engine) decideWinner(winner heldWinner, finished bool) {
	winner.attempt.NonceFinished = finished
	e.recordJudgedSample(winner.outcome, winner.attempt)
	if e.deps.Metrics != nil {
		e.deps.Metrics.RecordAwaitedFinish(winner.outcome, winner.attempt)
	}
	if finished && e.deps.Journal != nil {
		e.deps.Journal.RecordStep(RaceStep{
			Kind: RaceStepWinnerFinishLanded, RequestID: winner.outcome.RequestID, EscrowID: winner.outcome.EscrowID,
			Nonce: winner.attempt.Nonce, Participant: winner.attempt.Participant,
			FinishLag: e.settles.now().Sub(winner.attempt.Completed),
		})
	}
}
