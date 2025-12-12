package payloadstorage

import (
	"context"
	"errors"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// HybridStorage uses PostgreSQL as primary storage with file-based fallback.
// Store: tries PG first, falls back to file on error.
// Retrieve: tries PG first, on error OR not found also checks file.
// PruneEpoch: prunes both (best effort).
type HybridStorage struct {
	pg   *PostgresStorage
	file *FileStorage
}

func NewHybridStorage(pg *PostgresStorage, file *FileStorage) *HybridStorage {
	return &HybridStorage{pg: pg, file: file}
}

func (h *HybridStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	err := h.pg.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)
	if err != nil {
		logging.Warn("PostgreSQL store failed, falling back to file", types.PayloadStorage,
			"inferenceId", inferenceId, "error", err)
		return h.file.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)
	}
	return nil
}

func (h *HybridStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	prompt, response, err := h.pg.Retrieve(ctx, inferenceId, epochId)
	if err == nil {
		return prompt, response, nil
	}

	// On any error (including not found), also check file storage
	// This handles: PG down, data written to file during PG outage, migration scenarios
	if !errors.Is(err, ErrNotFound) {
		logging.Debug("PostgreSQL retrieve failed, checking file", types.PayloadStorage,
			"inferenceId", inferenceId, "error", err)
	}

	prompt, response, fileErr := h.file.Retrieve(ctx, inferenceId, epochId)
	if fileErr == nil {
		return prompt, response, nil
	}

	// Both failed - return original PG error if it wasn't "not found"
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrNotFound
	}
	return nil, nil, err
}

func (h *HybridStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	// Best effort: prune both storages
	pgErr := h.pg.PruneEpoch(ctx, epochId)
	fileErr := h.file.PruneEpoch(ctx, epochId)

	if pgErr != nil {
		logging.Warn("PostgreSQL prune failed", types.PayloadStorage, "epochId", epochId, "error", pgErr)
	}
	if fileErr != nil {
		logging.Warn("File prune failed", types.PayloadStorage, "epochId", epochId, "error", fileErr)
	}

	// Return PG error if any, otherwise file error
	if pgErr != nil {
		return pgErr
	}
	return fileErr
}

var _ PayloadStorage = (*HybridStorage)(nil)
