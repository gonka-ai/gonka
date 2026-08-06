package accounting

import (
	"context"
	"strings"
	"time"

	"devshard/types"
)

const SchemaVersion = 4

type Disposition string

const (
	DispositionProtocolOnly         Disposition = "protocol_only"
	DispositionGhost                Disposition = "ghost"
	DispositionFinishedUsed         Disposition = "finished_used"
	DispositionFinishedUnused       Disposition = "finished_unused"
	DispositionFinishedUsageUnknown Disposition = "finished_usage_unknown"
	DispositionUnfinishedRefused    Disposition = "unfinished_refused"
	DispositionUnfinishedExecution  Disposition = "unfinished_execution"
)

type Phase string

const (
	PhaseNormal          Phase = "normal"
	PhasePoC             Phase = "poc"
	PhaseConfirmationPoC Phase = "confirmation_poc"
)

type QuarantineMode string

const (
	QuarantineNone      QuarantineMode = "none"
	QuarantineProbe     QuarantineMode = "probe"
	QuarantineShadow    QuarantineMode = "shadow"
	QuarantineProbation QuarantineMode = "probation"
)

type NoSendReason string

const (
	NoSendPoCUnavailable         NoSendReason = "poc_unavailable_host"
	NoSendParticipantThrottled   NoSendReason = "participant_throttled_no_send"
	NoSendParticipantCapability  NoSendReason = "participant_capability_no_send"
	NoSendNoCompatibleAfterStale NoSendReason = "no_compatible_request_after_stale"
	NoSendUnknown                NoSendReason = "unknown"
)

type FailureOrigin string

const (
	FailureHostResponse     FailureOrigin = "host_response"
	FailureGatewayPolicy    FailureOrigin = "gateway_policy"
	FailureClient           FailureOrigin = "client"
	FailureTransportUnknown FailureOrigin = "transport_unknown"
)

type Usage string

const (
	UsageWinner       Usage = "winner"
	UsageLoser        Usage = "loser"
	UsageUnknownValue Usage = "unknown"
)

// UsageFor classifies an attempt against the race winner. Single definition:
// the gateway uses it to label the attempt span, the recorder to label the
// counter, so the two can never disagree.
func UsageFor(nonce, winnerNonce uint64) Usage {
	switch {
	case winnerNonce == 0:
		return UsageUnknownValue
	case nonce == winnerNonce:
		return UsageWinner
	default:
		return UsageLoser
	}
}

// DispositionForUsage maps a settled Usage onto its finished_* disposition.
// Returns "" for an unsettled attempt, which is not yet classifiable.
func DispositionForUsage(usage Usage) Disposition {
	switch usage {
	case UsageWinner:
		return DispositionFinishedUsed
	case UsageLoser:
		return DispositionFinishedUnused
	case UsageUnknownValue:
		return DispositionFinishedUsageUnknown
	default:
		return ""
	}
}

func settledUsage(usage Usage) bool { return DispositionForUsage(usage) != "" }

type TimeoutKind string

const (
	TimeoutRefused   TimeoutKind = "refused"
	TimeoutExecution TimeoutKind = "execution"
)

type TimeoutOutcome string

const (
	TimeoutSkipped              TimeoutOutcome = "skipped"
	TimeoutVoteCollectionFailed TimeoutOutcome = "vote_collection_failed"
	TimeoutInsufficientVotes    TimeoutOutcome = "insufficient_votes"
	TimeoutDiffSendFailed       TimeoutOutcome = "diff_send_failed"
	TimeoutApplied              TimeoutOutcome = "applied"
)

type TimeoutReason string

const (
	TimeoutPhaseTransitionAborted   TimeoutReason = "phase_transition_aborted"
	TimeoutLongResponseAfterContent TimeoutReason = "long_response_after_content"
	TimeoutStateRootDiverged        TimeoutReason = "escrow_state_root_diverged"
	TimeoutContextCanceled          TimeoutReason = "context_canceled"
	TimeoutDiffDeliveryFailed       TimeoutReason = "timeout_diff_delivery_failed"
	TimeoutNotApplied               TimeoutReason = "timeout_not_applied"
	TimeoutReasonUnknown            TimeoutReason = "unknown"
)

type ProtocolKind string

