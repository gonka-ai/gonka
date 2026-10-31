package engine

import (
	"cmp"
	"time"

	"devshard/cmd/gateway/limits"
)

// raceTimer is the coordinator's one timer, re-armed to whatever nextDeadline chose.
type raceTimer interface {
	Reset(delay time.Duration)
	Stop()
	Fired() <-chan time.Time
}

// deadlineTrigger is what fires at a deadline; the declaration order is the tie-break precedence. See race.md, "Deadlines".
type (
	deadlineTrigger int
	deadlineArm     struct {
		At         time.Time
		Trigger    deadlineTrigger
		Escalation ArmedEscalation
	}
)

// deadlineJudgement is what became of a receipt or first-token deadline a running attempt owed; an excused one passed inside a proof-of-compute phase.
type deadlineJudgement uint8

const (
	deadlineOwed deadlineJudgement = iota
	deadlineMissed
	deadlineExcused
)

// Pick is when an unanswered pick stops being worth waiting for, and is zero while none is running.
type deadlinePlan struct {
	Policy    EscalationPolicy
	Request   EscalationRequest
	Attempts  []EscalationAttempt
	Budget    int
	Limit     int
	Drain     time.Time
	Pick      time.Time
	Cancelled bool

	RetryRuledOut bool
}

// nextDeadline is the earliest armed deadline and what fires there, precedence breaking exact ties. See race.md, "Deadlines".
func nextDeadline(now time.Time, plan deadlinePlan) deadlineArm {
	var arm deadlineArm
	consider := func(at time.Time, trigger deadlineTrigger) {
		if at.IsZero() {
			return
		}
		if arm.Trigger == triggerNone || at.Before(arm.At) || (at.Equal(arm.At) && trigger < arm.Trigger) {
			arm.At, arm.Trigger = at, trigger
		}
	}
	if plan.Cancelled {
		return arm
	}
	consider(plan.hardTimeout(), triggerHardTimeout)
	if armed, ok := plan.escalation(now); ok {
		consider(armed.Deadline, triggerEscalation)
		if arm.Trigger == triggerEscalation {
			arm.Escalation = armed
		}
	}
	consider(plan.latencyDeadline(), triggerMissedDeadline)
	consider(plan.Pick, triggerPick)
	consider(plan.stall(), triggerStall)
	if arm.Trigger != triggerEscalation {
		arm.Escalation = ArmedEscalation{}
	}
	return arm
}

func (p deadlinePlan) escalation(now time.Time) (ArmedEscalation, bool) {
	if p.escalationDisarmed() {
		return ArmedEscalation{}, false
	}
	return p.Policy.NextEscalation(now, p.Attempts, p.Request)
}

func (p deadlinePlan) hardTimeout() time.Time {
	var earliest time.Time
	consider := func(at time.Time) { earliest = earlierSet(earliest, at) }
	if p.detached() {
		consider(p.Drain)
	}
	for index := range p.Attempts {
		attempt := &p.Attempts[index]
		switch {
		case attempt.Done:
			if attempt.Crowned && !attempt.Completed.IsZero() && p.Policy.LoserGrace > 0 {
				consider(attempt.Completed.Add(p.Policy.LoserGrace))
			}
		case !attempt.LaunchTime.IsZero():
			consider(attempt.LaunchTime.Add(p.Policy.hardTimeout()))
		}
	}
	return earliest
}

func (p deadlinePlan) latencyDeadline() time.Time {
	var earliest time.Time
	for index := range p.Attempts {
		if _, deadline, owed := p.Policy.owedDeadline(&p.Attempts[index], p.Request); owed {
			earliest = earlierSet(earliest, deadline)
		}
	}
	return earliest
}

func (p deadlinePlan) stall() time.Time {
	if p.Policy.InterChunkStall <= 0 {
		return time.Time{}
	}
	var earliest time.Time
	for index := range p.Attempts {
		attempt := &p.Attempts[index]
		if attempt.Done || attempt.Stalled ||
			attempt.FirstToken.IsZero() || attempt.LastChunk.IsZero() {
			continue
		}
		earliest = earlierSet(earliest, attempt.LastChunk.Add(p.Policy.InterChunkStall))
	}
	return earliest
}

