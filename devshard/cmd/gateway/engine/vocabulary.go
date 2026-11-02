package engine

// The strings the engine puts on the wire: metric label values, log fields, and the facts the nonce
// ledger classifies on. They are a contract with everything downstream -- a dashboard query, an alert,
// a stored counter -- so they are declared here once and referenced by name. Renaming a constant breaks
// the build; editing its value silently moves the wire string under every panel that reads it.

// MissProofCompleteness is how much of an error stream a miss claim carried, reported on a claim the verifiers rejected; MissProofNone is a vote that carried no proof.
type MissProofCompleteness string

const (
	MissProofNone      MissProofCompleteness = ""
	MissProofWhole     MissProofCompleteness = "whole"
	MissProofPartial   MissProofCompleteness = "partial"
	MissProofTruncated MissProofCompleteness = "truncated"
)

// The named types of an attempt's wire strings: why it started, its place in the race, how it ended, what the client saw of it, and why it did not win.
type (
	StartReason       string
	AttemptRole       string
	AttemptResult     string
	AttemptVisibility string
	FailureReason     string
)

// Why an attempt started, reported on every nonce the race commits.
const (
	StartPrimary           StartReason = "primary"
	StartPrimarySuspicious StartReason = "primary_suspicious"
	StartPrimaryDegraded   StartReason = "primary_degraded"
)

const noTokenEstimate uint64 = 0

// EscalationStage names the condition that earned an attempt one more attempt beside it.
const (
	StageNone           EscalationStage = ""
	StageSuspicious     EscalationStage = "suspicious_host_immediate_escalation"
	StageAttemptFailed  EscalationStage = "attempt_failed"
	StageReceiptTimeout EscalationStage = "receipt_timeout_wait_elapsed"
	StageFirstToken     EscalationStage = "first_token_timeout_wait_elapsed"
	StageHedge          EscalationStage = "slow_start_wait_elapsed"
	StageStall          EscalationStage = "stall_before_content"
)

// What carried an answer's first renderable bytes, reported on every attempt that produced content.
const (
	sourceDeltaContent            = "delta.content"
	sourceDeltaReasoning          = "delta.reasoning"
	sourceDeltaReasoningContent   = "delta.reasoning_content"
	sourceDeltaToolCalls          = "delta.tool_calls"
	sourceMessageContent          = "message.content"
	sourceMessageReasoning        = "message.reasoning"
	sourceMessageReasoningContent = "message.reasoning_content"
	sourceMessageToolCalls        = "message.tool_calls"
	sourceStopWithTokens          = "message.empty_stop_completion_tokens"
)

// An attempt's place in its race, and why the crown fell to it.
const (
	RoleUnset       AttemptRole = ""
	RolePrimary     AttemptRole = "primary"
	RoleSpeculative AttemptRole = "speculative"

	crownFirstClaim           = "first_claim"
	crownNoRival              = "no_rival"
	crownRivalsMissedDeadline = "rivals_missed_their_deadline"
)

// How an attempt ended, and what the client saw of it. Visibility is the only account of a race that
// produced an answer nobody received.
const (
	AttemptOutcomeSuccess AttemptResult = "success"
	AttemptOutcomeFailed  AttemptResult = "failed"

	VisibilityWinner            AttemptVisibility = "user_visible_winner"
	VisibilityWinnerClientGone  AttemptVisibility = "winner_client_gone"
	VisibilityNoWinner          AttemptVisibility = "no_winner"
	VisibilitySuppressedLoser   AttemptVisibility = "suppressed_loser"
	VisibilityFailedNotFinished AttemptVisibility = "failed_not_finished"
)

// TimeoutKind is what a nonce was owed.
type TimeoutKind string

// TimeoutAction is what the gateway did about it.
type TimeoutAction string

// TimeoutReason is why.
type TimeoutReason string

