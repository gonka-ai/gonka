package statsstorage

import (
	"context"
	"errors"
)

var ErrStatsDisabled = errors.New("stats storage is disabled")

// DisabledStorage is a no-op implementation of StatsStorage that returns an error for all operations.
type DisabledStorage struct{}

func (d *DisabledStorage) UpsertInference(ctx context.Context, rec InferenceRecord) error {
	return nil
}

func (d *DisabledStorage) UpdateInferenceStatus(ctx context.Context, inferenceID, status string) error {
	return nil
}

func (d *DisabledStorage) GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo UnixMillis) ([]InferenceRecord, error) {
	return nil, ErrStatsDisabled
}

func (d *DisabledStorage) GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	return nil, ErrStatsDisabled
}

func (d *DisabledStorage) GetDebugStats(ctx context.Context) (DebugStats, error) {
	return DebugStats{}, ErrStatsDisabled
}

func (d *DisabledStorage) PruneOlderThan(ctx context.Context, cutoffTimestamp UnixMillis) error {
	return nil
}

func (d *DisabledStorage) Close() {}

var _ StatsStorage = (*DisabledStorage)(nil)
