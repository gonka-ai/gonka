package admin

import (
	"decentralized-api/apiconfig"
)

// MLNodeOnboardingState describes the user-visible onboarding state of
// an individual MLnode as derived at the API layer. The broker does
// not track or mutate this state — it is computed on each admin read
// from broker outputs and chain state.
type MLNodeOnboardingState string

const (
	MLNodeState_WAITING_FOR_POC MLNodeOnboardingState = "WAITING_FOR_POC"
	MLNodeState_TESTING         MLNodeOnboardingState = "TESTING"
	MLNodeState_TEST_FAILED     MLNodeOnboardingState = "TEST_FAILED"
)

// ParticipantState describes whether the participant the API is
// running on behalf of has joined the active set in the current epoch.
type ParticipantState string

const (
	ParticipantState_INACTIVE_WAITING     ParticipantState = "INACTIVE_WAITING"
	ParticipantState_ACTIVE_PARTICIPATING ParticipantState = "ACTIVE_PARTICIPATING"
)

// SecondsUntilPoCUnknown is the sentinel SecondsUntilNextPoC value used
// when the chain phase tracker has not synced yet. It suppresses
// timing-based alerting so onboarding never renders a bogus
// "PoC starting soon (in 0s)" derived from an unknown schedule.
const SecondsUntilPoCUnknown int64 = -1

// OnboardingStateInputs aggregates everything required to derive
// onboarding state for one MLnode. All fields are inputs computed
// elsewhere — none of these are mutated by the helpers in this file.
type OnboardingStateInputs struct {
	ParticipantActive   bool
	IsTesting           bool
	TestFailed          bool
	SecondsUntilNextPoC int64
}

// DeriveParticipantState reports whether the participant is in the
// active set right now.
func DeriveParticipantState(active bool) ParticipantState {
	if active {
		return ParticipantState_ACTIVE_PARTICIPATING
	}
	return ParticipantState_INACTIVE_WAITING
}

// DeriveMLNodeState computes the MLnode onboarding state for a node
// given the inputs. Returns the state and whether the user should be
// alerted to bring the MLnode online ("alert" implies the PoC window
// is approaching or active).
func DeriveMLNodeState(in OnboardingStateInputs) (state MLNodeOnboardingState, alert bool) {
	switch {
	case in.TestFailed:
		return MLNodeState_TEST_FAILED, true
	case in.IsTesting:
		return MLNodeState_TESTING, true
	case in.SecondsUntilNextPoC < 0:
		// Timing unknown (tracker not synced yet): waiting, but we have
		// no basis to alert the operator to come online.
		return MLNodeState_WAITING_FOR_POC, false
	case in.SecondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds:
		return MLNodeState_WAITING_FOR_POC, true
	default:
		return MLNodeState_WAITING_FOR_POC, false
	}
}
