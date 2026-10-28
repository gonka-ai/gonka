package engine

import (
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
)

func (c *raceCoordinator) apply(event AttemptEvent) {
	attempt := c.byNonce[event.Nonce]
	if attempt == nil {
		return
	}
	if attempt.abandoned {
		if event.Kind == AttemptDone && event.Outcome != nil {
			event.Outcome.MissProof.releaseHeldFinish()
		}
		return
	}
	switch event.Kind {
	case AttemptDispatched:
		attempt.sendTime = event.At
		if c.feedsFleet(attempt) {
			c.deps.Fleet.recordDispatch(attempt, attempt.clockStart(), c.request.InputTokensFromBytes)
		}
	case AttemptCatchUpFaulted:
		if attempt.catchUpFault.IsZero() {
			attempt.catchUpFault = event.At
		}
	case AttemptReceipt:
		attempt.receiptTime = event.At
	case AttemptFirstToken:
		attempt.firstToken, attempt.lastChunk = event.At, event.At
		c.deps.Fleet.recordFirstToken(attempt, event.At)
	case AttemptContent:
		attempt.firstContent, attempt.lastChunk = event.At, event.At
	case AttemptChunk:
		attempt.lastChunk = event.At
	case AttemptDone:
		if event.Outcome != nil {
			event.Outcome.MissProof.takeHeldFinish()
		}
		c.complete(attempt, event)
	}
}

func (c *raceCoordinator) complete(attempt *liveAttempt, event AttemptEvent) {
	attempt.finishWith(event, c.target.NonceFinished(attempt.nonce))
	c.retire(attempt)

	if attempt.outcome == nil {
		c.traceUnreported(attempt, "")
		return
	}
	attempt.recordDeadlines(attempt.outcome)
	c.countRefusal(attempt)
	c.traceStep(RaceStep{
		Kind: RaceStepAttemptFinished, RequestID: c.request.RequestID, EscrowID: c.escrowID,
		Nonce: attempt.nonce, Participant: attempt.participant,
		Terminal: c.racedTerminal(attempt, *attempt.outcome), NonceFinished: attempt.nonceFinished,
		HasOutcome: true, PhaseAborted: c.phaseAborted(attempt, *attempt.outcome), Outcome: *attempt.outcome,
	})
	if signal := CapabilityOf(*attempt.outcome); signal.Refused() {
		RecordCapability(c.deps.Perf, attempt.participant, c.request.Model, signal)
		c.exclude(attempt.participant)
	}
	if rulesOutRetry(*attempt.outcome, c.deps.ModelContextLength, c.request.InputTokensFromBytes+c.request.OutputTokens) {
		c.retryRuledOut = true
		c.stopPicking()
	}
	if attempt.outcome.HostAbsent {
		c.deps.Picker.HostAbsent(c.escrowID, attempt.participant)
	}
	if attempt.outcome.StateDivergent {
		c.stateDiverged(attempt)
		return
	}
	if attempt.nonceFinished {
		c.deps.Picker.HostServed(c.escrowID, attempt.participant, attempt.outcome.SendTime)
	}
}

// A host rolls its diff back when its root disagrees, so replaying the retained chain costs one request to try.
func (c *raceCoordinator) stateDiverged(attempt *liveAttempt) {
	const cause = "escrow state root diverged"
	c.exclude(attempt.participant)

	if !c.deps.Picker.HostDiverged(c.escrowID, attempt.participant, attempt.completed) {
		c.traceStep(RaceStep{
			Kind: RaceStepHostBlocked, RequestID: c.request.RequestID, EscrowID: c.escrowID,
			Nonce: attempt.nonce, Participant: attempt.participant,
		})
		return
	}
	rewound := c.target != nil && c.target.RewindHostCatchUp(attempt.hostIdx, cause)
	c.traceStep(RaceStep{
		Kind: RaceStepHostRewound, RequestID: c.request.RequestID, EscrowID: c.escrowID,
		Nonce: attempt.nonce, Participant: attempt.participant, Rewound: rewound,
	})
}

