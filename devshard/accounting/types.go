package accounting

import (
	"context"
	"strings"
	"time"

	"devshard/types"
)

// 6 adds the capability block to a participant record and the protocol event feed.
const SchemaVersion = 6

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
	NoSendPoCUnavailable           NoSendReason = "poc_unavailable_host"
	NoSendParticipantThrottled     NoSendReason = "participant_throttled_no_send"
	NoSendParticipantStateDiverged NoSendReason = "participant_state_diverged_no_send"
	NoSendParticipantCapability    NoSendReason = "participant_capability_no_send"
	NoSendNoCompatibleAfterStale   NoSendReason = "no_compatible_request_after_stale"
	NoSendUnknown                  NoSendReason = "unknown"
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
	TimeoutEscrowGone               TimeoutReason = "escrow_gone_from_hosts"
	TimeoutReasonUnknown            TimeoutReason = "unknown"
	// Why a round failed to gather votes. The outcome says the round was lost; these say how.
	TimeoutVerifierVersionUnsupported TimeoutReason = "verifier_version_unsupported"
	TimeoutVerifierEscrowMissing      TimeoutReason = "verifier_escrow_missing"
	TimeoutVerifierInferenceMissing   TimeoutReason = "verifier_inference_missing"
	TimeoutVerifierUnreachable        TimeoutReason = "verifier_unreachable"
	TimeoutVoteWeightShort            TimeoutReason = "vote_weight_short"
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
}

type VerdictRecord struct {
	Nonce uint64
	Slot  uint32
	Kind  ProtocolKind
}

const (
	SlowReceiptAfter  = 2500 * time.Millisecond
	SlowChunkGapAfter = 5 * time.Second
	ClockDriftBeyond  = 5 * time.Second
	SlowDecodeAfter   = 40 * time.Millisecond
)

type AttemptTiming struct {
	Acknowledgement    time.Duration
	MaxChunkGap        time.Duration
	ClockOffset        time.Duration
	ClockMeasured      bool
	TimePerOutputToken time.Duration
}

func (t AttemptTiming) receiptWasSlow() bool { return t.Acknowledgement > SlowReceiptAfter }
func (t AttemptTiming) chunkWasSlow() bool   { return t.MaxChunkGap > SlowChunkGapAfter }
func (t AttemptTiming) decodeWasSlow() bool {
	return t.TimePerOutputToken > SlowDecodeAfter
}
func (t AttemptTiming) clockHasDrifted() bool {
	return t.ClockMeasured && (t.ClockOffset > ClockDriftBeyond || t.ClockOffset < -ClockDriftBeyond)
}

