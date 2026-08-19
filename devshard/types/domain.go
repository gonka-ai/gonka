package types

import (
	"fmt"
	"strings"
)

// DevshardStateRootAndProtocolVersion is the default protocol name when no
// link-time stamp is set (plain `go test` / local builds). Release binaries set
// the protocol name via `make devshardd-build DEVSHARD_VERSION=<name>` — same
// as approved_versions.name. See devshard/docs/upgrade.md.
const DevshardStateRootAndProtocolVersion = "v2"

// DefaultStateRootVersion is the tag used when no explicit bind version is provided.
const DefaultStateRootVersion = DevshardStateRootAndProtocolVersion

// NormalizeVersion returns the state-root / settlement protocol tag, defaulting when empty.
func NormalizeVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return DefaultStateRootVersion
	}
	return version
}

// SessionPhase represents the phase of a devshard session.
type SessionPhase uint8

const (
	PhaseActive     SessionPhase = 0
	PhaseFinalizing SessionPhase = 1
	PhaseSettlement SessionPhase = 2
)

// InferenceStatus represents the lifecycle state of an inference.
type InferenceStatus uint8

const (
	StatusPending InferenceStatus = iota
	StatusStarted
	StatusFinished
	StatusChallenged
	StatusValidated
	StatusInvalidated
	StatusTimedOut
)

// InferenceRecord tracks the state of a single inference within a session.
type InferenceRecord struct {
	Status       InferenceStatus `json:"status"`
	ExecutorSlot uint32          `json:"executor_slot"`
	Model        string          `json:"model"`
	PromptHash   []byte          `json:"prompt_hash"`
	ResponseHash []byte          `json:"response_hash,omitempty"`
	InputLength  uint64          `json:"input_length"`
	MaxTokens    uint64          `json:"max_tokens"`
	InputTokens  uint64          `json:"input_tokens,omitempty"`
	OutputTokens uint64          `json:"output_tokens,omitempty"`
	ReservedCost uint64          `json:"reserved_cost"`
	ActualCost   uint64          `json:"actual_cost,omitempty"`
	StartedAt    int64           `json:"started_at"`
	ConfirmedAt  int64           `json:"confirmed_at,omitempty"`
	VotesValid   uint32          `json:"votes_valid,omitempty"`
	VotesInvalid uint32          `json:"votes_invalid,omitempty"`
	ValidatedBy  Bitmap128       `json:"validated_by,omitempty"`
}

// HostStats tracks per-host performance metrics within a session.
type HostStats struct {
	Missed               uint32
	Invalid              uint32
	Cost                 uint64
	RequiredValidations  uint32
	CompletedValidations uint32
}

// ProtocolVersion identifies the gateway/runtime protocol version for
// compatibility of rotation-created escrows and related registry stamps.
type ProtocolVersion string

const (
	ProtocolV1 ProtocolVersion = "1"
	ProtocolV2 ProtocolVersion = "2"
	ProtocolV3 ProtocolVersion = "3"
)

// ParseProtocolVersion parses a string into a ProtocolVersion.
// Empty string defaults to ProtocolV1.
func ParseProtocolVersion(s string) (ProtocolVersion, error) {
	switch strings.TrimSpace(s) {
	case "", string(ProtocolV1), "v1":
		return ProtocolV1, nil
	case string(ProtocolV2), "v2":
		return ProtocolV2, nil
	case string(ProtocolV3), "v3":
		return ProtocolV3, nil
	default:
		return "", fmt.Errorf("unknown protocol version %q", s)
	}
}

// SessionConfig holds session-level parameters.
type SessionConfig struct {
	RefusalTimeout             int64  // seconds before reason=refused timeout
	ExecutionTimeout           int64  // seconds before reason=execution timeout
	TokenPrice                 uint64 // price per input / output token (flat per session)
	CreateDevshardFee          uint64 // one-time fee charged when creating a devshard session
	FeePerNonce                uint64 // fee charged per applied nonce (diff)
	// VoteThreshold is frozen in state.Config at session creation (from escrow lane A).
	// Consensus logic must read it only via state.StateMachine (applyValidationVote,
	// applyTimeout); external packages use StateMachine.VoteThreshold() for display.
	VoteThreshold              uint32
	ValidationRate             uint32 // basis points (10000 = 100%, 1000 = 10%)
	InferenceSealGraceNonces   uint32
	InferenceSealGraceSeconds  uint32
	AutoSealEveryNNonces       uint32
}