// Without this an attempt the race stopped listening to leaves a spent nonce nothing downstream can see.
func (c *raceCoordinator) unreportedOutcome(attempt *liveAttempt) AttemptOutcome {
	hostLabel := ""
	if c.target != nil {
		hostLabel = c.target.HostLabel(attempt.hostIdx)
	}
	outcome := AttemptOutcome{
		Nonce:       attempt.nonce,
		Participant: attempt.participant,
		HostIdx:     attempt.hostIdx,
		HostLabel:   hostLabel,
		SendTime:    attempt.sendTime,
		ReceiptTime: attempt.receiptTime,
		Terminal:    TerminalUnclassified,
	}
	attempt.recordDeadlines(&outcome)
	return outcome
}

// recordDeadlines marks the deadlines the host was narrowed for; an excused one charged it nothing.
func (attempt *liveAttempt) recordDeadlines(outcome *AttemptOutcome) {
	outcome.ReceiptDeadlineMissed = attempt.receiptDeadline == deadlineMissed
	outcome.FirstTokenDeadlineMissed = attempt.firstTokenDeadline == deadlineMissed
}

// An attempt goroutine knows only its own cancellation, not the crown, the silence, the backstop or the nonce's finish.
func (c *raceCoordinator) racedTerminal(attempt *liveAttempt, outcome AttemptOutcome) Terminal {
	terminal := outcome.Terminal
	if terminal == TerminalLost && attempt.answerTruncated(outcome) {
		terminal = TerminalStreamTruncated
	}
	if terminal == TerminalClientCancelled && attempt.backstopped {
		terminal = TerminalHardTimeout
	}
	if terminal == TerminalClientCancelled && (attempt.wentSilentMidContent(outcome) || c.abandonedByHosts()) {
		terminal = TerminalStalled
	}
	if attempt == c.winner && terminal == TerminalLost {
		return TerminalWon
	}
	return terminal
}

func (attempt *liveAttempt) answerTruncated(outcome AttemptOutcome) bool {
	return !outcome.HostTerminated && !attempt.nonceFinished
}

func (attempt *liveAttempt) wentSilentMidContent(outcome AttemptOutcome) bool {
	return attempt.stalled && outcome.ContentChunks > 0
}

func (c *raceCoordinator) traceUnreported(attempt *liveAttempt, reason string) {
	c.traceStep(RaceStep{
		Kind: RaceStepAttemptFinished, RequestID: c.request.RequestID, EscrowID: c.escrowID,
		Nonce: attempt.nonce, Participant: attempt.participant, NonceFinished: attempt.nonceFinished,
		Reason: reason,
	})
}

func (c *raceCoordinator) feedsFleet(attempt *liveAttempt) bool {
	return attempt == c.attempts[0] && c.decision == StartPrimary
}

func (c *raceCoordinator) retire(attempt *liveAttempt) {
	c.pending--
	c.deps.Fleet.forget(attempt)
	attempt.cancel()
	c.deps.Perf.Release(attempt.participant)
}

func (c *raceCoordinator) report() RaceOutcome {
	c.markReported()
	c.releaseUnreadFinishes()
	outcome := c.outcome()
	outcome.observeCrowning(c.deps.Crown)
	if c.deps.Report != nil {
		c.deps.Report(outcome)
	}
	return outcome
}

func (c *raceCoordinator) markReported() {
	c.reportOnce.Do(func() { close(c.done) })
}

// abandon frees what a panicking coordinator launched: no attempt waits on a race that will never read it.
func (c *raceCoordinator) abandon() {
	c.markReported()
	if c.picking() {
		c.pickCancel()
		go c.releaseUnreadPick()
	}
	for _, attempt := range c.attempts {
		if attempt.cancel != nil {
			attempt.cancel()
		}
	}
}

func (c *raceCoordinator) releaseUnreadPick() {
	if result := <-c.picked; result.err == nil {
		result.assignment.ReleaseHostSlot()
		result.assignment.ReleaseEscrow()
	}
}