// The vocabulary of a TimeoutEvent: what a nonce was owed, what the gateway did about it, and why. A
// reason outside this set normalises to "unknown" in the ledger's report, and the fact becomes invisible.
const (
	TimeoutKindUnset     TimeoutKind = ""
	TimeoutKindRefused   TimeoutKind = "refused"
	TimeoutKindExecution TimeoutKind = "execution"
	TimeoutKindErrorMiss TimeoutKind = "error_miss"

	TimeoutActionUnset     TimeoutAction = ""
	TimeoutActionSkipped   TimeoutAction = "skipped"
	TimeoutActionStarted   TimeoutAction = "started"
	TimeoutActionCompleted TimeoutAction = "completed"
	TimeoutActionFailed    TimeoutAction = "failed"
	TimeoutActionAbandoned TimeoutAction = "abandoned_by_restart"

	TimeoutReasonUnset           TimeoutReason = ""
	TimeoutReasonNone            TimeoutReason = "none"
	TimeoutReasonNoPoster        TimeoutReason = "no_poster"
	TimeoutReasonPhaseAborted    TimeoutReason = "phase_transition_aborted"
	TimeoutReasonEmptyStream     TimeoutReason = "empty_stream_without_non_empty_winner"
	TimeoutReasonNonceFinished   TimeoutReason = "nonce_already_finished"
	TimeoutReasonLongResponse    TimeoutReason = "long_response_after_content"
	TimeoutReasonCollectionError TimeoutReason = "timeout_collection_error"
	TimeoutReasonNotApplied      TimeoutReason = "timeout_not_applied"
	TimeoutReasonEscrowGone      TimeoutReason = "escrow_gone_from_hosts"
)

// How an attempt ended, as the ledger, the metrics and the logs all name it.
const (
	TerminalNameWon          = "won"
	TerminalNameLost         = "lost"
	TerminalNameUnclassified = "unclassified"
	TerminalNameUnnamed      = "unnamed"
)

// Why an attempt did not win. A terminal that names no reason is one that won or lost on merit.
const (
	ReasonUnset                 FailureReason = ""
	ReasonThrottled            FailureReason = "http_429"
	ReasonUnavailable          FailureReason = "http_503"
	ReasonForbidden            FailureReason = "http_forbidden"
	ReasonNotFound             FailureReason = "http_not_found"
	ReasonTimestampDrift       FailureReason = "http_timestamp_drift"
	ReasonRejected             FailureReason = "http_error"
	ReasonUpstreamServer       FailureReason = "http_server_error"
	ReasonOffPath              FailureReason = "off_path"
	ReasonDialFailure          FailureReason = "transport_error"
	ReasonStreamTruncated      FailureReason = "sse_truncated"
	ReasonUnexpectedEOF        FailureReason = "eof_transport"
	ReasonResponseTooLarge     FailureReason = "response_too_large"
	ReasonRequestTooLarge      FailureReason = "request_too_large"
	ReasonEscrowLookupLimited  FailureReason = "escrow_lookup_limited"
	ReasonClientCancelled      FailureReason = "client_cancelled"
	ReasonNoReceipt            FailureReason = "no_receipt"
	ReasonEmptyStream          FailureReason = "empty_stream"
	ReasonErrorStream          FailureReason = "error_stream"
	ReasonStalled              FailureReason = "stalled"
	ReasonHardTimeout          FailureReason = "hard_timeout"
	ReasonNonceNotFinished     FailureReason = "not_finished"
	ReasonUnknown              FailureReason = "unknown"
	ReasonAbandonedAfterCancel FailureReason = "abandoned_after_cancel"
	ReasonPhaseAborted                       = FailureReason(TimeoutReasonPhaseAborted)
)

// Why an attempt was started beside another, as the metric label names it.
const (
	EscalationReasonSuspicious    StartReason = "suspicious_host"
	EscalationReasonAttemptFailed StartReason = "attempt_failed"
	EscalationReasonReceipt       StartReason = "receipt_timeout"
	EscalationReasonFirstToken    StartReason = "first_token_timeout"
	EscalationReasonHedge         StartReason = "slow_start"
	EscalationReasonStall         StartReason = "stalled_before_content"
)