// EscrowState is the full state of a devshard session.
type EscrowState struct {
	EscrowID string
	// StateRootAndProtocolVersion is the protocol tag stamped at session creation
	// (WithStateRootAndProtocolVersion) and copied into settlement payloads. It
	// matches CreateSessionParams.Version / host boundVersion (approved_versions.name).
	StateRootAndProtocolVersion string
	Config        SessionConfig
	Group         []SlotAssignment
	Balance       uint64
	Fees          uint64 // total fees collected (devshard create + per-nonce)
	Phase         SessionPhase
	FinalizeNonce uint64
	Inferences    map[uint64]*InferenceRecord
	HostStats     map[uint32]*HostStats
	WarmKeys      map[uint32]string // slot ID -> warm key address, lazily populated
	LatestNonce   uint64

	// Height-sync forced turn + cadence swallow (hashed into state root).
	HeightSyncForcedStart         uint64
	HeightSyncForcedEnd           uint64 // inclusive; cleared once LatestNonce > ForcedEnd
	HeightSyncCadenceSwallowUntil uint64 // suppress periodic Anchor on (SwallowFe, SwallowUntil]
	HeightSyncSwallowFe           uint64
	HeightSyncTurnK               uint64 // snapshot K from opening MsgForceHeightSyncTurn
	HeightSyncTurnSlots           uint64
	HeightSyncTurnReason          string

	// SealedAcc is the Phase 1 incremental accumulator over sealed inference
	// commitments (32 bytes). Updated on each SealInference and settlement drain.
	SealedAcc []byte `json:"sealed_acc,omitempty"`
}

// HeightSyncEscrowCommit is the subset of EscrowState hashed into the state root.
type HeightSyncEscrowCommit struct {
	ForcedStart, ForcedEnd         uint64
	CadenceSwallowUntil, SwallowFe uint64
	TurnK, TurnSlots               uint64
	Reason                         string
}

// HeightSyncEscrowCommitFromState extracts height-sync fields for state hashing.
func HeightSyncEscrowCommitFromState(s *EscrowState) HeightSyncEscrowCommit {
	if s == nil {
		return HeightSyncEscrowCommit{}
	}
	return HeightSyncEscrowCommit{
		ForcedStart:         s.HeightSyncForcedStart,
		ForcedEnd:           s.HeightSyncForcedEnd,
		CadenceSwallowUntil: s.HeightSyncCadenceSwallowUntil,
		SwallowFe:           s.HeightSyncSwallowFe,
		TurnK:               s.HeightSyncTurnK,
		TurnSlots:           s.HeightSyncTurnSlots,
		Reason:              s.HeightSyncTurnReason,
	}
}

// Diff is the protocol primitive: what the user creates and signs.
// UserSig covers hash(proto_serialize(Nonce, Txs)).
// Txs uses the proto-generated DevshardTx with its oneof discriminator,
// which structurally guarantees exactly one tx type per entry.
type Diff struct {
	Nonce         uint64
	Txs           []*DevshardTx
	UserSig       []byte
	PostStateRoot []byte
}

// DiffRecord is the storage representation: Diff + computed metadata.
type DiffRecord struct {
	Diff
	StateHash    []byte
	Signatures   map[uint32][]byte
	WarmKeyDelta map[uint32]string // warm key bindings introduced at this nonce
	CreatedAt    int64
}

// ComputeWarmKeyDelta returns entries in after that are not in before.
func ComputeWarmKeyDelta(before, after map[uint32]string) map[uint32]string {
	if len(after) == 0 {
		return nil
	}
	var delta map[uint32]string
	for slotID, addr := range after {
		if before[slotID] != addr {
			if delta == nil {
				delta = make(map[uint32]string)
			}
			delta[slotID] = addr
		}
	}
	return delta
}

// SlotAssignment maps a slot to a validator in the session group.
// SlotIDs must be compact indices 0..len(group)-1 (required by Bitmap128).
type SlotAssignment struct {
	SlotID           uint32
	ValidatorAddress string
}

// ValidateGroup checks that group[i].SlotID == i for all entries, group size
// is within bounds, and the group is non-empty. This ordering invariant is
// required by direct indexing in transport and user code: group[slotID].
func ValidateGroup(group []SlotAssignment) error {
	n := len(group)
	if n == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidGroup)
	}
	if n > MaxGroupSize {
		return fmt.Errorf("%w: %d slots exceeds max %d", ErrInvalidGroup, n, MaxGroupSize)
	}
	for i, s := range group {
		if s.SlotID != uint32(i) {
			return fmt.Errorf("%w: group[%d].SlotID = %d, want %d", ErrInvalidGroup, i, s.SlotID, i)
		}
	}
	return nil
}
