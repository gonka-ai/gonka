package statsstorage

import (
	"context"
	"os"
	"strconv"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// NewStatsStorage creates a stats storage backend.
// Uses PostgreSQL if DAPI_STATS_POSTGRES_ENABLED is set to "true" and PGHOST is configured.
// File storage is used if DAPI_STATS_FILE_STORAGE_ENABLED is set to "true".
// Otherwise, returns a disabled storage that returns errors for all stats operations.
func NewStatsStorage(ctx context.Context) (StatsStorage, error) {
	retentionDays := parseRetentionDays()

	if os.Getenv("DAPI_STATS_POSTGRES_ENABLED") == "true" && os.Getenv("PGHOST") != "" {
		pgStorage, err := NewPostgresStorage(ctx)
		if err != nil {
			return nil, err
		}
		pgHost := os.Getenv("PGHOST")
		logging.Info("Using PostgreSQL stats storage", types.System, "host", pgHost, "retention_days", retentionDays)
		return NewManagedStorage(pgStorage, retentionDays), nil
	}

	if os.Getenv("DAPI_STATS_FILE_STORAGE_ENABLED") == "true" {
		fileBasePath := os.Getenv("DAPI_STATS_STORAGE_PATH")
		if fileBasePath == "" {
			fileBasePath = "/root/.dapi/data/stats"
		}
		logging.Warn("CRITICAL: File-based stats storage is ENABLED. Use at your own peril.", types.System, "path", fileBasePath)
		fileStorage := NewFileStorage(fileBasePath)
		return NewManagedStorage(fileStorage, retentionDays), nil
	}

	logging.Info("Stats storage is disabled", types.System)
	return &DisabledStorage{}, nil
}

func parseRetentionDays() int {
	raw := os.Getenv("DAPI_STATS_RETENTION_DAYS")
	if raw == "" {
		return defaultRetentionDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		logging.Warn("Invalid DAPI_STATS_RETENTION_DAYS, using default", types.System, "value", raw, "default", defaultRetentionDays, "error", err)
		return defaultRetentionDays
	}
	return n
}
