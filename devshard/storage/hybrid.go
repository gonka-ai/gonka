package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"devshard/types"
)

const defaultPGConnectTimeout = 2 * time.Second

type hybridBackend uint8

const (
	hybridSQLite hybridBackend = iota + 1
	hybridPostgres
)

type hybridRoute struct {
	backend hybridBackend
	epochID uint64
}

// HybridStorage uses Postgres for new sessions when it is available and keeps
// SQLite as a local-only fallback while Postgres is down. Once an escrow is
// found in a backend, all future session-keyed calls for that escrow are routed
// there; devshard state is never merged between backends.
type HybridStorage struct {
	sqlite *SQLite

	mu             sync.Mutex
	pg             *Postgres
	lastRetry      time.Time
	retryInterval  time.Duration
	connectTimeout time.Duration
	routes         map[string]hybridRoute
}

func NewHybridStorage(ctx context.Context, sqlite *SQLite, retryInterval, connectTimeout time.Duration) *HybridStorage {
	if retryInterval <= 0 {
		retryInterval = 240 * time.Second
	}
	if connectTimeout <= 0 {
		connectTimeout = defaultPGConnectTimeout
	}
	h := &HybridStorage{
		sqlite:         sqlite,
		retryInterval:  retryInterval,
		connectTimeout: connectTimeout,
		routes:         make(map[string]hybridRoute),
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if pg, err := NewPostgres(connectCtx); err == nil {
		h.pg = pg
		slog.Info("devshard storage: using postgres with sqlite fallback")
	} else {
		slog.Warn("devshard storage: postgres unavailable, using sqlite fallback until reconnect", "error", err)
	}
	return h
}

func (h *HybridStorage) shouldAttemptConnect() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pg != nil {
		return false
	}
	if time.Since(h.lastRetry) < h.retryInterval {
		return false
	}
	h.lastRetry = time.Now()
	return true
}

func (h *HybridStorage) savePostgres(pg *Postgres) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pg = pg
}

func (h *HybridStorage) currentPostgres() *Postgres {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pg
}

func (h *HybridStorage) getOrConnectPostgres() *Postgres {
	if pg := h.currentPostgres(); pg != nil {
		return pg
	}
	if !h.shouldAttemptConnect() {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.connectTimeout)
	defer cancel()

	pg, err := NewPostgres(ctx)
	if err != nil {
		slog.Debug("devshard storage: postgres reconnect failed", "error", err)
		return nil
	}
	h.savePostgres(pg)
	slog.Info("devshard storage: postgres reconnected")
	return pg
}

func (h *HybridStorage) remember(escrowID string, backend hybridBackend, epochID uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[escrowID] = hybridRoute{backend: backend, epochID: epochID}
}

func (h *HybridStorage) remembered(escrowID string) (hybridRoute, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	route, ok := h.routes[escrowID]
	return route, ok
}

func (h *HybridStorage) storeForBackend(backend hybridBackend) (Storage, error) {
	switch backend {
	case hybridSQLite:
		return h.sqlite, nil
	case hybridPostgres:
		if pg := h.currentPostgres(); pg != nil {
			return pg, nil
		}
		return nil, fmt.Errorf("postgres backend unavailable")
	default:
		return nil, fmt.Errorf("unknown hybrid backend %d", backend)
	}
}

func (h *HybridStorage) backendForSession(escrowID string) (Storage, hybridBackend, error) {
	if route, ok := h.remembered(escrowID); ok {
		store, err := h.storeForBackend(route.backend)
		return store, route.backend, err
	}

	if pg := h.getOrConnectPostgres(); pg != nil {
		if meta, err := pg.GetSessionMeta(escrowID); err == nil {
			h.remember(escrowID, hybridPostgres, meta.EpochID)
			return pg, hybridPostgres, nil
		} else if !errors.Is(err, ErrSessionNotFound) {
			return nil, 0, err
		}
	}

	if meta, err := h.sqlite.GetSessionMeta(escrowID); err == nil {
		h.remember(escrowID, hybridSQLite, meta.EpochID)
		return h.sqlite, hybridSQLite, nil
	} else if !errors.Is(err, ErrSessionNotFound) {
		return nil, 0, err
	}

	return nil, 0, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
}

func (h *HybridStorage) backendForCreate(params CreateSessionParams) (Storage, hybridBackend, error) {
	if store, backend, err := h.backendForSession(params.EscrowID); err == nil {
		return store, backend, nil
	} else if !errors.Is(err, ErrSessionNotFound) {
		return nil, 0, err
	}

	if pg := h.getOrConnectPostgres(); pg != nil {
		return pg, hybridPostgres, nil
	}
	return h.sqlite, hybridSQLite, nil
}

func (h *HybridStorage) CreateSession(params CreateSessionParams) error {
	store, backend, err := h.backendForCreate(params)
	if err != nil {
		return err
	}
	if err := store.CreateSession(params); err != nil {
		return err
	}
	h.remember(params.EscrowID, backend, params.EpochID)
	return nil
}

