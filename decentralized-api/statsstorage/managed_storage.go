package statsstorage

import (
	"context"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	defaultRetentionDays = 30
	pruneInterval        = 24 * time.Hour
)

// ManagedStorage wraps StatsStorage with retention-based auto-pruning.
// Pruning runs on a static daily interval and can be disabled with non-positive retentionDays.
type ManagedStorage struct {
	storage       StatsStorage
	retentionDays int

	cancel context.CancelFunc
	once   sync.Once
}

func NewManagedStorage(storage StatsStorage, retentionDays int) StatsStorage {
	if storage == nil {
		return nil
	}
	m := &ManagedStorage{
		storage:       storage,
		retentionDays: retentionDays,
	}
	if retentionDays > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		go m.cleanupLoop(ctx)
		// Run one prune pass at startup so stale data does not linger until next daily tick.
		m.pruneOnce()
	} else {
		logging.Info("Stats auto-pruning is disabled", types.System, "retention_days", retentionDays)
	}
	return m
}

func (m *ManagedStorage) UpsertInference(ctx context.Context, rec InferenceRecord) error {
	return m.storage.UpsertInference(ctx, rec)
}

func (m *ManagedStorage) UpdateInferenceStatus(ctx context.Context, inferenceID, status string) error {
	return m.storage.UpdateInferenceStatus(ctx, inferenceID, status)
}

func (m *ManagedStorage) GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo UnixMillis) ([]InferenceRecord, error) {
	return m.storage.GetDeveloperInferencesByTime(ctx, developer, timeFrom, timeTo)
}

func (m *ManagedStorage) GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	return m.storage.GetSummaryByDeveloperEpochsBackwards(ctx, developer, epochsN)
}

func (m *ManagedStorage) GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	return m.storage.GetSummaryByEpochsBackwards(ctx, epochsN)
}

func (m *ManagedStorage) GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	return m.storage.GetSummaryByTimePeriod(ctx, timeFrom, timeTo)
}

func (m *ManagedStorage) GetModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	return m.storage.GetModelStatsByTime(ctx, timeFrom, timeTo)
}

func (m *ManagedStorage) GetDebugStats(ctx context.Context) (DebugStats, error) {
	return m.storage.GetDebugStats(ctx)
}

func (m *ManagedStorage) PruneOlderThan(ctx context.Context, cutoffTimestamp UnixMillis) error {
	return m.storage.PruneOlderThan(ctx, cutoffTimestamp)
}

func (m *ManagedStorage) Close() {
	m.once.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.storage.Close()
	})
}

func (m *ManagedStorage) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pruneOnce()
		}
	}
}

func (m *ManagedStorage) pruneOnce() {
	cutoff := time.Now().Add(-time.Duration(m.retentionDays) * 24 * time.Hour).UnixMilli()
	if err := m.storage.PruneOlderThan(context.Background(), UnixMillis(cutoff)); err != nil {
		logging.Warn("Stats auto-prune failed", types.System, "retention_days", m.retentionDays, "error", err)
		return
	}
	logging.Info("Stats auto-prune completed", types.System, "retention_days", m.retentionDays)
}

var _ StatsStorage = (*ManagedStorage)(nil)
