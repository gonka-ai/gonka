package nodemanager

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCacheTTL is the live-flow window. A model counts as actively served
// when it has a node observed within this span; pruning runs only for such
// models (see pruneAll).
const DefaultCacheTTL = 10 * time.Minute

// DefaultStaleTTL is the node-retirement age: an unobserved node is dropped
// after this long, and only while its model still has a live flow. Must be
// >= the fresh window.
const DefaultStaleTTL = time.Hour

// cachedNode is one ML node endpoint learned from a successful Acquire.
// lastSeen and endpoint are updated atomically so the inference hot path
// (Observe of an already-known node) never takes a write lock.
type cachedNode struct {
	nodeID   string
	endpoint atomic.Value // string
	lastSeen atomic.Int64 // unix nano
}

func (n *cachedNode) setEndpoint(endpoint string) {
	n.endpoint.Store(endpoint)
}

func (n *cachedNode) getEndpoint() string {
	v, _ := n.endpoint.Load().(string)
	return v
}

// modelCache holds nodes for a single model. The write lock is taken only to
// insert a new nodeID or to prune, not on every Observe.
type modelCache struct {
	mu     sync.RWMutex
	nodes  map[string]*cachedNode // nodeID -> node
	order  []string               // stable round-robin order of nodeIDs
	cursor atomic.Uint64
}

// Manager is a passive ML-node cache for standalone devshardd fallback.
// Observe records nodes from successful AcquireMLNode responses; PickNode
// selects one when dapi is unreachable.
//
// Selection prefers nodes inside staleTTL, then falls back to older retained
// nodes only when no fresher candidate remains. Pruning is flow-gated (see
// pruneAll), so the last-known nodes survive an outage of any length.
//
// Concurrency:
//   - Observe of a known node is lock-free (atomic lastSeen/endpoint update).
//   - Observe of a new node takes only that model's write lock.
//   - PickNode takes a per-model read lock.
//   - Prune runs periodically via Start, off the inference hot path.
type Manager struct {
	byModel       sync.Map // string -> *modelCache
	freshTTL      time.Duration
	staleTTL      time.Duration
	pruneInterval time.Duration
	now           func() time.Time
}

// NewManager returns a Manager whose live-flow window is ttl (DefaultCacheTTL
// when non-positive). Node-retirement age is DefaultStaleTTL, clamped up to ttl
// so it is never below the fresh window. Prune interval is ttl/2; call Start to
// enable pruning.
func NewManager(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	pruneInterval := ttl / 2
	if pruneInterval <= 0 {
		pruneInterval = time.Minute
	}
	staleTTL := DefaultStaleTTL
	if staleTTL < ttl {
		staleTTL = ttl
	}
	return &Manager{
		freshTTL:      ttl,
		staleTTL:      staleTTL,
		pruneInterval: pruneInterval,
		now:           time.Now,
	}
}

// Start runs periodic pruning until ctx is cancelled. Without it nothing is
// pruned; PickNode still serves whatever is cached.
func (m *Manager) Start(ctx context.Context) {
	interval := m.pruneInterval
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.pruneAll()
			}
		}
	}()
}

// Observe records (or refreshes) a node endpoint for model from a successful
// Acquire. Empty model, nodeID, or endpoint are ignored.
//
// Hot path (node already cached): atomic updates only, no prune.
// Cold path (new nodeID): per-model write lock for insert.
func (m *Manager) Observe(model, nodeID, endpoint string) {
	if model == "" || nodeID == "" || endpoint == "" {
		return
	}

	mc := m.getOrCreateModel(model)
	nowNano := m.now().UnixNano()

	mc.mu.RLock()
	node, ok := mc.nodes[nodeID]
	mc.mu.RUnlock()
	if ok {
		node.lastSeen.Store(nowNano)
		node.setEndpoint(endpoint)
		return
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()
	if node, ok = mc.nodes[nodeID]; ok {
		node.lastSeen.Store(nowNano)
		node.setEndpoint(endpoint)
		return
	}
	node = &cachedNode{nodeID: nodeID}
	node.lastSeen.Store(nowNano)
	node.setEndpoint(endpoint)
	mc.nodes[nodeID] = node
	mc.order = append(mc.order, nodeID)
}

// PickNode round-robins the next non-excluded node for model. excluded may be
// nil; ok is false when none remain. It prefers nodes within staleTTL and
// serves an older one only when no fresher candidate remains. A past-staleTTL
// node survives only while the model has no fresh flow (see pruneAll), so an
// older node is served only during a real outage, not when dapi is merely slow.
func (m *Manager) PickNode(model string, excluded map[string]struct{}) (endpoint, nodeID string, ok bool) {
	if model == "" {
		return "", "", false
	}
	v, loaded := m.byModel.Load(model)
	if !loaded {
		return "", "", false
	}
	mc := v.(*modelCache)

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	n := len(mc.order)
	if n == 0 {
		return "", "", false
	}

	nowNano := m.now().UnixNano()
	staleNano := m.staleTTL.Nanoseconds()
	start := int(mc.cursor.Load() % uint64(n))

	for _, preferFresh := range [2]bool{true, false} {
		for i := 0; i < n; i++ {
			idx := (start + i) % n
			id := mc.order[idx]
			if excluded != nil {
				if _, skip := excluded[id]; skip {
					continue
				}
			}
			node := mc.nodes[id]
			if node == nil {
				continue
			}
			if preferFresh && nowNano-node.lastSeen.Load() > staleNano {
				continue
			}
			mc.cursor.Store(uint64((idx + 1) % n))
			return node.getEndpoint(), id, true
		}
	}
	return "", "", false
}

func (m *Manager) getOrCreateModel(model string) *modelCache {
	if v, ok := m.byModel.Load(model); ok {
		return v.(*modelCache)
	}
	mc := &modelCache{nodes: make(map[string]*cachedNode)}
	actual, _ := m.byModel.LoadOrStore(model, mc)
	return actual.(*modelCache)
}

// pruneAll drops nodes unobserved for staleTTL, but only from models that still
// have a fresh observe (within freshTTL). A model with no fresh observe (dapi
// down or idle) is left intact, so its last-known nodes persist. Runs from the
// Start ticker, not from Observe/PickNode.
func (m *Manager) pruneAll() {
	nowNano := m.now().UnixNano()
	freshNano := m.freshTTL.Nanoseconds()
	staleNano := m.staleTTL.Nanoseconds()

	m.byModel.Range(func(_, value any) bool {
		mc := value.(*modelCache)
		mc.mu.Lock()
		if modelHasFreshNode(mc, nowNano, freshNano) {
			alive := mc.order[:0]
			for _, id := range mc.order {
				node := mc.nodes[id]
				if node == nil {
					continue
				}
				if nowNano-node.lastSeen.Load() > staleNano {
					delete(mc.nodes, id)
					continue
				}
				alive = append(alive, id)
			}
			for i := len(alive); i < len(mc.order); i++ {
				mc.order[i] = ""
			}
			mc.order = alive
			if len(mc.order) > 0 {
				mc.cursor.Store(mc.cursor.Load() % uint64(len(mc.order)))
			} else {
				mc.cursor.Store(0)
			}
		}
		mc.mu.Unlock()
		return true
	})
}

// modelHasFreshNode reports whether the model has at least one node observed
// within freshNano. Caller holds mc.mu.
func modelHasFreshNode(mc *modelCache, nowNano, freshNano int64) bool {
	for _, id := range mc.order {
		node := mc.nodes[id]
		if node != nil && nowNano-node.lastSeen.Load() < freshNano {
			return true
		}
	}
	return false
}
