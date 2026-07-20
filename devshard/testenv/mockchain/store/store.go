package store

import (
	"sync"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// GranteeKey identifies warm-key grant lookups.
type GranteeKey struct {
	GranterAddress string
	MessageTypeURL string
}

// EpochGroupKey identifies epoch group data by epoch + model.
type EpochGroupKey struct {
	EpochIndex uint64
	ModelID    string
}

// Store is the shared in-memory chain state for mock-chain (3a/3b/3c).
type Store struct {
	mu sync.RWMutex

	ChainID     string
	BlockHeight int64
	// ParamsBlockHeight is the chain height at which DevshardEscrowParams were last
	// published (mock-chain analogue of dapi params_block_height).
	ParamsBlockHeight int64
	// NextPocStartBlockHeight is the upcoming epoch PoC anchor (EpochContext.NextPoCStart).
	NextPocStartBlockHeight int64
	Params                 inferencetypes.Params
	Epoch             inferencetypes.Epoch

	Participants   map[string]*inferencetypes.Participant
	Escrows        map[uint64]*inferencetypes.DevshardEscrow
	Grantees       map[GranteeKey][]inferencetypes.Grantee
	EpochGroupData map[EpochGroupKey]*inferencetypes.EpochGroupData
	// Pubkeys maps bech32 address → base64-encoded compressed secp256k1 pubkey
	// for AccountByAddress (payload auth during validation).
	Pubkeys map[string]string

	Accounts     map[string]*Account
	nextEscrowID uint64

	// escrowQueryFault, when true, makes DevshardEscrow gRPC queries fail. It
	// lets citest simulate an unavailable request-time escrow fetch path so the
	// devshardd escrow long-poll warm cache is exercised.
	escrowQueryFault bool
}

// Account holds Cosmos auth sequence state for LCD tx signing (Phase 3c).
type Account struct {
	AccountNumber uint64
	Sequence      uint64
}

// New returns an empty store.
func New() *Store {
	return &Store{
		Participants:   make(map[string]*inferencetypes.Participant),
		Escrows:        make(map[uint64]*inferencetypes.DevshardEscrow),
		Grantees:       make(map[GranteeKey][]inferencetypes.Grantee),
		EpochGroupData: make(map[EpochGroupKey]*inferencetypes.EpochGroupData),
		Pubkeys:        make(map[string]string),
		Accounts:       make(map[string]*Account),
		nextEscrowID:   1,
	}
}

// InitAfterLoad recomputes counters after YAML seed or Replace.
func (s *Store) InitAfterLoad() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reinitEscrowCounterLocked()
}

// Replace installs a full snapshot (used by seed loader).
func (s *Store) Replace(other *Store) {
	if other == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ChainID = other.ChainID
	s.BlockHeight = other.BlockHeight
	s.ParamsBlockHeight = other.ParamsBlockHeight
	s.NextPocStartBlockHeight = other.NextPocStartBlockHeight
	s.Params = other.Params
	s.Epoch = other.Epoch
	s.Participants = cloneParticipantMap(other.Participants)
	s.Escrows = cloneEscrowMap(other.Escrows)
	s.Grantees = cloneGranteeMap(other.Grantees)
	s.EpochGroupData = cloneEpochGroupMap(other.EpochGroupData)
	if other.Pubkeys != nil {
		s.Pubkeys = cloneStringMap(other.Pubkeys)
	} else {
		s.Pubkeys = make(map[string]string)
	}
	if other.Accounts != nil {
		s.Accounts = cloneAccountMap(other.Accounts)
	} else {
		s.Accounts = make(map[string]*Account)
	}
	s.reinitEscrowCounterLocked()
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneParticipantMap(in map[string]*inferencetypes.Participant) map[string]*inferencetypes.Participant {
	out := make(map[string]*inferencetypes.Participant, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		c := *v
		out[k] = &c
	}
	return out
}

func cloneEscrowMap(in map[uint64]*inferencetypes.DevshardEscrow) map[uint64]*inferencetypes.DevshardEscrow {
	out := make(map[uint64]*inferencetypes.DevshardEscrow, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		c := *v
		if v.Slots != nil {
			c.Slots = append([]string(nil), v.Slots...)
		}
		out[k] = &c
	}
	return out
}

func cloneGranteeMap(in map[GranteeKey][]inferencetypes.Grantee) map[GranteeKey][]inferencetypes.Grantee {
	out := make(map[GranteeKey][]inferencetypes.Grantee, len(in))
	for k, v := range in {
		out[k] = append([]inferencetypes.Grantee(nil), v...)
	}
	return out
}

func cloneEpochGroupMap(in map[EpochGroupKey]*inferencetypes.EpochGroupData) map[EpochGroupKey]*inferencetypes.EpochGroupData {
	out := make(map[EpochGroupKey]*inferencetypes.EpochGroupData, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		c := *v
		if v.ModelSnapshot != nil {
			ms := *v.ModelSnapshot
			c.ModelSnapshot = &ms
		}
		out[k] = &c
	}
	return out
}

// ChainID returns the configured chain id.
func (s *Store) GetChainID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ChainID
}

