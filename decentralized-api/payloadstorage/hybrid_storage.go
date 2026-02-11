package payloadstorage

import (
	"context"
	"errors"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	pgConnectTimeout = 2 * time.Second
)

// HybridStorage uses PostgreSQL as primary storage with file-based fallback.
// Store: tries PG first (with lazy reconnection), falls back to file on error.
// Retrieve: tries PG first (no reconnection delay), on error OR not found also checks file.
// PruneEpoch: prunes both (best effort, no reconnection delay).
type HybridStorage struct {
	pg            *PostgresStorage
	file          *FileStorage
	mu            sync.Mutex
	lastRetry     time.Time
	retryInterval time.Duration
}

func NewHybridStorage(pg *PostgresStorage, file *FileStorage, retryInterval time.Duration) *HybridStorage {
	return &HybridStorage{pg: pg, file: file, retryInterval: retryInterval}
}

// shouldAttemptConnect checks if reconnection should be attempted.
// Returns (shouldAttempt, existingPg).
// If shouldAttempt is true, lastRetry is updated and caller should attempt connection.
// If shouldAttempt is false, existingPg is the current state (nil if rate-limited).
func (h *HybridStorage) shouldAttemptConnect() (bool, *PostgresStorage) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pg != nil {
		return false, h.pg
	}
	if time.Since(h.lastRetry) < h.retryInterval {
		return false, nil
	}
	h.lastRetry = time.Now()
	return true, nil
}

// saveConnection stores a successful connection.
func (h *HybridStorage) saveConnection(pg *PostgresStorage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	logging.Info("PostgreSQL connection established", types.PayloadStorage)
	h.pg = pg
}

// getOrConnectPg returns the current PostgresStorage or attempts to reconnect.
// Used by Store only. Non-blocking: other goroutines return immediately while one connects.
// Rate-limited to one attempt per 240s.
func (h *HybridStorage) getOrConnectPg(ctx context.Context) *PostgresStorage {
	shouldAttempt, pg := h.shouldAttemptConnect()
	if !shouldAttempt {
		return pg
	}

	connectCtx, cancel := context.WithTimeout(ctx, pgConnectTimeout)
	defer cancel()

	newPg, err := NewPostgresStorage(connectCtx)
	if err != nil {
		logging.Debug("PostgreSQL reconnect failed", types.PayloadStorage, "error", err)
		return nil
	}

	h.saveConnection(newPg)
	return newPg
}

// currentPg returns the current PostgresStorage without attempting reconnection.
// Used by Retrieve and PruneEpoch - never blocks for reconnection.
func (h *HybridStorage) currentPg() *PostgresStorage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pg
}

func (h *HybridStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	if pg := h.getOrConnectPg(ctx); pg != nil {
		err := pg.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store failed, falling back to file", types.PayloadStorage,
			"inferenceId", inferenceId, "error", err)
	}
	return h.file.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)
}

func (h *HybridStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	if pg := h.currentPg(); pg != nil {
		prompt, response, err := pg.Retrieve(ctx, inferenceId, epochId)
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

	// No cached PG connection - try file storage first
	prompt, response, fileErr := h.file.Retrieve(ctx, inferenceId, epochId)
	if fileErr == nil {
		return prompt, response, nil
	}

	// File failed - attempt PG connection and check there
	// Data might exist in PG from previous session when PG was available
	if pg := h.getOrConnectPg(ctx); pg != nil {
		prompt, response, pgErr := pg.Retrieve(ctx, inferenceId, epochId)
		if pgErr == nil {
			return prompt, response, nil
		}
		if errors.Is(pgErr, ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, pgErr
	}

	return nil, nil, fileErr
}

func (h *HybridStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	// Best effort: prune both storages (no reconnection delay for prune)
	var pgErr error
	if pg := h.currentPg(); pg != nil {
		pgErr = pg.PruneEpoch(ctx, epochId)
		if pgErr != nil {
			logging.Warn("PostgreSQL prune failed", types.PayloadStorage, "epochId", epochId, "error", pgErr)
		}
	}

	fileErr := h.file.PruneEpoch(ctx, epochId)
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
