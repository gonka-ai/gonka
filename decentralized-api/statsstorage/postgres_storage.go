package statsstorage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const createInferenceStatsTableSQL = `
CREATE TABLE IF NOT EXISTS inference_stats (
    inference_id TEXT PRIMARY KEY,
    requested_by TEXT NOT NULL,
    model TEXT NOT NULL,
    status TEXT NOT NULL,
    epoch_id BIGINT NOT NULL,
    prompt_token_count BIGINT NOT NULL,
    completion_token_count BIGINT NOT NULL,
    total_token_count BIGINT NOT NULL,
    actual_cost_in_coins BIGINT NOT NULL,
    start_block_timestamp BIGINT NOT NULL,
    end_block_timestamp BIGINT NOT NULL,
    inference_timestamp BIGINT NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS inference_stats_requested_by_time_idx
    ON inference_stats (requested_by, inference_timestamp);

CREATE INDEX IF NOT EXISTS inference_stats_epoch_idx
    ON inference_stats (epoch_id);

CREATE INDEX IF NOT EXISTS inference_stats_model_time_idx
    ON inference_stats (model, inference_timestamp);

CREATE INDEX IF NOT EXISTS inference_stats_inference_time_idx
    ON inference_stats (inference_timestamp);
`

type PostgresStorage struct {
	pool *pgxpool.Pool
}

func NewPostgresStorage(ctx context.Context) (*PostgresStorage, error) {
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &PostgresStorage{pool: pool}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStorage) ensureSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, createInferenceStatsTableSQL); err != nil {
		return fmt.Errorf("create inference_stats schema: %w", err)
	}
	return nil
}

func (s *PostgresStorage) UpsertInference(ctx context.Context, rec InferenceRecord) error {
	inferenceTimestamp := rec.EndBlockTimestamp
	if inferenceTimestamp == 0 {
		inferenceTimestamp = rec.StartBlockTimestamp
	}
	totalTokenCount := rec.TotalTokenCount
	if totalTokenCount == 0 {
		totalTokenCount = rec.PromptTokenCount + rec.CompletionTokenCount
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
		rec.InferenceID,
		rec.RequestedBy,
		rec.Model,
		rec.Status,
		rec.EpochID,
		rec.PromptTokenCount,
		rec.CompletionTokenCount,
		totalTokenCount,
		rec.ActualCostInCoins,
		rec.StartBlockTimestamp,
		rec.EndBlockTimestamp,
		inferenceTimestamp,
	)
	if err != nil {
		return fmt.Errorf("upsert inference_stats: %w", err)
	}
	return nil
}

func (s *PostgresStorage) UpdateInferenceStatus(ctx context.Context, inferenceID, status string) error {
	const q = `
UPDATE inference_stats
SET status = $2, updated_at = NOW()
WHERE inference_id = $1
`
	tag, err := s.pool.Exec(ctx, q, inferenceID, status)
	if err != nil {
		return fmt.Errorf("update inference status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInferenceRecordNotFound
	}
	return nil
}

func (s *PostgresStorage) GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo UnixMillis) ([]InferenceRecord, error) {
	const q = `
SELECT
    inference_id, requested_by, model, status, epoch_id,
    prompt_token_count, completion_token_count, total_token_count,
    actual_cost_in_coins, start_block_timestamp, end_block_timestamp, inference_timestamp
FROM inference_stats
WHERE requested_by = $1
  AND inference_timestamp >= $2
  AND inference_timestamp <= $3
ORDER BY inference_timestamp ASC, inference_id ASC
`
	rows, err := s.pool.Query(ctx, q, developer, timeFrom, timeTo)
	if err != nil {
		return nil, fmt.Errorf("query developer inferences by time: %w", err)
	}
	defer rows.Close()

	res := make([]InferenceRecord, 0)
	for rows.Next() {
		var r InferenceRecord
		if err := rows.Scan(
			&r.InferenceID,
			&r.RequestedBy,
			&r.Model,
			&r.Status,
			&r.EpochID,
			&r.PromptTokenCount,
			&r.CompletionTokenCount,
			&r.TotalTokenCount,
			&r.ActualCostInCoins,
			&r.StartBlockTimestamp,
			&r.EndBlockTimestamp,
			&r.InferenceTimestamp,
		); err != nil {
			return nil, fmt.Errorf("scan developer inference record: %w", err)
		}
		res = append(res, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate developer inferences by time: %w", err)
	}
	return res, nil
}

func (s *PostgresStorage) GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	const q = `
WITH bounds AS (
    SELECT COALESCE(MAX(epoch_id), 0) AS max_epoch
    FROM inference_stats
)
SELECT
    COALESCE(SUM(total_token_count), 0) AS ai_tokens,
    COALESCE(COUNT(*), 0) AS inferences,
    COALESCE(SUM(actual_cost_in_coins), 0) AS actual_inferences_cost
FROM inference_stats, bounds
WHERE requested_by = $1
  AND epoch_id > GREATEST(bounds.max_epoch - $2, 0)
  AND epoch_id <= bounds.max_epoch
`
	return s.scanSummaryRow(ctx, q, developer, epochsN)
}

func (s *PostgresStorage) GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	const q = `
WITH bounds AS (
    SELECT COALESCE(MAX(epoch_id), 0) AS max_epoch
    FROM inference_stats
)
SELECT
    COALESCE(SUM(total_token_count), 0) AS ai_tokens,
    COALESCE(COUNT(*), 0) AS inferences,
    COALESCE(SUM(actual_cost_in_coins), 0) AS actual_inferences_cost
FROM inference_stats, bounds
WHERE epoch_id > GREATEST(bounds.max_epoch - $1, 0)
  AND epoch_id <= bounds.max_epoch
`
	return s.scanSummaryRow(ctx, q, epochsN)
}

