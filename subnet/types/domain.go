package types

import "fmt"

// SessionPhase represents the phase of a subnet session.
type SessionPhase uint8

const (
	PhaseActive     SessionPhase = 0
	PhaseFinalizing SessionPhase = 1
	PhaseSettlement SessionPhase = 2
)

// InferenceStatus represents the lifecycle state of an inference.
type InferenceStatus uint8

const (
	StatusPending     InferenceStatus = iota
	StatusStarted
	StatusFinished
	StatusChallenged
	StatusValidated
	StatusInvalidated
	StatusTimedOut
)

// InferenceRecord tracks the state of a single inference within a session.
type InferenceRecord struct {
	Status       InferenceStatus
	ExecutorSlot uint32
	Model        string
	PromptHash   []byte
	ResponseHash []byte
	InputLength  uint64
	MaxTokens    uint64
	InputTokens  uint64
	OutputTokens uint64
	ReservedCost uint64
	ActualCost   uint64
	StartedAt    int64
	ConfirmedAt  int64
	VotesValid   uint32
	VotesInvalid uint32
	ValidatedBy  Bitmap128
}

// HostStats tracks per-host performance metrics within a session.
type HostStats struct {
	Missed               uint32
	Invalid              uint32
	Cost                 uint64
	RequiredValidations  uint32
	CompletedValidations uint32
}

// SessionConfig holds session-level parameters.
type SessionConfig struct {
	RefusalTimeout   int64  // seconds before reason=refused timeout
	ExecutionTimeout int64  // seconds before reason=execution timeout
	TokenPrice       uint64 // price per unit (flat per session)
	VoteThreshold    uint32 // minimum accept votes for timeout (total_slots / 2)
	ValidationRate   uint32 // basis points (10000 = 100%, 1000 = 10%)
}

// EscrowState is the full state of a subnet session.
type EscrowState struct {
	EscrowID    string
	Config      SessionConfig
	Group       []SlotAssignment
	Balance       uint64
	Phase         SessionPhase
	FinalizeNonce uint64
	Inferences    map[uint64]*InferenceRecord
	HostStats     map[uint32]*HostStats
	RevealedSeeds map[uint32]int64
	WarmKeys      map[uint32]string // slot ID -> warm key address, lazily populated
	LatestNonce   uint64
}

// Diff is the protocol primitive: what the user creates and signs.
// UserSig covers hash(proto_serialize(Nonce, Txs)).
// Txs uses the proto-generated SubnetTx with its oneof discriminator,
// which structurally guarantees exactly one tx type per entry.
type Diff struct {
	Nonce         uint64
	Txs           []*SubnetTx
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
