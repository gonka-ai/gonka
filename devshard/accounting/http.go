package accounting

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Handler serves the read-only accounting API:
//
//	GET /api/v1/epochs
//	GET /api/v1/epochs/{epoch}/participants
//	GET /api/v1/epochs/{epoch}/participants/{participant}
//
// {epoch} is a chain epoch index or "current". All endpoints accept
// optional model and escrow_id query filters (repeated or comma-separated).
type Handler struct {
	tracker      *Tracker
	currentEpoch CurrentEpochFunc
	mux          *http.ServeMux
}

func NewHandler(tracker *Tracker, currentEpoch CurrentEpochFunc) *Handler {
	h := &Handler{tracker: tracker, currentEpoch: currentEpoch, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /api/v1/epochs", h.epochs)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants", h.participants)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants/{participant}", h.participant)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) epochs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int            `json:"schema_version"`
		Epochs        []EpochSummary `json:"epochs"`
	}{SchemaVersion, h.tracker.Epochs(queryFilter(r, 0, ""))})
}

func (h *Handler) participants(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.resolveEpoch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                 `json:"schema_version"`
		EpochIndex    uint64              `json:"epoch_index"`
		Participants  []ParticipantRecord `json:"participants"`
	}{SchemaVersion, epoch, h.tracker.Query(queryFilter(r, epoch, ""))})
}

func (h *Handler) participant(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.resolveEpoch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	participant := r.PathValue("participant")
	records := h.tracker.Query(queryFilter(r, epoch, participant))
	if len(records) == 0 {
		writeError(w, http.StatusNotFound, "participant not found")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                 `json:"schema_version"`
		EpochIndex    uint64              `json:"epoch_index"`
		Participant   string              `json:"participant"`
		Records       []ParticipantRecord `json:"records"`
	}{SchemaVersion, epoch, participant, records})
}

func (h *Handler) resolveEpoch(r *http.Request) (uint64, error) {
	raw := r.PathValue("epoch")
	if raw == "current" {
		if h.currentEpoch == nil {
			return 0, errors.New("current epoch unavailable")
		}
		return h.currentEpoch(r.Context())
	}
	epoch, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("invalid epoch")
	}
	return epoch, nil
}

func queryFilter(r *http.Request, epoch uint64, participant string) QueryFilter {
	return QueryFilter{
		EpochIndex:  epoch,
		Model:       strings.TrimSpace(r.URL.Query().Get("model")),
		EscrowIDs:   r.URL.Query()["escrow_id"],
		Participant: participant,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
