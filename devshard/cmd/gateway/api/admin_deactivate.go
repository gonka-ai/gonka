package api

import (
	"context"
	"fmt"
	"net/http"
)

type deactivateOutcome struct {
	EscrowID string `json:"escrow_id"`
	Status   int    `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

type deactivateBatchSummary struct {
	Deactivated int `json:"deactivated"`
	Failed      int `json:"failed"`
}

func (s *Server) handleAdminDevshardsDeactivateBatch(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	var request DeactivateDevshardsRequest
	if err := decodeAdminBody(w, r, &request); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	escrowIDs, registered, admitted := s.admitBatch(w, r, request.EscrowIDs, request.BatchSize)
	if !admitted {
		return
	}
	stream := newNDJSONStream(w)
	deactivated, failed := streamBatch(stream, escrowIDs, request.BatchSize,
		func(escrowID string) deactivateOutcome {
			return s.deactivateEntry(r.Context(), escrowID, registered[escrowID])
		},
		func(outcome deactivateOutcome) bool { return outcome.Error != "" })
	stream.send(deactivateBatchSummary{Deactivated: deactivated, Failed: failed})
}

func (s *Server) deactivateEntry(ctx context.Context, escrowID string, registered bool) deactivateOutcome {
	err := fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID)
	if registered {
		err = s.operations.Deactivate(ctx, escrowID)
	}
	if err != nil {
		return deactivateOutcome{EscrowID: escrowID, Status: statusForError(err), Error: err.Error()}
	}
	auditAdmin("escrow deactivated", "escrow", escrowID)
	return deactivateOutcome{EscrowID: escrowID}
}
