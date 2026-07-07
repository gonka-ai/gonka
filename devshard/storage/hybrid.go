package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"devshard/types"
)

// HybridStorage routes every escrow to exactly one backend and can serve two
// backends at once. New escrows are created in Postgres when it is configured
// (PGHOST set), otherwise in SQLite. Existing escrows are always served by
// whichever backend physically holds them, so a store can drain legacy SQLite
// sessions while creating new Postgres sessions without a process restart.
//
// Ownership is derived from each backend's own persistent escrow index (SQLite
// _meta.db, Postgres devshard_session_index) rather than a separate route table
// that could be lost on reboot. Because CreateSession picks exactly one backend
// and never falls back, a given escrow only ever lives in one backend, so
// append logs cannot fork across backends.
type HybridStorage struct {
	sqlite   Storage
	pg       Storage
	preferPG bool
	storeDir string // enables .pg-bound maintenance; empty disables it

	// degradedOwnerOnly means only already-owned escrows may use the single
	// SQLite backend. New/unknown escrows fail with newSessionErr instead of
	// falling back to SQLite while Postgres is unavailable or unconfigured.
	degradedOwnerOnly bool
	newSessionErr     error

	reconnectStop chan struct{}
	reconnectDone chan struct{}

	mu    sync.RWMutex
	owner map[string]Storage

	// markerMu serializes .pg-bound maintenance with the Postgres session-count
	// changes that drive it, so a prune-driven clear cannot interleave with a
	// PG CreateSession and leave a live PG session unmarked.
	markerMu   sync.Mutex
	pgBoundSet bool // guarded by markerMu: whether .pg-bound is present on disk
}

// escrowOwner is implemented by backends that can answer whether they hold an
// escrow in their in-memory routing index.
type escrowOwner interface {
	HasEscrow(escrowID string) bool
}

// sessionPresence is implemented by backends that can report whether they still
// hold any session. Used to decide when .pg-bound can be cleared.
type sessionPresence interface {
	HasAnySessions() bool
}

// livePresence is implemented by backends that can prove emptiness against the
// database itself rather than the in-memory index. Required before clearing
// .pg-bound after a failed create: a timed-out insert may have committed
// server-side without the in-memory index ever learning about it.
type livePresence interface {
	HasAnySessionsLive() (bool, error)
}

// NewHybridStorage wraps a single backend. Every call is forwarded to it. Used
// when only one backend is available (SQLite-only or Postgres-only).
func NewHybridStorage(backend Storage) *HybridStorage {
	return &HybridStorage{sqlite: backend, owner: make(map[string]Storage)}
}

// newHybridRouter wires the per-session router. Either backend may be nil, but
// at least one must be non-nil. preferPG selects the backend for brand-new
// escrows when both backends are present. storeDir enables .pg-bound marker
// maintenance for the Postgres backend.
func newHybridRouter(sqlite, pg Storage, preferPG bool, storeDir string) *HybridStorage {
	return &HybridStorage{
		sqlite:   sqlite,
		pg:       pg,
		preferPG: preferPG,
		storeDir: storeDir,
		owner:    make(map[string]Storage),
	}
}

func newDegradedSQLiteRouter(sqlite Storage, storeDir string, newSessionErr error) *HybridStorage {
	return &HybridStorage{
		sqlite:            sqlite,
		storeDir:          storeDir,
		degradedOwnerOnly: true,
		newSessionErr:     newSessionErr,
		owner:             make(map[string]Storage),
	}
}

func (h *HybridStorage) backends() []Storage {
	h.mu.RLock()
	sqlite := h.sqlite
	pg := h.pg
	h.mu.RUnlock()

	bs := make([]Storage, 0, 2)
	if sqlite != nil {
		bs = append(bs, sqlite)
	}
	if pg != nil {
		bs = append(bs, pg)
	}
	return bs
}

// backendFor returns the backend that owns escrowID, or nil when neither
// backend knows it yet. When only one backend is configured it is returned
// without probing.
func (h *HybridStorage) backendFor(escrowID string) Storage {
	h.mu.RLock()
	sqlite := h.sqlite
	pg := h.pg
	degradedOwnerOnly := h.degradedOwnerOnly
	b := h.owner[escrowID]
	h.mu.RUnlock()

	if pg == nil {
		if degradedOwnerOnly {
			if owns(sqlite, escrowID) {
				return sqlite
			}
			return nil
		}
		return sqlite
	}
	if sqlite == nil {
		return pg
	}

	if b != nil {
		return b
	}

	if owns(sqlite, escrowID) {
		b = sqlite
	} else if owns(pg, escrowID) {
		b = pg
	}
	if b != nil {
		h.mu.Lock()
		h.owner[escrowID] = b
		h.mu.Unlock()
	}
	return b
}

