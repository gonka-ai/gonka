package engine

import (
	"time"

	"devshard/cmd/gateway/config"
	"devshard/types"
)

// The backstops, the large-prompt receipt doubling, the attempts a hedged start launches and the measured first-token curve. See README, "Escalation and the deadline ladder" and "Backstops are not tunable".
const (
	streamingHardTimeout = min(20*time.Minute, types.DefaultExecutionTimeoutSeconds*time.Second-time.Minute)
	schedulerPickTimeout = 2 * time.Minute

	receiptTimeoutDoubleAboveTokens     = 100_000
	receiptTimeoutLargePromptMultiplier = 2

	hedgedStartAttempts = 2

	firstTokenBaseSeconds      = 1.7
	firstTokenPerTokenSeconds  = 3e-5
	firstTokenQuadraticSeconds = 5e-10
)

// EscalationStage names the condition that earned an attempt one more attempt beside it.
type EscalationStage string

func (s EscalationStage) Reason() string {
	switch s {
	case StageSuspicious:
		return EscalationReasonSuspicious
	case StageAttemptFailed:
		return EscalationReasonAttemptFailed
	case StageReceiptTimeout:
		return EscalationReasonReceipt
	case StageFirstToken:
		return EscalationReasonFirstToken
	case StageHedge:
		return EscalationReasonHedge
	case StageStall:
		return EscalationReasonStall
	}
	return ""
}

// The ladder is pure over its arguments: no host performance, no phase snapshot, no clock of its own.
type EscalationPolicy struct {
	ReceiptTimeout        time.Duration
	FirstTokenFloor       time.Duration
	FirstTokenCeiling     time.Duration
	InterChunkStall       time.Duration
	LoserGrace            time.Duration
	MaxAttemptsPerRequest int
	HardTimeout           time.Duration
	HedgeFirstTokenFloor  time.Duration
}

// E2EOverrides are the bounds a test stand may shorten; every zero keeps the production bound.
type E2EOverrides struct {
	HardTimeout time.Duration
}

func (p EscalationPolicy) hardTimeout() time.Duration {
	if p.HardTimeout > 0 {
		return p.HardTimeout
	}
	return streamingHardTimeout
}

func EscalationPolicyFromConfig(engine config.Engine) EscalationPolicy {
	return EscalationPolicy{
		ReceiptTimeout:        time.Duration(engine.ReceiptTimeoutMS) * time.Millisecond,
		FirstTokenFloor:       time.Duration(engine.FirstTokenFloorMS) * time.Millisecond,
		FirstTokenCeiling:     time.Duration(engine.FirstTokenCeilingMS) * time.Millisecond,
		InterChunkStall:       time.Duration(engine.InterChunkStallMS) * time.Millisecond,
		LoserGrace:            time.Duration(engine.LoserGraceMS) * time.Millisecond,
		MaxAttemptsPerRequest: int(engine.MaxAttemptsPerRequest),
		HedgeFirstTokenFloor:  time.Duration(engine.HedgeFirstTokenFloorMS) * time.Millisecond,
	}
}

const (
	firstTokenObservedLimit            = 4
	firstTokenObservedSlackNumerator   = 3
	firstTokenObservedSlackDenominator = 2
)

type EscalationRequest struct {
	InputTokens uint64
}

type EscalationAttempt struct {
	FirstContentP75 time.Duration
	FleetHedgeScale float64
	Suspicious      bool
	Escalated       bool
	Done            bool
	Crowned         bool
	Stalled         bool
	NonceFinished   bool
	EmptyStream     bool
	LaunchTime      time.Time
	ClockStart      time.Time
	ReceiptTime     time.Time
	FirstToken      time.Time
	FirstContent    time.Time
	LastChunk       time.Time
	Completed       time.Time

	ReceiptDeadlineJudged    bool
	FirstTokenDeadlineJudged bool
}

// StartPlan is how a race begins; ImmediateAttempts counts the primary.
type StartPlan struct {
	ImmediateAttempts int
	Reason            string
}

// ArmedEscalation is a deadline to arm, deliberately not a permission to escalate; only Confirm converts it. See race.md, "Escalation".
type ArmedEscalation struct {
	Attempt  int
	Stage    EscalationStage
	Deadline time.Time
}

type ConfirmedEscalation struct {
	Attempt int
	Stage   EscalationStage
}