// BlockHeight returns the latest block height.
func (s *Store) GetBlockHeight() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.BlockHeight
}

// Params returns module params.
func (s *Store) GetParams() inferencetypes.Params {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Params
}

// Epoch returns the current epoch.
func (s *Store) GetEpoch() inferencetypes.Epoch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Epoch
}

// GetPubkey returns the base64 compressed secp256k1 pubkey for address, or "".
func (s *Store) GetPubkey(address string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Pubkeys == nil {
		return ""
	}
	return s.Pubkeys[address]
}

// Participant returns a participant by address or nil.
func (s *Store) GetParticipant(address string) *inferencetypes.Participant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.Participants[address]
	if !ok || p == nil {
		return nil
	}
	c := *p
	return &c
}

// Escrow returns an escrow by id or nil.
func (s *Store) GetEscrow(id uint64) *inferencetypes.DevshardEscrow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.Escrows[id]
	if !ok || e == nil {
		return nil
	}
	c := *e
	if e.Slots != nil {
		c.Slots = append([]string(nil), e.Slots...)
	}
	return &c
}

// Grantees returns grantees for a granter + message type.
func (s *Store) GetGrantees(granter, messageTypeURL string) []inferencetypes.Grantee {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := GranteeKey{GranterAddress: granter, MessageTypeURL: messageTypeURL}
	return append([]inferencetypes.Grantee(nil), s.Grantees[key]...)
}

// EpochGroupData returns epoch group data for epoch + model or nil.
func (s *Store) GetEpochGroupData(epochIndex uint64, modelID string) *inferencetypes.EpochGroupData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := EpochGroupKey{EpochIndex: epochIndex, ModelID: modelID}
	egd, ok := s.EpochGroupData[key]
	if !ok || egd == nil {
		return nil
	}
	c := *egd
	if egd.ModelSnapshot != nil {
		ms := *egd.ModelSnapshot
		c.ModelSnapshot = &ms
	}
	return &c
}

// PutEscrow inserts or replaces an escrow record (used by RPC event injection and Phase 3c).
func (s *Store) PutEscrow(e *inferencetypes.DevshardEscrow) {
	if e == nil || e.Id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *e
	if e.Slots != nil {
		c.Slots = append([]string(nil), e.Slots...)
	}
	s.Escrows[e.Id] = &c
}

// SetEscrowQueryFault toggles DevshardEscrow query failures (citest fault injection).
func (s *Store) SetEscrowQueryFault(faulted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.escrowQueryFault = faulted
}

// EscrowQueryFaulted reports whether DevshardEscrow queries should fail.
func (s *Store) EscrowQueryFaulted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.escrowQueryFault
}

// AdvanceBlock increments the latest block height and returns the new value.
func (s *Store) AdvanceBlock() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.BlockHeight == 0 {
		s.BlockHeight = 1
	} else {
		s.BlockHeight++
	}
	return s.BlockHeight
}

// SetBlockHeight sets the block height explicitly (tests).
func (s *Store) SetBlockHeight(height int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BlockHeight = height
}