// resolveOwner returns the backend that physically holds escrowID, or nil when
// neither backend knows it. SQLite is checked first because its lookup is fully
// in-memory.
func (h *HybridStorage) resolveOwner(escrowID string) Storage {
	h.mu.RLock()
	sqlite := h.sqlite
	pg := h.pg
	h.mu.RUnlock()
	if owns(sqlite, escrowID) {
		return sqlite
	}
	if owns(pg, escrowID) {
		return pg
	}
	return nil
}

func (h *HybridStorage) postgresBackend() Storage {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pg
}

func (h *HybridStorage) newSessionError() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.newSessionErr
}

func owns(b Storage, escrowID string) bool {
	if b == nil {
		return false
	}
	o, ok := b.(escrowOwner)
	if !ok {
		return false
	}
	return o.HasEscrow(escrowID)
}

// routed returns the owning backend for an existing escrow, or ErrSessionNotFound.
func (h *HybridStorage) routed(escrowID string) (Storage, error) {
	b := h.backendFor(escrowID)
	if b == nil {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	return b, nil
}

func (h *HybridStorage) rememberOwner(escrowID string, b Storage) {
	h.mu.Lock()
	h.owner[escrowID] = b
	h.mu.Unlock()
}

func (h *HybridStorage) clearOwnerCache() {
	h.mu.Lock()
	h.owner = make(map[string]Storage)
	h.mu.Unlock()
}

// newSessionBackend picks the backend for a brand-new escrow: Postgres when it
// is configured (preferPG), otherwise SQLite. Falls back to whichever backend
// is present when only one is configured.
func (h *HybridStorage) newSessionBackend() Storage {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.preferPG && h.pg != nil {
		return h.pg
	}
	if h.sqlite != nil {
		return h.sqlite
	}
	return h.pg
}

func (h *HybridStorage) CreateSession(params CreateSessionParams) error {
	b := h.backendFor(params.EscrowID)
	if b == nil {
		if err := h.newSessionError(); err != nil {
			return fmt.Errorf("%w: escrow %s", err, params.EscrowID)
		}
		b = h.newSessionBackend()
	}
	if b == nil {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, params.EscrowID)
	}

	pg := h.postgresBackend()
	if pg != nil && b == pg && h.storeDir != "" {
		// Postgres-bound session: keep .pg-bound present for as long as PG holds
		// any session. Write the marker ahead of the insert and hold markerMu
		// across the insert so a concurrent prune-driven clear cannot observe an
		// empty index between the write-ahead and the insert landing.
		h.markerMu.Lock()
		defer h.markerMu.Unlock()
		if err := h.ensurePGBoundLocked(); err != nil {
			return err
		}
		if err := b.CreateSession(params); err != nil {
			h.clearPGBoundAfterFailedCreateLocked(params.EscrowID, err)
			return err
		}
		h.rememberOwner(params.EscrowID, b)
		return nil
	}

	if err := b.CreateSession(params); err != nil {
		return err
	}
	h.rememberOwner(params.EscrowID, b)
	return nil
}

// ensurePGBoundLocked writes the .pg-bound marker if it is not already present.
// Caller must hold markerMu.
func (h *HybridStorage) ensurePGBoundLocked() error {
	if h.pgBoundSet {
		return nil
	}
	if err := WritePGBound(h.storeDir); err != nil {
		return fmt.Errorf("write pg-bound: %w", err)
	}
	h.pgBoundSet = true
	return nil
}

