package payloads

import (
	"context"
	"errors"
	"sync"
	"time"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

const pgConnectTimeout = 2 * time.Second

// HybridStorage uses PostgreSQL as primary storage with file-based fallback.
// Store: tries PG first (with lazy reconnection), falls back to file on error.
// Retrieve: tries PG first, on error or not found also checks file.
// DropEpoch: prunes both (best effort).
type HybridStorage struct {
	pg            *postgresStorage
	file          *FileStorage
	mu            sync.Mutex
	lastRetry     time.Time
	retryInterval time.Duration
}

func NewHybridStorage(pg *postgresStorage, file *FileStorage, retryInterval time.Duration) *HybridStorage {
	return &HybridStorage{pg: pg, file: file, retryInterval: retryInterval}
}

func (h *HybridStorage) shouldAttemptConnect() (bool, *postgresStorage) {
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

func (h *HybridStorage) saveConnection(pg *postgresStorage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	logging.Info("PostgreSQL payload connection established", types.PayloadStorage)
	h.pg = pg
}

func (h *HybridStorage) getOrConnectPg(ctx context.Context) *postgresStorage {
	shouldAttempt, pg := h.shouldAttemptConnect()
	if !shouldAttempt {
		return pg
	}

	connectCtx, cancel := context.WithTimeout(ctx, pgConnectTimeout)
	defer cancel()

	newPg, err := newPostgresStorage(connectCtx)
	if err != nil {
		logging.Debug("PostgreSQL payload reconnect failed", types.PayloadStorage, "error", err)
		return nil
	}

	h.saveConnection(newPg)
	return newPg
}

func (h *HybridStorage) currentPg() *postgresStorage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pg
}

func (h *HybridStorage) Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error {
	if pg := h.getOrConnectPg(ctx); pg != nil {
		err := pg.Store(ctx, escrowId, inferenceId, epochId, promptPayload, responsePayload)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL payload store failed, falling back to file", types.PayloadStorage,
			"escrowId", escrowId, "inferenceId", inferenceId, "error", err)
	}
	return h.file.Store(ctx, escrowId, inferenceId, epochId, promptPayload, responsePayload)
}

func (h *HybridStorage) Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) ([]byte, []byte, error) {
	if pg := h.currentPg(); pg != nil {
		prompt, response, err := pg.Retrieve(ctx, escrowId, inferenceId, epochId)
		if err == nil {
			return prompt, response, nil
		}

		if !errors.Is(err, ErrNotFound) {
			logging.Debug("PostgreSQL payload retrieve failed, checking file", types.PayloadStorage,
				"escrowId", escrowId, "inferenceId", inferenceId, "error", err)
		}

		prompt, response, fileErr := h.file.Retrieve(ctx, escrowId, inferenceId, epochId)
		if fileErr == nil {
			return prompt, response, nil
		}
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	prompt, response, fileErr := h.file.Retrieve(ctx, escrowId, inferenceId, epochId)
	if fileErr == nil {
		return prompt, response, nil
	}

	if pg := h.getOrConnectPg(ctx); pg != nil {
		prompt, response, pgErr := pg.Retrieve(ctx, escrowId, inferenceId, epochId)
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

func (h *HybridStorage) DropEpoch(ctx context.Context, epochId uint64) error {
	var pgErr error
	if pg := h.currentPg(); pg != nil {
		pgErr = pg.DropEpoch(ctx, epochId)
		if pgErr != nil {
			logging.Warn("PostgreSQL payload prune failed", types.PayloadStorage, "epochId", epochId, "error", pgErr)
		}
	}

	fileErr := h.file.DropEpoch(ctx, epochId)
	if pgErr != nil {
		return pgErr
	}
	return fileErr
}

func (h *HybridStorage) Close() {
	if pg := h.currentPg(); pg != nil {
		pg.Close()
	}
}

var _ Storage = (*HybridStorage)(nil)
