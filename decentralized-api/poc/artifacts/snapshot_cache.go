package artifacts

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

const (
	// snapshotCacheMaxEntries must fit the pinned working set plus rotation
	// room. With 2 models the worst case pins 4 trees (early + historical
	// final per model); 6 leaves 2 rotating slots even then. Note that
	// exceeding the cap with pinned-only entries evicts the LRU pinned tree.
	snapshotCacheMaxEntries    = 6
	snapshotCacheIdleTTL       = 20 * time.Minute
	snapshotCacheMaxConcurrent = 2
)

type snapshotCacheKey struct {
	store *SMSTArtifactStore
	count uint32
}

type snapshotCacheEntry struct {
	key        snapshotCacheKey
	tree       *SMST
	lastAccess time.Time
	pinned     bool
	elem       *list.Element
}

type snapshotBuildCall struct {
	done chan struct{}
	tree *SMST
	err  error
}

type snapshotCache struct {
	mu         sync.Mutex
	maxEntries int
	idleTTL    time.Duration
	entries    map[snapshotCacheKey]*snapshotCacheEntry
	lru        *list.List
	inflight   map[snapshotCacheKey]*snapshotBuildCall
	buildSem   chan struct{}
	now        func() time.Time
}

var globalSnapshotCache = newSnapshotCache(snapshotCacheMaxEntries, snapshotCacheMaxConcurrent)

func newSnapshotCache(maxEntries, maxConcurrent int) *snapshotCache {
	if maxConcurrent <= 0 {
		maxConcurrent = snapshotCacheMaxConcurrent
	}
	return &snapshotCache{
		maxEntries: maxEntries,
		idleTTL:    snapshotCacheIdleTTL,
		entries:    make(map[snapshotCacheKey]*snapshotCacheEntry, maxEntries),
		lru:        list.New(),
		inflight:   make(map[snapshotCacheKey]*snapshotBuildCall),
		buildSem:   make(chan struct{}, maxConcurrent),
		now:        time.Now,
	}
}

func (c *snapshotCache) getOrBuild(store *SMSTArtifactStore, count uint32, pinned bool) (*SMST, error) {
	key := snapshotCacheKey{store: store, count: count}
	return c.getOrBuildWithBuilder(key, pinned, func() (*SMST, error) {
		return store.buildSnapshotTree(count)
	})
}

func (c *snapshotCache) getOrBuildWithBuilder(key snapshotCacheKey, pinned bool, builder func() (*SMST, error)) (*SMST, error) {
	c.mu.Lock()
	now := c.now()
	c.pruneExpiredLocked(now)
	if entry, ok := c.entries[key]; ok {
		entry.lastAccess = now
		entry.pinned = entry.pinned || pinned
		c.lru.MoveToFront(entry.elem)
		tree := entry.tree
		c.mu.Unlock()
		return tree, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.tree, call.err
	}

	call := &snapshotBuildCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	c.buildSem <- struct{}{}
	tree, err := builder()
	<-c.buildSem

	c.mu.Lock()
	call.tree = tree
	call.err = err
	delete(c.inflight, key)
	if err == nil {
		c.insertLocked(key, tree, pinned, c.now())
	}
	close(call.done)
	c.mu.Unlock()

	return tree, err
}

func (c *snapshotCache) purgeStore(store *SMSTArtifactStore) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for elem := c.lru.Back(); elem != nil; {
		prev := elem.Prev()
		entry := elem.Value.(*snapshotCacheEntry)
		if entry.key.store == store {
			c.removeEntryLocked(entry)
		}
		elem = prev
	}
}

func (c *snapshotCache) insertLocked(key snapshotCacheKey, tree *SMST, pinned bool, now time.Time) {
	if entry, ok := c.entries[key]; ok {
		entry.tree = tree
		entry.lastAccess = now
		entry.pinned = entry.pinned || pinned
		c.lru.MoveToFront(entry.elem)
		return
	}

	entry := &snapshotCacheEntry{
		key:        key,
		tree:       tree,
		lastAccess: now,
		pinned:     pinned,
	}
	entry.elem = c.lru.PushFront(entry)
	c.entries[key] = entry
	c.enforceMaxEntriesLocked()
}

func (c *snapshotCache) pruneExpiredLocked(now time.Time) {
	for elem := c.lru.Back(); elem != nil; {
		prev := elem.Prev()
		entry := elem.Value.(*snapshotCacheEntry)
		if !entry.pinned && now.Sub(entry.lastAccess) > c.idleTTL {
			c.removeEntryLocked(entry)
		}
		elem = prev
	}
}

func (c *snapshotCache) enforceMaxEntriesLocked() {
	for len(c.entries) > c.maxEntries {
		if entry := c.oldestUnpinnedLocked(); entry != nil {
			c.removeEntryLocked(entry)
			continue
		}
		if back := c.lru.Back(); back != nil {
			c.removeEntryLocked(back.Value.(*snapshotCacheEntry))
			continue
		}
		return
	}
}

func (c *snapshotCache) oldestUnpinnedLocked() *snapshotCacheEntry {
	for elem := c.lru.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*snapshotCacheEntry)
		if !entry.pinned {
			return entry
		}
	}
	return nil
}

func (c *snapshotCache) removeEntryLocked(entry *snapshotCacheEntry) {
	delete(c.entries, entry.key)
	c.lru.Remove(entry.elem)
}

func (s *SMSTArtifactStore) buildSnapshotTree(count uint32) (*SMST, error) {
	offsets, buffered, err := s.snapshotRebuildInputs(count)
	if err != nil {
		return nil, err
	}
	tree := rebuildTreeFromInputs(s.dataFile, offsets, buffered, count)
	if tree.Count() != count {
		return nil, fmt.Errorf("rebuilt count %d differs from requested %d", tree.Count(), count)
	}
	return tree, nil
}