// clearPGBoundAfterFailedCreateLocked removes the write-ahead marker when the
// PG session insert failed and Postgres provably has no sessions. It preserves
// the original CreateSession error; cleanup failures are logged only.
//
// The in-memory index saying "empty" is not enough here: a create that failed
// with a timeout may have committed server-side (ack lost) without updating
// the index. The marker is therefore cleared only when a live DB query proves
// emptiness. If the live check fails (e.g. PG outage) or the backend cannot
// perform one, the marker is kept; boot-time reconciliation or a prune-driven
// clear removes a genuinely stale marker later.
func (h *HybridStorage) clearPGBoundAfterFailedCreateLocked(escrowID string, createErr error) {
	pg := h.postgresBackend()
	if !h.pgBoundSet || pgHasSessions(pg) {
		return
	}
	lp, ok := pg.(livePresence)
	if !ok {
		return
	}
	has, err := lp.HasAnySessionsLive()
	if err != nil {
		slog.Warn("devshard storage: keeping .pg-bound after failed postgres create; live emptiness check failed",
			"dir", h.storeDir, "escrow_id", escrowID, "create_error", createErr, "live_check_error", err)
		return
	}
	if has {
		// The failed create (or a concurrent writer) actually landed rows.
		return
	}
	if err := os.Remove(PGBoundPath(h.storeDir)); err != nil && !os.IsNotExist(err) {
		slog.Warn("devshard storage: failed to clear .pg-bound after postgres create failed",
			"dir", h.storeDir, "escrow_id", escrowID, "create_error", createErr, "cleanup_error", err)
		return
	}
	h.pgBoundSet = false
	slog.Warn("devshard storage: cleared .pg-bound after postgres create failed with no postgres sessions",
		"dir", h.storeDir, "escrow_id", escrowID, "create_error", createErr)
}

type storageOpener func(context.Context) (Storage, error)

func (h *HybridStorage) startPostgresReconnect(ctx context.Context, opener storageOpener, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	h.mu.Lock()
	if h.reconnectStop != nil || h.pg != nil || !h.degradedOwnerOnly {
		h.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	h.reconnectStop = stop
	h.reconnectDone = done
	h.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
			}

			pg, err := opener(ctx)
			if err != nil {
				slog.Warn("devshard storage: postgres reconnect failed; staying in degraded sqlite-owned-only mode",
					"dir", h.storeDir, "error", err)
				continue
			}
			if err := h.promotePostgres(pg); err != nil {
				_ = pg.Close()
				slog.Warn("devshard storage: postgres reconnect succeeded but promotion failed; staying degraded",
					"dir", h.storeDir, "error", err)
				continue
			}
			return
		}
	}()
}

func (h *HybridStorage) promotePostgres(pg Storage) error {
	if pg == nil {
		return fmt.Errorf("postgres backend is nil")
	}
	if err := h.reconcilePGBoundFor(pg); err != nil {
		return err
	}
	h.mu.Lock()
	if h.pg != nil {
		h.mu.Unlock()
		return nil
	}
	h.pg = pg
	h.preferPG = true
	h.degradedOwnerOnly = false
	h.newSessionErr = nil
	h.owner = make(map[string]Storage)
	h.mu.Unlock()
	slog.Info("devshard storage: postgres reconnected; leaving degraded sqlite-owned-only mode", "dir", h.storeDir)
	return nil
}

// clearPGBoundIfDrained removes .pg-bound once Postgres holds no sessions, so a
// later SQLite-only boot is allowed without manual cleanup. It is a no-op until
// PG is genuinely empty and while the marker is already absent.
func (h *HybridStorage) clearPGBoundIfDrained() {
	pg := h.postgresBackend()
	if pg == nil || h.storeDir == "" {
		return
	}
	h.markerMu.Lock()
	defer h.markerMu.Unlock()
	if !h.pgBoundSet || pgHasSessions(pg) {
		return
	}
	if err := os.Remove(PGBoundPath(h.storeDir)); err != nil && !os.IsNotExist(err) {
		slog.Warn("devshard storage: failed to clear .pg-bound after postgres drained", "dir", h.storeDir, "error", err)
		return
	}
	h.pgBoundSet = false
	slog.Info("devshard storage: cleared .pg-bound; postgres has no remaining sessions", "dir", h.storeDir)
}

// reconcilePGBoundAtBoot aligns the .pg-bound marker with Postgres reality at
// startup: present when PG holds sessions, absent when it does not. This clears
// a stale marker left behind after a previous run's escrows fully drained.
func (h *HybridStorage) reconcilePGBoundAtBoot() error {
	h.mu.RLock()
	pg := h.pg
	h.mu.RUnlock()
	return h.reconcilePGBoundFor(pg)
}

