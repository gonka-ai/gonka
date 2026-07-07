package artifacts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ManagedArtifactStore wraps per-(stage, model) ArtifactStores with stage-based pruning.
// The large retention buffer ensures pruned stores are "cold" with no active use.
type ManagedArtifactStore struct {
	mu          sync.RWMutex
	baseDir     string
	stores      map[storeKey]ArtifactStore
	retainCount int
	cancel      context.CancelFunc
	flushCancel context.CancelFunc
	// pruneHooks are invoked (best-effort) with the stage height whenever a
	// stage is pruned. Used to ride other DAPI-local stage data (e.g. the
	// early-share guard's checkpoints) on the same retention cadence.
	pruneHooks []func(stageHeight int64)
}

// AddPruneHook registers a callback invoked with the stage height each time a
// stage is pruned. Hooks must be added before pruning begins (i.e. at wiring
// time) and should not block.
func (m *ManagedArtifactStore) AddPruneHook(fn func(stageHeight int64)) {
	if fn == nil {
		return
	}
	m.withWriteLock(func() {
		m.pruneHooks = append(m.pruneHooks, fn)
	})
}

func (m *ManagedArtifactStore) runPruneHooks(stageHeight int64) {
	var hooks []func(stageHeight int64)
	m.withReadLock(func() {
		hooks = append(hooks, m.pruneHooks...)
	})
	for _, fn := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Warn("ManagedArtifactStore: prune hook panicked", types.PoC, "stage", stageHeight, "panic", r)
				}
			}()
			fn(stageHeight)
		}()
	}
}

type storeKey struct {
	stage   int64
	modelID string
}

type StageModelStore struct {
	ModelID string
	Store   ArtifactStore
}

func (m *ManagedArtifactStore) withReadLock(fn func()) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fn()
}

func (m *ManagedArtifactStore) withWriteLock(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn()
}

func (m *ManagedArtifactStore) getCachedStore(key storeKey) (ArtifactStore, bool) {
	var (
		store ArtifactStore
		ok    bool
	)
	m.withReadLock(func() {
		store, ok = m.stores[key]
	})
	return store, ok
}

func (m *ManagedArtifactStore) putStoreIfAbsent(key storeKey, store ArtifactStore) (ArtifactStore, bool) {
	var (
		existing ArtifactStore
		loaded   bool
	)
	m.withWriteLock(func() {
		existing, loaded = m.stores[key]
		if !loaded {
			m.stores[key] = store
			existing = store
		}
	})
	return existing, loaded
}

func (m *ManagedArtifactStore) snapshotStores() []ArtifactStore {
	var stores []ArtifactStore
	m.withReadLock(func() {
		stores = make([]ArtifactStore, 0, len(m.stores))
		for _, store := range m.stores {
			stores = append(stores, store)
		}
	})
	return stores
}

func (m *ManagedArtifactStore) snapshotStageModelIDs(pocStageStartHeight int64) []string {
	modelSet := make(map[string]struct{})
	m.withReadLock(func() {
		for key := range m.stores {
			if key.stage == pocStageStartHeight {
				modelSet[key.modelID] = struct{}{}
			}
		}
	})

	modelIDs := make([]string, 0, len(modelSet))
	for modelID := range modelSet {
		modelIDs = append(modelIDs, modelID)
	}
	return modelIDs
}

func (m *ManagedArtifactStore) removeStageStores(pocStageStartHeight int64) []ArtifactStore {
	var stores []ArtifactStore
	m.withWriteLock(func() {
		for key, store := range m.stores {
			if key.stage != pocStageStartHeight {
				continue
			}
			stores = append(stores, store)
			delete(m.stores, key)
		}
	})
	return stores
}

func (m *ManagedArtifactStore) drainStores() []struct {
	key   storeKey
	store ArtifactStore
} {
	var stores []struct {
		key   storeKey
		store ArtifactStore
	}
	m.withWriteLock(func() {
		stores = make([]struct {
			key   storeKey
			store ArtifactStore
		}, 0, len(m.stores))
		for key, store := range m.stores {
			stores = append(stores, struct {
				key   storeKey
				store ArtifactStore
			}{key: key, store: store})
		}
		m.stores = make(map[storeKey]ArtifactStore)
	})
	return stores
}

