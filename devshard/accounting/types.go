package accounting

import (
	"context"
	"time"

	"devshard/types"
)

const SchemaVersion = 1

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

// A timeout reason qualifies a non-applied outcome. Outcomes that explain
// themselves, such as insufficient_votes, carry no reason at all; only a
// missing qualifier where one is expected becomes unknown.
const (
	TimeoutPhaseTransitionAborted   TimeoutReason = "phase_transition_aborted"
	TimeoutLongResponseAfterContent TimeoutReason = "long_response_after_content"
	TimeoutStateRootDiverged        TimeoutReason = "escrow_state_root_diverged"
	TimeoutContextCanceled          TimeoutReason = "context_canceled"
	TimeoutDiffDeliveryFailed       TimeoutReason = "timeout_diff_delivery_failed"
	TimeoutNotApplied               TimeoutReason = "timeout_not_applied"
	TimeoutReasonUnknown            TimeoutReason = "unknown"
)

type ProtocolTransitionKind string

// An applied timeout is not a protocol transition event: HandleTimeout owns
// the timeout outcome, and HostStats is the independent protocol fact.
const (
	ProtocolReceiptApplied ProtocolTransitionKind = "receipt_applied"
	ProtocolFinishApplied  ProtocolTransitionKind = "finish_applied"
	ProtocolChallenged     ProtocolTransitionKind = "challenged"
	ProtocolValidated      ProtocolTransitionKind = "validated"
	ProtocolInvalidated    ProtocolTransitionKind = "invalidated"
)

type EscrowPhase string

const (
	EscrowActive     EscrowPhase = "active"
	EscrowFinalizing EscrowPhase = "finalizing"
	EscrowSettled    EscrowPhase = "settled"
)

type EscrowMetadata struct {
	EscrowID      string                 `json:"escrow_id"`
	CreationEpoch uint64                 `json:"creation_epoch"`
	Model         string                 `json:"model"`
	Slots         []types.SlotAssignment `json:"slots"`
	Phase         EscrowPhase            `json:"phase"`
}

type Event interface {
	accountingEvent()
}

type EscrowRegistered struct {
	Metadata EscrowMetadata
}

func (EscrowRegistered) accountingEvent() {}

type EscrowPhaseChanged struct {
	EscrowID string
	Phase    EscrowPhase
}

func (EscrowPhaseChanged) accountingEvent() {}

// DiffApplied records the protocol fact that a diff consumed Nonce. Inference
// is true only when the diff carries MsgStartInference with inference_id=Nonce.
type DiffApplied struct {
	EscrowID  string
	Nonce     uint64
	Inference bool
}

func (DiffApplied) accountingEvent() {}

type LatestNonceObserved struct {
	EscrowID    string
	LatestNonce uint64
}

func (LatestNonceObserved) accountingEvent() {}

type ProtocolTransition struct {
	EscrowID string
	Nonce    uint64
	Kind     ProtocolTransitionKind
}

func (ProtocolTransition) accountingEvent() {}

type Ghost struct {
	EscrowID      string
	Nonce         uint64
	DispatchPhase Phase
	Quarantine    QuarantineMode
	Reason        NoSendReason
	DetailReason  string
}

func (Ghost) accountingEvent() {}

type RealSend struct {
	EscrowID      string
	Nonce         uint64
	DispatchPhase Phase
	Quarantine    QuarantineMode
}

func (RealSend) accountingEvent() {}

type Winner struct {
	EscrowID string
	Nonce    uint64
}

func (Winner) accountingEvent() {}

type Loser struct {
	EscrowID string
	Nonce    uint64
}

func (Loser) accountingEvent() {}

type UsageUnknown struct {
	EscrowID string
	Nonce    uint64
}

func (UsageUnknown) accountingEvent() {}

type TimeoutRequired struct {
	EscrowID        string
	Nonce           uint64
	Kind            TimeoutKind
	EvaluationPhase Phase
	FailureOrigin   FailureOrigin
	DetailReason    string
}

func (TimeoutRequired) accountingEvent() {}

type TimeoutOutcomeRecorded struct {
	EscrowID string
	Nonce    uint64
	Outcome  TimeoutOutcome
	Reason   TimeoutReason
}

func (TimeoutOutcomeRecorded) accountingEvent() {}