func (s *PostgresStorage) GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	const q = `
SELECT
    COALESCE(SUM(total_token_count), 0) AS ai_tokens,
    COALESCE(COUNT(*), 0) AS inferences,
    COALESCE(SUM(actual_cost_in_coins), 0) AS actual_inferences_cost
FROM inference_stats
WHERE inference_timestamp >= $1
  AND inference_timestamp <= $2
`
	return s.scanSummaryRow(ctx, q, timeFrom, timeTo)
}

func (s *PostgresStorage) GetModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	const q = `
SELECT
    model,
    COALESCE(SUM(total_token_count), 0) AS ai_tokens,
    COALESCE(COUNT(*), 0) AS inferences
FROM inference_stats
WHERE inference_timestamp >= $1
  AND inference_timestamp <= $2
GROUP BY model
ORDER BY model ASC
`
	rows, err := s.pool.Query(ctx, q, timeFrom, timeTo)
	if err != nil {
		return nil, fmt.Errorf("query model stats by time: %w", err)
	}
	defer rows.Close()

	res := make([]ModelSummary, 0)
	for rows.Next() {
		var (
			ms          ModelSummary
			inferencesI int64
		)
		if err := rows.Scan(&ms.Model, &ms.AiTokens, &inferencesI); err != nil {
			return nil, fmt.Errorf("scan model stats row: %w", err)
		}
		ms.Inferences = int32(inferencesI)
		res = append(res, ms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model stats rows: %w", err)
	}
	return res, nil
}

func (s *PostgresStorage) GetDebugStats(ctx context.Context) (DebugStats, error) {
	timeStatsByDeveloper, err := s.getDebugStatsByTime(ctx)
	if err != nil {
		return DebugStats{}, err
	}
	epochStatsByDeveloper, err := s.getDebugStatsByEpoch(ctx)
	if err != nil {
		return DebugStats{}, err
	}
	return DebugStats{
		StatsByTime:  timeStatsByDeveloper,
		StatsByEpoch: epochStatsByDeveloper,
	}, nil
}

func (s *PostgresStorage) PruneOlderThan(ctx context.Context, cutoffTimestamp UnixMillis) error {
	const q = `
DELETE FROM inference_stats
WHERE inference_timestamp < $1
`
	if _, err := s.pool.Exec(ctx, q, cutoffTimestamp); err != nil {
		return fmt.Errorf("prune inference_stats older than cutoff: %w", err)
	}
	return nil
}

func (s *PostgresStorage) getDebugStatsByTime(ctx context.Context) ([]DeveloperTimeStats, error) {
	const q = `
SELECT
    requested_by,
    inference_id, model, status, epoch_id,
    prompt_token_count, completion_token_count, total_token_count,
    actual_cost_in_coins, start_block_timestamp, end_block_timestamp, inference_timestamp
FROM inference_stats
ORDER BY requested_by ASC, inference_timestamp ASC, inference_id ASC
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query debug stats by time: %w", err)
	}
	defer rows.Close()

	byDeveloper := make(map[string][]InferenceRecord)
	order := make([]string, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var (
			developer string
			r         InferenceRecord
		)
		if err := rows.Scan(
			&developer,
			&r.InferenceID,
			&r.Model,
			&r.Status,
			&r.EpochID,
			&r.PromptTokenCount,
			&r.CompletionTokenCount,
			&r.TotalTokenCount,
			&r.ActualCostInCoins,
			&r.StartBlockTimestamp,
			&r.EndBlockTimestamp,
			&r.InferenceTimestamp,
		); err != nil {
			return nil, fmt.Errorf("scan debug stats by time row: %w", err)
		}
		r.RequestedBy = developer
		if _, ok := seen[developer]; !ok {
			seen[developer] = struct{}{}
			order = append(order, developer)
		}
		byDeveloper[developer] = append(byDeveloper[developer], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate debug stats by time rows: %w", err)
	}

	res := make([]DeveloperTimeStats, 0, len(order))
	for _, developer := range order {
		res = append(res, DeveloperTimeStats{
			Developer: developer,
			Stats:     byDeveloper[developer],
		})
	}
	return res, nil
}

func (s *PostgresStorage) getDebugStatsByEpoch(ctx context.Context) ([]DeveloperEpochStats, error) {
	const q = `
SELECT
    requested_by,
    epoch_id,
    COALESCE(array_agg(inference_id ORDER BY inference_id), '{}') AS inference_ids
FROM inference_stats
GROUP BY requested_by, epoch_id
ORDER BY requested_by ASC, epoch_id ASC
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query debug stats by epoch: %w", err)
	}
	defer rows.Close()

	res := make([]DeveloperEpochStats, 0)
	for rows.Next() {
		var r DeveloperEpochStats
		if err := rows.Scan(&r.Developer, &r.EpochID, &r.InferenceIDs); err != nil {
			return nil, fmt.Errorf("scan debug stats by epoch row: %w", err)
		}
		res = append(res, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate debug stats by epoch rows: %w", err)
	}
	return res, nil
}

func (s *PostgresStorage) scanSummaryRow(ctx context.Context, query string, args ...interface{}) (Summary, error) {
	var (
		summary     Summary
		inferencesI int64
	)
	if err := s.pool.QueryRow(ctx, query, args...).Scan(
		&summary.AiTokens,
		&inferencesI,
		&summary.ActualInferencesCost,
	); err != nil {
		return Summary{}, fmt.Errorf("query summary: %w", err)
	}
	summary.Inferences = int32(inferencesI)
	return summary, nil
}

func (s *PostgresStorage) Close() {
	s.pool.Close()
}

var _ StatsStorage = (*PostgresStorage)(nil)
