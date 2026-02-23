package types

// SetParticipantReason indicates why SetParticipant was called.
// Use SetParticipantReasonNone for validation/finish/start/epoch/genesis — skip status computation, only ensure CurrentEpochStats is set.
// Use a specific reason to run only the corresponding status check(s).
type SetParticipantReason int

const (
	SetParticipantReasonNone SetParticipantReason = iota
	SetParticipantReasonCompletedInference
	SetParticipantReasonMissedInference
	SetParticipantReasonInvalidation
	SetParticipantReasonConfirmationPoC
)