type CounterKey struct {
	SlotID                 uint32         `json:"slot_id"`
	Disposition            Disposition    `json:"disposition"`
	DispatchPhase          Phase          `json:"dispatch_phase,omitempty"`
	TimeoutEvaluationPhase Phase          `json:"timeout_evaluation_phase,omitempty"`
	QuarantineMode         QuarantineMode `json:"quarantine_mode,omitempty"`
	NoSendReason           NoSendReason   `json:"no_send_reason,omitempty"`
	FailureOrigin          FailureOrigin  `json:"failure_origin,omitempty"`
	LogprobsDecoded        bool           `json:"logprobs_decoded,omitempty"`
	SlowReceipt            bool           `json:"slow_receipt,omitempty"`
	SlowChunk              bool           `json:"slow_chunk,omitempty"`
	ClockDrifted           bool           `json:"clock_drifted,omitempty"`
	SlowDecode             bool           `json:"slow_decode,omitempty"`
	DetailReason           string         `json:"detail_reason,omitempty"`
	DeliveryReason         string         `json:"delivery_reason,omitempty"`
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
	DeliveryReasons       map[string]uint64         `json:"delivery_reasons,omitempty"`
	ProtocolMisses        uint64                    `json:"protocol_misses"`
	ProtocolInvalid       uint64                    `json:"protocol_invalid"`
	RequiredValidations   uint64                    `json:"required_validations"`
	CompletedValidations  uint64                    `json:"completed_validations"`
	ValidationsPerformed  uint64                    `json:"validations_performed"`
	TimeoutsApplied       uint64                    `json:"timeouts_applied"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	RecordedInvalid       uint64                    `json:"recorded_invalid_transitions"`
	InFlight              uint64                    `json:"in_flight"`
	InFlightRequests      uint64                    `json:"in_flight_requests"`
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
	RequiredValidations   uint64                    `json:"required_validations"`
	CompletedValidations  uint64                    `json:"completed_validations"`
	ValidationsPerformed  uint64                    `json:"validations_performed"`
	TimeoutsApplied       uint64                    `json:"timeouts_applied"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	InFlight              uint64                    `json:"in_flight"`
	InFlightRequests      uint64                    `json:"in_flight_requests"`
	TimeoutPending        uint64                    `json:"timeout_pending"`
	PendingClassification uint64                    `json:"pending_classification"`
	Unclassified          uint64                    `json:"unclassified"`
	Overclassified        uint64                    `json:"overclassified"`
	UnknownReasonTotal    uint64                    `json:"unknown_reason_total"`
	CrossChecks           CrossChecks               `json:"cross_checks"`
	Capability            *HostCapability           `json:"capability,omitempty"`
	Findings              []Finding                 `json:"findings"`
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
	RequiredValidations   uint64                    `json:"required_validations"`
	CompletedValidations  uint64                    `json:"completed_validations"`
	UnresolvedChallenges  uint64                    `json:"unresolved_challenges"`
	InFlight              uint64                    `json:"in_flight"`
	InFlightRequests      uint64                    `json:"in_flight_requests"`
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
	case NoSendPoCUnavailable, NoSendParticipantThrottled, NoSendParticipantStateDiverged, NoSendParticipantCapability, NoSendNoCompatibleAfterStale:
		return value
	default:
		return NoSendUnknown
	}
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

// reasonOrigin is the ledger's vocabulary of named reasons and who each one blames. A reason absent
// from it is one the ledger does not report, so a new cause has to be entered here to reach a counter.
var reasonOrigin = map[string]FailureOrigin{
	"context_canceled":             FailureClient,
	"phase_transition_aborted":     FailureGatewayPolicy,
	"long_response_after_content":  FailureGatewayPolicy,
	"timeout_not_applied":          FailureGatewayPolicy,
	"nonce_already_finished":       FailureGatewayPolicy,
	"not_finished":                 FailureHostResponse,
	"escrow_state_root_diverged":   FailureHostResponse,
	"timeout_diff_delivery_failed": FailureHostResponse,
	"escrow_gone_from_hosts":       FailureHostResponse,
	"verifier_version_unsupported": FailureTransportUnknown,
	"verifier_escrow_missing":      FailureTransportUnknown,
	"verifier_inference_missing":   FailureTransportUnknown,
	"verifier_unreachable":         FailureTransportUnknown,
	"vote_weight_short":            FailureTransportUnknown,
}

// originOfFragment places reasons that are generated rather than named: transport errors and stream
// states carry a host's own wording. Order decides ties, so the narrower fragment comes first.
var originOfFragment = []struct {
	fragment string
	origin   FailureOrigin
}{
	{"client", FailureClient},
	{"policy", FailureGatewayPolicy},
	{"http_", FailureHostResponse},
	{"stream", FailureHostResponse},
	{"response", FailureHostResponse},
}

// TimeoutReasonFromString reports why a timeout ended as it did. An outcome that ends a round without
// concluding it has no reason of its own, so it reads as unknown rather than repeating the outcome.
func TimeoutReasonFromString(outcome TimeoutOutcome, reason string) TimeoutReason {
	if _, named := reasonOrigin[reason]; named {
		return TimeoutReason(reason)
	}
	switch outcome {
	case TimeoutSkipped, TimeoutVoteCollectionFailed, TimeoutInsufficientVotes, TimeoutDiffSendFailed:
		return TimeoutReasonUnknown
	}
	return ""
}

// FailureOriginFromDetail attributes a failure. A named reason is settled first so one that merely
// contains another's word cannot be misfiled; anything unplaceable reached no one far enough to blame.
func FailureOriginFromDetail(detail string) FailureOrigin {
	if origin, named := reasonOrigin[detail]; named {
		return origin
	}
	for _, rule := range originOfFragment {
		if strings.Contains(detail, rule.fragment) {
			return rule.origin
		}
	}
	return FailureTransportUnknown
}
