package storage

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

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

// ManagedStorage wraps a Storage with periodic per-epoch pruning.
//
// Retention math mirrors payloadstorage.ManagedStorage: keep the highest N
// epochs, drop everything older. maxObservedEpoch comes from CreateSession
// calls (and, if provided, from EpochProvider). PruneInterval defaults to 30s
// to match the payload pruner cadence.
type ManagedStorage struct {
	inner         Storage
	retain        uint64
	pruneInterval time.Duration
	epochs        EpochProvider

	maxObservedEpoch atomic.Uint64

	mu         sync.Mutex
	prunedUpTo uint64 // exclusive: every epoch < prunedUpTo has been pruned

	lifecycleMu sync.Mutex
	started     bool
	stop        chan struct{}
	done        chan struct{}
}

// NewManagedStorage wraps inner with a pruner that retains the last `retain`
// epochs (current epoch counts as one of them, so retain=3 keeps current + 2
// previous). Call Start after migration/recovery to enable the background loop.
//
// epochs is optional. If non-nil, the pruner consults it on every tick so the
// retention horizon advances even on quiet hosts. Pass nil in tests where you
// want full control over retention from CreateSession alone.
func NewManagedStorage(inner Storage, retain uint64, pruneInterval time.Duration, epochs EpochProvider) *ManagedStorage {
	if retain == 0 {
		retain = 1
	}
	if pruneInterval <= 0 {
		pruneInterval = 30 * time.Second
	}
	m := &ManagedStorage{
		inner:         inner,
		retain:        retain,
		pruneInterval: pruneInterval,
		epochs:        epochs,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
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

func (m *ManagedStorage) loop() {
	defer close(m.done)
	t := time.NewTicker(m.pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.PruneOnce(context.Background())
		}
	}
}

// Start enables the background pruning loop. It is idempotent so callers can
// wire it after recovery without coordinating ownership.
func (m *ManagedStorage) Start() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.started {
		return
	}
	m.started = true
	go m.loop()
}

// PruneOnce runs a single retention pass. Exported so tests can drive the
// pruner deterministically without spinning a real ticker.
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

// Close stops the background pruner and closes the wrapped store.
func (m *ManagedStorage) Close() error {
	m.lifecycleMu.Lock()
	if !m.started {
		m.lifecycleMu.Unlock()
		return m.inner.Close()
	}
	close(m.stop)
	m.started = false
	done := m.done
	m.lifecycleMu.Unlock()
	<-done
	return m.inner.Close()
}

// --- Storage delegation ---

func (m *ManagedStorage) CreateSession(params CreateSessionParams) error {
	m.mu.Lock()
	if params.EpochID < m.prunedUpTo {
		m.mu.Unlock()
		return fmt.Errorf("%w: epoch %d below prune cursor %d", ErrEpochPruned, params.EpochID, m.prunedUpTo)
	}
	if err := m.inner.CreateSession(params); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
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

// PruneEpoch is exposed so callers can trigger an explicit drop. The managed
// background pass uses this method too.
func (m *ManagedStorage) PruneEpoch(epochID uint64) error {
	return m.inner.PruneEpoch(epochID)
}

var _ Storage = (*ManagedStorage)(nil)
