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

func (d *DisabledStorage) UpsertDevshardEscrow(ctx context.Context, totals DevshardEscrow, modelStats []DevshardModelAggregate) error {
	return nil
}

func (d *DisabledStorage) GetMaxInferenceEpoch(ctx context.Context) (uint64, error) {
	return 0, ErrStatsDisabled
}

func (d *DisabledStorage) GetMaxDevshardEpoch(ctx context.Context) (uint64, error) {
	return 0, ErrStatsDisabled
}

func (d *DisabledStorage) GetSummaryByDeveloperEpochRange(ctx context.Context, developer string, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetSummaryByEpochRange(ctx context.Context, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardSummaryByDeveloperEpochRange(ctx context.Context, developer string, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardSummaryByEpochRange(ctx context.Context, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	return Summary{}, ErrStatsDisabled
}

func (d *DisabledStorage) GetDevshardModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	return nil, ErrStatsDisabled
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
