package mlnode

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager_Defaults(t *testing.T) {
	m := NewManager(0)
	assert.Equal(t, DefaultCacheTTL, m.freshTTL)
	assert.Equal(t, DefaultStaleTTL, m.staleTTL)
	assert.Equal(t, DefaultCacheTTL/2, m.pruneInterval)

	m = NewManager(-time.Second)
	assert.Equal(t, DefaultCacheTTL, m.freshTTL)
	assert.Equal(t, DefaultStaleTTL, m.staleTTL)

	m = NewManager(time.Minute)
	assert.Equal(t, time.Minute, m.freshTTL)
	assert.Equal(t, 30*time.Second, m.pruneInterval)
	assert.Equal(t, DefaultStaleTTL, m.staleTTL)

	// staleTTL is clamped up so it is never below the fresh window.
	m = NewManager(2 * time.Hour)
	assert.Equal(t, 2*time.Hour, m.freshTTL)
	assert.Equal(t, 2*time.Hour, m.staleTTL)
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

	// All excluded -> no candidate.
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

func TestManager_PickNode_ServesStaleNode(t *testing.T) {
	// PickNode is only reached during a dapi outage, so age must not gate
	// selection: a long-idle last-known node is still served.
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute)
	m.staleTTL = 2 * time.Minute
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")

	now = now.Add(time.Hour) // far past both windows, and no prune ran
	endpoint, nodeID, ok := m.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", nodeID)
	assert.Equal(t, "http://n1", endpoint)
}

func TestManager_PickNode_PrefersFreshOverStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute) // freshTTL
	m.staleTTL = 2 * time.Minute
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-stale", "http://stale")
	now = now.Add(3 * time.Minute) // node-stale is now past staleTTL
	m.Observe("model-a", "node-fresh", "http://fresh")

	// The stale node is never served while a fresher one exists, even as
	// round-robin cycles.
	for range 3 {
		_, id, ok := m.PickNode("model-a", nil)
		require.True(t, ok)
		assert.Equal(t, "node-fresh", id)
	}
}

func TestManager_PickNode_ServesStaleWhenFreshExcluded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute)
	m.staleTTL = 2 * time.Minute
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-stale", "http://stale")
	now = now.Add(3 * time.Minute)
	m.Observe("model-a", "node-fresh", "http://fresh")

	// With the only fresh node excluded, the second pass serves the stale one.
	excluded := map[string]struct{}{"node-fresh": {}}
	_, id, ok := m.PickNode("model-a", excluded)
	require.True(t, ok)
	assert.Equal(t, "node-stale", id)
}

func TestManager_PruneAll_DropsStaleWhenFlowPresent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute) // freshTTL
	m.staleTTL = 5 * time.Minute
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-old", "http://old")

	// node-old ages past staleTTL; node-fresh proves the model is still churning.
	now = now.Add(6 * time.Minute)
	m.Observe("model-a", "node-fresh", "http://fresh")

	m.pruneAll()

	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	_, hasOld := mc.nodes["node-old"]
	_, hasFresh := mc.nodes["node-fresh"]
	mc.mu.RUnlock()
	assert.False(t, hasOld, "stale node dropped once a fresh observe proves live flow")
	assert.True(t, hasFresh)
}

func TestManager_PruneAll_RetainsWhenNoFlow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager(time.Minute) // freshTTL
	m.staleTTL = 5 * time.Minute
	m.now = func() time.Time { return now }

	m.Observe("model-a", "node-1", "http://n1")
	m.Observe("model-a", "node-2", "http://n2")

	// dapi down: no more observes. Advance far past both windows.
	now = now.Add(time.Hour)
	m.pruneAll()

	mc := loadModel(t, m, "model-a")
	mc.mu.RLock()
	require.Len(t, mc.nodes, 2, "no live flow -> nothing pruned; last-known set retained")
	mc.mu.RUnlock()

	// The retained nodes are still selectable for fallback.
	_, _, ok := m.PickNode("model-a", nil)
	assert.True(t, ok)
}

func TestManager_Start_RetainsWithoutFlow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var nowMu sync.Mutex
	m := NewManager(50 * time.Millisecond)
	m.staleTTL = 100 * time.Millisecond
	m.pruneInterval = 20 * time.Millisecond
	m.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	m.Observe("model-a", "node-1", "http://n1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	nowMu.Lock()
	now = now.Add(time.Second) // far past fresh and stale windows
	nowMu.Unlock()

	// Prune ticks keep running, but with no fresh observe the model is never
	// pruned away.
	require.Never(t, func() bool {
		_, exists := m.byModel.Load("model-a")
		return !exists
	}, 200*time.Millisecond, 20*time.Millisecond)
}

func TestManager_Start_PrunesStaleWithFlow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var nowMu sync.Mutex
	m := NewManager(50 * time.Millisecond)
	m.staleTTL = 100 * time.Millisecond
	m.pruneInterval = 20 * time.Millisecond
	m.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	m.Observe("model-a", "node-old", "http://old")

	nowMu.Lock()
	now = now.Add(200 * time.Millisecond) // node-old is now stale
	nowMu.Unlock()
	m.Observe("model-a", "node-fresh", "http://fresh") // live flow

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	// A live flow lets the ticker drop the stale node while keeping the fresh one.
	require.Eventually(t, func() bool {
		v, ok := m.byModel.Load("model-a")
		if !ok {
			return false
		}
		mc := v.(*modelCache)
		mc.mu.RLock()
		defer mc.mu.RUnlock()
		_, hasOld := mc.nodes["node-old"]
		_, hasFresh := mc.nodes["node-fresh"]
		return !hasOld && hasFresh
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
