package api

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/registry"
	"devshard/types"
	"devshard/user"
)

// escrowInspection is one escrow's state as an operator reads it. See README.md, "The operator and recovery surface".
type escrowInspection struct {
	EscrowID         string   `json:"escrow_id"`
	Phase            string   `json:"phase"`
	Nonce            uint64   `json:"nonce"`
	LatestNonce      uint64   `json:"latest_nonce"`
	FinalizeNonce    uint64   `json:"finalize_nonce,omitempty"`
	Balance          uint64   `json:"balance"`
	Fees             uint64   `json:"fees"`
	Participants     []string `json:"participants"`
	Slots            []string `json:"slots"`
	LiveInferences   int      `json:"live_inferences"`
	SealedInferences int      `json:"sealed_inferences"`
	Pending          int      `json:"pending"`
}

type escrowInspectionDetail struct {
	escrowInspection
	Config    types.SessionConfig    `json:"config"`
	Group     []types.SlotAssignment `json:"group"`
	HostStats []hostStatsEntry       `json:"host_stats"`
}

type hostStatsEntry struct {
	Slot uint32 `json:"slot"`
	types.HostStats
}

type inferenceEntry struct {
	Nonce  uint64                `json:"nonce"`
	Status string                `json:"status"`
	Record types.InferenceRecord `json:"record"`
}

type signatureEntry struct {
	Nonce      uint64   `json:"nonce"`
	Slots      []uint32 `json:"slots"`
	SigWeight  uint32   `json:"sig_weight"`
	TotalSlots uint32   `json:"total_slots"`
	HasQuorum  bool     `json:"has_quorum"`
}

func (s *Server) handleDevshardState(w http.ResponseWriter, r *http.Request) {
	session, escrowID, release, held := s.inspectable(w, r)
	if !held {
		return
	}
	defer release()
	writeJSON(w, http.StatusOK, inspect(escrowID, session))
}

func (s *Server) handleDevshardDebugState(w http.ResponseWriter, r *http.Request) {
	session, escrowID, release, held := s.inspectable(w, r)
	if !held {
		return
	}
	defer release()
	state := session.SnapshotState()
	detail := escrowInspectionDetail{
		escrowInspection: inspect(escrowID, session),
		Config:           state.Config,
		Group:            state.Group,
		HostStats:        make([]hostStatsEntry, 0, len(state.HostStats)),
	}
	for _, slot := range slices.Sorted(maps.Keys(state.HostStats)) {
		stats := state.HostStats[slot]
		if stats == nil {
			continue
		}
		detail.HostStats = append(detail.HostStats, hostStatsEntry{Slot: slot, HostStats: *stats})
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleDevshardDebugInferences lists sealed records too: a settled escrow has sealed every one, and the live view alone reads as an escrow that served nothing.
func (s *Server) handleDevshardDebugInferences(w http.ResponseWriter, r *http.Request) {
	s.writeInferences(w, r, registry.EscrowSession.AllInferences, func(types.InferenceStatus) bool { return true })
}

// handleDevshardDebugPending reads the live records alone, since a sealed record is never unresolved and is read back from storage.
func (s *Server) handleDevshardDebugPending(w http.ResponseWriter, r *http.Request) {
	s.writeInferences(w, r, liveInferences, unresolved)
}

func liveInferences(session registry.EscrowSession) map[uint64]types.InferenceRecord {
	live := session.SnapshotState().Inferences
	records := make(map[uint64]types.InferenceRecord, len(live))
	for nonce, record := range live {
		if record != nil {
			records[nonce] = *record
		}
	}
	return records
}

func (s *Server) handleDevshardDebugSignatures(w http.ResponseWriter, r *http.Request) {
	session, escrowID, release, held := s.inspectable(w, r)
	if !held {
		return
	}
	defer release()
	status, highestQuorum, hasQuorum := session.SignatureStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"escrow_id":            escrowID,
		"highest_quorum_nonce": highestQuorum,
		"has_quorum":           hasQuorum,
		"nonces":               signatureEntries(session.SignedSlots(), status),
	})
}

func signatureEntries(signed map[uint64]types.Bitmap128, status []user.SignatureStatusEntry) []signatureEntry {
	weights := make(map[uint64]user.SignatureStatusEntry, len(status))
	for _, entry := range status {
		weights[entry.Nonce] = entry
	}
	entries := make([]signatureEntry, 0, len(signed))
	for _, nonce := range slices.Sorted(maps.Keys(signed)) {
		weight := weights[nonce]
		entries = append(entries, signatureEntry{
			Nonce:      nonce,
			Slots:      signed[nonce].SetBits(),
			SigWeight:  weight.SigWeight,
			TotalSlots: weight.Total,
			HasQuorum:  weight.HasQuorum,
		})
	}
	return entries
}

func (s *Server) writeInferences(w http.ResponseWriter, r *http.Request, read func(registry.EscrowSession) map[uint64]types.InferenceRecord, keep func(types.InferenceStatus) bool) {
	session, escrowID, release, held := s.inspectable(w, r)
	if !held {
		return
	}
	defer release()
	records := read(session)
	entries := make([]inferenceEntry, 0, len(records))
	for _, nonce := range slices.Sorted(maps.Keys(records)) {
		record := records[nonce]
		if !keep(record.Status) {
			continue
		}
		entries = append(entries, inferenceEntry{Nonce: nonce, Status: statusName(record.Status), Record: record})
	}
	writeJSON(w, http.StatusOK, map[string]any{"escrow_id": escrowID, "inferences": entries})
}

// inspectable resolves a handle for reading; a settled escrow is rehydrated. See operations.md, "What is exposed".
func (s *Server) inspectable(w http.ResponseWriter, r *http.Request) (registry.EscrowSession, string, func(), bool) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return nil, "", nil, false
	}
	escrowID := r.PathValue("id")
	session, release, err := s.escrows.Inspect(r.Context(), escrowID)
	if err != nil {
		if errors.Is(err, escrow.ErrUnknownEscrow) {
			s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
			return nil, "", nil, false
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, "", nil, false
	}
	return session, escrowID, release, true
}

func inspect(escrowID string, session registry.EscrowSession) escrowInspection {
	state := session.SnapshotState()
	pending := 0
	for _, record := range state.Inferences {
		if record != nil && unresolved(record.Status) {
			pending++
		}
	}
	return escrowInspection{
		EscrowID:         escrowID,
		Phase:            phaseName(state.Phase),
		Nonce:            session.Nonce(),
		LatestNonce:      state.LatestNonce,
		FinalizeNonce:    state.FinalizeNonce,
		Balance:          state.Balance,
		Fees:             state.Fees,
		Participants:     session.ParticipantKeys(),
		Slots:            session.HostParticipantKeyList(),
		LiveInferences:   len(state.Inferences),
		SealedInferences: session.SealedInferences(),
		Pending:          pending,
	}
}

// unresolved reports an inference with no result yet: the ones holding a stuck escrow short of settlement.
func unresolved(status types.InferenceStatus) bool {
	return status == types.StatusPending || status == types.StatusStarted || status == types.StatusChallenged
}

func statusName(status types.InferenceStatus) string {
	switch status {
	case types.StatusPending:
		return statusNamePending
	case types.StatusStarted:
		return statusNameStarted
	case types.StatusFinished:
		return statusNameFinished
	case types.StatusChallenged:
		return statusNameChallenged
	case types.StatusValidated:
		return statusNameValidated
	case types.StatusInvalidated:
		return statusNameInvalidated
	case types.StatusTimedOut:
		return statusNameTimedOut
	}
	return statusNameUnknown
}