func (h *HybridStorage) reconcilePGBoundFor(pg Storage) error {
	if pg == nil || h.storeDir == "" {
		return nil
	}
	h.markerMu.Lock()
	defer h.markerMu.Unlock()
	present, err := ReadPGBound(h.storeDir)
	if err != nil {
		return err
	}
	h.pgBoundSet = present
	if pgHasSessions(pg) {
		return h.ensurePGBoundLocked()
	}
	if present {
		if err := os.Remove(PGBoundPath(h.storeDir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale pg-bound: %w", err)
		}
		h.pgBoundSet = false
	}
	return nil
}

// pgHasSessions reports whether the Postgres backend still holds any session.
// When the backend cannot report presence it is treated as non-empty so the
// marker is retained conservatively.
func pgHasSessions(b Storage) bool {
	c, ok := b.(sessionPresence)
	if !ok {
		return true
	}
	return c.HasAnySessions()
}

func (h *HybridStorage) MarkSettled(escrowID string) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.MarkSettled(escrowID)
}

// ListActiveSessions unions active sessions across both backends so recovery
// replays SQLite and Postgres escrows together.
func (h *HybridStorage) ListActiveSessions() ([]ActiveSession, error) {
	var out []ActiveSession
	for _, b := range h.backends() {
		sessions, err := b.ListActiveSessions()
		if err != nil {
			return nil, err
		}
		out = append(out, sessions...)
	}
	return out, nil
}

func (h *HybridStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.AppendDiff(escrowID, rec)
}

func (h *HybridStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return nil, err
	}
	return b.GetDiffs(escrowID, fromNonce, toNonce)
}

func (h *HybridStorage) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.AddSignature(escrowID, nonce, slotID, sig)
}

func (h *HybridStorage) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return nil, err
	}
	return b.GetSignatures(escrowID, nonce)
}

func (h *HybridStorage) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return nil, err
	}
	return b.GetSessionMeta(escrowID)
}

func (h *HybridStorage) MarkFinalized(escrowID string, nonce uint64) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.MarkFinalized(escrowID, nonce)
}

func (h *HybridStorage) LastFinalized(escrowID string) (uint64, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return 0, err
	}
	return b.LastFinalized(escrowID)
}

func (h *HybridStorage) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.SaveSnapshot(escrowID, nonce, data)
}

func (h *HybridStorage) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return 0, nil, err
	}
	return b.LoadSnapshot(escrowID)
}

func (h *HybridStorage) InsertSealedInference(escrowID string, row InferenceRow) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.InsertSealedInference(escrowID, row)
}

func (h *HybridStorage) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return InferenceRow{}, false, err
	}
	return b.GetSealedInference(escrowID, inferenceID)
}

func (h *HybridStorage) DeleteSealedInferences(escrowID string) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.DeleteSealedInferences(escrowID)
}

func (h *HybridStorage) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.RecordValidationsAppliedOnce(escrowID, entries)
}

func (h *HybridStorage) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.DrainInferenceValidationObs(escrowID, inferenceID)
}

func (h *HybridStorage) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	b, err := h.routed(escrowID)
	if err != nil {
		return nil, err
	}
	return b.GetValidationObservability(escrowID)
}

// PruneEpoch drops the epoch partition in every backend. Ownership cache is
// cleared afterwards because pruned escrows no longer belong to any backend.
func (h *HybridStorage) PruneEpoch(epochID uint64) error {
	for _, b := range h.backends() {
		if err := b.PruneEpoch(epochID); err != nil {
			return err
		}
	}
	h.clearOwnerCache()
	h.clearPGBoundIfDrained()
	return nil
}

func (h *HybridStorage) pruneBefore(cutoff uint64) error {
	for _, b := range h.backends() {
		rp, ok := b.(rangePruner)
		if !ok {
			return fmt.Errorf("storage backend does not support range prune")
		}
		if err := rp.pruneBefore(cutoff); err != nil {
			return err
		}
	}
	h.clearOwnerCache()
	h.clearPGBoundIfDrained()
	return nil
}

func (h *HybridStorage) Close() error {
	h.mu.Lock()
	stop := h.reconnectStop
	done := h.reconnectDone
	if stop != nil {
		select {
		case <-stop:
		default:
			close(stop)
		}
		h.reconnectStop = nil
	}
	h.mu.Unlock()
	if done != nil {
		<-done
	}

	var firstErr error
	for _, b := range h.backends() {
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var _ Storage = (*HybridStorage)(nil)
