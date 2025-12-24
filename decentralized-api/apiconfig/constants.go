package apiconfig

// MLNodeOnboardingState represents the onboarding state of an ML node
type MLNodeOnboardingState string

// Constants for MLNodeOnboardingState
const (
	MLNodeState_WAITING_FOR_POC MLNodeOnboardingState = "WAITING_FOR_POC"
	MLNodeState_TESTING         MLNodeOnboardingState = "TESTING"
	MLNodeState_TEST_FAILED     MLNodeOnboardingState = "TEST_FAILED"
)

// ParticipantState represents the state of a participant
type ParticipantState string

// Constants for ParticipantState
const (
	ParticipantState_INACTIVE_WAITING     ParticipantState = "INACTIVE_WAITING"
	ParticipantState_ACTIVE_PARTICIPATING ParticipantState = "ACTIVE_PARTICIPATING"
)
