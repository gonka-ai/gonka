package payloadstorage

import (
	"bytes"
	"context"
	"fmt"
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

func (m *mockStorage) DeleteInference(ctx context.Context, inferenceId string, epochId uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[inferenceId]; !ok {
		return ErrNotFound
	}
	delete(m.data, inferenceId)
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

	p1, r1, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	mock.mu.Lock()
	mock.data["inf-1"] = []byte("modifiedmodified")
	mock.mu.Unlock()

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

	_, _, err := ms.Retrieve(ctx, "inf-1", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	mock.mu.Lock()
	mock.data["inf-1"] = []byte("newdatnewdat")
	mock.mu.Unlock()

	time.Sleep(15 * time.Millisecond)

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

	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	ms.cleanup()

	pruned := mock.getPruned()
	if len(pruned) != 8 {
		t.Errorf("expected 8 epochs pruned, got %d: %v", len(pruned), pruned)
	}
}

func TestManagedStorage_AutoPruneSkipsOldEpochs(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	ms.Store(ctx, "inf-1", 100, []byte("p"), []byte("r"))

	ms.cleanup()

	pruned := mock.getPruned()
	if len(pruned) > maxPruneLookback {
		t.Errorf("should prune at most %d epochs, got %d: %v", maxPruneLookback, len(pruned), pruned)
	}

	for _, e := range pruned {
		if e < 88 {
			t.Errorf("should not prune epoch %d (too old, should skip)", e)
		}
	}
}

func TestManagedStorage_DeleteInferenceEvictsCache(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 3, time.Minute, 100)
	ctx := context.Background()

	if err := ms.Store(ctx, "inf-1", 4, []byte("prompt"), []byte("response")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Warm the cache.
	if _, _, err := ms.Retrieve(ctx, "inf-1", 4); err != nil {
		t.Fatalf("Retrieve (warm) failed: %v", err)
	}

	if err := ms.DeleteInference(ctx, "inf-1", 4); err != nil {
		t.Fatalf("DeleteInference failed: %v", err)
	}

	// Backing storage is gone and cache must have been evicted; a fresh Retrieve
	// has to surface ErrNotFound rather than returning the cached blob.
	_, _, err := ms.Retrieve(ctx, "inf-1", 4)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after DeleteInference, got %v", err)
	}
}

func TestManagedStorage_DeleteInferenceMissing(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 3, time.Minute, 100)
	ctx := context.Background()

	if err := ms.DeleteInference(ctx, "nope", 1); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing payload, got %v", err)
	}
}