// NewManagedArtifactStore creates a new managed store with automatic pruning.
// retainCount specifies how many recent stores to keep (based on poc_stage_start_block_height).
func NewManagedArtifactStore(baseDir string, retainCount int) *ManagedArtifactStore {
	ctx, cancel := context.WithCancel(context.Background())
	m := &ManagedArtifactStore{
		baseDir:     baseDir,
		stores:      make(map[storeKey]ArtifactStore),
		retainCount: retainCount,
		cancel:      cancel,
	}
	go m.cleanupLoop(ctx)
	return m
}

func (m *ManagedArtifactStore) storeKey(pocStageStartHeight int64, modelID string) (storeKey, error) {
	if modelID == "" {
		return storeKey{}, fmt.Errorf("model_id is required")
	}
	return storeKey{stage: pocStageStartHeight, modelID: modelID}, nil
}

func (m *ManagedArtifactStore) stageDir(pocStageStartHeight int64) string {
	return filepath.Join(m.baseDir, strconv.FormatInt(pocStageStartHeight, 10))
}

func (m *ManagedArtifactStore) modelDir(pocStageStartHeight int64, modelID string) string {
	return filepath.Join(m.stageDir(pocStageStartHeight), encodeModelID(modelID))
}

func encodeModelID(modelID string) string {
	return url.PathEscape(modelID)
}

func decodeModelID(encoded string) (string, error) {
	modelID, err := url.PathUnescape(encoded)
	if err != nil {
		return "", fmt.Errorf("decode model_id %q: %w", encoded, err)
	}
	if modelID == "" {
		return "", fmt.Errorf("decoded empty model_id")
	}
	return modelID, nil
}

// GetOrCreateStore returns the store for the given PoC stage/model, creating it if needed.
func (m *ManagedArtifactStore) GetOrCreateStore(pocStageStartHeight int64, modelID string) (ArtifactStore, error) {
	key, err := m.storeKey(pocStageStartHeight, modelID)
	if err != nil {
		return nil, err
	}

	if store, ok := m.getCachedStore(key); ok {
		return store, nil
	}

	storeDir := m.modelDir(pocStageStartHeight, modelID)
	store, err := OpenSMST(storeDir)
	if err != nil {
		return nil, fmt.Errorf("open store for stage %d model %q: %w", pocStageStartHeight, modelID, err)
	}

	existing, loaded := m.putStoreIfAbsent(key, store)
	if loaded {
		_ = store.Close()
		return existing, nil
	}

	return existing, nil
}

// GetStore returns the store for the given PoC stage/model, or an error if it doesn't exist.
// Does not create new stores (for proof requests).
func (m *ManagedArtifactStore) GetStore(pocStageStartHeight int64, modelID string) (ArtifactStore, error) {
	key, err := m.storeKey(pocStageStartHeight, modelID)
	if err != nil {
		return nil, err
	}

	store, ok := m.getCachedStore(key)
	if ok {
		return store, nil
	}

	// Try to open from disk (may exist from previous run)
	storeDir := m.modelDir(pocStageStartHeight, modelID)
	if _, err := os.Stat(storeDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("store for stage %d model %q not found", pocStageStartHeight, modelID)
	}

	store, err = OpenSMST(storeDir)
	if err != nil {
		return nil, fmt.Errorf("open store for stage %d model %q: %w", pocStageStartHeight, modelID, err)
	}

	existing, loaded := m.putStoreIfAbsent(key, store)
	if loaded {
		_ = store.Close()
		return existing, nil
	}
	return existing, nil
}

func (m *ManagedArtifactStore) GetStoresForStage(pocStageStartHeight int64) ([]StageModelStore, error) {
	modelIDs, err := m.listModelIDsForStage(pocStageStartHeight)
	if err != nil {
		return nil, err
	}

	stores := make([]StageModelStore, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		store, err := m.GetStore(pocStageStartHeight, modelID)
		if err != nil {
			return nil, err
		}
		stores = append(stores, StageModelStore{
			ModelID: modelID,
			Store:   store,
		})
	}
	return stores, nil
}

