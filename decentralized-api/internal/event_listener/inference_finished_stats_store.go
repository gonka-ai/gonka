package event_listener

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"decentralized-api/internal/event_listener/chainevents"
)

const inferenceFinishedStatsSchema = `
CREATE TABLE IF NOT EXISTS inference_finished_stats (
	inference_id TEXT PRIMARY KEY,
	requested_by TEXT NOT NULL,
	executed_by TEXT NOT NULL,
	model TEXT NOT NULL,
	epoch_id INTEGER NOT NULL,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	actual_cost INTEGER NOT NULL,
	end_timestamp_ms INTEGER NOT NULL,
	tx_height INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now')),
	updated_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
);
CREATE INDEX IF NOT EXISTS idx_ifs_model_time ON inference_finished_stats(model, end_timestamp_ms);
CREATE INDEX IF NOT EXISTS idx_ifs_requested_by_time ON inference_finished_stats(requested_by, end_timestamp_ms);
`

const (
	statsWriterMaxRetries = 4
)

const inferenceFinishedStatsUpsertSQL = `
INSERT INTO inference_finished_stats (
	inference_id,
	requested_by,
	executed_by,
	model,
	epoch_id,
	prompt_tokens,
	completion_tokens,
	total_tokens,
	actual_cost,
	end_timestamp_ms,
	tx_height
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(inference_id) DO UPDATE SET
	requested_by = excluded.requested_by,
	executed_by = excluded.executed_by,
	model = excluded.model,
	epoch_id = excluded.epoch_id,
	prompt_tokens = excluded.prompt_tokens,
	completion_tokens = excluded.completion_tokens,
	total_tokens = excluded.total_tokens,
	actual_cost = excluded.actual_cost,
	end_timestamp_ms = excluded.end_timestamp_ms,
	tx_height = excluded.tx_height,
	updated_at = (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
`

type inferenceFinishedStatsRow struct {
	InferenceID      string
	RequestedBy      string
	ExecutedBy       string
	Model            string
	EpochID          int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ActualCost       int64
	EndTimestampMs   int64
	TxHeight         int64
}

type InferenceFinishedStatsStore struct {
	db *sql.DB
	mu sync.Mutex
}

func NewInferenceFinishedStatsStore(db *sql.DB) (*InferenceFinishedStatsStore, error) {
	if db == nil {
		return nil, nil
	}
	store := &InferenceFinishedStatsStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *InferenceFinishedStatsStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, inferenceFinishedStatsSchema)
	return err
}

func (s *InferenceFinishedStatsStore) UpsertFromTxEvent(event *chainevents.JSONRPCResponse) error {
	if s == nil || s.db == nil || event == nil {
		return nil
	}

	events := event.Result.Events

	row, ok, err := parseInferenceFinishedStatsRow(events)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	return s.upsertRowWithRetry(row)
}

func (s *InferenceFinishedStatsStore) upsertRowWithRetry(row inferenceFinishedStatsRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < statsWriterMaxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.upsertRow(ctx, row)
		cancel()
		if err == nil {
			return nil
		}
		if !isRetryableSQLiteError(err) {
			return err
		}
		lastErr = err
		time.Sleep(time.Duration(40*(1<<attempt)) * time.Millisecond)
	}
	return lastErr
}

func (s *InferenceFinishedStatsStore) upsertRow(ctx context.Context, row inferenceFinishedStatsRow) error {
	_, err := s.db.ExecContext(
		ctx,
		inferenceFinishedStatsUpsertSQL,
		row.InferenceID,
		row.RequestedBy,
		row.ExecutedBy,
		row.Model,
		row.EpochID,
		row.PromptTokens,
		row.CompletionTokens,
		row.TotalTokens,
		row.ActualCost,
		row.EndTimestampMs,
		row.TxHeight,
	)
	return err
}

func isRetryableSQLiteError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "database is busy")
}

func parseInferenceFinishedStatsRow(events map[string][]string) (inferenceFinishedStatsRow, bool, error) {
	inferenceID, ok := firstEventValue(events, "inference_finished.inference_id")
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	requestedBy, ok := firstEventValue(events, "inference_finished.requested_by")
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}
	executedBy, ok := firstEventValue(events, "inference_finished.executed_by")
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}
	model, ok := firstEventValue(events, "inference_finished.model")
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	epochID, err := parseRequiredInt64(events, "inference_finished.epoch_id")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse epoch_id for inference %s: %w", inferenceID, err)
	}
	promptTokens, err := parseRequiredInt64(events, "inference_finished.prompt_token_count")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse prompt_token_count for inference %s: %w", inferenceID, err)
	}
	completionTokens, err := parseRequiredInt64(events, "inference_finished.completion_token_count")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse completion_token_count for inference %s: %w", inferenceID, err)
	}
	actualCost, err := parseRequiredInt64(events, "inference_finished.actual_cost")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse actual_cost for inference %s: %w", inferenceID, err)
	}
	endTimestampMs, err := parseRequiredInt64(events, "inference_finished.end_block_timestamp")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse end_block_timestamp for inference %s: %w", inferenceID, err)
	}
	txHeight, err := parseOptionalInt64(events, "tx.height")
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse tx.height for inference %s: %w", inferenceID, err)
	}

	return inferenceFinishedStatsRow{
		InferenceID:      inferenceID,
		RequestedBy:      requestedBy,
		ExecutedBy:       executedBy,
		Model:            model,
		EpochID:          epochID,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		ActualCost:       actualCost,
		EndTimestampMs:   endTimestampMs,
		TxHeight:         txHeight,
	}, true, nil
}

func firstEventValue(events map[string][]string, key string) (string, bool) {
	values := events[key]
	if len(values) == 0 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func parseRequiredInt64(events map[string][]string, key string) (int64, error) {
	raw, ok := firstEventValue(events, key)
	if !ok {
		return 0, fmt.Errorf("missing key %s", key)
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}

func parseOptionalInt64(events map[string][]string, key string) (int64, error) {
	raw, ok := firstEventValue(events, key)
	if !ok {
		return 0, nil
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}
