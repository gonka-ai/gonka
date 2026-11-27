package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"devshard/cmd/gateway/store"
)

// InsertRequest stores one ledger row; a repeated request id is a no-op.
func (s *Backend) InsertRequest(ctx context.Context, record store.RequestRecord) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO request_accounting (
			request_id, escrow_id, model, outcome, decision, stream,
			winner_nonce, winner_participant, winner_host, winner_host_idx,
			attempts, input_tokens, input_tokens_source, prompt_tokens, winner_output_tokens, total_output_tokens,
			escrow_missing, balance_exhausted,
			started_at, completed_at, first_token_ms, duration_ms, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (request_id) DO NOTHING`,
		record.RequestID, record.EscrowID, record.Model, string(record.Outcome), record.Decision,
		flag(record.Stream),
		record.WinnerNonce, record.WinnerParticipant, record.WinnerHost, record.WinnerHostIdx,
		record.Attempts, record.InputTokens, string(record.InputTokensSource), record.PromptTokens, record.WinnerOutputTokens, record.TotalOutputTokens,
		flag(record.EscrowMissing), flag(record.BalanceExhausted),
		store.FormatTime(record.StartedAt), store.FormatTime(record.CompletedAt),
		record.FirstTokenMS, record.DurationMS, store.FormatTime(record.RecordedAt))
	return err
}

// DeleteRequestsBefore drops the rows recorded before cutoff.
func (s *Backend) DeleteRequestsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.database.exec(ctx, `DELETE FROM request_accounting WHERE recorded_at < $1`, store.FormatTime(cutoff))
	return err
}

// DeleteRequestsBeyond keeps the newest keepRows rows and drops the rest.
func (s *Backend) DeleteRequestsBeyond(ctx context.Context, keepRows int64) error {
	_, err := s.database.exec(ctx, `
		DELETE FROM request_accounting WHERE request_id IN (
			SELECT request_id FROM request_accounting
			ORDER BY recorded_at DESC, request_id DESC OFFSET $1)`, keepRows)
	return err
}

// FindRequest reads one ledger row and whether it exists.
func (s *Backend) FindRequest(ctx context.Context, requestID string) (store.RequestRecord, bool, error) {
	var record store.RequestRecord
	var outcome, startedAt, completedAt, recordedAt string
	err := s.database.scanRow(ctx, `
		SELECT request_id, escrow_id, model, outcome, decision, stream,
			winner_nonce, winner_participant, winner_host, winner_host_idx,
			attempts, input_tokens, input_tokens_source, prompt_tokens, winner_output_tokens, total_output_tokens,
			escrow_missing, balance_exhausted,
			started_at, completed_at, first_token_ms, duration_ms, recorded_at
		FROM request_accounting WHERE request_id = $1`, []any{requestID},
		&record.RequestID, &record.EscrowID, &record.Model, &outcome, &record.Decision, &record.Stream,
		&record.WinnerNonce, &record.WinnerParticipant, &record.WinnerHost, &record.WinnerHostIdx,
		&record.Attempts, &record.InputTokens, &record.InputTokensSource, &record.PromptTokens, &record.WinnerOutputTokens, &record.TotalOutputTokens,
		&record.EscrowMissing, &record.BalanceExhausted,
		&startedAt, &completedAt, &record.FirstTokenMS, &record.DurationMS, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.RequestRecord{}, false, nil
	}
	if err != nil {
		return store.RequestRecord{}, false, fmt.Errorf("loading request %s: %w", requestID, err)
	}
	record.Outcome = store.RequestOutcome(outcome)
	timestamps := []struct {
		target *time.Time
		raw    string
	}{
		{&record.StartedAt, startedAt},
		{&record.CompletedAt, completedAt},
		{&record.RecordedAt, recordedAt},
	}
	for _, timestamp := range timestamps {
		parsed, err := store.ParseTime(timestamp.raw)
		if err != nil {
			return store.RequestRecord{}, false, fmt.Errorf("parsing request %s timestamp %q: %w", requestID, timestamp.raw, err)
		}
		*timestamp.target = parsed
	}
	return record, true, nil
}