func (h *HybridStorage) MarkSettled(escrowID string) error {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return err
	}
	return store.MarkSettled(escrowID)
}

func (h *HybridStorage) ListActiveSessions() ([]ActiveSession, error) {
	sqliteActive, err := h.sqlite.ListActiveSessions()
	if err != nil {
		return nil, err
	}
	result := make([]ActiveSession, 0, len(sqliteActive))
	seen := make(map[string]ActiveSession, len(sqliteActive))
	for _, sess := range sqliteActive {
		result = append(result, sess)
		seen[sess.EscrowID] = sess
		h.remember(sess.EscrowID, hybridSQLite, sess.EpochID)
	}

	pg := h.getOrConnectPostgres()
	if pg == nil {
		return result, nil
	}
	pgActive, err := pg.ListActiveSessions()
	if err != nil {
		slog.Warn("devshard storage: postgres active-session list failed", "error", err)
		return result, nil
	}
	for _, sess := range pgActive {
		if existing, ok := seen[sess.EscrowID]; ok {
			sqliteVersion, sqliteVersionErr := sessionVersionForLog(h.sqlite, sess.EscrowID)
			postgresVersion, postgresVersionErr := sessionVersionForLog(pg, sess.EscrowID)
			keyvals := []any{
				"escrow_id", sess.EscrowID,
				"sqlite_epoch_id", existing.EpochID,
				"sqlite_version", sqliteVersion,
				"postgres_epoch_id", sess.EpochID,
				"postgres_version", postgresVersion,
			}
			if sqliteVersionErr != nil {
				keyvals = append(keyvals, "sqlite_version_error", sqliteVersionErr)
			}
			if postgresVersionErr != nil {
				keyvals = append(keyvals, "postgres_version_error", postgresVersionErr)
			}
			slog.Warn("devshard storage: duplicate active session in sqlite and postgres, using sqlite copy", keyvals...)
			continue
		}
		result = append(result, sess)
		h.remember(sess.EscrowID, hybridPostgres, sess.EpochID)
	}
	return result, nil
}

func sessionVersionForLog(store Storage, escrowID string) (string, error) {
	meta, err := store.GetSessionMeta(escrowID)
	if err != nil {
		return "", err
	}
	return types.NormalizeSessionVersion(meta.Version), nil
}

func (h *HybridStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return err
	}
	return store.AppendDiff(escrowID, rec)
}

func (h *HybridStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return nil, err
	}
	return store.GetDiffs(escrowID, fromNonce, toNonce)
}

func (h *HybridStorage) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return err
	}
	return store.AddSignature(escrowID, nonce, slotID, sig)
}

func (h *HybridStorage) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return nil, err
	}
	return store.GetSignatures(escrowID, nonce)
}

func (h *HybridStorage) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return nil, err
	}
	return store.GetSessionMeta(escrowID)
}

func (h *HybridStorage) MarkFinalized(escrowID string, nonce uint64) error {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return err
	}
	return store.MarkFinalized(escrowID, nonce)
}

func (h *HybridStorage) LastFinalized(escrowID string) (uint64, error) {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return 0, err
	}
	return store.LastFinalized(escrowID)
}

func (h *HybridStorage) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return err
	}
	return store.SaveSnapshot(escrowID, nonce, data)
}

func (h *HybridStorage) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	store, _, err := h.backendForSession(escrowID)
	if err != nil {
		return 0, nil, err
	}
	return store.LoadSnapshot(escrowID)
}

func (h *HybridStorage) PruneEpoch(epochID uint64) error {
	sqliteErr := h.sqlite.PruneEpoch(epochID)
	var pgErr error
	if pg := h.currentPostgres(); pg != nil {
		pgErr = pg.PruneEpoch(epochID)
	} else {
		pgErr = fmt.Errorf("postgres backend unavailable for prune")
	}
	h.forgetPruned(func(ep uint64) bool { return ep == epochID })
	if pgErr != nil {
		return pgErr
	}
	return sqliteErr
}

func (h *HybridStorage) pruneBefore(cutoff uint64) error {
	sqliteErr := h.sqlite.pruneBefore(cutoff)
	var pgErr error
	if pg := h.currentPostgres(); pg != nil {
		pgErr = pg.pruneBefore(cutoff)
	} else {
		pgErr = fmt.Errorf("postgres backend unavailable for prune")
	}
	h.forgetPruned(func(ep uint64) bool { return ep < cutoff })
	if pgErr != nil {
		return pgErr
	}
	return sqliteErr
}

func (h *HybridStorage) forgetPruned(match func(uint64) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for escrowID, route := range h.routes {
		if match(route.epochID) {
			delete(h.routes, escrowID)
		}
	}
}

func (h *HybridStorage) Close() error {
	sqliteErr := h.sqlite.Close()
	var pgErr error
	if pg := h.currentPostgres(); pg != nil {
		pgErr = pg.Close()
	}
	if pgErr != nil {
		return pgErr
	}
	return sqliteErr
}

var _ Storage = (*HybridStorage)(nil)
