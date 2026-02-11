package payloadstorage

import (
	"context"
	"os"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// NewPayloadStorage creates a PayloadStorage based on environment configuration.
// If PGHOST is set, uses HybridStorage (PG primary + file fallback).
// If PostgreSQL is not accessible at startup, HybridStorage will retry lazily on Store operations.
// If PGHOST is not set, uses FileStorage only.
func NewPayloadStorage(ctx context.Context, fileBasePath string) PayloadStorage {
	fileStorage := NewFileStorage(fileBasePath)

	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		logging.Info("PGHOST not set, using file storage only", types.PayloadStorage)
		return fileStorage
	}

	retryIntervalStr := os.Getenv("PG_RETRY_INTERVAL")
	retryInterval, err := time.ParseDuration(retryIntervalStr)
	if err != nil || retryInterval <= 0 {
		retryInterval = 240 * time.Second
	}

	pgStorage, err := NewPostgresStorage(ctx)
	if err != nil {
		logging.Warn("PostgreSQL connection failed, will retry lazily on Store", types.PayloadStorage,
			"host", pgHost, "error", err)
		return NewHybridStorage(nil, fileStorage, retryInterval)
	}

	logging.Info("Using PostgreSQL with file fallback", types.PayloadStorage, "host", pgHost)
	return NewHybridStorage(pgStorage, fileStorage, retryInterval)
}
