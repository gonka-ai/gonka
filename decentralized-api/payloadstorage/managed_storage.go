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
	maxRetryAttempts        = 5
	maxFailedEpochs         = 10000
)

type cachedEntry struct {
	promptPayload   []byte
	responsePayload []byte
	expiresAt       time.Time
}

// ManagedStorage wraps PayloadStorage with read caching and automatic epoch pruning.
type ManagedStorage struct {
	storage      PayloadStorage
	retainCount  uint64
	cacheTTL     time.Duration
	maxCacheSize int

	mu           sync.RWMutex
	cache        map[string]*cachedEntry
	maxEpoch     uint64
	minPruned    uint64
	failedEpochs map[uint64]int
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
		failedEpochs: make(map[uint64]int),
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

// DeleteInference evicts the cache entry for inferenceId and forwards the
// delete to the backing storage. Cache eviction is unconditional so a stale
// cache cannot resurrect a deleted payload via Retrieve.
func (m *ManagedStorage) DeleteInference(ctx context.Context, inferenceId string, epochId uint64) error {
	m.mu.Lock()
	delete(m.cache, inferenceId)
	m.mu.Unlock()
	return m.storage.DeleteInference(ctx, inferenceId, epochId)
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

	var pruneFrom, pruneTo uint64
	if m.maxEpoch > m.retainCount {
		pruneTo = m.maxEpoch - m.retainCount
		if m.minPruned+maxPruneLookback < pruneTo {
			m.minPruned = pruneTo - maxPruneLookback
			for e := range m.failedEpochs {
				if e < m.minPruned {
					delete(m.failedEpochs, e)
				}
			}
		}
		pruneFrom = m.minPruned
	}
	m.mu.Unlock()

	if pruneFrom < pruneTo {
		for epoch := pruneFrom; epoch < pruneTo; epoch++ {
			if err := m.storage.PruneEpoch(context.Background(), epoch); err != nil {
				m.mu.Lock()
				m.failedEpochs[epoch]++
				attempts := m.failedEpochs[epoch]
				drop := attempts >= maxRetryAttempts
				if drop {
					delete(m.failedEpochs, epoch)
				}
				if len(m.failedEpochs) > maxFailedEpochs {
					var oldest uint64
					first := true
					for e := range m.failedEpochs {
						if first || e < oldest {
							oldest = e
							first = false
						}
					}
					delete(m.failedEpochs, oldest)
				}
				m.mu.Unlock()

				if drop {
					logging.Error("Auto-prune persistent failure", types.PayloadStorage, "epochId", epoch, "error", err, "attempts", attempts)
				} else if attempts == 1 {
					logging.Warn("Auto-prune failed", types.PayloadStorage, "epochId", epoch, "error", err, "attempt", attempts)
				}
				continue
			}
			logging.Info("Auto-pruned epoch", types.PayloadStorage, "epochId", epoch)
			m.mu.Lock()
			if epoch+1 > m.minPruned {
				m.minPruned = epoch + 1
			}
			delete(m.failedEpochs, epoch)
			m.mu.Unlock()
		}
	}

	m.mu.RLock()
	retries := make([]uint64, 0, len(m.failedEpochs))
	for e := range m.failedEpochs {
		retries = append(retries, e)
	}
	m.mu.RUnlock()

	for _, e := range retries {
		if err := m.storage.PruneEpoch(context.Background(), e); err != nil {
			m.mu.Lock()
			m.failedEpochs[e]++
			attempts := m.failedEpochs[e]
			drop := attempts >= maxRetryAttempts
			if drop {
				delete(m.failedEpochs, e)
			}
			m.mu.Unlock()

			if drop {
				logging.Error("Auto-prune retry persistent failure", types.PayloadStorage, "epochId", e, "error", err, "attempts", attempts)
			}
			continue
		}
		logging.Info("Auto-pruned previously failed epoch", types.PayloadStorage, "epochId", e)
		m.mu.Lock()
		delete(m.failedEpochs, e)
		m.mu.Unlock()
	}
}

var _ PayloadStorage = (*ManagedStorage)(nil)
