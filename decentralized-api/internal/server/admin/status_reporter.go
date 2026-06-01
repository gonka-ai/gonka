package admin

import (
	"decentralized-api/apiconfig"
)

// BuildMLNodeMessage produces the human-facing one-liner shown to the
// operator for the MLnode's current onboarding state.
//
// failingModel is included in the TEST_FAILED message when non-empty;
// the WAITING_FOR_POC message branches on whether the next PoC is
// inside the online-alert window.
func BuildMLNodeMessage(state MLNodeOnboardingState, secondsUntilNextPoC int64, failingModel string) string {
	switch state {
	case MLNodeState_TESTING:
		return "Testing MLnode configuration - model loading in progress"
	case MLNodeState_TEST_FAILED:
		if failingModel == "" {
			return "MLnode test failed"
		}
		return "MLnode test failed: model '" + failingModel + "' could not be loaded"
	case MLNodeState_WAITING_FOR_POC:
		if secondsUntilNextPoC < 0 {
			// Schedule not yet known (chain phase tracker not synced):
			// avoid an invented countdown like "in 0s".
			return "Waiting for next PoC cycle (schedule syncing)"
		}
		if secondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds {
			return "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) +
				") - MLnode must be online now"
		}
		return "Waiting for next PoC cycle (starts in " + formatShortDuration(secondsUntilNextPoC) +
			") - safe to be offline until " + formatShortDuration(apiconfig.OnlineAlertLeadSeconds) + " before PoC"
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
