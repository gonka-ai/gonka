package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
)

const adminSettleTimeout = 5 * time.Minute

type settleOutcome struct {
	EscrowID string `json:"escrow_id"`
	TxHash   string `json:"tx_hash,omitempty"`
	Settler  string `json:"settler,omitempty"`
	Status   int    `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

type settleBatchSummary struct {
	Settled int `json:"settled"`
	Failed  int `json:"failed"`
}

func (s *Server) handleAdminDevshardSettle(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.settleOne(r.Context(), escrowID, isForcedSettle(r))
	if err != nil {
		s.writeErrorFor(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func isForcedSettle(r *http.Request) bool { return r.URL.Query().Get("force") == "true" }

func (s *Server) settleOne(ctx context.Context, escrowID string, forced bool) (chain.SettleEscrowResult, error) {
	var overridden int64
	switch {
	case forced:
		overridden = s.countInFlight(escrowID)
	case s.escrows.IsBusy(escrowID):
		return chain.SettleEscrowResult{}, fmt.Errorf("%w: %s", escrow.ErrDevshardBusy, escrowID)
	}
	settling, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminSettleTimeout)
	defer cancel()
	result, err := s.operations.Settle(settling, escrowID, forced)
	if err != nil {
		return result, err
	}
	if forced {
		auditAdmin("escrow settled under force", "escrow", escrowID, "in_flight", overridden)
	}
	return result, nil
}

func (s *Server) countInFlight(escrowID string) int64 {
	for _, escrowState := range s.escrows.Snapshot() {
		if escrowState.ID == escrowID {
			return escrowState.InFlight
		}
	}
	return 0
}

func (s *Server) handleAdminDevshardsSettleBatch(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	var request SettleDevshardsRequest
	if err := decodeAdminBody(w, r, &request); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	escrowIDs, registered, admitted := s.admitBatch(w, r, request.EscrowIDs, request.BatchSize)
	if !admitted {
		return
	}
	forced := request.Force || isForcedSettle(r)
	stream := newNDJSONStream(w)
	settled, failed := streamBatch(stream, escrowIDs, request.BatchSize,
		func(escrowID string) settleOutcome {
			return s.settleEntry(r.Context(), escrowID, registered[escrowID], forced)
		},
		func(outcome settleOutcome) bool { return outcome.Error != "" })
	stream.send(settleBatchSummary{Settled: settled, Failed: failed})
}

func (s *Server) settleEntry(ctx context.Context, escrowID string, registered, forced bool) settleOutcome {
	if !registered {
		return settleRefused(escrowID, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
	}
	if err := ctx.Err(); err != nil {
		return settleRefused(escrowID, err)
	}
	result, err := s.settleOne(ctx, escrowID, forced)
	if err != nil {
		return settleRefused(escrowID, err)
	}
	return settleOutcome{EscrowID: escrowID, TxHash: result.TxHash, Settler: result.Settler}
}

func settleRefused(escrowID string, err error) settleOutcome {
	return settleOutcome{EscrowID: escrowID, Status: statusForError(err), Error: err.Error()}
}
