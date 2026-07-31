package accounting

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Handler struct {
	book         *Book
	currentEpoch CurrentEpochFunc
}

func NewHandler(book *Book, currentEpoch CurrentEpochFunc) *Handler {
	return &Handler{book: book, currentEpoch: currentEpoch}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.book == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "accounting book is unavailable")
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/v1/epochs" {
		h.serveEpochs(w, r)
		return
	}
	const prefix = "/api/v1/epochs/"
	if !strings.HasPrefix(path, prefix) {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) < 2 || parts[1] != "participants" {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	epoch, err := h.resolveEpoch(r, parts[0])
	if err != nil {
		status := http.StatusBadRequest
		if parts[0] == "current" {
			status = http.StatusServiceUnavailable
		}
		writeJSONError(w, status, err.Error())
		return
	}
	filter := QueryFilter{
		EpochIndex: epoch,
		Model:      strings.TrimSpace(r.URL.Query().Get("model")),
		EscrowIDs:  r.URL.Query()["escrow_id"],
	}
	switch len(parts) {
	case 2:
		records := h.book.Query(filter)
		writeJSON(w, http.StatusOK, struct {
			SchemaVersion int                 `json:"schema_version"`
			EpochIndex    uint64              `json:"epoch_index"`
			Participants  []ParticipantRecord `json:"participants"`
		}{
			SchemaVersion: SchemaVersion,
			EpochIndex:    epoch,
			Participants:  records,
		})
	case 3:
		participant, err := url.PathUnescape(parts[2])
		if err != nil || strings.TrimSpace(participant) == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid participant")
			return
		}
		filter.Participant = participant
		records := h.book.Query(filter)
		if len(records) == 0 {
			writeJSONError(w, http.StatusNotFound, "participant not found")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			SchemaVersion int                 `json:"schema_version"`
			EpochIndex    uint64              `json:"epoch_index"`
			Participant   string              `json:"participant"`
			Records       []ParticipantRecord `json:"records"`
		}{
			SchemaVersion: SchemaVersion,
			EpochIndex:    epoch,
			Participant:   participant,
			Records:       records,
		})
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveEpochs(w http.ResponseWriter, r *http.Request) {
	epochs := h.book.Epochs(QueryFilter{
		Model:     strings.TrimSpace(r.URL.Query().Get("model")),
		EscrowIDs: r.URL.Query()["escrow_id"],
	})
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int            `json:"schema_version"`
		Epochs        []EpochSummary `json:"epochs"`
	}{
		SchemaVersion: SchemaVersion,
		Epochs:        epochs,
	})
}

func (h *Handler) resolveEpoch(r *http.Request, value string) (uint64, error) {
	if value == "current" {
		if h.currentEpoch == nil {
			return 0, errors.New("current epoch is unavailable")
		}
		return h.currentEpoch(r.Context())
	}
	epoch, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("invalid epoch")
	}
	return epoch, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
