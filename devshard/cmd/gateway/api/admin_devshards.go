package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/store"
	"devshard/types"
)

func (s *Server) handleAdminDevshards(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		records, err := s.control.ListDevshards(r.Context())
		if writeControlFailure(w, err) {
			return
		}
		views := devshardViews(records)
		for index := range views {
			if session, resident := s.escrows.SettlementSession(views[index].EscrowID); resident {
				views[index].Runtime = &devshardRuntimeView{Nonce: session.PublishedNonce()}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"devshards": views})
		return
	}
	var request AddDevshardRequest
	if err := decodeAdminBody(w, r, &request); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	if strings.TrimSpace(request.EscrowID) == "" || strings.TrimSpace(request.Model) == "" {
		writeError(w, http.StatusBadRequest, "id and model are required")
		return
	}
	if strings.TrimSpace(request.PrivateKeyEnv) == "" {
		s.writeErrorFor(w, ErrPrivateKeyEnvRequired)
		return
	}
	if err := s.operations.AddDevshard(r.Context(), request); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	auditAdmin("escrow registered", "escrow", request.EscrowID, "model", request.Model)
	writeJSON(w, http.StatusOK, map[string]any{"id": request.EscrowID})
}

func (s *Server) handleAdminDevshardImport(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	var request ImportDevshardRequest
	if err := decodeAdminBody(w, r, &request); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	if strings.TrimSpace(request.EscrowID) == "" || strings.TrimSpace(request.SourcePath) == "" {
		writeError(w, http.StatusBadRequest, "id and source_path are required")
		return
	}
	if strings.TrimSpace(request.PrivateKeyEnv) == "" {
		s.writeErrorFor(w, ErrPrivateKeyEnvRequired)
		return
	}
	if err := s.operations.ImportDevshard(r.Context(), request); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	auditAdmin("escrow imported", "escrow", request.EscrowID, "model", request.Model)
	writeJSON(w, http.StatusOK, map[string]any{"id": request.EscrowID})
}

func (s *Server) handleAdminDevshardDelete(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodDelete) {
		return
	}
	escrowID := r.PathValue("id")
	record, found, err := s.devshardRecord(r, escrowID)
	if writeControlFailure(w, err) {
		return
	}
	if !found {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	err = s.operations.DeleteDevshard(r.Context(), escrowID)
	if isDeleteRefusal(err) {
		s.writeErrorFor(w, err)
		return
	}
	if writeControlFailure(w, err) {
		return
	}
	auditAdmin("escrow deleted with its session storage", "escrow", escrowID, "model", record.Model)
	writeJSON(w, http.StatusOK, map[string]any{"id": escrowID, "deleted": true})
}

func isDeleteRefusal(err error) bool {
	return errors.Is(err, ErrUnknownDevshard) || errors.Is(err, escrow.ErrDevshardBusy)
}

func (s *Server) handleAdminDevshardActivate(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, "escrow activated", s.operations.Activate)
}

func (s *Server) handleAdminDevshardDeactivate(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, "escrow deactivated", s.operations.Deactivate)
}

func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, action string, apply func(ctx context.Context, escrowID string) error) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	escrowID := r.PathValue("id")
	_, found, err := s.devshardRecord(r, escrowID)
	if writeControlFailure(w, err) {
		return
	}
	if !found {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	if err := apply(r.Context(), escrowID); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	auditAdmin(action, "escrow", escrowID)
	writeJSON(w, http.StatusOK, map[string]any{"id": escrowID})
}

func (s *Server) handleAdminDevshardParticipants(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	escrowID := r.PathValue("id")
	session, held := s.escrows.SettlementSession(escrowID)
	if !held {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           escrowID,
		"participants": session.ParticipantKeys(),
		"slots":        session.HostParticipantKeyList(),
	})
}

func (s *Server) handleDevshardCollectSignatures(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	escrowID := r.PathValue("id")
	session, release, held := s.escrows.HoldSettlement(escrowID)
	if !held {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	defer release()
	if session.UserSession() == nil {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	raw := r.URL.Query().Get("nonce")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "missing 'nonce' query parameter")
		return
	}
	nonce, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'nonce' query parameter")
		return
	}
	if current := session.Nonce(); nonce > current {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("nonce %d is ahead of current nonce %d", nonce, current))
		return
	}
	auditAdmin("collect signatures", "escrow", escrowID, "nonce", nonce)
	weight, threshold, total := session.UserSession().CollectSignatures(r.Context(), nonce)
	writeJSON(w, http.StatusOK, map[string]any{
		"escrow_id":        escrowID,
		"nonce":            nonce,
		"sig_weight":       weight,
		"quorum_threshold": threshold,
		"total_slots":      total,
		"has_quorum":       weight >= threshold,
	})
}

func (s *Server) handleDevshardFinalize(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	escrowID := r.PathValue("id")
	session, release, held := s.escrows.HoldSettlement(escrowID)
	if !held {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	defer release()
	state := session.SnapshotState()
	if r.Method == http.MethodGet {
		if state.Phase == types.PhaseActive {
			writeError(w, http.StatusConflict, "devshard is not finalized")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"escrow_id":      escrowID,
			"phase":          phaseName(state.Phase),
			"finalize_nonce": state.FinalizeNonce,
		})
		return
	}
	if s.escrows.IsBusy(escrowID) {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", escrow.ErrDevshardBusy, escrowID))
		return
	}
	if writeControlFailure(w, session.Finalize(r.Context())) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"escrow_id": escrowID, "finalized": true})
}

func (s *Server) devshardRecord(r *http.Request, escrowID string) (store.DevshardRecord, bool, error) {
	records, err := s.control.ListDevshards(r.Context())
	if err != nil {
		return store.DevshardRecord{}, false, err
	}
	for _, record := range records {
		if record.EscrowID == escrowID {
			return record, true, nil
		}
	}
	return store.DevshardRecord{}, false, nil
}

// badRequestUnlessOversized keeps an ingest-cap rejection at 413; every other decode failure is the client's.
func badRequestUnlessOversized(err error) error {
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		return err
	}
	return filters.WrapReject(err)
}

// DevshardStoragePrefix names an escrow's session directory under the base storage dir.
const DevshardStoragePrefix = "escrow-"

// DevshardStoragePath: sessions, the delete route and session retention must derive it the same way. See operations.md, "What is exposed".
func DevshardStoragePath(baseStorageDir, escrowID string) string {
	return filepath.Join(baseStorageDir, DevshardStoragePrefix+escrowID)
}
