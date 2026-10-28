package engine

import (
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

type SampleExemption int

const (
	SampleRecorded SampleExemption = iota
	ExemptPhaseAborted
	ExemptErrorStream
	ExemptStateDivergent
	ExemptLocalFault
	ExemptGatewayPressure
	ExemptLongResponse
	ExemptPoCSuppressed
	ExemptRequestTooLarge
	ExemptEscrowLookupLimited
	ExemptEmptyStreamNoWinner
	ExemptNeverDispatched
	ExemptClientCancelled
	ExemptNeverReported
)

type AttemptLabels struct {
	Participant string
	Model       string
	Role        string
	Outcome     string
	Visibility  string
	Reason      string
}

// longResponseExempt gates on ContentSource, not ContentChunks, which counts error events too. See rules.md, "1. A committed nonce is always settled".
func (o RaceOutcome) longResponseExempt(a AttemptOutcome) bool {
	if a.Terminal == TerminalHardTimeout {
		return false
	}
	return !a.NonceFinished && a.ContentSource != "" && a.elapsed() >= longResponseExemption
}

// responsive decides whether a host earns a positive perf sample. See race.md, "The exemption ladder".
func (o RaceOutcome) responsive(a AttemptOutcome) bool {
	return a.Confirmed && a.NonceFinished && !a.emptyStream()
}

// FinishAwaited reports the served winner whose perf sample and metric labels wait for its nonce's finish. See race.md, "A served winner's sample waits for its finish".
func (o RaceOutcome) FinishAwaited(a AttemptOutcome) bool {
	return a.Terminal == TerminalWon && a.Confirmed && !a.NonceFinished && o.sampleExemption(a) == SampleRecorded
}

func (o RaceOutcome) served(a AttemptOutcome) bool {
	return a.wonOrLost() && o.responsive(a)
}

func (o RaceOutcome) emptyStreamUnderPoCBypass(a AttemptOutcome) bool {
	return a.emptyStream() && o.PoCBypassActive
}

// exemptOnBothLadders holds the rungs the verdict ladder shares with the sample ladder. See race.md, "The exemption ladder".
func (o RaceOutcome) exemptOnBothLadders(a AttemptOutcome) bool {
	return a.PhaseTransitionAborted || a.StateDivergent || a.LocalFault || a.GatewayPressure || o.longResponseExempt(a) || o.emptyStreamUnderPoCBypass(a)
}

// sampleExemption is the whole ladder of reasons an attempt contributes no perf sample. See README, "The exemption ladder".
func (o RaceOutcome) sampleExemption(a AttemptOutcome) SampleExemption {
	switch {
	case a.SendTime.IsZero():
		return ExemptNeverDispatched
	case a.Terminal == TerminalUnclassified:
		return ExemptNeverReported
	case a.PhaseTransitionAborted:
		return ExemptPhaseAborted
	case a.errorStream():
		return ExemptErrorStream
	case a.StateDivergent:
		return ExemptStateDivergent
	case a.LocalFault:
		return ExemptLocalFault
	case a.GatewayPressure:
		return ExemptGatewayPressure
	case o.longResponseExempt(a):
		return ExemptLongResponse
	case o.emptyStreamUnderPoCBypass(a):
		return ExemptPoCSuppressed
	case a.Terminal == TerminalRequestTooLarge:
		return ExemptRequestTooLarge
	case a.Terminal == TerminalEscrowLookupLimited:
		return ExemptEscrowLookupLimited
	case a.emptyStream() && !o.Succeeded:
		return ExemptEmptyStreamNoWinner
	case a.Terminal == TerminalClientCancelled && !o.silentPastDeadline(a):
		return ExemptClientCancelled
	}
	return SampleRecorded
}

// silentPastDeadline is a loser of a served race that missed an unexcused deadline with no token before the race cancelled it; the cancellation does not excuse that miss.
func (o RaceOutcome) silentPastDeadline(a AttemptOutcome) bool {
	return o.Succeeded && !o.Lifecycle.ClientGone && !o.PoCBypassActive && !o.IsWinner(a) &&
		!a.NonceFinished && a.FirstToken.IsZero() && a.missedADeadline()
}

func (o RaceOutcome) Sample(a AttemptOutcome) (perf.Sample, SampleExemption) {
	if exemption := o.sampleExemption(a); exemption != SampleRecorded {
		return perf.Sample{}, exemption
	}
	return perf.Sample{
		ParticipantKey: a.Participant,
		Model:          o.Model,
		Responsive:     o.responsive(a),
		FirstContent:   a.FirstContentDelay(),

		TimePerOutputToken: TimePerOutputToken(a),
	}, SampleRecorded
}

func (o RaceOutcome) Verdict(a AttemptOutcome) (limits.Verdict, bool) {
	switch {
	case o.exemptOnBothLadders(a):
		return limits.ModelOutcome, false
	case a.Terminal == TerminalEmptyStream && !a.NonceFinished:
		return limits.EmptyAnswerLeftOpen, true
	case a.Terminal == TerminalEmptyStream:
		return limits.EmptyAnswer, true
	case a.emptyStream() && a.elapsed() >= emptyStreamHeldTooLong:
		return limits.Overload, true
	case a.wonOrLost() && !o.responsive(a):
		return limits.ModelOutcome, false
	case a.wonOrLost() && a.missedADeadline():
		return limits.LateSuccess, true
	}
	return a.Terminal.verdict()
}

// observeCrowning reports only the attempts that say something about the host.
func (o RaceOutcome) observeCrowning(crown crownGate) {
	if crown == nil {
		return
	}
	for _, attempt := range o.Attempts {
		if !o.JudgesCrowning(attempt) {
			continue
		}
		crown.Observe(attempt.Participant, o.Model, o.DeniesCrowning(attempt))
	}
}

// JudgesCrowning reports that this attempt says something about the host's crowning. See README, "Crown denial".
func (o RaceOutcome) JudgesCrowning(a AttemptOutcome) bool {
	if a.PhaseTransitionAborted || o.PoCBypassActive {
		return false
	}
	return a.Terminal == TerminalEmptyStream || o.served(a)
}

// DeniesCrowning reports that a host produced nothing while claiming to serve. See race.md, "Crown denial".
func (o RaceOutcome) DeniesCrowning(a AttemptOutcome) bool {
	if a.PhaseTransitionAborted || o.PoCBypassActive {
		return false
	}
	return a.Terminal == TerminalEmptyStream
}

func (o RaceOutcome) Labels(a AttemptOutcome) AttemptLabels {
	role := a.Role
	if role == "" {
		role = RolePrimary
	}
	served := o.served(a)
	labels := AttemptLabels{
		Participant: a.Participant,
		Model:       o.Model,
		Role:        role,
		Outcome:     AttemptOutcomeFailed,
		Visibility:  o.visibility(a, served),
		Reason:      o.failureReason(a),
	}
	if served {
		labels.Outcome = AttemptOutcomeSuccess
		labels.Reason = ""
	}
	return labels
}

func (o RaceOutcome) visibility(a AttemptOutcome, served bool) string {
	switch {
	case served && o.IsWinner(a) && o.Lifecycle.ClientGone:
		return VisibilityWinnerClientGone
	case served && o.IsWinner(a):
		return VisibilityWinner
	case a.Suspicious:
		return VisibilityNoWinner
	case served:
		return VisibilitySuppressedLoser
	}
	return VisibilityFailedNotFinished
}

// IsWinner is the one test for "this attempt won the race"; whether a client saw it is Lifecycle.ClientGone.
func (o RaceOutcome) IsWinner(a AttemptOutcome) bool {
	return o.WinnerNonce != 0 && a.Nonce == o.WinnerNonce
}

func (o RaceOutcome) failureReason(a AttemptOutcome) string {
	if a.PhaseTransitionAborted {
		return TimeoutReasonPhaseAborted
	}
	if reason := a.Terminal.reason(); reason != "" {
		return reason
	}
	if !a.NonceFinished {
		return ReasonNonceNotFinished
	}
	return ReasonUnknown
}