const (
	ProtocolReceiptApplied ProtocolKind = "receipt_applied"
	ProtocolFinishApplied  ProtocolKind = "finish_applied"
	ProtocolTimeoutApplied ProtocolKind = "timeout_applied"
	ProtocolChallenged     ProtocolKind = "challenged"
	ProtocolValidated      ProtocolKind = "validated"
	ProtocolInvalidated    ProtocolKind = "invalidated"
)

type EscrowPhase string

const (
	EscrowActive     EscrowPhase = "active"
	EscrowFinalizing EscrowPhase = "finalizing"
	EscrowFinalized  EscrowPhase = "finalized"
	EscrowSettled    EscrowPhase = "settled"
)

type EscrowMetadata struct {
	EscrowID             string                 `json:"escrow_id"`
	CreationEpoch        uint64                 `json:"creation_epoch"`
	Model                string                 `json:"model"`
	Slots                []types.SlotAssignment `json:"slots"`
	Phase                EscrowPhase            `json:"phase"`
	RefusalTimeout       int64                  `json:"refusal_timeout_seconds"`
	ExecutionTimeout     int64                  `json:"execution_timeout_seconds"`
	TimeoutBufferSeconds int64                  `json:"timeout_buffer_seconds"`
}

type TimeoutRecord struct {
	EscrowID      string
	Nonce         uint64
	Kind          TimeoutKind
	Phase         Phase
	Outcome       TimeoutOutcome
	Reason        TimeoutReason
	FailureOrigin FailureOrigin
	DetailReason  string
	Trace         TraceRef
}

type VerdictRecord struct {
	Nonce uint64
	Slot  uint32
	Kind  ProtocolKind
}

type CounterKey struct {
	SlotID                 uint32         `json:"slot_id"`
	Disposition            Disposition    `json:"disposition"`
	DispatchPhase          Phase          `json:"dispatch_phase,omitempty"`
	TimeoutEvaluationPhase Phase          `json:"timeout_evaluation_phase,omitempty"`
	QuarantineMode         QuarantineMode `json:"quarantine_mode,omitempty"`
	NoSendReason           NoSendReason   `json:"no_send_reason,omitempty"`
	FailureOrigin          FailureOrigin  `json:"failure_origin,omitempty"`
	DetailReason           string         `json:"detail_reason,omitempty"`
	TimeoutKind            TimeoutKind    `json:"timeout_kind,omitempty"`
	TimeoutOutcome         TimeoutOutcome `json:"timeout_outcome,omitempty"`
	TimeoutReason          TimeoutReason  `json:"timeout_reason,omitempty"`
}

type CounterRecord struct {
	EscrowID string     `json:"escrow_id"`
	Key      CounterKey `json:"key"`
	Count    uint64     `json:"count"`
}

type EscrowNonce struct {
	EscrowID    string      `json:"escrow_id"`
	LatestNonce uint64      `json:"latest_nonce"`
	Phase       EscrowPhase `json:"phase"`
}

type SlotRecord struct {
	EscrowID              string                    `json:"escrow_id"`
	SlotID                uint32                    `json:"slot_id"`
	AssignedNonces        uint64                    `json:"assigned_nonces"`
	Dispositions          map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes       map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses        uint64                    `json:"protocol_misses"`
	ProtocolInvalid       uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	RecordedInvalid       uint64                    `json:"recorded_invalid_transitions"`
	InFlight              uint64                    `json:"in_flight"`
	TimeoutPending        uint64                    `json:"timeout_pending"`
	PendingClassification uint64                    `json:"pending_classification"`
	Unclassified          uint64                    `json:"unclassified"`
	Overclassified        uint64                    `json:"overclassified"`
	UnknownReasonTotal    uint64                    `json:"unknown_reason_total"`
	CrossCheckError       uint64                    `json:"cross_check_error"`
}

type CrossChecks struct {
	TimeoutApplied  uint64 `json:"timeout_applied"`
	HostMissed      uint64 `json:"host_missed"`
	RecordedInvalid uint64 `json:"recorded_invalid_transitions"`
	HostInvalid     uint64 `json:"host_invalid"`
	ErrorCount      uint64 `json:"error_count"`
}

