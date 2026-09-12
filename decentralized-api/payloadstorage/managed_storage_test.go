package payloadstorage

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockStorage struct {
	mu         sync.Mutex
	data       map[string][]byte
	pruned     []uint64
	storeCb    func(epochId uint64)
	pruneCb    func(epochId uint64) error
	pruneCtxCb func(ctx context.Context, epochId uint64) error
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
	// pruneCtxCb runs outside the lock so it can block on ctx (simulating a
	// stuck backend) without deadlocking observers like getPruned.
	if m.pruneCtxCb != nil {
		if err := m.pruneCtxCb(ctx, epochId); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pruneCb != nil {
		if err := m.pruneCb(epochId); err != nil {
			return err
		}
	}
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

	// Trigger cleanup manually; pruning runs synchronously
	ms.cleanup()

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

func TestManagedStorage_AutoPruneStopsAtFailedEpoch(t *testing.T) {
	mock := newMockStorage()
	pruneErr := errors.New("db connection lost")
	mock.pruneCb = func(epochId uint64) error {
		if epochId == 3 {
			return pruneErr
		}
		return nil
	}
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// threshold = 10 - 2 = 8; epoch 3 fails, so only 0-2 should be pruned
	ms.cleanup()

	pruned := mock.getPruned()
	if len(pruned) != 3 {
		t.Errorf("expected 3 epochs pruned before failure, got %d: %v", len(pruned), pruned)
	}
	for _, e := range pruned {
		if e >= 3 {
			t.Errorf("epoch %d should not be pruned past the failed epoch 3", e)
		}
	}

	ms.mu.RLock()
	minPruned := ms.minPruned
	ms.mu.RUnlock()
	if minPruned != 3 {
		t.Errorf("minPruned should stop at failed epoch 3, got %d", minPruned)
	}
}

func TestManagedStorage_AutoPruneRetriesFailedEpoch(t *testing.T) {
	mock := newMockStorage()
	failOnce := true
	mock.pruneCb = func(epochId uint64) error {
		if epochId == 3 && failOnce {
			failOnce = false
			return errors.New("transient failure")
		}
		return nil
	}
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ctx := context.Background()

	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// First cleanup: prunes 0-2, fails at 3
	ms.cleanup()
	// Second cleanup: retries 3, then continues through 7
	ms.cleanup()

	pruned := mock.getPruned()
	// threshold = 8: epochs 0-7 all pruned across the two ticks, none skipped
	if len(pruned) != 8 {
		t.Errorf("expected 8 epochs pruned after retry, got %d: %v", len(pruned), pruned)
	}
	seen := make(map[uint64]bool)
	for _, e := range pruned {
		if seen[e] {
			t.Errorf("epoch %d pruned more than once", e)
		}
		seen[e] = true
	}
	for e := uint64(0); e < 8; e++ {
		if !seen[e] {
			t.Errorf("epoch %d was never pruned", e)
		}
	}

	ms.mu.RLock()
	minPruned := ms.minPruned
	ms.mu.RUnlock()
	if minPruned != 8 {
		t.Errorf("minPruned should reach threshold 8 after retry, got %d", minPruned)
	}
}

func TestManagedStorage_AutoPruneTimesOutStuckEpoch(t *testing.T) {
	mock := newMockStorage()
	// Epoch 3 blocks until its context is cancelled, simulating a stuck backend
	// (e.g. a DROP TABLE waiting on a lock). Every other epoch prunes instantly.
	mock.pruneCtxCb = func(ctx context.Context, epochId uint64) error {
		if epochId == 3 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ms.pruneTimeout = 20 * time.Millisecond

	ctx := context.Background()
	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// Without the per-epoch timeout the stuck epoch 3 would block cleanupLoop
	// forever; with it, cleanup returns once the deadline fires.
	done := make(chan struct{})
	go func() {
		ms.cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup never returned: a stuck epoch froze cleanupLoop (per-epoch prune timeout missing)")
	}

	pruned := mock.getPruned()
	if len(pruned) != 3 {
		t.Errorf("expected epochs 0-2 pruned before the stuck epoch, got %d: %v", len(pruned), pruned)
	}
	for _, e := range pruned {
		if e >= 3 {
			t.Errorf("epoch %d should not be pruned past the stuck epoch 3", e)
		}
	}

	ms.mu.RLock()
	minPruned := ms.minPruned
	ms.mu.RUnlock()
	if minPruned != 3 {
		t.Errorf("minPruned should stop at the stuck epoch 3 so it retries next tick, got %d", minPruned)
	}
}

func TestManagedStorage_AutoPruneTimesOutCtxIgnoringBackend(t *testing.T) {
	mock := newMockStorage()
	release := make(chan struct{})
	// Epoch 3 blocks WITHOUT observing ctx, simulating a backend like
	// FileStorage.os.RemoveAll that ignores cancellation. Only the
	// goroutine+select wrapper (not a bare ctx) can release cleanupLoop here.
	mock.pruneCtxCb = func(ctx context.Context, epochId uint64) error {
		if epochId == 3 {
			<-release
		}
		return nil
	}
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ms.pruneTimeout = 20 * time.Millisecond
	defer close(release) // let the leaked prune goroutine finish after the test

	ctx := context.Background()
	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	done := make(chan struct{})
	go func() {
		ms.cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup never returned: a ctx-ignoring backend froze cleanupLoop")
	}

	pruned := mock.getPruned()
	// Epochs 0-2 pruned; epoch 3 is still blocked in the background goroutine,
	// so it is not counted and minPruned stops there for retry.
	if len(pruned) != 3 {
		t.Errorf("expected epochs 0-2 pruned before the stuck epoch, got %d: %v", len(pruned), pruned)
	}

	ms.mu.RLock()
	minPruned := ms.minPruned
	ms.mu.RUnlock()
	if minPruned != 3 {
		t.Errorf("minPruned should stop at the stuck epoch 3 for retry, got %d", minPruned)
	}
}

func TestManagedStorage_AutoPruneDoesNotStackStuckGoroutines(t *testing.T) {
	mock := newMockStorage()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var stuckInvocations int32
	// Epoch 3 blocks WITHOUT observing ctx, simulating a ctx-ignoring backend
	// (FileStorage's os.RemoveAll) whose prune goroutine keeps running after the
	// deadline. On the OLD code pruneEpochWithTimeout returned on timeout without
	// tracking that still-running prune, so every cleanup tick spawned a fresh
	// goroutine for the same stuck epoch -- N ticks meant N live goroutines all
	// calling PruneEpoch(3) (the maintainer's "stuck tasks keep growing"). The
	// single-flight guard caps that at exactly one.
	mock.pruneCtxCb = func(ctx context.Context, epochId uint64) error {
		if epochId == 3 {
			atomic.AddInt32(&stuckInvocations, 1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		return nil
	}
	ms := NewManagedStorageWithSize(mock, 2, time.Minute, 100)
	ms.pruneTimeout = 20 * time.Millisecond
	defer close(release) // let the single leaked prune goroutine finish after the test

	ctx := context.Background()
	for i := uint64(0); i <= 10; i++ {
		ms.Store(ctx, "inf-"+string(rune('a'+i)), i, []byte("p"), []byte("r"))
	}

	// Drive several cleanup ticks while epoch 3 stays stuck. Tick 1 spawns the
	// prune goroutine for epoch 3 and times out; ticks 2+ see the in-flight
	// marker and return without spawning. Old code: 3 invocations (one per tick).
	const ticks = 3
	for i := 0; i < ticks; i++ {
		ms.cleanup()
	}

	// The stuck prune runs in a background goroutine; wait until it has actually
	// entered the blocking callback so the count reflects real invocations rather
	// than an unscheduled goroutine (which would read 0 and spuriously pass/fail).
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stuck prune goroutine never entered PruneEpoch for epoch 3")
	}

	if got := atomic.LoadInt32(&stuckInvocations); got != 1 {
		t.Errorf("stuck epoch 3 must be invoked exactly once across %d ticks, got %d "+
			"(each extra invocation is a leaked background goroutine that never stops)", ticks, got)
	}

	// Epochs 0-2 pruned once; epoch 3 stuck, so minPruned stops there for retry.
	ms.mu.RLock()
	minPruned := ms.minPruned
	ms.mu.RUnlock()
	if minPruned != 3 {
		t.Errorf("minPruned should stop at the stuck epoch 3, got %d", minPruned)
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

	pruned := mock.getPruned()
	if len(pruned) != 0 {
		t.Errorf("should not prune when maxEpoch <= retainCount, got %v", pruned)
	}
}
