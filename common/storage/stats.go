package storage

import (
	"context"
	"fmt"
)

const statsSchema = `
CREATE TABLE IF NOT EXISTS inference_stats (
    inference_id           TEXT    PRIMARY KEY,
    requested_by           TEXT    NOT NULL,
    model                  TEXT    NOT NULL,
    status                 TEXT    NOT NULL,
    epoch_id               BIGINT  NOT NULL,
    prompt_token_count     BIGINT  NOT NULL,
    completion_token_count BIGINT  NOT NULL,
    total_token_count      BIGINT  NOT NULL,
    actual_cost_in_coins   BIGINT  NOT NULL,
    start_block_timestamp  BIGINT  NOT NULL,
    end_block_timestamp    BIGINT  NOT NULL,
    inference_timestamp    BIGINT  NOT NULL,
    created_at             TIMESTAMP DEFAULT NOW(),
    updated_at             TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS inference_stats_requested_by_time_idx ON inference_stats (requested_by, inference_timestamp);
CREATE INDEX IF NOT EXISTS inference_stats_epoch_idx             ON inference_stats (epoch_id);
CREATE INDEX IF NOT EXISTS inference_stats_model_time_idx        ON inference_stats (model, inference_timestamp);
CREATE INDEX IF NOT EXISTS inference_stats_inference_time_idx    ON inference_stats (inference_timestamp);
`

func (s *Storage) ensureStatsSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, statsSchema); err != nil {
		return fmt.Errorf("storage: ensure inference_stats schema: %w", err)
	}
	return nil
}

type UnixMillis int64

// InferenceStats holds per-inference billing and analytics data.
type InferenceRecord struct {
	InferenceID          string
	RequestedBy          string
	Model                string
	Status               string
	EpochID              uint64
	PromptTokenCount     uint64
	CompletionTokenCount uint64
	TotalTokenCount      uint64
	ActualCostInCoins    int64
	StartBlockTimestamp  UnixMillis
	EndBlockTimestamp    UnixMillis
	InferenceTimestamp   UnixMillis
}

// UpsertStats inserts or updates a row in inference_stats.
func (s *Storage) UpsertStats(ctx context.Context, stats InferenceRecord) error {
	inferenceTimestamp := stats.EndBlockTimestamp
	if inferenceTimestamp == 0 {
		inferenceTimestamp = stats.StartBlockTimestamp
	}
	totalTokenCount := stats.TotalTokenCount
	if totalTokenCount == 0 {
		totalTokenCount = stats.PromptTokenCount + stats.CompletionTokenCount
	}

	const q = `
INSERT INTO inference_stats (
    inference_id, requested_by, model, status, epoch_id,
    prompt_token_count, completion_token_count, total_token_count,
    actual_cost_in_coins, start_block_timestamp, end_block_timestamp, inference_timestamp, updated_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8,
    $9, $10, $11, $12, NOW()
)
ON CONFLICT (inference_id) DO UPDATE SET
    requested_by = EXCLUDED.requested_by,
    model = EXCLUDED.model,
    status = EXCLUDED.status,
    epoch_id = EXCLUDED.epoch_id,
    prompt_token_count = EXCLUDED.prompt_token_count,
    completion_token_count = EXCLUDED.completion_token_count,
    total_token_count = EXCLUDED.total_token_count,
    actual_cost_in_coins = EXCLUDED.actual_cost_in_coins,
    start_block_timestamp = EXCLUDED.start_block_timestamp,
    end_block_timestamp = EXCLUDED.end_block_timestamp,
    inference_timestamp = EXCLUDED.inference_timestamp,
    updated_at = NOW()
`
	_, err := s.pool.Exec(
		ctx,
		q,
		stats.InferenceID,
		stats.RequestedBy,
		stats.Model,
		stats.Status,
		stats.EpochID,
		stats.PromptTokenCount,
		stats.CompletionTokenCount,
		totalTokenCount,
		stats.ActualCostInCoins,
		stats.StartBlockTimestamp,
		stats.EndBlockTimestamp,
		inferenceTimestamp,
	)
	if err != nil {
		return fmt.Errorf("upsert inference_stats: %w", err)
	}
	return nil
}
