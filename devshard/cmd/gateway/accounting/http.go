package accounting

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Read-only, so the listener carries no authentication. See docs/accounting.md, "The surface".
type Handler struct {
	book         *Book
	currentEpoch CurrentEpochFunc
	capability   CapabilityFunc
}

func NewHandler(book *Book, currentEpoch CurrentEpochFunc, capability CapabilityFunc) http.Handler {
	handler := &Handler{book: book, currentEpoch: currentEpoch, capability: capability}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/escrows", handler.serveEscrows)
	mux.HandleFunc("GET /api/v1/epochs", handler.serveEpochs)
	mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants", handler.serveParticipants)
	mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants/{participant}", handler.serveParticipant)
	mux.HandleFunc("GET /api/v1/epochs/{epoch}/events", handler.serveEvents)
	mux.HandleFunc("GET /api/v1/epochs/{epoch}/events/{participant}", handler.serveEvents)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handler.book == nil {
			writeError(w, http.StatusServiceUnavailable, "accounting is unavailable")
			return
		}
		if trimmed := strings.TrimSuffix(r.URL.Path, "/"); trimmed != "" {
			r.URL.Path = trimmed
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *Handler) serveEscrows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int      `json:"schema_version"`
		Escrows       []string `json:"escrows"`
	}{SchemaVersion: SchemaVersion, Escrows: h.book.EscrowIDs()})
}

func (h *Handler) serveEpochs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int            `json:"schema_version"`
		Epochs        []EpochSummary `json:"epochs"`
	}{SchemaVersion: SchemaVersion, Epochs: h.book.Epochs(filterOf(r, 0))})
}

func (h *Handler) serveParticipants(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.epochOf(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records := h.book.Query(filterOf(r, epoch))
	attachCapabilities(records, h.capability)
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                 `json:"schema_version"`
		EpochIndex    uint64              `json:"epoch_index"`
		Participants  []ParticipantRecord `json:"participants"`
	}{SchemaVersion: SchemaVersion, EpochIndex: epoch, Participants: records})
}

func (h *Handler) serveParticipant(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.epochOf(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	participant := strings.TrimSpace(r.PathValue("participant"))
	if participant == "" {
		writeError(w, http.StatusBadRequest, "invalid participant")
		return
	}
	filter := filterOf(r, epoch)
	filter.Participant = participant
	records := h.book.Query(filter)
	if len(records) == 0 {
		writeError(w, http.StatusNotFound, "participant not found")
		return
	}
	attachCapabilities(records, h.capability)
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                 `json:"schema_version"`
		EpochIndex    uint64              `json:"epoch_index"`
		Participant   string              `json:"participant"`
		Records       []ParticipantRecord `json:"records"`
	}{SchemaVersion: SchemaVersion, EpochIndex: epoch, Participant: participant, Records: records})
}

// An empty feed rather than a 404: here nothing to report is the healthy case.
func (h *Handler) serveEvents(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.epochOf(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := filterOf(r, epoch)
	filter.Participant = strings.TrimSpace(r.PathValue("participant"))
	if filter.Participant == "" {
		filter.Participant = strings.TrimSpace(r.URL.Query().Get("participant"))
	}
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                   `json:"schema_version"`
		EpochIndex    uint64                `json:"epoch_index"`
		Participant   string                `json:"participant,omitempty"`
		Events        []ProtocolEventRecord `json:"events"`
	}{SchemaVersion: SchemaVersion, EpochIndex: epoch, Participant: filter.Participant, Events: h.book.Events(filter)})
}

// Epoch zero is refused: the filter reads it as "every epoch".
func (h *Handler) epochOf(r *http.Request) (uint64, error) {
	value := r.PathValue("epoch")
	if value == "current" {
		if h.currentEpoch == nil {
			return 0, errors.New("current epoch is unavailable")
		}
		return h.currentEpoch(r.Context())
	}
	epoch, err := strconv.ParseUint(value, 10, 64)
	if err != nil || epoch == 0 {
		return 0, fmt.Errorf("invalid epoch %q", value)
	}
	return epoch, nil
}

func filterOf(r *http.Request, epoch uint64) QueryFilter {
	return QueryFilter{
		EpochIndex: epoch,
		Model:      strings.TrimSpace(r.URL.Query().Get("model")),
		EscrowIDs:  escrowIDsOf(r),
	}
}

func escrowIDsOf(r *http.Request) []string {
	values := r.URL.Query()["escrow_id"]
	escrowIDs := make([]string, 0, len(values))
	for _, value := range values {
		for escrowID := range strings.SplitSeq(value, ",") {
			if escrowID = strings.TrimSpace(escrowID); escrowID != "" {
				escrowIDs = append(escrowIDs, escrowID)
			}
		}
	}
	return escrowIDs
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
