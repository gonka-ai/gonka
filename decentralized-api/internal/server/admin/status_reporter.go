package admin

import (
	"decentralized-api/apiconfig"
)

// BuildMLNodeMessage produces the human-facing one-liner shown to the
// operator for the MLnode's current onboarding state.
//
// failingModel is included in the TEST_FAILED message when non-empty.
// validated gates the reassuring "waiting for PoC" wording: per the
// proposal, the calm "validated, safe to be offline" message is only
// shown once a test has passed; until then we say the MLnode is not yet
// validated (it still tells the operator to be online when PoC is near).
func BuildMLNodeMessage(state MLNodeOnboardingState, secondsUntilNextPoC int64, failingModel string, validated bool) string {
	switch state {
	case MLNodeState_TESTING:
		return "Testing MLnode configuration - model loading in progress"
	case MLNodeState_TEST_FAILED:
		if failingModel == "" {
			return "MLnode test failed"
		}
		return "MLnode test failed: model '" + failingModel + "' could not be loaded"
	case MLNodeState_WAITING_FOR_POC:
		switch {
		case secondsUntilNextPoC < 0 && validated:
			// Schedule not yet known (chain phase tracker not synced):
			// avoid an invented countdown like "in 0s".
			return "Waiting for next PoC cycle (schedule syncing)"
		case secondsUntilNextPoC < 0:
			return "MLnode not yet validated - it will be tested before the next PoC"
		case secondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds && validated:
			return "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) +
				") - MLnode must be online now"
		case secondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds:
			return "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) +
				") - MLnode not yet validated, bring it online now"
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
