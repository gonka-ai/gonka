package payloadstorage

import (
	"context"
	"errors"
	"sync"
	"time"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	defaultManagedCacheSize = 1000
	maxPruneLookback        = 10
	// cleanupInterval is the cleanupLoop tick period. defaultPruneTimeout is
	// deliberately the same value so a stalled epoch costs at most about one tick
	// before cleanupLoop is free to run again; sharing the constant makes that
	// coupling real rather than two literals that could silently drift apart.
	cleanupInterval = 30 * time.Second
	// defaultPruneTimeout bounds a single PruneEpoch so a stuck backend cannot
	// freeze cleanupLoop.
	defaultPruneTimeout = cleanupInterval
)

// errPruneInFlight signals that a prune goroutine for this epoch spawned by an
// earlier tick is still running (it timed out but the ctx-ignoring backend has
// not returned yet). It is treated exactly like a timeout failure so pruneEpochs
// stops advancing minPruned and retries next tick, but it deliberately does NOT
// spawn another goroutine -- that is what caps stuck background prunes at one per
// epoch instead of one per tick.
var errPruneInFlight = errors.New("prune already in flight for epoch")

type cachedEntry struct {
	promptPayload   []byte
	responsePayload []byte
	expiresAt       time.Time
}

// ManagedStorage wraps PayloadStorage with read caching and automatic epoch pruning.
// - Caches Retrieve results to reduce disk I/O during validation bursts
// - Automatically prunes old epochs in background (only last 10 epochs, older data requires manual prune)
type ManagedStorage struct {
	storage      PayloadStorage
	retainCount  uint64
	cacheTTL     time.Duration
	maxCacheSize int
	pruneTimeout time.Duration

	mu        sync.RWMutex
	cache     map[string]*cachedEntry
	maxEpoch  uint64
	minPruned uint64
	// pruning tracks epochs whose background PruneEpoch goroutine is still
	// outstanding. A ctx-ignoring backend (FileStorage's os.RemoveAll) keeps
	// running after pruneEpochWithTimeout returns on its deadline; without this
	// set every tick would spawn a fresh goroutine for the same stuck epoch, so
	// stuck tasks would accumulate without bound. Guarded by mu.
	//
	// A permanently-wedged epoch's marker can outlive its relevance: once the
	// maxPruneLookback cap laps minPruned past that epoch it is never revisited,
	// yet the leaked goroutine never returns to clear the entry. That leaves at
	// most one stale marker per wedged epoch (bounded, negligible), and it is
	// harmless because nothing ever keys on that epoch again.
	pruning map[uint64]struct{}
}

func NewManagedStorage(storage PayloadStorage, retainCount uint64, cacheTTL time.Duration) *ManagedStorage {
	return NewManagedStorageWithSize(storage, retainCount, cacheTTL, defaultManagedCacheSize)
}

func NewManagedStorageWithSize(storage PayloadStorage, retainCount uint64, cacheTTL time.Duration, maxCacheSize int) *ManagedStorage {
	m := &ManagedStorage{
		storage:      storage,
		retainCount:  retainCount,
		cacheTTL:     cacheTTL,
		maxCacheSize: maxCacheSize,
		pruneTimeout: defaultPruneTimeout,
		cache:        make(map[string]*cachedEntry),
		pruning:      make(map[uint64]struct{}),
	}
	go m.cleanupLoop()
	return m
}

func (m *ManagedStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	if err := m.storage.Store(ctx, inferenceId, epochId, promptPayload, responsePayload); err != nil {
		return err
	}
	m.mu.Lock()
	if epochId > m.maxEpoch {
		m.maxEpoch = epochId
	}
	m.mu.Unlock()
	return nil
}

func (m *ManagedStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	m.mu.RLock()
	if c, ok := m.cache[inferenceId]; ok && time.Now().Before(c.expiresAt) {
		m.mu.RUnlock()
		return c.promptPayload, c.responsePayload, nil
	}
	m.mu.RUnlock()

	prompt, response, err := m.storage.Retrieve(ctx, inferenceId, epochId)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	m.cache[inferenceId] = &cachedEntry{
		promptPayload:   prompt,
		responsePayload: response,
		expiresAt:       time.Now().Add(m.cacheTTL),
	}
	m.mu.Unlock()

	return prompt, response, nil
}

