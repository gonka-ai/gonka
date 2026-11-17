package api

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	json "github.com/goccy/go-json"
	"golang.org/x/sync/errgroup"
)

const (
	defaultBatchSize = 4
	maxBatchSize     = 16
	batchEscrowLimit = 50
)

type ndjsonStream struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func newNDJSONStream(w http.ResponseWriter) *ndjsonStream {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	stream := &ndjsonStream{writer: w, controller: http.NewResponseController(w)}
	_ = stream.controller.Flush()
	return stream
}

func (n *ndjsonStream) send(payload any) {
	line, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = n.writer.Write(append(line, '\n'))
	_ = n.controller.Flush()
}

func (s *Server) admitBatch(w http.ResponseWriter, r *http.Request, requested []string, batchSize int) ([]string, map[string]bool, bool) {
	escrowIDs := distinctEscrowIDs(requested)
	if refusal := refuseBatch(escrowIDs, batchSize); refusal != "" {
		writeError(w, http.StatusBadRequest, refusal)
		return nil, nil, false
	}
	registered, err := s.registeredDevshards(r)
	if writeControlFailure(w, err) {
		return nil, nil, false
	}
	return escrowIDs, registered, true
}

func streamBatch[Outcome any](stream *ndjsonStream, escrowIDs []string, batchSize int, run func(escrowID string) Outcome, failed func(Outcome) bool) (succeededCount, failedCount int) {
	outcomes := make(chan Outcome, len(escrowIDs))
	go func() {
		defer close(outcomes)
		var running errgroup.Group
		running.SetLimit(cmp.Or(batchSize, defaultBatchSize))
		for _, escrowID := range escrowIDs {
			running.Go(func() error {
				outcomes <- run(escrowID)
				return nil
			})
		}
		_ = running.Wait()
	}()
	for outcome := range outcomes {
		if failed(outcome) {
			failedCount++
		} else {
			succeededCount++
		}
		stream.send(outcome)
	}
	return succeededCount, failedCount
}

func refuseBatch(escrowIDs []string, batchSize int) string {
	switch {
	case len(escrowIDs) == 0:
		return "escrow_ids is required"
	case len(escrowIDs) > batchEscrowLimit:
		return fmt.Sprintf("escrow_ids holds %d escrows; at most %d are taken in one call", len(escrowIDs), batchEscrowLimit)
	case batchSize < 0 || batchSize > maxBatchSize:
		return fmt.Sprintf("batch_size must be at most %d, or omitted for %d", maxBatchSize, defaultBatchSize)
	}
	return ""
}

func distinctEscrowIDs(escrowIDs []string) []string {
	seen := make(map[string]bool, len(escrowIDs))
	distinct := make([]string, 0, len(escrowIDs))
	for _, escrowID := range escrowIDs {
		escrowID = strings.TrimSpace(escrowID)
		if escrowID == "" || seen[escrowID] {
			continue
		}
		seen[escrowID] = true
		distinct = append(distinct, escrowID)
	}
	return distinct
}

func (s *Server) registeredDevshards(r *http.Request) (map[string]bool, error) {
	records, err := s.control.ListDevshards(r.Context())
	if err != nil {
		return nil, err
	}
	registered := make(map[string]bool, len(records))
	for _, record := range records {
		registered[record.EscrowID] = true
	}
	return registered, nil
}