func earlierSet(earliest, candidate time.Time) time.Time {
	if earliest.IsZero() || candidate.Before(earliest) {
		return candidate
	}
	return earliest
}
func (p deadlinePlan) detached() bool { return !p.Drain.IsZero() }
func (p deadlinePlan) crowned() bool {
	for index := range p.Attempts {
		if p.Attempts[index].Crowned {
			return true
		}
	}
	return false
}

func (p deadlinePlan) picking() bool { return !p.Pick.IsZero() }
func (p deadlinePlan) budgetSpent() bool {
	return p.heldAttempts() >= p.Budget || len(p.Attempts) >= p.Limit
}

func (p deadlinePlan) heldAttempts() int {
	held := 0
	for index := range p.Attempts {
		if !p.Attempts[index].Done {
			held++
		}
	}
	return held
}

// escalationDisarmed lists every reason no escalation arms. See README.md, "Escalation and the deadline ladder".
func (p deadlinePlan) escalationDisarmed() bool {
	return p.picking() || p.detached() || p.crowned() || p.budgetSpent() || p.RetryRuledOut
}

type wallTimer struct{ timer *time.Timer }

func newWallTimer() raceTimer {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &wallTimer{timer: timer}
}

func (t *wallTimer) Reset(delay time.Duration) {
	t.timer.Stop()
	t.timer.Reset(delay)
}
func (t *wallTimer) Stop()                   { t.timer.Stop() }
func (t *wallTimer) Fired() <-chan time.Time { return t.timer.C }

func (c *raceCoordinator) expire(arm deadlineArm) {
	c.catchUp()
	c.fire(arm)
}

func (c *raceCoordinator) rearm(arm deadlineArm) {
	if arm.Trigger == triggerNone {
		c.timer.Stop()
		return
	}
	c.timer.Reset(arm.At.Sub(c.deps.Now()))
}

func (c *raceCoordinator) fire(arm deadlineArm) {
	switch arm.Trigger {
	case triggerEscalation:
		c.escalate(arm.Escalation)
	case triggerMissedDeadline:
		c.judgeMissedDeadlines()
	case triggerPick:
		c.stopPicking()
	case triggerStall:
		c.markStalls()
	case triggerHardTimeout:
		c.cancelAll()
	}
}

func (c *raceCoordinator) escalate(armed ArmedEscalation) {
	confirmed, ok := c.deps.Policy.Confirm(armed, c.deps.Now(), c.plan().Attempts, c.escalationRequest())
	if !ok {
		return
	}
	// Consumed before the pick is started, so a pick that finds no host cannot retry the same trigger.
	c.attempts[confirmed.Attempt].escalated = true
	c.startPick(confirmed.Stage.Reason(), c.request.Params)
}

func (c *raceCoordinator) markStalls() {
	silentSince := c.deps.Now().Add(-c.deps.Policy.InterChunkStall)
	for _, attempt := range c.attempts {
		if attempt.done || attempt.firstToken.IsZero() || attempt.lastChunk.After(silentSince) {
			continue
		}
		attempt.stalled = true
	}
}

// missedDeadlineVerdict names the verdict each deadline carries. See race.md, "Deadlines".
func missedDeadlineVerdict(stage EscalationStage) limits.Verdict {
	if stage == StageReceiptTimeout {
		return limits.MissedReceiptDeadline
	}
	return limits.MissedFirstTokenDeadline
}

// judgeMissedDeadlines narrows the window of every host past a deadline it owed and leaves its attempt running. See race.md, "Deadlines".
func (c *raceCoordinator) judgeMissedDeadlines() {
	now := c.deps.Now()
	plan := c.plan()
	judgement := deadlineMissed
	if pocGenerating(c.deps.Snapshots.Snapshot(), c.deps.Modes) {
		judgement = deadlineExcused
	}
	for index := range plan.Attempts {
		stage, deadline, owed := c.deps.Policy.owedDeadline(&plan.Attempts[index], plan.Request)
		if !owed || now.Before(deadline) {
			continue
		}
		attempt := c.attempts[index]
		attempt.judgeDeadline(stage, judgement)
		if judgement == deadlineMissed {
			c.deps.Limiter.OnResult(limits.Result{
				Participant: attempt.participant,
				Model:       c.request.Model,
				Verdict:     missedDeadlineVerdict(stage),
			})
		}
	}
}

