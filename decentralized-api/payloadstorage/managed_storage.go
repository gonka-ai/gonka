package payloadstorage

import (
	"context"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	defaultManagedCacheSize = 1000
	maxPruneLookback        = 10
)

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

	mu        sync.RWMutex
	cache     map[string]*cachedEntry
	maxEpoch  uint64
	minPruned uint64
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
		cache:        make(map[string]*cachedEntry),
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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanup()
	}
}

func (m *ManagedStorage) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

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

	if m.maxEpoch > m.retainCount {
		threshold := m.maxEpoch - m.retainCount

		if m.minPruned+maxPruneLookback < threshold {
			m.minPruned = threshold - maxPruneLookback
		}

		for epoch := m.minPruned; epoch < threshold; epoch++ {
			go func(e uint64) {
				if err := m.storage.PruneEpoch(context.Background(), e); err != nil {
					logging.Warn("Auto-prune failed", types.PayloadStorage, "epochId", e, "error", err)
				} else {
					logging.Info("Auto-pruned epoch", types.PayloadStorage, "epochId", e)
				}
			}(epoch)
		}
		m.minPruned = threshold
	}
}

var _ PayloadStorage = (*ManagedStorage)(nil)
