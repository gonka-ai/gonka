package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
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

	onPromoted []func()
	promoted   bool

	mu sync.RWMutex

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

// escrowIDLister is implemented by backends that can expose their in-memory
// escrow index for duplicate-backend diagnostics.
type escrowIDLister interface {
	EscrowIDs() []string
}

// PostgresPromotionWatcher lets callers react after a degraded router promotes
// Postgres in-process.
type PostgresPromotionWatcher interface {
	OnPostgresPromoted(func())
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
	}
}

func newDegradedSQLiteRouter(sqlite Storage, storeDir string, newSessionErr error) *HybridStorage {
	return &HybridStorage{
		sqlite:            sqlite,
		storeDir:          storeDir,
		degradedOwnerOnly: true,
		newSessionErr:     newSessionErr,
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
// backend knows it yet. If both backends claim it, the escrow is quarantined.
func (h *HybridStorage) backendFor(escrowID string) (Storage, error) {
	h.mu.RLock()
	sqlite := h.sqlite
	pg := h.pg
	degradedOwnerOnly := h.degradedOwnerOnly
	h.mu.RUnlock()

	if pg == nil {
		if degradedOwnerOnly {
			if owns(sqlite, escrowID) {
				return sqlite, nil
			}
			return nil, nil
		}
		return sqlite, nil
	}
	if sqlite == nil {
		return pg, nil
	}

	sqliteOwns := owns(sqlite, escrowID)
	pgOwns := owns(pg, escrowID)
	switch {
	case sqliteOwns && pgOwns:
		return nil, fmt.Errorf("%w: %s", ErrEscrowBackendConflict, escrowID)
	case sqliteOwns:
		return sqlite, nil
	case pgOwns:
		return pg, nil
	default:
		return nil, nil
	}
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
	b, err := h.backendFor(escrowID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	return b, nil
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
	b, err := h.backendFor(params.EscrowID)
	if err != nil {
		return err
	}
	if b == nil {
		if err := h.newSessionError(); err != nil {
			return fmt.Errorf("%w: escrow %s", err, params.EscrowID)
		}
		b = h.newSessionBackend()
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
			h.maybeClearPGBoundLocked("postgres_create_failed", "escrow_id", params.EscrowID, "create_error", err)
			return err
		}
		return nil
	}

	if err := b.CreateSession(params); err != nil {
		return err
	}
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

// maybeClearPGBoundLocked removes the write-ahead marker only when Postgres
// provably has no sessions. Caller must hold markerMu.
func (h *HybridStorage) maybeClearPGBoundLocked(reason string, attrs ...any) {
	pg := h.postgresBackend()
	if pg == nil || h.storeDir == "" || !h.pgBoundSet || pgHasSessions(pg) {
		return
	}
	args := []any{"dir", h.storeDir, "reason", reason}
	args = append(args, attrs...)

	lp, ok := pg.(livePresence)
	if !ok {
		slog.Warn("devshard storage: keeping .pg-bound; backend cannot live-check postgres emptiness", args...)
		return
	}
	has, err := lp.HasAnySessionsLive()
	if err != nil {
		args = append(args, "live_check_error", err)
		slog.Warn("devshard storage: keeping .pg-bound; live postgres emptiness check failed", args...)
		return
	}
	if has {
		return
	}
	if err := os.Remove(PGBoundPath(h.storeDir)); err != nil && !os.IsNotExist(err) {
		args = append(args, "cleanup_error", err)
		slog.Warn("devshard storage: failed to clear .pg-bound after postgres drained", args...)
		return
	}
	h.pgBoundSet = false
	slog.Info("devshard storage: cleared .pg-bound; postgres has no remaining sessions", args...)
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
		_ = pg.Close()
		return nil
	}
	h.pg = pg
	h.preferPG = true
	h.degradedOwnerOnly = false
	h.newSessionErr = nil
	h.promoted = true
	hooks := append([]func(){}, h.onPromoted...)
	h.mu.Unlock()
	slog.Info("devshard storage: postgres reconnected; leaving degraded sqlite-owned-only mode", "dir", h.storeDir)
	h.logConflictedEscrows("postgres promotion")
	for _, hook := range hooks {
		go hook()
	}
	return nil
}

// OnPostgresPromoted registers fn to run after a degraded router promotes
// Postgres. If promotion already happened, fn runs asynchronously immediately.
func (h *HybridStorage) OnPostgresPromoted(fn func()) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	if h.promoted {
		h.mu.Unlock()
		go fn()
		return
	}
	h.onPromoted = append(h.onPromoted, fn)
	h.mu.Unlock()
}

func (h *HybridStorage) maybeClearPGBound(reason string, attrs ...any) {
	h.markerMu.Lock()
	defer h.markerMu.Unlock()
	h.maybeClearPGBoundLocked(reason, attrs...)
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
	// At boot and promotion time the Postgres in-memory index was just rebuilt
	// from devshard_session_index, so it is authoritative for marker reconcile.
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

func (h *HybridStorage) logConflictedEscrows(phase string) {
	escrowIDs := h.conflictedEscrowIDs()
	if len(escrowIDs) == 0 {
		return
	}
	slog.Error(
		"devshard storage: escrow exists in both sqlite and postgres; quarantining conflicted escrows",
		"dir", h.storeDir,
		"phase", phase,
		"escrow_ids", escrowIDs,
		"remediation", "inspect both backends and remove the stale fork before continuing the escrow",
	)
}

func (h *HybridStorage) conflictedEscrowIDs() []string {
	h.mu.RLock()
	sqlite := h.sqlite
	pg := h.pg
	h.mu.RUnlock()
	return intersectEscrowIDs(sqlite, pg)
}

func intersectEscrowIDs(a, b Storage) []string {
	if a == nil || b == nil {
		return nil
	}
	aIDs, ok := escrowIDs(a)
	if !ok || len(aIDs) == 0 {
		return nil
	}
	bIDs, ok := escrowIDs(b)
	if !ok || len(bIDs) == 0 {
		return nil
	}
	bSet := make(map[string]struct{}, len(bIDs))
	for _, id := range bIDs {
		bSet[id] = struct{}{}
	}
	var conflicts []string
	for _, id := range aIDs {
		if _, ok := bSet[id]; ok {
			conflicts = append(conflicts, id)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}

func escrowIDs(b Storage) ([]string, bool) {
	l, ok := b.(escrowIDLister)
	if !ok {
		return nil, false
	}
	return l.EscrowIDs(), true
}

func (h *HybridStorage) MarkSettled(escrowID string) error {
	b, err := h.routed(escrowID)
	if err != nil {
		return err
	}
	return b.MarkSettled(escrowID)
}

// ListActiveSessions unions active sessions across both backends so recovery
// replays SQLite and Postgres escrows together. If both backends list the same
// escrow, keep one entry; follow-up reads will quarantine the conflict.
func (h *HybridStorage) ListActiveSessions() ([]ActiveSession, error) {
	var out []ActiveSession
	seen := make(map[string]struct{})
	for _, b := range h.backends() {
		sessions, err := b.ListActiveSessions()
		if err != nil {
			return nil, err
		}
		for _, sess := range sessions {
			if _, ok := seen[sess.EscrowID]; ok {
				continue
			}
			seen[sess.EscrowID] = struct{}{}
			out = append(out, sess)
		}
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

// PruneEpoch drops the epoch partition in every backend.
func (h *HybridStorage) PruneEpoch(epochID uint64) error {
	for _, b := range h.backends() {
		if err := b.PruneEpoch(epochID); err != nil {
			return err
		}
	}
	h.maybeClearPGBound("prune_epoch", "epoch_id", epochID)
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
	h.maybeClearPGBound("range_prune", "cutoff", cutoff)
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
