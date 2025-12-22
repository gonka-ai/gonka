package payloadstorage

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

type mockStorage struct {
	mu      sync.Mutex
	data    map[string][]byte
	pruned  []uint64
	storeCb func(epochId uint64)
}

func newMockStorage() *mockStorage {
	return &mockStorage{data: make(map[string][]byte)}
}

func (m *mockStorage) Store(ctx context.Context, inferenceId string, epochId uint64, prompt, response []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[inferenceId] = append(prompt, response...)
	if m.storeCb != nil {
		m.storeCb(epochId)
	}
	return nil
}

func (m *mockStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[inferenceId]
	if !ok {
		return nil, nil, ErrNotFound
	}
	half := len(d) / 2
	return d[:half], d[half:], nil
}

func (m *mockStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned = append(m.pruned, epochId)
	return nil
}

func (m *mockStorage) getPruned() []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]uint64, len(m.pruned))
	copy(result, m.pruned)
	return result
}

func TestManagedStorage_CacheHit(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 3, time.Minute, 100)
	ctx := context.Background()

	if err := mock.Store(ctx, "inf-1", 1, []byte("prompt"), []byte("response")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// First retrieve - cache miss
	p1, r1, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Modify underlying storage
	mock.mu.Lock()
	mock.data["inf-1"] = []byte("modifiedmodified")
	mock.mu.Unlock()

	// Second retrieve - should hit cache, return original data
	p2, r2, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if !bytes.Equal(p1, p2) || !bytes.Equal(r1, r2) {
		t.Errorf("cache should return same data: got %q/%q, want %q/%q", p2, r2, p1, r1)
	}
}

func TestManagedStorage_CacheExpiration(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 3, 10*time.Millisecond, 100)
	ctx := context.Background()

	if err := mock.Store(ctx, "inf-1", 1, []byte("prompt"), []byte("response")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// First retrieve
	_, _, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Modify underlying storage
	mock.mu.Lock()
	mock.data["inf-1"] = []byte("newdatnewdat")
	mock.mu.Unlock()

	// Wait for cache to expire
	time.Sleep(15 * time.Millisecond)

	// Retrieve should get new data
	p, r, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if string(p) != "newdat" || string(r) != "newdat" {
		t.Errorf("expired cache should fetch fresh data: got %q/%q", p, r)
	}
}

func TestManagedStorage_StoreTracksMaxEpoch(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 3, time.Minute, 100)
	ctx := context.Background()

	// Store in various epochs
	ms.Store(ctx, "inf-1", 5, []byte("p"), []byte("r"))
	ms.Store(ctx, "inf-2", 3, []byte("p"), []byte("r"))
	ms.Store(ctx, "inf-3", 10, []byte("p"), []byte("r"))
	ms.Store(ctx, "inf-4", 7, []byte("p"), []byte("r"))

	ms.mu.RLock()
	maxEpoch := ms.maxEpoch
	ms.mu.RUnlock()

	if maxEpoch != 10 {
		t.Errorf("maxEpoch should be 10, got %d", maxEpoch)
	}
}

func TestManagedStorage_AutoPruneTriggersInCleanup(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	// Store enough to trigger pruning (retainCount=2, so epochs 0-7 should be pruned when maxEpoch=10)
	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// Trigger cleanup manually
	ms.cleanup()

	// Wait for async prune goroutines
	time.Sleep(50 * time.Millisecond)

	pruned := mock.getPruned()
	// threshold = 10 - 2 = 8
	// minPruned starts at 0, but only last 10 should be pruned
	// so epochs 0-7 should be pruned (8 epochs)
	if len(pruned) != 8 {
		t.Errorf("expected 8 epochs pruned, got %d: %v", len(pruned), pruned)
	}
}

func TestManagedStorage_AutoPruneSkipsOldEpochs(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	// Jump straight to epoch 100 (simulating restart with existing data)
	ms.Store(ctx, "inf-1", 100, []byte("p"), []byte("r"))

	// Trigger cleanup
	ms.cleanup()
	time.Sleep(50 * time.Millisecond)

	pruned := mock.getPruned()
	// threshold = 100 - 2 = 98
	// minPruned=0, but 0 + 10 < 98, so minPruned should jump to 98 - 10 = 88
	// Only epochs 88-97 should be pruned (10 epochs max)
	if len(pruned) > maxPruneLookback {
		t.Errorf("should prune at most %d epochs, got %d: %v", maxPruneLookback, len(pruned), pruned)
	}

	// Verify we're pruning recent epochs, not from 0
	for _, e := range pruned {
		if e < 88 {
			t.Errorf("should not prune epoch %d (too old, should skip)", e)
		}
	}
}

func TestManagedStorage_NoPruneWhenBelowRetainCount(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 5, time.Minute, 100)
	ctx := context.Background()

	// Store in epochs 0-4 (maxEpoch=4, retainCount=5)
	for i := uint64(0); i <= 4; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	ms.cleanup()
	time.Sleep(50 * time.Millisecond)

	pruned := mock.getPruned()
	if len(pruned) != 0 {
		t.Errorf("should not prune when maxEpoch <= retainCount, got %v", pruned)
	}
}

