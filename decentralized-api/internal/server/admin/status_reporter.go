package admin

import (
	"decentralized-api/apiconfig"
)

// BuildMLNodeMessage produces the human-facing one-liner shown to the
// operator for the MLnode's current onboarding state.
//
// failingModel is included in the TEST_FAILED message when non-empty.
// validated gates the reassuring "safe to be offline" wording (only shown
// once a test has passed). shouldBeOnline reflects the current PoC window
// (it honors the phase, so during an active PoC it is true even though the
// countdown points to the next epoch) — when true we never tell the
// operator it is safe to be offline.
func BuildMLNodeMessage(state MLNodeOnboardingState, secondsUntilNextPoC int64, failingModel string, validated, shouldBeOnline bool) string {
	switch state {
	case MLNodeState_TESTING:
		return "Testing MLnode configuration - model loading in progress"
	case MLNodeState_TEST_FAILED:
		if failingModel == "" {
			return "MLnode test failed"
		}
		// Neutral wording: FailingModel is set for any failing stage (load,
		// health, inference probe, timeout), not just a load failure, so naming
		// the model without claiming "could not be loaded" avoids misdirecting
		// the operator. The precise per-stage error is on GET /nodes/:id/test.
		return "MLnode test failed: validation failed for model '" + failingModel + "'"
	case MLNodeState_WAITING_FOR_POC:
		if shouldBeOnline {
			// In or approaching the PoC window — must be online now,
			// regardless of the countdown (which points to the next epoch's
			// PoC once the current one has started).
			when := "PoC window active"
			if secondsUntilNextPoC >= 0 && secondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds {
				when = "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) + ")"
			}
			if validated {
				return when + " - MLnode must be online now"
			}
			return when + " - MLnode not yet validated, bring it online now"
		}
		switch {
		case secondsUntilNextPoC < 0 && validated:
			// Schedule not yet known (chain phase tracker not synced):
			// avoid an invented countdown like "in 0s".
			return "Waiting for next PoC cycle (schedule syncing)"
		case secondsUntilNextPoC < 0:
			return "MLnode not yet validated - it will be tested before the next PoC"
		case validated:
			return "Waiting for next PoC cycle (starts in " + formatShortDuration(secondsUntilNextPoC) +
				") - validated, safe to be offline until " + formatShortDuration(apiconfig.OnlineAlertLeadSeconds) + " before PoC"
		default:
			return "Waiting for next PoC cycle (starts in " + formatShortDuration(secondsUntilNextPoC) +
				") - MLnode not yet validated; it will be auto-tested, or run a manual test"
		}
	}
	return ""
}

// BuildParticipantMessage produces the participant-level guidance line.
func BuildParticipantMessage(state ParticipantState) string {
	switch state {
	case ParticipantState_ACTIVE_PARTICIPATING:
		return "Participant is in active set and participating"
	case ParticipantState_INACTIVE_WAITING:
		return "Participant not yet active - model assignment will occur after joining active set"
	case ParticipantState_UNKNOWN:
		return "Participant activity status unavailable - syncing with chain"
	}
	return ""
}

// BuildInactiveGuidance is shown alongside BuildParticipantMessage when
// the participant is not yet active, explaining what the system will
// do automatically.
func BuildInactiveGuidance(secondsUntilNextPoC int64) string {
	if secondsUntilNextPoC > apiconfig.AutoTestMinSecondsBeforePoC {
		return "MLnode will be tested automatically when there is more than " +
			formatShortDuration(apiconfig.AutoTestMinSecondsBeforePoC) + " until next PoC"
	}
	return ""
}
