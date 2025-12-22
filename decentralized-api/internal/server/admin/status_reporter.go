package admin

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type StatusReporter struct{}

func NewStatusReporter() *StatusReporter { return &StatusReporter{} }

func (r *StatusReporter) BuildMLNodeMessage(state MLNodeOnboardingState, secondsUntilNextPoC int64, failingModel string) string {
	switch state {
	case MLNodeState_TESTING:
		return "Testing MLnode configuration - model loading in progress"
	case MLNodeState_TEST_FAILED:
		if failingModel == "" {
			return "MLnode test failed"
		}
		return "MLnode test failed: model '" + failingModel + "' could not be loaded"
	case MLNodeState_WAITING_FOR_POC:
		if secondsUntilNextPoC <= 600 {
			return "PoC starting soon (in " + formatShortDuration(secondsUntilNextPoC) + ") - MLnode must be online now"
		}
		return "Waiting for next PoC cycle (starts in " + formatShortDuration(secondsUntilNextPoC) + ") - you can safely turn off the server and restart it 10 minutes before PoC"
	default:
		return ""
	}
}

func (r *StatusReporter) BuildParticipantMessage(pstate ParticipantState) string {
	switch pstate {
	case ParticipantState_ACTIVE_PARTICIPATING:
		return "Participant is in active set and participating"
	case ParticipantState_INACTIVE_WAITING:
		return "Participant not yet active - model assignment will occur after joining active set"
	default:
		return ""
	}
}

func (r *StatusReporter) BuildNoModelGuidance(secondsUntilNextPoC int64) string {
	if secondsUntilNextPoC > 3600 {
		return "MLnode will be tested automatically when there is more than 1 hour until next PoC"
	}
	return ""
}

func (r *StatusReporter) LogOnboardingTransition(prev MLNodeOnboardingState, next MLNodeOnboardingState) {
	logging.Info("Onboarding state transition", types.Nodes, "prev", string(prev), "next", string(next))
}

func (r *StatusReporter) LogTesting(message string) {
	logging.Info(message, types.Nodes)
}

func (r *StatusReporter) LogParticipantStatusChange(prev ParticipantState, next ParticipantState) {
	logging.Info("Participant status change", types.Participants, "prev", string(prev), "next", string(next))
}

func (r *StatusReporter) LogTimingGuidance(secondsUntilNextPoC int64) {
	logging.Info("Timing guidance", types.Nodes, "seconds_until_next_poc", secondsUntilNextPoC)
}