func (m *ManagedArtifactStore) listModelIDsForStage(pocStageStartHeight int64) ([]string, error) {
	modelSet := make(map[string]struct{})

	stageDir := m.stageDir(pocStageStartHeight)
	entries, err := os.ReadDir(stageDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read stage dir for stage %d: %w", pocStageStartHeight, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		modelID, err := decodeModelID(entry.Name())
		if err != nil {
			return nil, err
		}
		modelSet[modelID] = struct{}{}
	}

	for _, modelID := range m.snapshotStageModelIDs(pocStageStartHeight) {
		modelSet[modelID] = struct{}{}
	}

	modelIDs := make([]string, 0, len(modelSet))
	for modelID := range modelSet {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)
	return modelIDs, nil
}

// PruneStore removes the store directory and closes any open store.
func (m *ManagedArtifactStore) PruneStore(pocStageStartHeight int64) error {
	stores := m.removeStageStores(pocStageStartHeight)

	var errs []error
	for _, store := range stores {
		if err := store.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	storeDir := m.stageDir(pocStageStartHeight)
	if err := os.RemoveAll(storeDir); err != nil {
		errs = append(errs, fmt.Errorf("remove store dir: %w", err))
	}

	m.runPruneHooks(pocStageStartHeight)

	logging.Info("Pruned artifact store", types.PoC, "height", pocStageStartHeight)
	if len(errs) > 0 {
		return fmt.Errorf("prune store errors: %v", errs)
	}
	return nil
}

func (m *ManagedArtifactStore) Flush() error {
	stores := m.snapshotStores()

	var errs []error
	for _, s := range stores {
		if s == nil {
			continue
		}
		if err := s.Flush(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("flush errors: %v", errs)
	}
	return nil
}

// StartPeriodicFlush flushes all open stores at the specified interval.
// Can be stopped with StopPeriodicFlush().
func (m *ManagedArtifactStore) StartPeriodicFlush(interval time.Duration) {
	var (
		ctx context.Context
		ok  bool
	)
	m.withWriteLock(func() {
		if m.flushCancel != nil {
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		m.flushCancel = cancel
		ok = true
	})
	if !ok {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.Flush(); err != nil {
					logging.Warn("Periodic artifact flush failed", types.PoC, "error", err)
				}
			}
		}
	}()
}

// StopPeriodicFlush stops the periodic flush goroutine and performs a final flush.
func (m *ManagedArtifactStore) StopPeriodicFlush() {
	m.withWriteLock(func() {
		if m.flushCancel != nil {
			m.flushCancel()
			m.flushCancel = nil
		}
	})

	// Final flush to persist any remaining data
	if err := m.Flush(); err != nil {
		logging.Warn("Final artifact flush failed", types.PoC, "error", err)
	}
}

// Close stops the cleanup loop, flushes and closes all stores.
func (m *ManagedArtifactStore) Close() error {
	// Stop cleanup goroutine first
	if m.cancel != nil {
		m.cancel()
	}
	// Stop flush goroutine
	if m.flushCancel != nil {
		m.flushCancel()
	}

	stores := m.drainStores()

	var errs []error
	for _, item := range stores {
		if item.store == nil {
			continue
		}
		if err := item.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close stage %d model %q: %w", item.key.stage, item.key.modelID, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// ListStores returns sorted list of poc_stage_start_block_heights with stores on disk.
func (m *ManagedArtifactStore) ListStores() ([]int64, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read base dir: %w", err)
	}

	var heights []int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		height, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue // skip non-numeric dirs
		}
		heights = append(heights, height)
	}

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	return heights, nil
}

func (m *ManagedArtifactStore) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *ManagedArtifactStore) cleanup() {
	heights, err := m.ListStores()
	if err != nil {
		logging.Warn("Failed to list artifact stores for cleanup", types.PoC, "error", err)
		return
	}

	if len(heights) <= m.retainCount {
		return
	}

	toPrune := heights[:len(heights)-m.retainCount]
	for _, height := range toPrune {
		if err := m.PruneStore(height); err != nil {
			logging.Warn("Auto-prune artifact store failed", types.PoC, "height", height, "error", err)
		}
	}
}