type ParticipantRecord struct {
	SchemaVersion         int                       `json:"schema_version"`
	UpdatedAt             time.Time                 `json:"updated_at"`
	EpochIndex            uint64                    `json:"epoch_index"`
	Participant           string                    `json:"participant"`
	Model                 string                    `json:"model"`
	LatestNonces          []EscrowNonce             `json:"latest_nonces"`
	AssignedNonces        uint64                    `json:"assigned_nonces"`
	Dispositions          map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes       map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses        uint64                    `json:"protocol_misses"`
	ProtocolInvalid       uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	InFlight              uint64                    `json:"in_flight"`
	TimeoutPending        uint64                    `json:"timeout_pending"`
	PendingClassification uint64                    `json:"pending_classification"`
	Unclassified          uint64                    `json:"unclassified"`
	Overclassified        uint64                    `json:"overclassified"`
	UnknownReasonTotal    uint64                    `json:"unknown_reason_total"`
	CrossChecks           CrossChecks               `json:"cross_checks"`
	Counters              []CounterRecord           `json:"counters"`
	Slots                 []SlotRecord              `json:"slots"`
}

type EpochSummary struct {
	SchemaVersion         int                       `json:"schema_version"`
	UpdatedAt             time.Time                 `json:"updated_at"`
	EpochIndex            uint64                    `json:"epoch_index"`
	AssignedNonces        uint64                    `json:"assigned_nonces"`
	Dispositions          map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes       map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses        uint64                    `json:"protocol_misses"`
	ProtocolInvalid       uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	InFlight              uint64                    `json:"in_flight"`
	TimeoutPending        uint64                    `json:"timeout_pending"`
	PendingClassification uint64                    `json:"pending_classification"`
	Unclassified          uint64                    `json:"unclassified"`
	Overclassified        uint64                    `json:"overclassified"`
	UnknownReasonTotal    uint64                    `json:"unknown_reason_total"`
	CrossCheckErrors      uint64                    `json:"cross_check_errors"`
}

type QueryFilter struct {
	EpochIndex  uint64
	Model       string
	EscrowIDs   []string
	Participant string
}

type CurrentEpochFunc func(context.Context) (uint64, error)

func NoSendReasonFromString(reason string) NoSendReason {
	switch value := NoSendReason(reason); value {
	case NoSendPoCUnavailable, NoSendParticipantThrottled, NoSendParticipantCapability, NoSendNoCompatibleAfterStale:
		return value
	default:
		return NoSendUnknown
	}
}

// NoSendFromReason splits a raw picker reason into the bounded no-send enum
// and the free-form detail that carries an unrecognised value.
func NoSendFromReason(reason string) (NoSendReason, string) {
	noSend := NoSendReasonFromString(reason)
	if noSend == NoSendUnknown {
		return noSend, reason
	}
	return noSend, ""
}

func QuarantineFromString(value string) QuarantineMode {
	switch value {
	case "probe":
		return QuarantineProbe
	case "shadow":
		return QuarantineShadow
	case "probation":
		return QuarantineProbation
	default:
		return QuarantineNone
	}
}

func TimeoutOutcomeFromAction(action, reason string) TimeoutOutcome {
	switch action {
	case "completed":
		return TimeoutApplied
	case "skipped":
		return TimeoutSkipped
	case "failed":
		return TimeoutOutcome(reason)
	default:
		return ""
	}
}

func TimeoutReasonFromString(outcome TimeoutOutcome, reason string) TimeoutReason {
	switch value := TimeoutReason(reason); value {
	case TimeoutPhaseTransitionAborted, TimeoutLongResponseAfterContent, TimeoutStateRootDiverged,
		TimeoutContextCanceled, TimeoutDiffDeliveryFailed, TimeoutNotApplied:
		return value
	}
	if outcome == TimeoutSkipped {
		return TimeoutReasonUnknown
	}
	switch outcome {
	case TimeoutVoteCollectionFailed, TimeoutInsufficientVotes, TimeoutDiffSendFailed:
		return TimeoutReasonUnknown
	}
	return ""
}

func FailureOriginFromDetail(detail string) FailureOrigin {
	switch {
	case detail == "context_canceled" || strings.Contains(detail, "client"):
		return FailureClient
	case detail == "phase_transition_aborted",
		detail == "long_response_after_content",
		detail == "timeout_not_applied",
		detail == "nonce_already_finished",
		strings.Contains(detail, "policy"):
		return FailureGatewayPolicy
	case detail == "not_finished",
		detail == "escrow_state_root_diverged",
		strings.Contains(detail, "http_"),
		strings.Contains(detail, "stream"),
		strings.Contains(detail, "response"):
		return FailureHostResponse
	default:
		return FailureTransportUnknown
	}
}