func (c *raceCoordinator) releaseUnreadFinishes() {
	for {
		select {
		case event := <-c.events:
			if event.Kind == AttemptDone && event.Outcome != nil {
				event.Outcome.MissProof.releaseUntakenFinish()
			}
		default:
			return
		}
	}
}

func (c *raceCoordinator) outcome() RaceOutcome {
	outcome := c.raceFacts()
	outcome.Attempts = make([]AttemptOutcome, 0, len(c.attempts))
	for _, attempt := range c.attempts {
		record := c.attemptRecord(attempt)
		if attempt == c.winner {
			outcome.WinnerNonce = attempt.nonce
			outcome.Succeeded = record.Terminal == TerminalWon
		}
		outcome.Lifecycle.EscrowMissing = outcome.Lifecycle.EscrowMissing || attempt.lifecycle.EscrowMissing
		outcome.Lifecycle.BalanceExhausted = outcome.Lifecycle.BalanceExhausted || attempt.lifecycle.BalanceExhausted
		outcome.Attempts = append(outcome.Attempts, record)
	}
	return outcome
}

func (c *raceCoordinator) raceFacts() RaceOutcome {
	return RaceOutcome{
		RequestID:            c.request.RequestID,
		EscrowID:             c.escrowID,
		Model:                c.request.Model,
		InputTokens:          c.request.InputTokens,
		InputTokensSource:    c.request.InputTokensSource,
		InputTokensFromBytes: c.request.InputTokensFromBytes,
		OutputTokens:         c.request.OutputTokens,
		ClientStream:         c.request.ClientStream,
		Decision:             c.decision,
		PoCBypassActive:      c.pocBypass,
		Lifecycle:            Lifecycle{BalanceExhausted: c.balanceExhausted, ClientGone: !c.clientGoneAt.IsZero()},

		ModelContextLength: c.deps.ModelContextLength,
	}
}

func (c *raceCoordinator) attemptRecord(attempt *liveAttempt) AttemptOutcome {
	var record AttemptOutcome
	if attempt.outcome != nil {
		record = *attempt.outcome
	} else {
		record = c.unreportedOutcome(attempt)
	}
	record.StartedAt = c.started
	record.NonceFinished = attempt.nonceFinished
	record.PhaseTransitionAborted = c.phaseAborted(attempt, record)
	record.Terminal = c.racedTerminal(attempt, record)
	return record
}

func (c *raceCoordinator) countRefusal(attempt *liveAttempt) {
	if verdict, moves := c.raceFacts().Verdict(c.attemptRecord(attempt)); moves && verdict == limits.Overload {
		c.deps.Limiter.CountRefusalIfIdle(attempt.participant, c.request.Model)
	}
}

// pocBypassActive reports the gateway serving through a chain phase that refuses new inferences.
func pocBypassActive(snapshot chain.PhaseSnapshot, modes config.Modes) bool {
	return modes.PoCMode == config.PoCModeRelaxed && snapshot.RequestsBlocked
}

// The narrower phase that takes a host away mid-attempt; only the bypass has an attempt running in it.
func pocGenerating(snapshot chain.PhaseSnapshot, modes config.Modes) bool {
	if modes.PoCMode != config.PoCModeRelaxed {
		return false
	}
	return snapshot.EpochPhase == chain.EpochPhasePoCGenerate ||
		snapshot.ConfirmationPoCPhase == chain.ConfirmationPoCGeneration
}

// Asked with the phase the coordinator sees, so the log line and the ladders cannot disagree.
func (c *raceCoordinator) phaseAborted(attempt *liveAttempt, outcome AttemptOutcome) bool {
	return phaseAborted(outcome, attempt.inInference, pocGenerating(c.deps.Snapshots.Snapshot(), c.deps.Modes))
}

// A no-receipt attempt never reached the host's queue, so the transition cannot be what ended it.
func phaseAborted(a AttemptOutcome, startedInInference, pocGenerating bool) bool {
	if !startedInInference || !pocGenerating {
		return false
	}
	return !a.NonceFinished && a.ErrorSource == "" && a.Terminal != TerminalNoReceipt
}