func (attempt *liveAttempt) judgeDeadline(stage EscalationStage, judgement deadlineJudgement) {
	switch stage {
	case StageReceiptTimeout:
		attempt.receiptDeadline = judgement
	case StageFirstToken:
		attempt.firstTokenDeadline = judgement
	}
}

func (c *raceCoordinator) cancelAll() {
	c.cancelled = true
	c.stopPicking()
	now := c.deps.Now()
	if c.abandonAfter == nil {
		c.abandonAfter = time.After(cmp.Or(c.deps.CancelGrace, cancelledAttemptGrace))
	}
	for _, attempt := range c.attempts {
		if attempt.done {
			continue
		}
		if !attempt.launchTime.IsZero() && !now.Before(attempt.launchTime.Add(c.deps.Policy.hardTimeout())) {
			attempt.backstopped = true
		}
		attempt.cancel()
	}
}

func (c *raceCoordinator) plan() deadlinePlan {
	c.scratch = c.scratch[:0]
	for _, attempt := range c.attempts {
		c.scratch = append(c.scratch, EscalationAttempt{
			Suspicious:    attempt.suspicious,
			Escalated:     attempt.escalated,
			Done:          attempt.done,
			Crowned:       attempt == c.winner,
			Stalled:       attempt.stalled,
			NonceFinished: attempt.nonceFinished,
			EmptyStream:   attempt.outcome != nil && attempt.outcome.Terminal == TerminalEmptyStream,
			LaunchTime:    attempt.launchTime,
			ClockStart:    attempt.clockStart(),
			ReceiptTime:   attempt.receiptTime,
			FirstToken:    attempt.firstToken,
			FirstContent:  attempt.firstContent,
			LastChunk:     attempt.lastChunk,
			Completed:     attempt.completed,

			FirstContentP75: attempt.observedFirst,
			FleetHedgeScale: attempt.fleetScale,

			ReceiptDeadlineJudged:    attempt.receiptDeadline != deadlineOwed,
			FirstTokenDeadlineJudged: attempt.firstTokenDeadline != deadlineOwed,
		})
	}
	return deadlinePlan{
		Policy:    c.deps.Policy,
		Request:   c.escalationRequest(),
		Attempts:  c.scratch,
		Budget:    c.budget,
		Limit:     c.attemptLimit,
		Drain:     c.drain.deadline(c.clientGoneAt),
		Pick:      c.pickDeadline(),
		Cancelled: c.cancelled,

		RetryRuledOut: c.retryRuledOut,
	}
}

// clockStart is the send, or while the catch-up runs the first time it found the host unreachable, a minute after launch at the latest. See race.md, "Deadlines".
func (attempt *liveAttempt) clockStart() time.Time {
	if !attempt.sendTime.IsZero() || attempt.launchTime.IsZero() {
		return attempt.sendTime
	}
	latest := attempt.launchTime.Add(maxCatchUpBeforeSend)
	if !attempt.catchUpFault.IsZero() && attempt.catchUpFault.Before(latest) {
		return attempt.catchUpFault
	}
	return latest
}

func (c *raceCoordinator) escalationRequest() EscalationRequest {
	return EscalationRequest{InputTokens: c.request.InputTokensFromBytes}
}

func (c *raceCoordinator) abandonCancelled() {
	c.catchUp()
	now := c.deps.Now()
	for _, attempt := range c.attempts {
		if attempt.done {
			continue
		}
		attempt.abandon(now, c.target.NonceFinished(attempt.nonce))
		c.retire(attempt)
		c.traceUnreported(attempt, ReasonAbandonedAfterCancel)
	}
}
