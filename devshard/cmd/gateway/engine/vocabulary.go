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

// Why an attempt started, reported on every nonce the race commits.
const (
	StartPrimary           = "primary"
	StartPrimarySuspicious = "primary_suspicious"
	StartPrimaryDegraded   = "primary_degraded"
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
	RolePrimary     = "primary"
	RoleSpeculative = "speculative"

	crownFirstClaim           = "first_claim"
	crownNoRival              = "no_rival"
	crownRivalsMissedDeadline = "rivals_missed_their_deadline"
)

// How an attempt ended, and what the client saw of it. Visibility is the only account of a race that
// produced an answer nobody received.
const (
	AttemptOutcomeSuccess = "success"
	AttemptOutcomeFailed  = "failed"

	VisibilityWinner            = "user_visible_winner"
	VisibilityWinnerClientGone  = "winner_client_gone"
	VisibilityNoWinner          = "no_winner"
	VisibilitySuppressedLoser   = "suppressed_loser"
	VisibilityFailedNotFinished = "failed_not_finished"
)

// The vocabulary of a TimeoutEvent: what a nonce was owed, what the gateway did about it, and why. A
// reason outside this set normalises to "unknown" in the ledger's report, and the fact becomes invisible.
const (
	TimeoutKindRefused   = "refused"
	TimeoutKindExecution = "execution"
	TimeoutKindErrorMiss = "error_miss"

	TimeoutActionSkipped   = "skipped"
	TimeoutActionStarted   = "started"
	TimeoutActionCompleted = "completed"
	TimeoutActionFailed    = "failed"
	TimeoutActionAbandoned = "abandoned_by_restart"

	TimeoutReasonNone            = "none"
	TimeoutReasonNoPoster        = "no_poster"
	TimeoutReasonPhaseAborted    = "phase_transition_aborted"
	TimeoutReasonEmptyStream     = "empty_stream_without_non_empty_winner"
	TimeoutReasonNonceFinished   = "nonce_already_finished"
	TimeoutReasonLongResponse    = "long_response_after_content"
	TimeoutReasonCollectionError = "timeout_collection_error"
	TimeoutReasonNotApplied      = "timeout_not_applied"
	TimeoutReasonEscrowGone      = "escrow_gone_from_hosts"
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
	ReasonThrottled            = "http_429"
	ReasonUnavailable          = "http_503"
	ReasonForbidden            = "http_forbidden"
	ReasonNotFound             = "http_not_found"
	ReasonTimestampDrift       = "http_timestamp_drift"
	ReasonRejected             = "http_error"
	ReasonUpstreamServer       = "http_server_error"
	ReasonOffPath              = "off_path"
	ReasonDialFailure          = "transport_error"
	ReasonStreamTruncated      = "sse_truncated"
	ReasonUnexpectedEOF        = "eof_transport"
	ReasonResponseTooLarge     = "response_too_large"
	ReasonRequestTooLarge      = "request_too_large"
	ReasonEscrowLookupLimited  = "escrow_lookup_limited"
	ReasonClientCancelled      = "client_cancelled"
	ReasonNoReceipt            = "no_receipt"
	ReasonEmptyStream          = "empty_stream"
	ReasonErrorStream          = "error_stream"
	ReasonStalled              = "stalled"
	ReasonHardTimeout          = "hard_timeout"
	ReasonNonceNotFinished     = "not_finished"
	ReasonUnknown              = "unknown"
	ReasonAbandonedAfterCancel = "abandoned_after_cancel"
)

// Why an attempt was started beside another, as the metric label names it.
const (
	EscalationReasonSuspicious    = "suspicious_host"
	EscalationReasonAttemptFailed = "attempt_failed"
	EscalationReasonReceipt       = "receipt_timeout"
	EscalationReasonFirstToken    = "first_token_timeout"
	EscalationReasonHedge         = "slow_start"
	EscalationReasonStall         = "stalled_before_content"
)
