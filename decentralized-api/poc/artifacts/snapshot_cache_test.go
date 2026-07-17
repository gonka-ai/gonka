package artifacts

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotCacheCapAndPinnedEntries(t *testing.T) {
	c := newSnapshotCache(4, 2)
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }

	store := &SMSTArtifactStore{}
	c.mu.Lock()
	for i := uint32(1); i <= 4; i++ {
		c.insertLocked(snapshotCacheKey{store: store, count: i}, NewSMST(smstDefaultDepth), i == 1, now)
		now = now.Add(time.Second)
	}
	c.insertLocked(snapshotCacheKey{store: store, count: 5}, NewSMST(smstDefaultDepth), false, now)
	c.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) != 4 {
		t.Fatalf("cache size = %d, want 4", len(c.entries))
	}
	if _, ok := c.entries[snapshotCacheKey{store: store, count: 1}]; !ok {
		t.Fatal("pinned entry was evicted by unpinned pressure")
	}
	if _, ok := c.entries[snapshotCacheKey{store: store, count: 2}]; ok {
		t.Fatal("oldest unpinned entry should have been evicted")
	}
}

func TestSnapshotCacheIdleEvictionAndPurge(t *testing.T) {
	c := newSnapshotCache(4, 2)
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	c.idleTTL = time.Minute

	storeA := &SMSTArtifactStore{}
	storeB := &SMSTArtifactStore{}

	c.mu.Lock()
	c.insertLocked(snapshotCacheKey{store: storeA, count: 1}, NewSMST(smstDefaultDepth), false, now)
	c.insertLocked(snapshotCacheKey{store: storeA, count: 2}, NewSMST(smstDefaultDepth), true, now)
	c.insertLocked(snapshotCacheKey{store: storeB, count: 1}, NewSMST(smstDefaultDepth), true, now)
	c.mu.Unlock()

	now = now.Add(2 * time.Minute)
	c.mu.Lock()
	c.pruneExpiredLocked(now)
	c.mu.Unlock()

	c.mu.Lock()
	if _, ok := c.entries[snapshotCacheKey{store: storeA, count: 1}]; ok {
		t.Fatal("idle unpinned entry should be evicted")
	}
	if _, ok := c.entries[snapshotCacheKey{store: storeA, count: 2}]; !ok {
		t.Fatal("pinned entry should not be idle-evicted")
	}
	c.mu.Unlock()

	c.purgeStore(storeA)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[snapshotCacheKey{store: storeA, count: 2}]; ok {
		t.Fatal("purgeStore should remove pinned entries for that store")
	}
	if _, ok := c.entries[snapshotCacheKey{store: storeB, count: 1}]; !ok {
		t.Fatal("purgeStore removed unrelated store entry")
	}
}

func TestLiveCountServedWithoutCache(t *testing.T) {
	store, err := OpenSMST(t.TempDir())
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	for _, nonce := range []int32{10, 20, 30} {
		if err := store.AddWithNode(nonce, []byte{byte(nonce)}, ""); err != nil {
			t.Fatalf("Add(%d): %v", nonce, err)
		}
	}
	store.Flush()
	count := store.Count()

	if _, err := store.GetArtifactsAndProofs([]uint32{0, 1}, count); err != nil {
		t.Fatalf("GetArtifactsAndProofs: %v", err)
	}
	if _, err := store.GetArtifactsAndProofsByNonce([]int32{20}, count); err != nil {
		t.Fatalf("GetArtifactsAndProofsByNonce: %v", err)
	}

	globalSnapshotCache.mu.Lock()
	_, cached := globalSnapshotCache.entries[snapshotCacheKey{store: store, count: count}]
	globalSnapshotCache.mu.Unlock()
	if cached {
		t.Fatal("live count must be served from the live tree, not cached")
	}
}

func TestWarmSnapshotPinsEntry(t *testing.T) {
	// Cold path: tip already past the count and no retained capture, so Warm
	// rebuilds into the process snapshot cache. COW off so flush does not
	// retain; tip advance then leaves earlyCount without an in-memory clone.
	t.Setenv(envSMSTCOW, "0")
	store, err := OpenSMST(t.TempDir())
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	for _, nonce := range []int32{1, 2, 3} {
		if err := store.AddWithNode(nonce, []byte{byte(nonce)}, ""); err != nil {
			t.Fatalf("Add(%d): %v", nonce, err)
		}
	}
	store.Flush()
	earlyCount := store.Count()

	if err := store.AddWithNode(4, []byte{4}, ""); err != nil {
		t.Fatalf("Add(4): %v", err)
	}
	store.Flush()

	store.WarmSnapshot(earlyCount)

	globalSnapshotCache.mu.Lock()
	entry, ok := globalSnapshotCache.entries[snapshotCacheKey{store: store, count: earlyCount}]
	globalSnapshotCache.mu.Unlock()
	if !ok {
		t.Fatal("warm-up must cache the snapshot tree")
	}
	if !entry.pinned {
		t.Fatal("warm-up entry must be pinned")
	}
}

func TestSnapshotCacheSingleFlight(t *testing.T) {
	c := newSnapshotCache(4, 2)
	key := snapshotCacheKey{store: &SMSTArtifactStore{}, count: 1}

	var builds int32
	start := make(chan struct{})
	done := make(chan struct{})
	builder := func() (*SMST, error) {
		atomic.AddInt32(&builds, 1)
		close(start)
		<-done
		return NewSMST(smstDefaultDepth), nil
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.getOrBuildWithBuilder(key, false, builder)
			errs <- err
		}()
	}

	<-start
	time.Sleep(20 * time.Millisecond)
	close(done)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
}