// Decide hedges a primary the race already has reason to distrust. See race.md, "Escalation" and capacity.md, "Outlier ejection".
func (p EscalationPolicy) Decide(budget int, primarySuspicious, primaryDegraded bool) StartPlan {
	switch {
	case budget < hedgedStartAttempts:
	case primarySuspicious:
		return StartPlan{ImmediateAttempts: hedgedStartAttempts, Reason: StartPrimarySuspicious}
	case primaryDegraded:
		return StartPlan{ImmediateAttempts: hedgedStartAttempts, Reason: StartPrimaryDegraded}
	}
	return StartPlan{ImmediateAttempts: 1, Reason: StartPrimary}
}

// AttemptBudget caps how many attempts one race may hold; scarce nonces force a single attempt. See race.md, "Escalation".
func (p EscalationPolicy) AttemptBudget(hostCount int, nonceScarce bool) int {
	if hostCount < 1 {
		return 1
	}
	if nonceScarce {
		return 1
	}
	if p.MaxAttemptsPerRequest < 1 || p.MaxAttemptsPerRequest > hostCount {
		return hostCount
	}
	return p.MaxAttemptsPerRequest
}

// AttemptLimit caps how many attempts one race may start in total, failed ones included; scarce nonces allow no replacement. See race.md, "Escalation".
func (p EscalationPolicy) AttemptLimit(hostCount int, nonceScarce bool) int {
	if hostCount < 1 || nonceScarce {
		return 1
	}
	return hostCount
}

func (p EscalationPolicy) NextEscalation(now time.Time, attempts []EscalationAttempt, request EscalationRequest) (ArmedEscalation, bool) {
	var earliest ArmedEscalation
	found := false
	for index := range attempts {
		armed, ok := p.triggerFor(&attempts[index], request, now)
		if !ok {
			continue
		}
		armed.Attempt = index
		if !found || armed.Deadline.Before(earliest.Deadline) {
			earliest, found = armed, true
		}
	}
	return earliest, found
}

func (p EscalationPolicy) Confirm(armed ArmedEscalation, now time.Time, attempts []EscalationAttempt, request EscalationRequest) (ConfirmedEscalation, bool) {
	if armed.Attempt < 0 || armed.Attempt >= len(attempts) {
		return ConfirmedEscalation{}, false
	}
	current, ok := p.triggerFor(&attempts[armed.Attempt], request, now)
	if !ok || current.Stage != armed.Stage || now.Before(current.Deadline) {
		return ConfirmedEscalation{}, false
	}
	return ConfirmedEscalation{Attempt: armed.Attempt, Stage: armed.Stage}, true
}

// The attempt is taken by pointer: the ladder is walked for every attempt on every event, and the value is wide.
func (p EscalationPolicy) triggerFor(attempt *EscalationAttempt, request EscalationRequest, now time.Time) (ArmedEscalation, bool) {
	switch {
	case attempt.Escalated:
		return ArmedEscalation{}, false
	case attempt.Suspicious:
		return ArmedEscalation{Stage: StageSuspicious, Deadline: now}, true
	case attempt.answered():
		return ArmedEscalation{}, false
	case attempt.Done:
		return ArmedEscalation{Stage: StageAttemptFailed, Deadline: now}, true
	case attempt.Stalled:
		return ArmedEscalation{Stage: StageStall, Deadline: now}, true
	case attempt.ClockStart.IsZero():
		return ArmedEscalation{}, false
	case attempt.ReceiptTime.IsZero():
		return p.hedgeBefore(StageReceiptTimeout, p.receiptDeadline(attempt, request), attempt, request), true
	case !attempt.FirstToken.IsZero():
		return ArmedEscalation{}, false
	}
	return p.hedgeBefore(StageFirstToken, p.firstTokenDeadline(attempt, request), attempt, request), true
}

func (p EscalationPolicy) hedgeBefore(stage EscalationStage, judged time.Time, attempt *EscalationAttempt, request EscalationRequest) ArmedEscalation {
	if p.HedgeFirstTokenFloor > 0 {
		hedge := attempt.ClockStart.Add(p.hedgeWait(attempt, request))
		if hedge.Before(judged) {
			return ArmedEscalation{Stage: StageHedge, Deadline: hedge}
		}
	}
	return ArmedEscalation{Stage: stage, Deadline: judged}
}

