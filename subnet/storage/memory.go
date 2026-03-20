package storage

import (
	"fmt"
	"sync"

	"subnet/types"
)

func copySignatures(src map[uint32][]byte) map[uint32][]byte {
	if src == nil {
		return nil
	}
	dst := make(map[uint32][]byte, len(src))
	for k, v := range src {
		dst[k] = append([]byte(nil), v...)
	}
	return dst
}

func copyGroup(src []types.SlotAssignment) []types.SlotAssignment {
	dst := make([]types.SlotAssignment, len(src))
	copy(dst, src)
	return dst
}

func copyWarmKeyDelta(src map[uint32]string) map[uint32]string {
	if src == nil {
		return nil
	}
	dst := make(map[uint32]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

type sessionData struct {
	escrowID      string
	creatorAddr   string
	config        types.SessionConfig
	group         []types.SlotAssignment
	balance       uint64
	diffs         []types.DiffRecord
	nonceToIndex  map[uint64]int
	lastFinalized uint64
	status        string // "active", "settled"
}

// Memory is an in-memory storage implementation for testing.
type Memory struct {
	mu       sync.RWMutex
	sessions map[string]*sessionData
}

func NewMemory() *Memory {
	return &Memory{
		sessions: make(map[string]*sessionData),
	}
}

func (m *Memory) CreateSession(params CreateSessionParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[params.EscrowID]; exists {
		return nil
	}

	m.sessions[params.EscrowID] = &sessionData{
		escrowID:     params.EscrowID,
		creatorAddr:  params.CreatorAddr,
		config:       params.Config,
		group:        copyGroup(params.Group),
		balance:      params.InitialBalance,
		nonceToIndex: make(map[uint64]int),
		status:       "active",
	}
	return nil
}

func (m *Memory) MarkSettled(escrowID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	s.status = "settled"
	return nil
}

func (m *Memory) ListActiveSessions() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []string
	for id, s := range m.sessions {
		if s.status == "active" {
			result = append(result, id)
		}
	}
	return result, nil
}

func (m *Memory) AppendDiff(escrowID string, rec types.DiffRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}

	if _, exists := s.nonceToIndex[rec.Nonce]; exists {
		return fmt.Errorf("duplicate nonce %d for session %s", rec.Nonce, escrowID)
	}

	rec.Signatures = copySignatures(rec.Signatures)
	rec.WarmKeyDelta = copyWarmKeyDelta(rec.WarmKeyDelta)

	s.diffs = append(s.diffs, rec)
	s.nonceToIndex[rec.Nonce] = len(s.diffs) - 1
	return nil
}

func (m *Memory) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}

	idx, ok := s.nonceToIndex[nonce]
	if !ok {
		return fmt.Errorf("diff at nonce %d not found for session %s", nonce, escrowID)
	}
	if s.diffs[idx].Signatures == nil {
		s.diffs[idx].Signatures = make(map[uint32][]byte)
	}
	sc := make([]byte, len(sig))
	copy(sc, sig)
	s.diffs[idx].Signatures[slotID] = sc
	return nil
}

func (m *Memory) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}

	meta := &SessionMeta{
		EscrowID:       s.escrowID,
		CreatorAddr:    s.creatorAddr,
		Config:         s.config,
		Group:          copyGroup(s.group),
		InitialBalance: s.balance,
		LastFinalized:  s.lastFinalized,
		Status:         s.status,
	}

	if len(s.diffs) > 0 {
		meta.LatestNonce = s.diffs[len(s.diffs)-1].Nonce
	}

	return meta, nil
}

func (m *Memory) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", escrowID)
	}

	idx, ok := s.nonceToIndex[nonce]
	if !ok {
		return nil, fmt.Errorf("diff at nonce %d not found for session %s", nonce, escrowID)
	}

	return copySignatures(s.diffs[idx].Signatures), nil
}

func (m *Memory) MarkFinalized(escrowID string, nonce uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}

	if nonce > s.lastFinalized {
		s.lastFinalized = nonce
	}
	return nil
}

func (m *Memory) LastFinalized(escrowID string) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return 0, fmt.Errorf("session %s not found", escrowID)
	}

	return s.lastFinalized, nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", escrowID)
	}

	var result []types.DiffRecord
	for _, d := range s.diffs {
		if d.Nonce < fromNonce || d.Nonce > toNonce {
			continue
		}
		dc := d
		dc.Signatures = copySignatures(d.Signatures)
		dc.WarmKeyDelta = copyWarmKeyDelta(d.WarmKeyDelta)
		result = append(result, dc)
	}

	return result, nil
}
