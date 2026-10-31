package accounting

// The strings the ledger stores and serves. They are the report's contract with its readers -- a
// tracker, an operator -- and they are stored in the snapshot, so a rename is a migration
// and not a rename. Declared here once; the compiler carries any change to every site that names one.

// What became of a nonce. Every committed nonce ends in exactly one of these, and the whole ledger
// exists to say which.
const (
	DispositionGhost                Disposition = "ghost"
	DispositionFinishedUsed         Disposition = "finished_used"
	DispositionFinishedUnused       Disposition = "finished_unused"
	DispositionFinishedUsageUnknown Disposition = "finished_usage_unknown"
	DispositionUnfinishedRefused    Disposition = "unfinished_refused"
	DispositionUnfinishedExecution  Disposition = "unfinished_execution"
)

// What spent a nonce that carried no inference. See docs/accounting.md, "Service nonces".
type ServicePurpose string

const (
	ServiceHeartbeat ServicePurpose = "heartbeat"
	ServiceTimeout   ServicePurpose = "timeout"
	ServiceErrorMiss ServicePurpose = "error_miss"
	ServiceFinalize  ServicePurpose = "finalize"
	ServiceFlush     ServicePurpose = "flush"
)

// Whether the answer reached a client, and the chain phase the nonce was spent in.
const (
	UsageWinner  Usage = "winner"
	UsageLoser   Usage = "loser"
	UsageUnknown Usage = "unknown"

	PhaseNormal Phase = "normal"
	PhasePoC    Phase = "poc"
)

// How an attempt ended, as the ledger files it. The first three are absences rather than verdicts: a
// terminal nobody reported, one the race could not name, and one this build does not know.
const (
	TerminalUnreported   = "unreported"
	TerminalUnnamed      = "unnamed"
	TerminalUnclassified = "unclassified"

	TerminalWarmupProbe     = "warmup_probe"
	TerminalClientGone      = "client_gone_before_delivery"
	TerminalClientCancelled = "client_cancelled"
)

// A settled timeout round, in the ledger's outcome vocabulary; TimeoutNoOutcome is a round the ledger does not settle.
const (
	TimeoutNoOutcome            TimeoutOutcome = ""
	TimeoutSkipped              TimeoutOutcome = "skipped"
	TimeoutVoteCollectionFailed TimeoutOutcome = "vote_collection_failed"
	TimeoutInsufficientVotes    TimeoutOutcome = "insufficient_votes"
	TimeoutDiffSendFailed       TimeoutOutcome = "diff_send_failed"
	TimeoutApplied              TimeoutOutcome = "applied"
)

// The protocol facts the event ring keeps, as the drill-down under a participant's counters.
const (
	ProtocolTimeoutApplied ProtocolKind = "timeout_applied"
	ProtocolInvalidated    ProtocolKind = "invalidated"
)

// This gateway's reading of what the counters mean. A finding is derived on every read and never
// stored: the counters are the fact, the finding is only an interpretation of them. See
// docs/accounting.md for what each one says and what to check. SeverityNone is a rate that flagged nothing.
const (
	FindingExecutionTimeouts    = "execution_timeouts"
	FindingRefusals             = "refusals"
	FindingUnusedAnswers        = "answers_unused"
	FindingGatewayThrottled     = "throttled_by_gateway"
	FindingChainDisagreement    = "ledger_disagrees_with_chain"
	FindingLedgerOvercounted    = "ledger_overcounted"
	FindingChainMisses          = "chain_recorded_misses"
	FindingChainInvalid         = "chain_recorded_invalid"
	FindingUnresolvedChallenges = "challenges_unresolved"
	FindingUndecidedTimeouts    = "timeouts_undecided"
	FindingUnknownReasons       = "reasons_unknown"
	FindingStateDiverged        = "blocked_by_state_divergence"
	FindingDecodedLogprobs      = "logprobs_not_token_ids"
	FindingFailureTerminals     = "failure_terminals"
	FindingSlowReceipts         = "slow_receipts"
	FindingSlowChunks           = "slow_chunks"
	FindingClockDrift           = "clock_drift"
	FindingSlowDecode           = "slow_decode"

	SeverityNone     Severity = ""
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// FirstContentOverflowBound keys the first-content bucket past the largest bound. See docs/accounting.md, "Time to first content".
const FirstContentOverflowBound = "+Inf"

// ghostReasonThrottledBeforeTheSplit is what the scheduler wrote before a full window and an open cut-off
// were told apart. Ledger rows outlive a deploy, so the finding still has to count them.
const ghostReasonThrottledBeforeTheSplit = "participant_throttled_no_send"