func TestManagedStorage_NoPruneWhenBelowRetainCount(t *testing.T) {
	mock := newMockStorage()
	ms := NewManagedStorageWithSize(mock, 5, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 4; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	ms.cleanup()

	pruned := mock.getPruned()
	if len(pruned) != 0 {
		t.Errorf("should not prune when maxEpoch <= retainCount, got %v", pruned)
	}
}

type failingMockStorage struct {
	*mockStorage
	failEpochs map[uint64]bool
}

func newFailingMockStorage(failEpochs map[uint64]bool) *failingMockStorage {
	return &failingMockStorage{mockStorage: newMockStorage(), failEpochs: failEpochs}
}

func (f *failingMockStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	if f.failEpochs[epochId] {
		return fmt.Errorf("prune epoch %d: simulated storage failure", epochId)
	}
	return f.mockStorage.PruneEpoch(ctx, epochId)
}

func TestManagedStorage_PrunesPastFailure(t *testing.T) {
	mock := newFailingMockStorage(map[uint64]bool{1: true})
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 5; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// threshold = 5 - 2 = 3.
	// Epoch 0 succeeds, epoch 1 fails, epoch 2 succeeds.
	// minPruned should advance past the gap to 3 (non-contiguous),
	// epoch 1 tracked in failedEpochs.
	ms.cleanup()

	ms.mu.RLock()
	minPruned := ms.minPruned
	failedCount := len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 3 {
		t.Errorf("minPruned should advance to 3 (epochs 0 and 2 succeeded), got %d", minPruned)
	}
	if failedCount != 1 {
		t.Errorf("failedEpochs should contain 1 entry (epoch 1), got %d", failedCount)
	}

	pruned := mock.getPruned()
	prunedSet := make(map[uint64]bool)
	for _, e := range pruned {
		prunedSet[e] = true
	}
	if !prunedSet[0] || !prunedSet[2] {
		t.Errorf("epochs 0 and 2 should be pruned, got %v", pruned)
	}
	if prunedSet[1] {
		t.Errorf("epoch 1 should NOT be pruned (failed), but was in %v", pruned)
	}
}

func TestManagedStorage_RetryHealsFailedEpoch(t *testing.T) {
	mock := newFailingMockStorage(map[uint64]bool{1: true})
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 5; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	ms.cleanup()

	ms.mu.RLock()
	minPruned := ms.minPruned
	failedCount := len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 3 {
		t.Errorf("minPruned should be 3 after first cleanup, got %d", minPruned)
	}
	if failedCount != 1 {
		t.Errorf("failedEpochs should have 1 entry, got %d", failedCount)
	}

	// Fix the mock so epoch 1 succeeds, then run cleanup again.
	mock.failEpochs = nil
	ms.cleanup()

	ms.mu.RLock()
	minPruned = ms.minPruned
	failedCount = len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 3 {
		t.Errorf("minPruned should remain 3 (no new eligible epochs), got %d", minPruned)
	}
	if failedCount != 0 {
		t.Errorf("failedEpochs should be empty after retry success, got %d entries", failedCount)
	}

	pruned := mock.getPruned()
	prunedSet := make(map[uint64]bool)
	for _, e := range pruned {
		prunedSet[e] = true
	}
	if !prunedSet[1] {
		t.Errorf("epoch 1 should be pruned after retry, got %v", pruned)
	}
}

func TestManagedStorage_PersistentFailureDoesNotBlockSubsequentEpochs(t *testing.T) {
	mock := newFailingMockStorage(map[uint64]bool{1: true})
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 5; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// Tick 1: epoch 1 fails, but epochs 0 and 2 succeed.
	ms.cleanup()

	ms.mu.RLock()
	minPruned := ms.minPruned
	failedCount := len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 3 {
		t.Errorf("minPruned should be 3 after first cleanup, got %d", minPruned)
	}
	if failedCount != 1 {
		t.Errorf("failedEpochs should have 1 entry, got %d", failedCount)
	}

	// Tick 2: epoch 1 still fails. New epochs arrived.
	for i := uint64(6); i <= 8; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// threshold = 8 - 2 = 6. pruneFrom = minPruned = 3.
	// Epochs 3, 4, 5 should be pruned. Epoch 1 retry still fails.
	ms.cleanup()

	ms.mu.RLock()
	minPruned = ms.minPruned
	failedCount = len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 6 {
		t.Errorf("minPruned should advance to 6 (epochs 3-5 pruned), got %d", minPruned)
	}
	if failedCount != 1 {
		t.Errorf("failedEpochs should still have 1 entry (epoch 1), got %d", failedCount)
	}

	pruned := mock.getPruned()
	prunedSet := make(map[uint64]bool)
	for _, e := range pruned {
		prunedSet[e] = true
	}
	for _, e := range []uint64{3, 4, 5} {
		if !prunedSet[e] {
			t.Errorf("epoch %d should be pruned despite persistent failure at epoch 1", e)
		}
	}
}

func TestManagedStorage_LookbackCapCleansFailedEpochs(t *testing.T) {
	mock := newFailingMockStorage(map[uint64]bool{88: true})
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	// Jump to epoch 100 so threshold = 98, lookback cap sets minPruned = 88
	ms.Store(ctx, "inf-1", 100, []byte("p"), []byte("r"))
	ms.cleanup()

	ms.mu.RLock()
	minPruned := ms.minPruned
	failedCount := len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 98 {
		t.Errorf("minPruned should be 98 after cleanup, got %d", minPruned)
	}

	// Now jump far ahead so lookback cap would skip past the failed epoch
	ms.Store(ctx, "inf-2", 120, []byte("p"), []byte("r"))
	// threshold = 120 - 2 = 118
	// minPruned(98) + maxPruneLookback(10) = 108 < 118 → minPruned jumps to 118-10 = 108
	// Epoch 88 is below new minPruned(108) → should be cleaned from failedEpochs
	ms.cleanup()

	ms.mu.RLock()
	minPruned = ms.minPruned
	failedCount = len(ms.failedEpochs)
	ms.mu.RUnlock()

	if minPruned != 118 {
		t.Errorf("minPruned should be 118 after lookahead cap, got %d", minPruned)
	}
	if failedCount != 0 {
		t.Errorf("failedEpochs should be empty (epoch 88 below minPruned), got %d entries", failedCount)
	}
}
