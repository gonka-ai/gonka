package storage

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"devshard/types"
)

// EpochProvider lets ManagedStorage learn the chain's current epoch even
// when the host is quiet (no CreateSession activity). Optional: if nil,
// retention is driven entirely by the highest epoch we have ever stored to.
type EpochProvider interface {
	CurrentEpochID() uint64
}

type rangePruner interface {
	pruneBefore(cutoff uint64) error
}

// ManagedStorage wraps a Storage with per-epoch retention pruning.
//
// Retention math mirrors payloadstorage.ManagedStorage: keep the highest N
// epochs, drop everything older. maxObservedEpoch comes from CreateSession
// calls (and, if provided, from EpochProvider).
//
// Pruning runs only when callers invoke PruneOnce — typically from an
// epoch-change hook (dapi runtime-config publish or devshardd long-poll).
// Start runs one catch-up PruneOnce after recovery; it does not start a timer.
type ManagedStorage struct {
	inner  Storage
	retain uint64
	epochs EpochProvider

	maxObservedEpoch atomic.Uint64

	mu         sync.RWMutex
	prunedUpTo uint64 // exclusive: every epoch < prunedUpTo has been pruned
}

// NewManagedStorage wraps inner with a pruner that retains the last `retain`
// epochs (current epoch counts as one of them, so retain=3 keeps current + 2
// previous). Call Start after migration/recovery for a one-shot catch-up prune,
// and register epoch-change listeners that call PruneOnce.
//
// epochs is optional. If non-nil, PruneOnce consults it so the retention horizon
// advances even on quiet hosts. Pass nil in tests where you drive pruning only
// via explicit PruneOnce calls.
func NewManagedStorage(inner Storage, retain uint64, epochs EpochProvider) *ManagedStorage {
	if retain == 0 {
		retain = 1
	}
	m := &ManagedStorage{
		inner:  inner,
		retain: retain,
		epochs: epochs,
	}
	_, hasRangePrune := inner.(rangePruner)
	slog.Info("devshard managed storage initialized", "range_prune", hasRangePrune, "retain", retain)
	return m
}

func (m *ManagedStorage) observe(epochID uint64) {
	for {
		cur := m.maxObservedEpoch.Load()
		if epochID <= cur {
			return
		}
		if m.maxObservedEpoch.CompareAndSwap(cur, epochID) {
			return
		}
	}
}

// CurrentEpochID returns the epoch observed by the managed pruner. It is used
// only for temporary payload fallback during epoch-0 migration.
func (m *ManagedStorage) CurrentEpochID() uint64 {
	if m.epochs != nil {
		m.observe(m.epochs.CurrentEpochID())
	}
	return m.maxObservedEpoch.Load()
}

// Start runs a single catch-up prune after recovery. Epoch transitions must
// trigger additional PruneOnce calls via the host's epoch-change hook.
func (m *ManagedStorage) Start() {
	m.PruneOnce(context.Background())
}

// PruneOnceAsync runs PruneOnce in a background goroutine. Panics are recovered
// and logged so a storage-driver fault cannot permanently stop epoch pruning.
func (m *ManagedStorage) PruneOnceAsync(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("devshard epoch prune panicked", "panic", r)
			}
		}()
		m.PruneOnce(ctx)
	}()
}

// PruneOnce runs a single retention pass. Exported so tests and epoch hooks can
// drive pruning without a background loop.
func (m *ManagedStorage) PruneOnce(_ context.Context) {
	if m.epochs != nil {
		m.observe(m.epochs.CurrentEpochID())
	}
	maxE := m.maxObservedEpoch.Load()
	if maxE+1 <= m.retain {
		return // not enough epochs yet
	}
	cutoff := maxE + 1 - m.retain // every epoch < cutoff is pruneable

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prunedUpTo >= cutoff {
		return
	}

	if rp, ok := m.inner.(rangePruner); ok {
		if err := rp.pruneBefore(cutoff); err != nil {
			slog.Warn("devshard range prune failed", "cutoff", cutoff, "error", err)
			return
		}
		m.prunedUpTo = cutoff
		slog.Info("devshard pruned epochs", "before", cutoff, "max_observed", maxE, "retain", m.retain)
		return
	}

	for e := m.prunedUpTo; e < cutoff; e++ {
		if err := m.inner.PruneEpoch(e); err != nil {
			slog.Warn("devshard prune failed", "epoch", e, "error", err)
			return
		}
		m.prunedUpTo = e + 1
		slog.Info("devshard pruned epoch", "epoch", e, "max_observed", maxE, "retain", m.retain)
	}
}

// Close closes the wrapped store.
func (m *ManagedStorage) Close() error {
	return m.inner.Close()
}

// --- Storage delegation ---

func (m *ManagedStorage) CreateSession(params CreateSessionParams) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if params.EpochID < m.prunedUpTo {
		return fmt.Errorf("%w: epoch %d below prune cursor %d", ErrEpochPruned, params.EpochID, m.prunedUpTo)
	}
	if err := m.inner.CreateSession(params); err != nil {
		return err
	}
	m.observe(params.EpochID)
	return nil
}

func (m *ManagedStorage) MarkSettled(escrowID string) error {
	return m.inner.MarkSettled(escrowID)
}

func (m *ManagedStorage) ListActiveSessions() ([]ActiveSession, error) {
	return m.inner.ListActiveSessions()
}

func (m *ManagedStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	return m.inner.AppendDiff(escrowID, rec)
}

func (m *ManagedStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	return m.inner.GetDiffs(escrowID, fromNonce, toNonce)
}

func (m *ManagedStorage) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	return m.inner.AddSignature(escrowID, nonce, slotID, sig)
}

func (m *ManagedStorage) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	return m.inner.GetSignatures(escrowID, nonce)
}

func (m *ManagedStorage) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	return m.inner.GetSessionMeta(escrowID)
}

func (m *ManagedStorage) MarkFinalized(escrowID string, nonce uint64) error {
	return m.inner.MarkFinalized(escrowID, nonce)
}

func (m *ManagedStorage) LastFinalized(escrowID string) (uint64, error) {
	return m.inner.LastFinalized(escrowID)
}

func (m *ManagedStorage) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	return m.inner.SaveSnapshot(escrowID, nonce, data)
}

func (m *ManagedStorage) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	return m.inner.LoadSnapshot(escrowID)
}

func (m *ManagedStorage) InsertSealedInference(escrowID string, row InferenceRow) error {
	return m.inner.InsertSealedInference(escrowID, row)
}

func (m *ManagedStorage) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	return m.inner.GetSealedInference(escrowID, inferenceID)
}

func (m *ManagedStorage) DeleteSealedInferences(escrowID string) error {
	return m.inner.DeleteSealedInferences(escrowID)
}

func (m *ManagedStorage) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	return m.inner.RecordValidationsAppliedOnce(escrowID, entries)
}

func (m *ManagedStorage) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	return m.inner.DrainInferenceValidationObs(escrowID, inferenceID)
}

func (m *ManagedStorage) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	return m.inner.GetValidationObservability(escrowID)
}

// PruneEpoch is exposed so callers can trigger an explicit drop. PruneOnce uses
// this path when the inner store does not implement rangePruner.
func (m *ManagedStorage) PruneEpoch(epochID uint64) error {
	return m.inner.PruneEpoch(epochID)
}

var _ Storage = (*ManagedStorage)(nil)
