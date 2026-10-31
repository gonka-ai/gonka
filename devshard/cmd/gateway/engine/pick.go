package engine

import (
	"context"
	"errors"
	"slices"
	"time"

	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

func (c *raceCoordinator) pick(ctx context.Context) (scheduler.Assignment, error) {
	return c.observePick(c.deps.Picker.Pick(ctx, c.requestProfile(c.request.Params)))
}

// observePick latches the out-of-funds fact a declined nonce carries, except a pinned escrow's own shortfall; no host ever reports it.
func (c *raceCoordinator) observePick(assignment scheduler.Assignment, err error) (scheduler.Assignment, error) {
	if errors.Is(err, types.ErrInsufficientBalance) && !errors.Is(err, scheduler.ErrPinnedEscrowShort) {
		c.balanceExhausted = true
	}
	return assignment, err
}

func (c *raceCoordinator) requestProfile(params any) scheduler.RequestProfile {
	return scheduler.RequestProfile{
		RequestID:         c.request.RequestID,
		Model:             c.request.Model,
		Escrow:            c.escrowID,
		InputTokens:       int(c.request.InputTokens),
		InputTokensSource: c.request.InputTokensSource,
		InputBytes:        int(c.request.InputBytes),
		OutputTokens:      int(c.request.OutputTokens),
		Exclude:           c.excluded,
		Params:            params,
	}
}

type pickedHost struct {
	assignment scheduler.Assignment
	err        error
}

func (c *raceCoordinator) picking() bool { return c.pickCancel != nil }

func (c *raceCoordinator) budgetSpent() bool {
	return c.heldAttempts() >= c.budget || len(c.attempts) >= c.attemptLimit
}
func (c *raceCoordinator) clientDeparted() bool       { return c.drain.clientErr() != nil }
func (c *raceCoordinator) owedNoFurtherAttempt() bool { return c.retryRuledOut || c.clientDeparted() }
func (c *raceCoordinator) nothingInFlight() bool      { return c.pending == 0 && !c.picking() }

func (c *raceCoordinator) heldAttempts() int {
	held := 0
	for _, attempt := range c.attempts {
		if !attempt.done {
			held++
		}
	}
	return held
}

// startPick runs at most one speculative pick beside the race, never on the coordinator's goroutine and never for a client that has left. See race.md, "Escalation" and "Client departure and the drain".
func (c *raceCoordinator) startPick(reason string, params any) {
	if c.picking() || c.budgetSpent() || c.owedNoFurtherAttempt() {
		return
	}
	ctx, cancel := context.WithTimeout(c.drain.race, schedulerPickTimeout)
	c.pickCancel, c.pickStarted, c.pickReason, c.pickStopped = cancel, c.deps.Now(), reason, false
	profile := c.requestProfile(params)
	profile.Escalation = true
	go func() {
		assignment, err := c.deps.Picker.Pick(ctx, profile)
		c.picked <- pickedHost{assignment: assignment, err: err}
	}()
}

func (c *raceCoordinator) startNextImmediate() {
	if c.moreImmediate <= 0 {
		return
	}
	c.moreImmediate--
	c.startPick(c.decision, c.request.Params)
}

// applyPick spends what the scheduler answered with: a committed nonce is sent even once the client left or a winner was crowned, and stranded only when the race ruled every send out. See race.md, "Client departure and the drain".
func (c *raceCoordinator) applyPick(result pickedHost) {
	c.catchUp()
	c.pickCancel()
	c.pickCancel = nil
	assignment, err := c.observePick(result.assignment, result.err)
	switch {
	case err != nil:
		c.reportUnfilledPick(err)
		c.startErr, c.moreImmediate = err, 0
	case c.cancelled || c.retryRuledOut:
		c.strand(assignment, RoleSpeculative)
	default:
		c.launch(assignment, RoleSpeculative, c.pickReason)
	}
	c.startNextImmediate()
}

// reportUnfilledPick traces an escalation that reached no attempt; only the race itself cancels a pick, and that is no refusal.
func (c *raceCoordinator) reportUnfilledPick(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	c.traceStep(RaceStep{
		Kind: RaceStepEscalationUnfilled, RequestID: c.request.RequestID, EscrowID: c.escrowID,
		Reason: c.pickReason, Attempts: len(c.attempts), Err: err,
	})
}

// stopPicking gives up on a pick the race can no longer spend; the scheduler gives back what it had handed over.
func (c *raceCoordinator) stopPicking() {
	c.moreImmediate = 0
	if c.picking() {
		c.pickCancel()
		c.pickStopped = true
	}
}

func (c *raceCoordinator) pickDeadline() time.Time {
	if !c.picking() || c.pickStopped {
		return time.Time{}
	}
	return c.pickStarted.Add(schedulerPickTimeout)
}

func (c *raceCoordinator) observedFirstContent(participant string) time.Duration {
	observed, _ := c.deps.Perf.FirstContentP75(participant, c.request.Model)
	return observed
}

func (c *raceCoordinator) launch(assignment scheduler.Assignment, role, startReason string) {
	// The race holds the escrow for as long as its vote is owed, so the commit's own hold goes back.
	assignment.ReleaseEscrow()
	nonce := assignment.Nonce.Nonce()
	attemptCtx, cancelWithCause := context.WithCancelCause(c.drain.race)
	cancel := func() { cancelWithCause(nil) }
	writer := newWinnerWriter(nonce, c.request.Client, c.crown, c.done, newPrefixAllowance(c.deps.PrefixBudget, assignment.Host, cancelWithCause))
	sink := &attemptSink{winner: writer}

	attempt := &liveAttempt{
		nonce:       nonce,
		hostIdx:     assignment.Nonce.HostIdx(),
		participant: assignment.Host,
		suspicious:  c.denied(assignment.Host),
		cancel:      cancel,
		launchTime:  c.deps.Now(),
		inInference: !pocGenerating(c.deps.Snapshots.Snapshot(), c.deps.Modes),
		// Read once: plan() runs on every event, and the quantile cannot move mid-attempt.
		observedFirst: c.observedFirstContent(assignment.Host),
		fleetScale:    c.deps.Fleet.hedgeScale(c.deps.Now()),
	}
	c.attempts = append(c.attempts, attempt)
	c.byNonce[nonce] = attempt
	c.pending++
	// Excluded on dispatch, not on failure: a sibling slot would cost a second nonce for one host's opinion.
	c.exclude(attempt.participant)
	c.deps.Perf.Acquire(attempt.participant)
	c.traceStep(RaceStep{
		Kind: RaceStepNonceCommitted, RequestID: c.request.RequestID, EscrowID: assignment.Escrow, Nonce: nonce,
		Participant: attempt.participant, Slot: attempt.hostIdx, Role: role, Reason: startReason,
	})

	go runAttempt(attemptCtx, AttemptSpec{
		Escrow:      assignment.Escrow,
		Model:       c.request.Model,
		Participant: attempt.participant,
		HostIdx:     attempt.hostIdx,
		HostLabel:   c.target.HostLabel(attempt.hostIdx),
		Role:        role,
		StartReason: startReason,
		Suspicious:  attempt.suspicious,
		Nonce:       assignment.Nonce,
		Dispatch:    c.target,
		ReleaseSlot: assignment.ReleaseHostSlot,
		Classifier:  contentGate{streamClassifier: c.deps.Classify(attempt.participant), sink: sink},
		Sink:        sink,
		Now:         c.deps.Now,
		Events:      c.events,
		Reported:    c.done,
	})
}

// abandonedByHosts reports the backstop cancelling every attempt while a client still waited, uncrowned.
func (c *raceCoordinator) abandonedByHosts() bool {
	return c.cancelled && c.winner == nil && !c.handedOff
}

func (c *raceCoordinator) denied(participant string) bool {
	return c.deps.Crown != nil && c.deps.Crown.Denied(participant, c.request.Model)
}

func (c *raceCoordinator) degraded(participant string) bool {
	return c.deps.Perf.Degraded(participant, c.request.Model)
}

func (c *raceCoordinator) exclude(participant string) {
	if slices.Contains(c.excluded, participant) {
		return
	}
	c.excluded = append(c.excluded, participant)
}