// HostStatsObserved replaces the protocol HostStats facts for one slot.
type HostStatsObserved struct {
	EscrowID string
	SlotID   uint32
	Stats    types.HostStats
}

func (HostStatsObserved) accountingEvent() {}

// CounterKey identifies one bucket of classified nonces. Unresolved challenges
// and recorded invalid transitions are plain per-slot tallies and are kept
// outside this map rather than as key variants with every dimension empty.
type CounterKey struct {
	SlotID                 uint32         `json:"slot_id"`
	Disposition            Disposition    `json:"disposition,omitempty"`
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
	EscrowID string `json:"escrow_id"`
	CounterKey
	Count uint64 `json:"count"`
}

type EscrowNonce struct {
	EscrowID    string `json:"escrow_id"`
	LatestNonce uint64 `json:"latest_nonce"`
}

type SlotRecord struct {
	EscrowID             string                    `json:"escrow_id"`
	SlotID               uint32                    `json:"slot_id"`
	AssignedNonces       uint64                    `json:"assigned_nonces"`
	Dispositions         map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes      map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses       uint64                    `json:"protocol_misses"`
	ProtocolInvalid      uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges uint64                    `json:"unresolved_challenges"`
	RecordedInvalid      uint64                    `json:"recorded_invalid_transitions"`
	InFlight             uint64                    `json:"in_flight"`
	Unclassified         uint64                    `json:"unclassified"`
	UnknownReasonTotal   uint64                    `json:"unknown_reason_total"`
}

type CrossChecks struct {
	TimeoutApplied  uint64 `json:"timeout_applied"`
	HostMissed      uint64 `json:"host_missed"`
	RecordedInvalid uint64 `json:"recorded_invalid_transitions"`
	HostInvalid     uint64 `json:"host_invalid"`
	ErrorCount      uint64 `json:"error_count"`
}

type ParticipantRecord struct {
	SchemaVersion        int                       `json:"schema_version"`
	UpdatedAt            time.Time                 `json:"updated_at"`
	EpochIndex           uint64                    `json:"epoch_index"`
	Participant          string                    `json:"participant"`
	Model                string                    `json:"model"`
	LatestNonces         []EscrowNonce             `json:"latest_nonces"`
	AssignedNonces       uint64                    `json:"assigned_nonces"`
	Dispositions         map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes      map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses       uint64                    `json:"protocol_misses"`
	ProtocolInvalid      uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges uint64                    `json:"unresolved_challenges"`
	InFlight             uint64                    `json:"in_flight"`
	Unclassified         uint64                    `json:"unclassified"`
	UnknownReasonTotal   uint64                    `json:"unknown_reason_total"`
	RecordingErrors      uint64                    `json:"recording_errors"`
	WriterErrors         uint64                    `json:"writer_errors"`
	CrossChecks          CrossChecks               `json:"cross_checks"`
	Counters             []CounterRecord           `json:"counters"`
	Slots                []SlotRecord              `json:"slots"`
}

type EpochSummary struct {
	SchemaVersion        int                       `json:"schema_version"`
	UpdatedAt            time.Time                 `json:"updated_at"`
	EpochIndex           uint64                    `json:"epoch_index"`
	AssignedNonces       uint64                    `json:"assigned_nonces"`
	Dispositions         map[Disposition]uint64    `json:"dispositions"`
	TimeoutOutcomes      map[TimeoutOutcome]uint64 `json:"timeout_outcomes"`
	ProtocolMisses       uint64                    `json:"protocol_misses"`
	ProtocolInvalid      uint64                    `json:"protocol_invalid"`
	UnresolvedChallenges uint64                    `json:"unresolved_challenges"`
	InFlight             uint64                    `json:"in_flight"`
	Unclassified         uint64                    `json:"unclassified"`
	UnknownReasonTotal   uint64                    `json:"unknown_reason_total"`
	RecordingErrors      uint64                    `json:"recording_errors"`
	WriterErrors         uint64                    `json:"writer_errors"`
	CrossCheckErrors     uint64                    `json:"cross_check_errors"`
}

type QueryFilter struct {
	EpochIndex  uint64
	Model       string
	EscrowIDs   []string
	Participant string
}

type CurrentEpochFunc func(context.Context) (uint64, error)
