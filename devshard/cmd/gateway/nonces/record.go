package nonces

import (
	"errors"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/scheduler"
)

// NonceAssigned files the nonce and its request's prompt count at once, so an attempt sealed before its race reports keeps the count.
func (n *Recorder) NonceAssigned(escrowID string, nonce uint64, profile scheduler.RequestProfile) {
	if n == nil || nonce == 0 {
		return
	}
	n.reportFact(n.service.Book.RecordAssigned(escrowID, nonce, profile.RequestID))
	if profile.InputTokensSource.Tokenized() {
		n.reportFact(n.service.Book.RecordInputCount(escrowID, nonce, uint64(max(profile.InputTokens, 0)), profile.InputTokensSource))
	}
}

func (n *Recorder) RecordGhost(escrowID string, nonce uint64, reason string) {
	if n == nil || nonce == 0 {
		return
	}
	n.reportFact(n.service.Book.RecordGhost(escrowID, nonce, reason))
}

func (n *Recorder) RecordRace(outcome engine.RaceOutcome) {
	if n == nil {
		return
	}
	phase := accounting.PhaseNormal
	if outcome.PoCBypassActive {
		phase = accounting.PhasePoC
	}
	attempts := make([]accounting.Attempt, 0, len(outcome.Attempts))
	for _, attempt := range outcome.Attempts {
		attempts = append(attempts, accounting.Attempt{
			Nonce:             attempt.Nonce,
			RequestID:         outcome.RequestID,
			OutputTokens:      attempt.OutputTokens(),
			FirstContentDelay: attempt.FirstContentDelay(),
			Sent:              !attempt.SendTime.IsZero(),
			Acknowledged:      !attempt.ReceiptTime.IsZero(),
			Usage:             usageOf(outcome, attempt),
			Terminal:          terminalFor(outcome, attempt),
			Phase:             phase,
			SlowReceipt:       slowReceipt(attempt),
			SlowChunk:         attempt.MaxChunkGap > accounting.SlowChunkGap,
			ClockDrifted:      clockDrifted(attempt),
			SlowDecode:        engine.TimePerOutputToken(attempt) > accounting.SlowDecode,
			LogprobsDecoded:   attempt.LogprobsDecoded,

			InputTokens:       outcome.InputTokens,
			InputTokensSource: outcome.InputTokensSource,
		})
	}
	n.reportFact(n.service.Book.RecordRace(outcome.EscrowID, attempts))
}

// slowReceipt needs both stamps: a missing receipt is a refusal already counted, not a second failure.
func slowReceipt(attempt engine.AttemptOutcome) bool {
	if attempt.SendTime.IsZero() || attempt.ReceiptTime.IsZero() {
		return false
	}
	return attempt.ReceiptTime.Sub(attempt.SendTime) > accounting.SlowReceipt
}

// clockDrifted reads the offset in either direction; both directions break a deadline. See README.md.
func clockDrifted(attempt engine.AttemptOutcome) bool {
	offset, measured := engine.ClockOffset(attempt)
	return measured && (offset > accounting.ClockDrift || offset < -accounting.ClockDrift)
}

func (n *Recorder) RecordTimeout(event engine.TimeoutEvent) {
	if n != nil {
		n.reportFact(n.service.Book.RecordTimeout(event.EscrowID, event.Nonce, event.Kind, event.Action, event.Reason))
	}
}

// RecordDiffFacts applies what a composed diff said, on the journal's goroutine rather than under the session lock.
func (n *Recorder) RecordDiffFacts(escrowID string, facts []accounting.DiffFact) {
	if n == nil {
		return
	}
	for _, fact := range facts {
		switch fact.Kind {
		case accounting.DiffFactValidation:
			n.reportFact(n.service.Book.RecordValidation(escrowID, fact.ValidatorSlot))
		case accounting.DiffFactInvalidVerdict:
			n.reportFact(n.service.Book.RecordInvalidVerdict(escrowID, fact.Nonce))
		case accounting.DiffFactAppliedTimeout:
			n.reportFact(n.service.Book.RecordAppliedTimeout(escrowID, fact.Nonce))
		case accounting.DiffFactServiceNonce:
			n.reportFact(n.service.Book.RecordServiceNonce(escrowID, fact.Nonce, fact.Purpose))
		}
	}
}

// RecordProbe settles the warmup's own nonce and returns the book's refusal for the journal to name.
func (n *Recorder) RecordProbe(escrowID string, attempt accounting.Attempt) error {
	if n == nil {
		return nil
	}
	err := n.service.Book.RecordRace(escrowID, []accounting.Attempt{attempt})
	if errors.Is(err, accounting.ErrUnknownEscrow) {
		n.unknownEscrowFacts.Add(1)
	}
	return err
}

// A winner crowned after its client left needs its own terminal, or that population is unfindable.
func terminalFor(outcome engine.RaceOutcome, attempt engine.AttemptOutcome) string {
	if outcome.Lifecycle.ClientGone && outcome.IsWinner(attempt) {
		return accounting.TerminalClientGone
	}
	return attempt.Terminal.String()
}

func usageOf(outcome engine.RaceOutcome, attempt engine.AttemptOutcome) accounting.Usage {
	switch {
	case outcome.IsWinner(attempt):
		return accounting.UsageWinner
	case outcome.Succeeded:
		return accounting.UsageLoser
	default:
		return accounting.UsageUnknown
	}
}
