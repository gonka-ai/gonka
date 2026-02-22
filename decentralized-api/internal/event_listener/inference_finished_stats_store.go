package event_listener

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
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

type InferenceFinishedStatsStore struct {
	db *sql.DB
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

	inferenceID, ok := firstEventValue(events, "inference_finished.inference_id")
	if !ok {
		return nil
	}

	// Backward compatible: old events include only inference_id.
	requestedBy, ok := firstEventValue(events, "inference_finished.requested_by")
	if !ok {
		return nil
	}
	executedBy, ok := firstEventValue(events, "inference_finished.executed_by")
	if !ok {
		return nil
	}
	model, ok := firstEventValue(events, "inference_finished.model")
	if !ok {
		return nil
	}

	epochID, err := parseRequiredInt64(events, "inference_finished.epoch_id")
	if err != nil {
		return fmt.Errorf("parse epoch_id for inference %s: %w", inferenceID, err)
	}
	promptTokens, err := parseRequiredInt64(events, "inference_finished.prompt_token_count")
	if err != nil {
		return fmt.Errorf("parse prompt_token_count for inference %s: %w", inferenceID, err)
	}
	completionTokens, err := parseRequiredInt64(events, "inference_finished.completion_token_count")
	if err != nil {
		return fmt.Errorf("parse completion_token_count for inference %s: %w", inferenceID, err)
	}
	actualCost, err := parseRequiredInt64(events, "inference_finished.actual_cost")
	if err != nil {
		return fmt.Errorf("parse actual_cost for inference %s: %w", inferenceID, err)
	}
	endTimestampMs, err := parseRequiredInt64(events, "inference_finished.end_block_timestamp")
	if err != nil {
		return fmt.Errorf("parse end_block_timestamp for inference %s: %w", inferenceID, err)
	}

	txHeight, err := parseOptionalInt64(events, "tx.height")
	if err != nil {
		return fmt.Errorf("parse tx.height for inference %s: %w", inferenceID, err)
	}

	totalTokens := promptTokens + completionTokens

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const upsertSQL = `
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

	_, err = s.db.ExecContext(
		ctx,
		upsertSQL,
		inferenceID,
		requestedBy,
		executedBy,
		model,
		epochID,
		promptTokens,
		completionTokens,
		totalTokens,
		actualCost,
		endTimestampMs,
		txHeight,
	)
	return err
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
