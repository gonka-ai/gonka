package mlnode

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager_DefaultTTL(t *testing.T) {
	m := NewManager(0)
	assert.Equal(t, DefaultCacheTTL, m.ttl)
	assert.Equal(t, DefaultCacheTTL/2, m.pruneInterval)

	m = NewManager(-time.Second)
	assert.Equal(t, DefaultCacheTTL, m.ttl)

	m = NewManager(time.Minute)
	assert.Equal(t, time.Minute, m.ttl)
	assert.Equal(t, 30*time.Second, m.pruneInterval)
}

func TestManager_Observe_InsertsAndRefreshes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Hour)
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1/v1")
	endpoint, nodeID, ok := m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", nodeID)
	assert.Equal(t, "http://n1/v1", endpoint)

	// Same node_id updates endpoint and lastSeen without growing the cache.
	now = now.Add(time.Minute)
	m.Observe("model-a", "node-1", "http://n1/v2")
	endpoint, nodeID, ok = m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", nodeID)
	assert.Equal(t, "http://n1/v2", endpoint)

	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	require.Len(t, mc.nodes, 1)
	assert.Equal(t, now.UnixNano(), mc.nodes["node-1"].lastSeen.Load())
	mc.mu.RUnlock()
}

func TestManager_Observe_IgnoresEmptyFields(t *testing.T) {
	m := NewManager(time.Hour)
	m.Observe("", "node-1", "http://n1")
	m.Observe("model-a", "", "http://n1")
	m.Observe("model-a", "node-1", "")

	_, _, ok := m.PickNode("model-a", nil)
	assert.False(t, ok)
}

func TestManager_Observe_KnownNodeIsLockFree(t *testing.T) {
	// Concurrent Observe of an already-known node must not deadlock and must
	// refresh lastSeen. This is the inference hot path.
	now := time.Unix(1_700_000_000, 0)
	var nowMu sync.Mutex
	m := NewManager(time.Hour)
	m.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	m.Observe("model-a", "node-1", "http://n1")

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				nowMu.Lock()
				now = now.Add(time.Nanosecond)
				nowMu.Unlock()
				m.Observe("model-a", "node-1", "http://n1")
			}
		}()
	}
	wg.Wait()

	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	require.Len(t, mc.nodes, 1)
	assert.Greater(t, mc.nodes["node-1"].lastSeen.Load(), time.Unix(1_700_000_000, 0).UnixNano())
	mc.mu.RUnlock()
}

func TestManager_PickNode_RoundRobin(t *testing.T) {
	m := NewManager(time.Hour)
	m.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")
	m.Observe("model-a", "node-3", "http://n3")

	got := make([]string, 0, 6)
	for range 6 {
		_, nodeID, ok := m.PickNode("model-a", nil)
		require.True(t, ok)
		got = append(got, nodeID)
	}
	assert.Equal(t, []string{"node-1", "node-2", "node-3", "node-1", "node-2", "node-3"}, got)
}

func TestManager_PickNode_Exclusion(t *testing.T) {
	m := NewManager(time.Hour)
	m.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")
	m.Observe("model-a", "node-3", "http://n3")

	excluded := map[string]struct{}{"node-1": {}, "node-3": {}}
	endpoint, nodeID, ok := m.PickNode("model-a", excluded)
	require.True(t, ok)
	assert.Equal(t, "node-2", nodeID)
	assert.Equal(t, "http://n2", endpoint)

	// All excluded → no candidate.
	excluded["node-2"] = struct{}{}
	_, _, ok = m.PickNode("model-a", excluded)
	assert.False(t, ok)
}

func TestManager_PickNode_RoundRobinSkipsExcluded(t *testing.T) {
	m := NewManager(time.Hour)
	m.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")
	m.Observe("model-a", "node-3", "http://n3")

	// Advance past node-1 so cursor points at node-2.
	_, id, ok := m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", id)

	// Exclude node-2; next pick should be node-3, then wrap to node-1.
	excluded := map[string]struct{}{"node-2": {}}
	_, id, ok = m.PickNode("model-a", excluded)
	require.True(t, ok)
	assert.Equal(t, "node-3", id)

	_, id, ok = m.PickNode("model-a", excluded)
	require.True(t, ok)
	assert.Equal(t, "node-1", id)
}

func TestManager_PerModelIsolation(t *testing.T) {
	m := NewManager(time.Hour)
	m.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	m.Observe("model-a", "node-1", "http://a1")
	m.Observe("model-b", "node-2", "http://b2")

	_, id, ok := m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", id)

	_, id, ok = m.PickNode("model-b", nil)
	require.True(t, ok)
	assert.Equal(t, "node-2", id)

	_, _, ok = m.PickNode("model-c", nil)
	assert.False(t, ok)
}

func TestManager_PickNode_SkipsExpiredWithoutPrune(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute)
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")

	// Advance past TTL for both entries. PickNode skips them but does not prune.
	now = now.Add(time.Minute + time.Second)
	_, _, ok := m.PickNode("model-a", nil)
	assert.False(t, ok)

	// Entries remain until pruneAll.
	_, exists := m.byModel.Load("model-a")
	assert.True(t, exists)
	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	assert.Len(t, mc.nodes, 2)
	mc.mu.RUnlock()
}

func TestManager_PruneAll_RemovesExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute)
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")
	now = now.Add(30 * time.Second)
	m.Observe("model-a", "node-2", "http://n2") // refresh node-2

	now = now.Add(31 * time.Second) // node-1 expired, node-2 still within TTL
	m.pruneAll()

	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	require.Len(t, mc.nodes, 1)
	_, has1 := mc.nodes["node-1"]
	_, has2 := mc.nodes["node-2"]
	mc.mu.RUnlock()
	assert.False(t, has1)
	assert.True(t, has2)

	// All expired → model removed.
	now = now.Add(time.Minute + time.Second)
	m.pruneAll()
	_, exists := m.byModel.Load("model-a")
	assert.False(t, exists)
}

func TestManager_TTL_KeepsFreshEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute)
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")
	now = now.Add(30 * time.Second)
	m.Observe("model-a", "node-2", "http://n2")

	// node-1 lastSeen at t0; now is t0+60s → exactly at TTL boundary (kept: <= ttl).
	now = now.Add(30 * time.Second)
	got := make(map[string]struct{})
	for range 2 {
		_, id, ok := m.PickNode("model-a", nil)
		require.True(t, ok)
		got[id] = struct{}{}
	}
	assert.Equal(t, map[string]struct{}{"node-1": {}, "node-2": {}}, got)

	// One more second ages node-1 out of PickNode eligibility.
	now = now.Add(time.Second)
	_, id, ok := m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-2", id)

	_, id, ok = m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-2", id)
}

func TestManager_Start_PrunesPeriodically(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(50 * time.Millisecond)
	m.pruneInterval = 20 * time.Millisecond
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	now = now.Add(100 * time.Millisecond)
	require.Eventually(t, func() bool {
		_, exists := m.byModel.Load("model-a")
		return !exists
	}, time.Second, 10*time.Millisecond)
}

func TestManager_PickNode_EmptyModel(t *testing.T) {
	m := NewManager(time.Hour)
	_, _, ok := m.PickNode("", nil)
	assert.False(t, ok)
}

func loadModel(t *testing.T, m *Manager, model string) *modelCache {
	t.Helper()
	v, ok := m.byModel.Load(model)
	require.True(t, ok)
	return v.(*modelCache)
}
