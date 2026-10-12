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
//	GET /api/v1/epochs/{epoch}/events
//	GET /api/v1/epochs/{epoch}/events/{participant}
//
// {epoch} is a chain epoch index or "current". All endpoints accept
// optional model and escrow_id query filters (repeated or comma-separated);
// the unscoped events path also accepts participant as a query filter.
type Handler struct {
	tracker      *Tracker
	currentEpoch CurrentEpochFunc
	capability   CapabilityFunc
	mux          *http.ServeMux
}

// capability may be nil, in which case records carry no capability block.
func NewHandler(tracker *Tracker, currentEpoch CurrentEpochFunc, capability CapabilityFunc) *Handler {
	h := &Handler{tracker: tracker, currentEpoch: currentEpoch, capability: capability, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /api/v1/epochs", h.epochs)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants", h.participants)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/events", h.events)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/events/{participant}", h.participantEvents)
	h.mux.HandleFunc("GET /api/v1/epochs/{epoch}/participants/{participant}", h.participant)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) epochs(w http.ResponseWriter, r *http.Request) {
	recordingErrors, writerErrors := h.tracker.ErrorCounts()
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion   int            `json:"schema_version"`
		RecordingErrors uint64         `json:"recording_errors"`
		WriterErrors    uint64         `json:"writer_errors"`
		Epochs          []EpochSummary `json:"epochs"`
	}{
		SchemaVersion:   SchemaVersion,
		RecordingErrors: recordingErrors,
		WriterErrors:    writerErrors,
		Epochs:          h.tracker.Epochs(queryFilter(r, 0, "")),
	})
}

func (h *Handler) participants(w http.ResponseWriter, r *http.Request) {
	epoch, err := h.resolveEpoch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records := h.tracker.Query(queryFilter(r, epoch, ""))
	attachCapabilities(records, h.capability)
	recordingErrors, writerErrors := h.tracker.ErrorCounts()
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion   int                 `json:"schema_version"`
		RecordingErrors uint64              `json:"recording_errors"`
		WriterErrors    uint64              `json:"writer_errors"`
		EpochIndex      uint64              `json:"epoch_index"`
		Participants    []ParticipantRecord `json:"participants"`
	}{
		SchemaVersion:   SchemaVersion,
		RecordingErrors: recordingErrors,
		WriterErrors:    writerErrors,
		EpochIndex:      epoch,
		Participants:    records,
	})
}

// events serves the feed on its own path: folding it into the participant record would restore the
// per-nonce growth that record was just rid of.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	h.writeEvents(w, r, strings.TrimSpace(r.URL.Query().Get("participant")))
}

func (h *Handler) participantEvents(w http.ResponseWriter, r *http.Request) {
	h.writeEvents(w, r, r.PathValue("participant"))
}

// A participant with no misses is answered with an empty feed rather than a 404: nothing to report
// is the healthy case here, unlike a participant absent from the epoch entirely.
func (h *Handler) writeEvents(w http.ResponseWriter, r *http.Request, participant string) {
	epoch, err := h.resolveEpoch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion int                   `json:"schema_version"`
		EpochIndex    uint64                `json:"epoch_index"`
		Participant   string                `json:"participant,omitempty"`
		Events        []ProtocolEventRecord `json:"events"`
	}{
		SchemaVersion: SchemaVersion,
		EpochIndex:    epoch,
		Participant:   participant,
		Events:        h.tracker.Events(queryFilter(r, epoch, participant)),
	})
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
	attachCapabilities(records, h.capability)
	recordingErrors, writerErrors := h.tracker.ErrorCounts()
	writeJSON(w, http.StatusOK, struct {
		SchemaVersion   int                 `json:"schema_version"`
		RecordingErrors uint64              `json:"recording_errors"`
		WriterErrors    uint64              `json:"writer_errors"`
		EpochIndex      uint64              `json:"epoch_index"`
		Participant     string              `json:"participant"`
		Records         []ParticipantRecord `json:"records"`
	}{
		SchemaVersion:   SchemaVersion,
		RecordingErrors: recordingErrors,
		WriterErrors:    writerErrors,
		EpochIndex:      epoch,
		Participant:     participant,
		Records:         records,
	})
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
