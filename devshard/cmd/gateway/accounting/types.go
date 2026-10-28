// Package accounting answers where every committed nonce went: settlement counts nonces, so a burn and
// an unfinished send both cost a participant's record. See docs/accounting.md.
package accounting

import (
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/types"
)

const SchemaVersion = 10

type Disposition string

type Usage string

type Phase string

const (
	SlowReceipt  = 2500 * time.Millisecond
	SlowChunkGap = 5 * time.Second
	ClockDrift   = 5 * time.Second
	SlowDecode   = 40 * time.Millisecond
)

type EscrowMetadata struct {
	EscrowID      string
	Model         string
	CreationEpoch uint64
	Slots         []types.SlotAssignment
}

// One nonce's share of a race outcome. Sent is false for a stranded nonce, which is not a ghost.
type Attempt struct {
	Nonce             uint64
	RequestID         string
	OutputTokens      int64
	FirstContentDelay time.Duration
	Sent              bool
	Acknowledged      bool
	Usage             Usage
	Terminal          string
	Phase             Phase
	SlowReceipt       bool
	SlowChunk         bool
	ClockDrifted      bool
	SlowDecode        bool
	LogprobsDecoded   bool

	InputTokens       uint64
	InputTokensSource filters.InputTokensSource
}

// One bucket of classified nonces. See README.md, "How a nonce is classified".
type CounterKey struct {
	SlotID      uint32      `json:"slot_id"`
	Disposition Disposition `json:"disposition"`

	GhostReason     string `json:"ghost_reason,omitempty"`
	TimeoutKind     string `json:"timeout_kind,omitempty"`
	TimeoutAction   string `json:"timeout_action,omitempty"`
	TimeoutReason   string `json:"timeout_reason,omitempty"`
	Terminal        string `json:"terminal,omitempty"`
	Phase           Phase  `json:"phase,omitempty"`
	SlowReceipt     bool   `json:"slow_receipt,omitempty"`
	SlowChunk       bool   `json:"slow_chunk,omitempty"`
	ClockDrifted    bool   `json:"clock_drifted,omitempty"`
	SlowDecode      bool   `json:"slow_decode,omitempty"`
	LogprobsDecoded bool   `json:"logprobs_decoded,omitempty"`
}

// Overcounted is the impossible case: more classified than the chain assigned.
type SlotRecord struct {
	EscrowID    string `json:"escrow_id"`
	SlotID      uint32 `json:"slot_id"`
	Participant string `json:"participant"`
	nonceTotals
	rejected uint64
	hostActivity
	timeoutTally
}

// The furthest nonce an escrow reached: tells a quiet participant from a stalled escrow.
type EscrowNonce struct {
	EscrowID    string `json:"escrow_id"`
	LatestNonce uint64 `json:"latest_nonce"`
	Retired     bool   `json:"retired,omitempty"`
}

// CrossChecks holds both sides of every count the chain also keeps, and the size of their disagreement.
type CrossChecks struct {
	TimeoutApplied  uint64 `json:"timeout_applied"`
	HostMissed      uint64 `json:"host_missed"`
	RecordedInvalid uint64 `json:"recorded_invalid_transitions"`
	HostInvalid     uint64 `json:"host_invalid"`
	ErrorCount      uint64 `json:"error_count"`
}

type ParticipantRecord struct {
	SchemaVersion int       `json:"schema_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	EpochIndex    uint64    `json:"epoch_index"`
	Participant   string    `json:"participant"`
	Model         string    `json:"model"`

	nonceTotals
	hostActivity
	timeoutTally

	LatestNonces []EscrowNonce   `json:"latest_nonces"`
	CrossChecks  CrossChecks     `json:"cross_checks"`
	Capability   *HostCapability `json:"capability,omitempty"`

	Counters []CounterRecord `json:"counters"`
	Slots    []SlotRecord    `json:"slots"`
	Findings []Finding       `json:"findings"`
}

type EpochSummary struct {
	SchemaVersion int       `json:"schema_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	EpochIndex    uint64    `json:"epoch_index"`
	Participants  int       `json:"participants"`
	nonceTotals
	hostActivity
	FirstContentByModel []ModelFirstContent `json:"first_content_by_model"`
}

// A zero field constrains nothing; epoch zero is unconstrained rather than selectable.
type QueryFilter struct {
	EpochIndex  uint64
	Model       string
	Participant string
	EscrowIDs   []string
}

type CounterRecord struct {
	EscrowID string `json:"escrow_id"`
	CounterKey
	Count uint64 `json:"count"`
}
