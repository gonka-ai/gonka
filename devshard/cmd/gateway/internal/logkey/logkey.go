// Package logkey is the gateway's log vocabulary: every key a log line may carry, declared once and
// referenced by name, so a rename reaches every emitter through the compiler.
package logkey

const (
	Request             = "request"
	Escrow              = "escrow"
	Nonce               = "nonce"
	Model               = "model"
	Host                = "host"
	Hosts               = "hosts"
	Slot                = "slot"
	Role                = "role"
	Reason              = "reason"
	Kind                = "kind"
	Outcome             = "outcome"
	Action              = "action"
	Rewound             = "rewound"
	Error               = "error"
	BurnedDuringRequest = "burned_during_request"
	BurnsInARow         = "burns_in_a_row"

	BackoffCount        = "backoff_count"
	Strikes             = "strikes"
	CutOffForMS         = "cut_off_for_ms"
	EjectionCount       = "ejection_count"
	ConsecutiveFailures = "consecutive_failures"
	FailureRate         = "failure_rate"
	FailureVolume       = "failure_volume"
	WithheldForMS       = "withheld_for_ms"

	SweptDue     = "swept_due"
	SweptApplied = "swept_applied"
	SweptFailed  = "swept_failed"

	ContextLimit         = "context_limit"
	PreviousContextLimit = "previous_context_limit"

	ParticipantAllowlist    = "participant_allowlist"
	UnthrottledParticipants = "unthrottled_participants"
)

const (
	ReceiptTimeoutMS       = "receipt_timeout_ms"
	FirstTokenFloorMS      = "first_token_floor_ms"
	FirstTokenCeilingMS    = "first_token_ceiling_ms"
	InterChunkStallMS      = "inter_chunk_stall_ms"
	LoserGraceMS           = "loser_grace_ms"
	HedgeFirstTokenFloorMS = "hedge_first_token_floor_ms"
	MaxAttemptsPerRequest  = "max_attempts_per_request"
)

// Which deadlines an attempt's host was narrowed for.
const (
	MissedReceiptDeadline    = "missed_receipt_deadline"
	MissedFirstTokenDeadline = "missed_first_token_deadline"
)

const hostLabelLength = 8

func ShortHost(address string) string {
	if len(address) <= hostLabelLength {
		return address
	}
	return address[len(address)-hostLabelLength:]
}

// What an attempt delivered and how long each stage took.
const (
	Terminal          = "terminal"
	NonceFinished     = "nonce_finished"
	StateDivergent    = "state_divergent"
	LocalFault        = "local_fault"
	GatewayPressure   = "gateway_pressure"
	PhaseAborted      = "phase_aborted"
	ContentChunks     = "content_chunks"
	StreamChunks      = "stream_chunks"
	OutputBytes       = "output_bytes"
	UsageTokens       = "usage_tokens"
	DroppedEvents     = "dropped_events"
	MaxGapMS          = "max_gap_ms"
	UpstreamStatus    = "upstream_status"
	UpstreamBody      = "upstream_body"
	PendingTxs        = "pending_txs"
	LastChunkHead     = "last_chunk_head"
	FirstChunkHead    = "first_chunk_head"
	UsagePromptTokens = "usage_prompt_tokens"
	LogprobTokens     = "logprob_tokens"
	FinishReason      = "finish_reason"
	MaxGapAtChunk     = "max_gap_at_chunk"
	MeanGapMS         = "mean_gap_ms"
	ReceiptMS         = "receipt_ms"
	FirstTokenMS      = "first_token_ms"
	FirstContentMS    = "first_content_ms"
	AttemptMS         = "attempt_ms"
	FinishLagMS       = "finish_lag_ms"
)

// What the client asked for and what it got.
const (
	Stream            = "stream"
	InputTokens       = "input_tokens"
	InputTokensSource = "input_tokens_source"
	OutputTokens      = "output_tokens"
	Bytes             = "bytes"
	Terminated        = "terminated"
	DurationMS        = "duration_ms"
	DeliverError      = "deliver_error"
	CatchUpError      = "catch_up_error"
	Served            = "served"
	HostClockOffsetMS = "host_clock_offset_ms"
	HostReceiptMS     = "host_receipt_ms"
)

// The chain and the escrow set behind the request.
const (
	Epoch           = "epoch"
	Height          = "height"
	ProjectedHeight = "projected_height"
	StaleForMS      = "stale_for_ms"
	SwitchHeight    = "switch_height"
	Phase           = "phase"
	Tx              = "tx"
	Settler         = "settler"
	Created         = "created"
	Retired         = "retired"
	Promoted        = "promoted"
	Recorded        = "recorded"
	Used            = "used"
	OpenModels      = "open_models"
	Tokenizer       = "tokenizer"
	GatewayDisabled = "gateway_disabled"
	InFlight        = "in_flight"
	Attempts        = "attempts"
	EscrowBuilders  = "escrow_builders"
	Balance         = "balance"
	Reserved        = "reserved"
	Have            = "have"
	Need            = "need"
	Creates         = "creates"
	Retires         = "retires"
	Reasons         = "reasons"
	Free            = "free"
	MoneyShort      = "money_short"
	Liquid          = "liquid"
	FullCount       = "full_count"
	Want            = "want"
	Amount          = "amount"
	Counted         = "counted"
	MaxUnsettled    = "max_unsettled"
	ChainEpoch      = "chain_epoch"
	SettleBy        = "settle_by"
	MarginBlocks    = "margin_blocks"
	BlockTimeMS     = "block_time_ms"
	SettleWindowsMS = "settle_windows_ms"
	Configured      = "configured"
	Removed         = "removed"
)

// Process and transport.
const (
	Port             = "port"
	Addr             = "addr"
	StorageDir       = "storage_dir"
	Version          = "version"
	RoutePrefix      = "route_prefix"
	Subsystem        = "subsystem"
	IgnoredVariables = "ignored_variables"
	Route            = "route"
	Method           = "method"
	Status           = "status"

	SkippedLines = "skipped_lines"
)

// Which sources the height follower came up on. See ../../heights/README.md.
const (
	NodeManager = "node_manager"
	DirectChain = "direct_chain"
	CometRPC    = "comet_rpc"
)

// Which feed answers for the chain's operational governance. See ../../runtime_params.go.
const (
	ParamsSource = "params_source"
)