func (p EscalationPolicy) hedgeWait(attempt *EscalationAttempt, request EscalationRequest) time.Duration {
	curve := firstTokenCurve(request.InputTokens)
	if attempt.FleetHedgeScale != fleetScaleUnknown {
		return max(p.HedgeFirstTokenFloor, time.Duration(float64(curve)*attempt.FleetHedgeScale))
	}
	return max(p.HedgeFirstTokenFloor, curve)
}

// answered reports an attempt that finished its nonce with something a client can render; an empty stream closes its nonce and still leaves nothing.
func (attempt *EscalationAttempt) answered() bool {
	return attempt.Done && attempt.NonceFinished && !attempt.EmptyStream
}

// owedDeadline is the deadline a running attempt's host is judged on once, whether or not the race may still escalate. See race.md, "Deadlines".
func (p EscalationPolicy) owedDeadline(attempt *EscalationAttempt, request EscalationRequest) (EscalationStage, time.Time, bool) {
	switch {
	case attempt.owesNoDeadline():
		return StageNone, time.Time{}, false
	case attempt.ReceiptTime.IsZero():
		return StageReceiptTimeout, p.receiptDeadline(attempt, request), true
	}
	return StageFirstToken, p.firstTokenDeadline(attempt, request), true
}

func (attempt *EscalationAttempt) owesNoDeadline() bool {
	if attempt.Done || attempt.ClockStart.IsZero() {
		return true
	}
	if attempt.ReceiptTime.IsZero() {
		return attempt.ReceiptDeadlineJudged
	}
	return !attempt.FirstToken.IsZero() || attempt.FirstTokenDeadlineJudged
}

func (p EscalationPolicy) receiptDeadline(attempt *EscalationAttempt, request EscalationRequest) time.Time {
	return attempt.ClockStart.Add(p.receiptTimeout(request.InputTokens))
}

// firstTokenDeadline is measured from dispatch, but the host owes a first token, not the time its receipt took.
func (p EscalationPolicy) firstTokenDeadline(attempt *EscalationAttempt, request EscalationRequest) time.Time {
	deadline := attempt.ClockStart.Add(p.firstTokenBudget(request.InputTokens, attempt.FirstContentP75))
	if graceFromReceipt := attempt.ReceiptTime.Add(p.FirstTokenFloor); graceFromReceipt.After(deadline) {
		return graceFromReceipt
	}
	return deadline
}

func (p EscalationPolicy) receiptTimeout(inputTokens uint64) time.Duration {
	if inputTokens > receiptTimeoutDoubleAboveTokens {
		return receiptTimeoutLargePromptMultiplier * p.ReceiptTimeout
	}
	return p.ReceiptTimeout
}

// firstTokenBudget only ever extends: a host slower than the curve keeps it, since its history would delay the rescue.
func (p EscalationPolicy) firstTokenBudget(inputTokens uint64, observed time.Duration) time.Duration {
	curve := p.firstTokenTimeout(inputTokens)
	if observed <= 0 || observed > firstTokenObservedLimit*curve {
		return curve
	}
	budget := observed * firstTokenObservedSlackNumerator / firstTokenObservedSlackDenominator
	if budget < curve {
		return curve
	}
	return p.capToFirstTokenCeiling(budget)
}

// firstTokenTimeout is the measured fit over prompt size, floored and capped. See race.md, "Escalation".
func (p EscalationPolicy) firstTokenTimeout(inputTokens uint64) time.Duration {
	return p.capToFirstTokenCeiling(max(p.FirstTokenFloor, firstTokenCurve(inputTokens)))
}

func firstTokenCurve(inputTokens uint64) time.Duration {
	tokens := float64(inputTokens)
	seconds := firstTokenBaseSeconds + firstTokenPerTokenSeconds*tokens + firstTokenQuadraticSeconds*tokens*tokens
	return time.Duration(seconds * float64(time.Second))
}

// capToFirstTokenCeiling keeps the curve under the backstop; a zero ceiling means the operator removed it.
func (p EscalationPolicy) capToFirstTokenCeiling(wait time.Duration) time.Duration {
	if p.FirstTokenCeiling > 0 && wait > p.FirstTokenCeiling {
		return p.FirstTokenCeiling
	}
	return wait
}
