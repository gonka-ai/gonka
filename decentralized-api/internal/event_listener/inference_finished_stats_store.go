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
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
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
CREATE INDEX IF NOT EXISTS idx_ifs_time ON inference_finished_stats(end_timestamp_ms);
CREATE INDEX IF NOT EXISTS idx_ifs_time_model ON inference_finished_stats(end_timestamp_ms, model);
CREATE INDEX IF NOT EXISTS idx_ifs_model_time ON inference_finished_stats(model, end_timestamp_ms);
CREATE INDEX IF NOT EXISTS idx_ifs_requested_by_time ON inference_finished_stats(requested_by, end_timestamp_ms);
`

const (
	statsWriterMaxRetries = 4
	statsRetentionPeriod  = 30 * 24 * time.Hour
	statsPruneInterval    = 6 * time.Hour
	statsPruneTimeout     = 2 * time.Second
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
	db          *sql.DB
	mu          sync.Mutex
	lastPruneAt time.Time
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

	rows, skipped, err := parseInferenceFinishedStatsRows(events)
	if err != nil {
		return err
	}
	if skipped > 0 && hasRichInferenceFinishedPayload(events) {
		logging.Warn("Skipped malformed inference_finished stats rows", types.EventProcessing, "skipped", skipped, "total", len(events["inference_finished.inference_id"]))
	}
	if len(rows) == 0 {
		return nil
	}

	return s.upsertRowsWithRetry(rows)
}

func (s *InferenceFinishedStatsStore) upsertRowsWithRetry(rows []inferenceFinishedStatsRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybePruneLocked()

	var lastErr error
	for attempt := 0; attempt < statsWriterMaxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.upsertRows(ctx, rows)
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

func (s *InferenceFinishedStatsStore) maybePruneLocked() {
	now := time.Now()
	if !s.lastPruneAt.IsZero() && now.Sub(s.lastPruneAt) < statsPruneInterval {
		return
	}
	s.lastPruneAt = now

	cutoffMillis := now.Add(-statsRetentionPeriod).UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), statsPruneTimeout)
	defer cancel()

	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM inference_finished_stats WHERE end_timestamp_ms < ?`,
		cutoffMillis,
	)
	if err != nil {
		logging.Warn("Failed to prune inference_finished stats", types.EventProcessing, "error", err)
		return
	}
	deletedRows, err := result.RowsAffected()
	if err == nil && deletedRows > 0 {
		logging.Info("Pruned old inference_finished stats", types.EventProcessing, "deleted_rows", deletedRows, "cutoff_ms", cutoffMillis)
	}
}

func (s *InferenceFinishedStatsStore) upsertRows(ctx context.Context, rows []inferenceFinishedStatsRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, row := range rows {
		_, err = tx.ExecContext(
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
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func isRetryableSQLiteError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database is busy") ||
		strings.Contains(msg, "context deadline exceeded")
}

func parseInferenceFinishedStatsRows(events map[string][]string) ([]inferenceFinishedStatsRow, int, error) {
	ids := events["inference_finished.inference_id"]
	if len(ids) == 0 {
		return nil, 0, nil
	}

	rows := make([]inferenceFinishedStatsRow, 0, len(ids))
	skipped := 0
	for idx := range ids {
		row, ok, err := parseInferenceFinishedStatsRowAt(events, idx)
		if err != nil {
			return nil, skipped, err
		}
		if !ok {
			skipped++
			continue
		}
		rows = append(rows, row)
	}
	return rows, skipped, nil
}

func parseInferenceFinishedStatsRowAt(events map[string][]string, index int) (inferenceFinishedStatsRow, bool, error) {
	inferenceID, ok := firstEventValueAt(events, "inference_finished.inference_id", index, false)
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	requestedBy, ok := firstEventValueAt(events, "inference_finished.requested_by", index, false)
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}
	executedBy, ok := firstEventValueAt(events, "inference_finished.executed_by", index, false)
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}
	model, ok := firstEventValueAt(events, "inference_finished.model", index, false)
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	epochID, ok, err := parseRequiredInt64At(events, "inference_finished.epoch_id", index, false)
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse epoch_id for inference %s: %w", inferenceID, err)
	}
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	promptTokens, ok, err := parseRequiredInt64At(events, "inference_finished.prompt_token_count", index, false)
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse prompt_token_count for inference %s: %w", inferenceID, err)
	}
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	completionTokens, ok, err := parseRequiredInt64At(events, "inference_finished.completion_token_count", index, false)
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse completion_token_count for inference %s: %w", inferenceID, err)
	}
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	actualCost, ok, err := parseRequiredInt64At(events, "inference_finished.actual_cost", index, false)
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse actual_cost for inference %s: %w", inferenceID, err)
	}
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	endTimestampMs, ok, err := parseRequiredInt64At(events, "inference_finished.end_block_timestamp", index, false)
	if err != nil {
		return inferenceFinishedStatsRow{}, false, fmt.Errorf("parse end_block_timestamp for inference %s: %w", inferenceID, err)
	}
	if !ok {
		return inferenceFinishedStatsRow{}, false, nil
	}

	txHeight, err := parseOptionalInt64At(events, "tx.height", index, true)
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

func firstEventValueAt(events map[string][]string, key string, index int, allowShared bool) (string, bool) {
	values := events[key]
	if len(values) == 0 {
		return "", false
	}
	if index < len(values) && values[index] != "" {
		return values[index], true
	}
	if allowShared && len(values) == 1 && values[0] != "" {
		return values[0], true
	}
	return "", false
}

func parseRequiredInt64At(events map[string][]string, key string, index int, allowShared bool) (int64, bool, error) {
	raw, ok := firstEventValueAt(events, key, index, allowShared)
	if !ok {
		return 0, false, nil
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return val, true, nil
}

func parseOptionalInt64At(events map[string][]string, key string, index int, allowShared bool) (int64, error) {
	raw, ok := firstEventValueAt(events, key, index, allowShared)
	if !ok {
		return 0, nil
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}

func hasRichInferenceFinishedPayload(events map[string][]string) bool {
	return len(events["inference_finished.requested_by"]) > 0 ||
		len(events["inference_finished.executed_by"]) > 0 ||
		len(events["inference_finished.model"]) > 0
}
