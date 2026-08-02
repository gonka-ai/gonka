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
	tracker      *Tracker
	currentEpoch CurrentEpochFunc
}

func NewHandler(tracker *Tracker, currentEpoch CurrentEpochFunc) *Handler {
	return &Handler{tracker: tracker, currentEpoch: currentEpoch}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.tracker == nil {
		writeError(w, http.StatusServiceUnavailable, "accounting unavailable")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/v1/epochs" {
		writeJSON(w, http.StatusOK, struct {
			SchemaVersion int            `json:"schema_version"`
			Epochs        []EpochSummary `json:"epochs"`
		}{SchemaVersion: SchemaVersion, Epochs: h.tracker.Epochs(queryFilter(r, 0, ""))})
		return
	}
	const prefix = "/api/v1/epochs/"
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) < 2 || parts[1] != "participants" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	epoch, err := h.resolveEpoch(r, parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch len(parts) {
	case 2:
		writeJSON(w, http.StatusOK, struct {
			SchemaVersion int                 `json:"schema_version"`
			EpochIndex    uint64              `json:"epoch_index"`
			Participants  []ParticipantRecord `json:"participants"`
		}{
			SchemaVersion: SchemaVersion,
			EpochIndex:    epoch,
			Participants:  h.tracker.Query(queryFilter(r, epoch, "")),
		})
	case 3:
		participant, err := url.PathUnescape(parts[2])
		if err != nil || strings.TrimSpace(participant) == "" {
			writeError(w, http.StatusBadRequest, "invalid participant")
			return
		}
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
		}{SchemaVersion: SchemaVersion, EpochIndex: epoch, Participant: participant, Records: records})
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func queryFilter(r *http.Request, epoch uint64, participant string) QueryFilter {
	return QueryFilter{
		EpochIndex:  epoch,
		Model:       strings.TrimSpace(r.URL.Query().Get("model")),
		EscrowIDs:   escrowIDs(r),
		Participant: participant,
	}
}

func escrowIDs(r *http.Request) []string {
	var out []string
	for _, raw := range r.URL.Query()["escrow_id"] {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func (h *Handler) resolveEpoch(r *http.Request, raw string) (uint64, error) {
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
