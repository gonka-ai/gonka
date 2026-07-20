package storage

import (
	"fmt"
	"sync"

	"devshard/types"
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

type snapshotData struct {
	nonce uint64
	data  []byte
}

type sessionData struct {
	escrowID      string
	epochID       uint64
	version       string
	creatorAddr   string
	config        types.SessionConfig
	group         []types.SlotAssignment
	balance       uint64
	diffs         []types.DiffRecord
	nonceToIndex  map[uint64]int
	lastFinalized uint64
	status        string // "active", "settled"
	snapshot      *snapshotData
	inferences              map[uint64]InferenceRow
	inferenceValidationObs  map[uint64]map[uint32]SlotValidationObs
	sealedValidationObs     map[uint64]map[uint32]SlotValidationObs
}

// Memory is an in-memory storage implementation for testing.
type Memory struct {
	mu               sync.RWMutex
	sessions         map[string]*sessionData
	validationLeases map[string]map[uint64]memoryLease
}

func NewMemory() *Memory {
	return &Memory{
		sessions: make(map[string]*sessionData),
	}
}

func (m *Memory) CreateSession(params CreateSessionParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	params.Config = types.NormalizeSessionConfig(params.Config, len(params.Group))
	requestedVersion, err := requireSessionVersion(params.Version)
	if err != nil {
		return err
	}
	if existing, exists := m.sessions[params.EscrowID]; exists {
		if existing.epochID != params.EpochID {
			return fmt.Errorf("%w: escrow %s exists in epoch %d, requested epoch %d",
				ErrSessionEpochConflict, params.EscrowID, existing.epochID, params.EpochID)
		}
		if existing.version != requestedVersion {
			return fmt.Errorf("%w: escrow %s exists with version %s, requested %s",
				ErrSessionVersionConflict, params.EscrowID, existing.version, requestedVersion)
		}
		return nil
	}

	m.sessions[params.EscrowID] = &sessionData{
		escrowID:     params.EscrowID,
		epochID:      params.EpochID,
		version:      requestedVersion,
		creatorAddr:  params.CreatorAddr,
		config:       params.Config,
		group:        copyGroup(params.Group),
		balance:      params.InitialBalance,
		nonceToIndex: make(map[uint64]int),
		status:       "active",
		inferences:             make(map[uint64]InferenceRow),
		inferenceValidationObs: make(map[uint64]map[uint32]SlotValidationObs),
		sealedValidationObs:    make(map[uint64]map[uint32]SlotValidationObs),
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

func (m *Memory) ListActiveSessions() ([]ActiveSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []ActiveSession
	for id, s := range m.sessions {
		if s.status == "active" {
			result = append(result, ActiveSession{EscrowID: id, EpochID: s.epochID})
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

// AppendDiffs appends many diffs under one lock (used by HA migrate).
func (m *Memory) AppendDiffs(escrowID string, diffs []types.DiffRecord) error {
	for _, rec := range diffs {
		if err := m.AppendDiff(escrowID, rec); err != nil {
			return err
		}
	}
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
		EpochID:        s.epochID,
		Version:        s.version,
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

	if err := finalizeSessionMeta(meta); err != nil {
		return nil, err
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

func (m *Memory) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	if s.snapshot != nil && nonce < s.snapshot.nonce {
		return nil
	}
	cp := append([]byte(nil), data...)
	s.snapshot = &snapshotData{nonce: nonce, data: cp}
	return nil
}

func (m *Memory) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok || s.snapshot == nil {
		return 0, nil, ErrSnapshotNotFound
	}
	return s.snapshot.nonce, append([]byte(nil), s.snapshot.data...), nil
}

func (m *Memory) InsertSealedInference(escrowID string, row InferenceRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	s.inferences[row.InferenceID] = row
	return nil
}

func (m *Memory) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return InferenceRow{}, false, fmt.Errorf("session %s not found", escrowID)
	}
	row, exists := s.inferences[inferenceID]
	if !exists {
		return InferenceRow{}, false, nil
	}
	return row, true, nil
}

func (m *Memory) DeleteSealedInferences(escrowID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	s.inferences = make(map[uint64]InferenceRow)
	return nil
}

func (m *Memory) ClearValidationObs(escrowID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	s.inferenceValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	s.sealedValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	return nil
}

// ImportValidationObs replaces live/sealed validation-obs maps for an escrow
// with the provided rows (HA migrate).
func (m *Memory) ImportValidationObs(escrowID string, live, sealed []ValidationObsRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	s.inferenceValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	s.sealedValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	put := func(dst map[uint64]map[uint32]SlotValidationObs, rows []ValidationObsRow) {
		for _, r := range rows {
			bySlot := dst[r.InferenceID]
			if bySlot == nil {
				bySlot = make(map[uint32]SlotValidationObs)
				dst[r.InferenceID] = bySlot
			}
			bySlot[r.SlotID] = SlotValidationObs{
				SlotID:               r.SlotID,
				RequiredValidations:  r.Required,
				CompletedValidations: r.Completed,
			}
		}
	}
	put(s.inferenceValidationObs, live)
	put(s.sealedValidationObs, sealed)
	return nil
}

func (m *Memory) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	if len(entries) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	for _, e := range entries {
		m.recordValidationAppliedOnceLockedSession(s, e.InferenceID, e.SlotID)
	}
	return nil
}

func (m *Memory) recordValidationAppliedOnceLockedSession(s *sessionData, inferenceID uint64, slotID uint32) {
	if s.inferenceValidationObs == nil {
		s.inferenceValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	}
	bySlot := s.inferenceValidationObs[inferenceID]
	if bySlot == nil {
		bySlot = make(map[uint32]SlotValidationObs)
		s.inferenceValidationObs[inferenceID] = bySlot
	}
	if _, exists := bySlot[slotID]; exists {
		return
	}
	bySlot[slotID] = SlotValidationObs{
		SlotID:               slotID,
		RequiredValidations:  1,
		CompletedValidations: 1,
	}
}

func (m *Memory) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("session %s not found", escrowID)
	}
	bySlot := s.inferenceValidationObs[inferenceID]
	if len(bySlot) == 0 {
		return nil
	}
	if s.sealedValidationObs == nil {
		s.sealedValidationObs = make(map[uint64]map[uint32]SlotValidationObs)
	}
	sealed := s.sealedValidationObs[inferenceID]
	if sealed == nil {
		sealed = make(map[uint32]SlotValidationObs)
		s.sealedValidationObs[inferenceID] = sealed
	}
	for slotID, obs := range bySlot {
		cur := sealed[slotID]
		cur.SlotID = slotID
		cur.RequiredValidations += obs.RequiredValidations
		cur.CompletedValidations += obs.CompletedValidations
		sealed[slotID] = cur
	}
	delete(s.inferenceValidationObs, inferenceID)
	return nil
}

func (m *Memory) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[escrowID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", escrowID)
	}
	return mergeValidationObsBySlot(
		flattenInferenceValidationObs(s.inferenceValidationObs),
		flattenInferenceValidationObs(s.sealedValidationObs),
	), nil
}

func flattenInferenceValidationObs(src map[uint64]map[uint32]SlotValidationObs) []SlotValidationObs {
	if len(src) == 0 {
		return nil
	}
	var out []SlotValidationObs
	for _, bySlot := range src {
		for _, obs := range bySlot {
			out = append(out, obs)
		}
	}
	return out
}

func (m *Memory) PruneEpoch(epochID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		if s.epochID == epochID {
			delete(m.sessions, id)
		}
	}
	m.pruneValidationLeasesBefore(epochID + 1)
	return nil
}

func (m *Memory) pruneBefore(cutoff uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		if s.epochID < cutoff {
			delete(m.sessions, id)
		}
	}
	m.pruneValidationLeasesBefore(cutoff)
	return nil
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
