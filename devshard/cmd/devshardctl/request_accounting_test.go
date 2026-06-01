package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestHandleRequestAccountingReturnsJoinedInferenceCosts(t *testing.T) {
	store, err := NewPerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	perf := NewPerfTracker(store)

	requestID := "req-test"
	escrowID := "42"
	perf.RecordAccountingRequestStart(requestID, escrowID, "test-model", time.Unix(100, 0))
	perf.RecordAccountingAttempt(RequestAccountingAttempt{
		RequestID:      requestID,
		EscrowID:       escrowID,
		Nonce:          7,
		HostIdx:        1,
		ParticipantKey: "host-a",
		CreatedAt:      time.Unix(101, 0),
	})
	perf.RecordAccountingAttempt(RequestAccountingAttempt{
		RequestID:      requestID,
		EscrowID:       escrowID,
		Nonce:          8,
		HostIdx:        2,
		ParticipantKey: "host-b",
		CreatedAt:      time.Unix(102, 0),
	})
	perf.CompleteAccountingRequest(requestID, escrowID, 7, "primary_only", "success", time.Unix(103, 0))

	sm := gatewayTestStateMachineInPhase(t, types.PhaseActive)
	state := sm.ExportState()
	state.EscrowID = escrowID
	state.Inferences = map[uint64]*types.InferenceRecord{
		7: {
			Status:       types.StatusFinished,
			ExecutorSlot: 1,
			Model:        "test-model",
			InputLength:  1000,
			MaxTokens:    200,
			InputTokens:  100,
			OutputTokens: 50,
			ReservedCost: 1200,
			ActualCost:   150,
			StartedAt:    100,
			ConfirmedAt:  101,
		},
		8: {
			Status:       types.StatusFinished,
			ExecutorSlot: 2,
			Model:        "test-model",
			InputLength:  1000,
			MaxTokens:    200,
			InputTokens:  100,
			OutputTokens: 25,
			ReservedCost: 1200,
			ActualCost:   125,
			StartedAt:    100,
			ConfirmedAt:  102,
		},
	}
	sm.RestoreState(state)

	proxy := &Proxy{sm: sm, escrowID: escrowID, perf: perf}
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/"+requestID, nil)
	rec := httptest.NewRecorder()
	newRuntimeMux(proxy).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		RequestID   string `json:"request_id"`
		EscrowID    string `json:"escrow_id"`
		WinnerNonce uint64 `json:"winner_nonce"`
		Winner      struct {
			Nonce        uint64 `json:"nonce"`
			InputTokens  uint64 `json:"input_tokens"`
			OutputTokens uint64 `json:"output_tokens"`
			ActualCost   uint64 `json:"actual_cost"`
		} `json:"winner"`
		Cost struct {
			WinnerActualCost        uint64 `json:"winner_actual_cost"`
			OtherAttemptsActualCost uint64 `json:"other_attempts_actual_cost"`
			AllAttemptsActualCost   uint64 `json:"all_attempts_actual_cost"`
		} `json:"cost"`
		Attempts []struct {
			Nonce      uint64 `json:"nonce"`
			Winner     bool   `json:"winner"`
			ActualCost uint64 `json:"actual_cost"`
		} `json:"attempts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, requestID, body.RequestID)
	require.Equal(t, escrowID, body.EscrowID)
	require.EqualValues(t, 7, body.WinnerNonce)
	require.EqualValues(t, 7, body.Winner.Nonce)
	require.EqualValues(t, 100, body.Winner.InputTokens)
	require.EqualValues(t, 50, body.Winner.OutputTokens)
	require.EqualValues(t, 150, body.Winner.ActualCost)
	require.EqualValues(t, 150, body.Cost.WinnerActualCost)
	require.EqualValues(t, 125, body.Cost.OtherAttemptsActualCost)
	require.EqualValues(t, 275, body.Cost.AllAttemptsActualCost)
	require.Len(t, body.Attempts, 2)
	require.True(t, body.Attempts[0].Winner)
	require.False(t, body.Attempts[1].Winner)
}

func TestHandleRequestAccountingResolvesCachedRequestAliasCosts(t *testing.T) {
	store, err := NewPerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	perf := NewPerfTracker(store)

	sourceRequestID := "req-source"
	cachedRequestID := "req-cached"
	escrowID := "42"
	perf.RecordAccountingRequestStart(sourceRequestID, escrowID, "test-model", time.Unix(100, 0))
	perf.RecordAccountingAttempt(RequestAccountingAttempt{
		RequestID:      sourceRequestID,
		EscrowID:       escrowID,
		Nonce:          7,
		HostIdx:        1,
		ParticipantKey: "host-a",
		CreatedAt:      time.Unix(101, 0),
	})
	perf.RecordAccountingAttempt(RequestAccountingAttempt{
		RequestID:      sourceRequestID,
		EscrowID:       escrowID,
		Nonce:          8,
		HostIdx:        2,
		ParticipantKey: "host-b",
		CreatedAt:      time.Unix(102, 0),
	})
	perf.CompleteAccountingRequest(sourceRequestID, escrowID, 7, "primary_only", "success", time.Unix(103, 0))
	perf.RecordAccountingAlias(cachedRequestID, escrowID, sourceRequestID, escrowID, "cache_hit", time.Unix(104, 0))

	sm := gatewayTestStateMachineInPhase(t, types.PhaseActive)
	state := sm.ExportState()
	state.EscrowID = escrowID
	state.Inferences = map[uint64]*types.InferenceRecord{
		7: {
			Status:       types.StatusFinished,
			ExecutorSlot: 1,
			Model:        "test-model",
			InputLength:  1000,
			MaxTokens:    200,
			InputTokens:  100,
			OutputTokens: 50,
			ReservedCost: 1200,
			ActualCost:   150,
			StartedAt:    100,
			ConfirmedAt:  101,
		},
		8: {
			Status:       types.StatusFinished,
			ExecutorSlot: 2,
			Model:        "test-model",
			InputLength:  1000,
			MaxTokens:    200,
			InputTokens:  100,
			OutputTokens: 25,
			ReservedCost: 1200,
			ActualCost:   125,
			StartedAt:    100,
			ConfirmedAt:  102,
		},
	}
	sm.RestoreState(state)

	proxy := &Proxy{sm: sm, escrowID: escrowID, perf: perf}
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/"+cachedRequestID, nil)
	rec := httptest.NewRecorder()
	newRuntimeMux(proxy).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		RequestID           string `json:"request_id"`
		EscrowID            string `json:"escrow_id"`
		Outcome             string `json:"outcome"`
		Decision            string `json:"decision"`
		CachedFromRequestID string `json:"cached_from_request_id"`
		CachedFromEscrowID  string `json:"cached_from_escrow_id"`
		Cost                struct {
			WinnerActualCost        uint64 `json:"winner_actual_cost"`
			OtherAttemptsActualCost uint64 `json:"other_attempts_actual_cost"`
			AllAttemptsActualCost   uint64 `json:"all_attempts_actual_cost"`
		} `json:"cost"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, cachedRequestID, body.RequestID)
	require.Equal(t, escrowID, body.EscrowID)
	require.Equal(t, "cached", body.Outcome)
	require.Equal(t, "cache_hit", body.Decision)
	require.Equal(t, sourceRequestID, body.CachedFromRequestID)
	require.Equal(t, escrowID, body.CachedFromEscrowID)
	require.EqualValues(t, 150, body.Cost.WinnerActualCost)
	require.EqualValues(t, 125, body.Cost.OtherAttemptsActualCost)
	require.EqualValues(t, 275, body.Cost.AllAttemptsActualCost)
}