func (m *ManagedStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	return m.storage.PruneEpoch(ctx, epochId)
}

func (m *ManagedStorage) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanup()
	}
}

func (m *ManagedStorage) cleanup() {
	m.mu.Lock()

	now := time.Now()
	for id, c := range m.cache {
		if now.After(c.expiresAt) {
			delete(m.cache, id)
		}
	}

	for len(m.cache) > m.maxCacheSize {
		for key := range m.cache {
			delete(m.cache, key)
			break
		}
	}

	var start, threshold uint64
	if m.maxEpoch > m.retainCount {
		threshold = m.maxEpoch - m.retainCount

		if m.minPruned+maxPruneLookback < threshold {
			m.minPruned = threshold - maxPruneLookback
		}
		start = m.minPruned
	}
	m.mu.Unlock()

	m.pruneEpochs(start, threshold)
}

// pruneEpochs prunes epochs [start, threshold) in order, advancing minPruned
// only past epochs that pruned successfully so a failed epoch is retried on
// the next cleanup tick instead of being skipped forever (#850). Pruning runs
// outside m.mu so storage I/O does not block cache reads; cleanupLoop is the
// only caller, so prunes never overlap.
func (m *ManagedStorage) pruneEpochs(start, threshold uint64) {
	for epoch := start; epoch < threshold; epoch++ {
		if err := m.pruneEpochWithTimeout(epoch); err != nil {
			// errPruneInFlight is the expected steady state under a wedged
			// ctx-ignoring backend (an earlier goroutine is still running), not a
			// failure. Return quietly so we do not emit a WARN every tick forever
			// and trip alerting; pruneEpochWithTimeout already logged it at Info.
			if errors.Is(err, errPruneInFlight) {
				return
			}
			logging.Warn("Auto-prune failed, will retry on next cleanup", types.PayloadStorage, "epochId", epoch, "error", err)
			return
		}
		logging.Info("Auto-pruned epoch", types.PayloadStorage, "epochId", epoch)

		m.mu.Lock()
		m.minPruned = epoch + 1
		m.mu.Unlock()
	}
}

// pruneEpochWithTimeout bounds a single PruneEpoch with m.pruneTimeout so a
// stuck backend cannot freeze cleanupLoop. The deadline is passed to the
// backend so ctx-aware stores (Postgres) cancel the in-flight query, and the
// call also runs in a goroutine so cleanupLoop is released at the deadline even
// for backends that ignore ctx (FileStorage's os.RemoveAll). A timed-out prune
// is treated like any other failure: minPruned is not advanced and the epoch is
// retried on the next cleanup tick. Prunes are idempotent, so a goroutine that
// completes after the deadline is harmless.
//
// The pruning set makes that retry safe under a ctx-ignoring backend: after a
// timeout the background goroutine keeps running, so we refuse to spawn a second
// one for the same epoch and instead report errPruneInFlight. That caps live
// background prunes at one per epoch (rather than one per 30s tick, which would
// let stuck tasks grow without bound). The goroutine clears the marker when
// PruneEpoch finally returns, so the next tick starts fresh.
func (m *ManagedStorage) pruneEpochWithTimeout(epoch uint64) error {
	// Check-and-set the in-flight marker under a single lock acquisition so two
	// ticks can never both decide to spawn for the same epoch.
	m.mu.Lock()
	if _, inFlight := m.pruning[epoch]; inFlight {
		m.mu.Unlock()
		logging.Info("Auto-prune still in flight, skipping respawn", types.PayloadStorage, "epochId", epoch)
		return errPruneInFlight
	}
	m.pruning[epoch] = struct{}{}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), m.pruneTimeout)
	defer cancel()

	// Buffered so the prune goroutine never blocks on send if we already
	// returned via the deadline. The marker is cleared inside the goroutine when
	// PruneEpoch actually returns (success, error, or ctx-cancel), never at the
	// deadline -- otherwise a still-running prune would be respawned.
	done := make(chan error, 1)
	go func() {
		err := m.storage.PruneEpoch(ctx, epoch)
		m.mu.Lock()
		delete(m.pruning, epoch)
		m.mu.Unlock()
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ PayloadStorage = (*ManagedStorage)(nil)
