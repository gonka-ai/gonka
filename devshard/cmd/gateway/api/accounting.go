package api

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/safemath"
	"devshard/cmd/gateway/store"
)

// RaceLedger is how the engine's single recording point reaches the ledger; nothing else writes a row.
type RaceLedger struct{ ledger RequestLedger }

func NewRaceLedger(ledger RequestLedger) *RaceLedger { return &RaceLedger{ledger: ledger} }

func (l *RaceLedger) RecordRequest(outcome engine.RaceOutcome) {
	l.ledger.Record(requestRecord(outcome))
}

func requestRecord(outcome engine.RaceOutcome) store.RequestRecord {
	record := store.RequestRecord{
		RequestID:         outcome.RequestID,
		EscrowID:          outcome.EscrowID,
		Model:             outcome.Model,
		Outcome:           requestOutcome(outcome),
		Decision:          outcome.Decision,
		Stream:            outcome.ClientStream,
		WinnerNonce:       outcome.WinnerNonce,
		Attempts:          len(outcome.Attempts),
		InputTokens:       outcome.InputTokens,
		InputTokensSource: cmp.Or(outcome.InputTokensSource, filters.InputTokensBytesEstimate),
		EscrowMissing:     outcome.Lifecycle.EscrowMissing,
		BalanceExhausted:  outcome.Lifecycle.BalanceExhausted,
	}
	for _, attempt := range outcome.Attempts {
		record.TotalOutputTokens = safemath.AddSaturating(record.TotalOutputTokens, attempt.OutputTokens())
		if !attempt.SendTime.IsZero() && (record.StartedAt.IsZero() || attempt.SendTime.Before(record.StartedAt)) {
			record.StartedAt = attempt.SendTime
		}
		if attempt.Completed.After(record.CompletedAt) {
			record.CompletedAt = attempt.Completed
		}
		if !outcome.IsWinner(attempt) {
			continue
		}
		record.WinnerParticipant = attempt.Participant
		record.WinnerHost = attempt.HostLabel
		record.WinnerHostIdx = attempt.HostIdx
		record.WinnerOutputTokens = attempt.OutputTokens()
		record.PromptTokens = attempt.UsagePromptTokens
		record.FirstTokenMS = elapsedMS(attempt.SendTime, attempt.FirstToken)
		record.DurationMS = elapsedMS(attempt.SendTime, attempt.Completed)
	}
	return record
}

func requestOutcome(outcome engine.RaceOutcome) store.RequestOutcome {
	switch {
	case outcome.Succeeded:
		return store.RequestSettled
	case outcome.EscrowID == "":
		return store.RequestNoEscrow
	}
	return store.RequestFailed
}

func elapsedMS(from, to time.Time) int64 {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return to.Sub(from).Milliseconds()
}

type requestAccountingResponse struct {
	RequestID            string                    `json:"request_id"`
	EscrowID             string                    `json:"escrow_id,omitempty"`
	Model                string                    `json:"model,omitempty"`
	Outcome              string                    `json:"outcome"`
	Decision             string                    `json:"decision,omitempty"`
	Stream               bool                      `json:"stream"`
	WinnerNonce          uint64                    `json:"winner_nonce,omitempty"`
	WinnerParticipant    string                    `json:"winner_participant,omitempty"`
	WinnerHost           string                    `json:"winner_host,omitempty"`
	WinnerHostIdx        int                       `json:"winner_host_idx"`
	Attempts             int                       `json:"attempts"`
	EstimatedInput       uint64                    `json:"estimated_input_tokens"`
	EstimatedInputSource filters.InputTokensSource `json:"estimated_input_tokens_source"`
	PromptTokens         int64                     `json:"prompt_tokens"`
	WinnerOutputTokens   int64                     `json:"winner_output_tokens"`
	TotalOutputTokens    int64                     `json:"total_output_tokens"`
	EscrowMissing        bool                      `json:"escrow_missing,omitempty"`
	BalanceExhausted     bool                      `json:"balance_exhausted,omitempty"`
	StartedAt            string                    `json:"started_at,omitempty"`
	CompletedAt          string                    `json:"completed_at,omitempty"`
	FirstTokenMS         int64                     `json:"first_token_ms"`
	DurationMS           int64                     `json:"duration_ms"`
	RecordedAt           string                    `json:"recorded_at,omitempty"`
}

func (s *Server) handleRequestAccounting(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("id"))
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "request id is required")
		return
	}
	record, found, err := s.accounting.Find(r.Context(), requestID)
	if writeControlFailure(w, err) {
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("request %s not found", requestID))
		return
	}
	writeJSON(w, http.StatusOK, accountingResponse(record))
}

func accountingResponse(record store.RequestRecord) requestAccountingResponse {
	return requestAccountingResponse{
		RequestID:            record.RequestID,
		EscrowID:             record.EscrowID,
		Model:                record.Model,
		Outcome:              string(record.Outcome),
		Decision:             record.Decision,
		Stream:               record.Stream,
		WinnerNonce:          record.WinnerNonce,
		WinnerParticipant:    record.WinnerParticipant,
		WinnerHost:           record.WinnerHost,
		WinnerHostIdx:        record.WinnerHostIdx,
		Attempts:             record.Attempts,
		EstimatedInput:       record.InputTokens,
		EstimatedInputSource: record.InputTokensSource,
		PromptTokens:         record.PromptTokens,
		WinnerOutputTokens:   record.WinnerOutputTokens,
		TotalOutputTokens:    record.TotalOutputTokens,
		EscrowMissing:        record.EscrowMissing,
		BalanceExhausted:     record.BalanceExhausted,
		StartedAt:            store.FormatTime(record.StartedAt),
		CompletedAt:          store.FormatTime(record.CompletedAt),
		FirstTokenMS:         record.FirstTokenMS,
		DurationMS:           record.DurationMS,
		RecordedAt:           store.FormatTime(record.RecordedAt),
	}
}
