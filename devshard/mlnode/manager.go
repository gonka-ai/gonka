package mlnode

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCacheTTL is how long a passively observed node stays eligible for
// fallback selection without being re-observed.
const DefaultCacheTTL = 10 * time.Minute

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

// modelCache holds nodes for a single model. The write lock is only taken when
// inserting a new nodeID or during periodic prune — not on every Observe.
type modelCache struct {
	mu     sync.RWMutex
	nodes  map[string]*cachedNode // nodeID → node
	order  []string               // stable round-robin order of nodeIDs
	cursor atomic.Uint64
}

// Manager is a passive ML-node cache for standalone devshardd fallback.
//
// Entries are recorded via Observe on successful AcquireMLNode responses and
// selected via PickNode when dapi is unreachable.
//
// Concurrency:
//   - Observe of a known node is lock-free (atomic lastSeen/endpoint update).
//   - Observe of a new node takes only that model's write lock.
//   - PickNode takes a per-model read lock and skips expired entries.
//   - Prune runs periodically via Start (not on the inference hot path).
type Manager struct {
	byModel       sync.Map // string → *modelCache
	ttl           time.Duration
	pruneInterval time.Duration
	now           func() time.Time
}

// NewManager returns a Manager with the given cache TTL.
// A non-positive ttl uses DefaultCacheTTL. Periodic prune interval defaults
// to ttl/2 (call Start to enable pruning).
func NewManager(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	pruneInterval := ttl / 2
	if pruneInterval <= 0 {
		pruneInterval = time.Minute
	}
	return &Manager{
		ttl:           ttl,
		pruneInterval: pruneInterval,
		now:           time.Now,
	}
}

// Start runs periodic TTL pruning until ctx is cancelled. Safe to call once.
// Without Start, expired entries are skipped by PickNode but not removed.
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

// PickNode selects the next non-excluded, non-expired node for model using
// per-model round-robin. excluded may be nil. ok is false when no candidate
// remains. Expired entries are skipped (not removed — see Start/pruneAll).
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
	ttlNano := m.ttl.Nanoseconds()
	start := int(mc.cursor.Load() % uint64(n))

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
		if nowNano-node.lastSeen.Load() > ttlNano {
			continue
		}
		mc.cursor.Store(uint64((idx + 1) % n))
		return node.getEndpoint(), id, true
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

// pruneAll drops entries older than ttl. Called from the Start ticker, not
// from Observe/PickNode.
func (m *Manager) pruneAll() {
	nowNano := m.now().UnixNano()
	ttlNano := m.ttl.Nanoseconds()

	m.byModel.Range(func(key, value any) bool {
		mc := value.(*modelCache)
		mc.mu.Lock()
		alive := mc.order[:0]
		for _, id := range mc.order {
			node := mc.nodes[id]
			if node == nil {
				continue
			}
			if nowNano-node.lastSeen.Load() > ttlNano {
				delete(mc.nodes, id)
				continue
			}
			alive = append(alive, id)
		}
		for i := len(alive); i < len(mc.order); i++ {
			mc.order[i] = ""
		}
		mc.order = alive
		empty := len(mc.order) == 0
		if !empty {
			mc.cursor.Store(mc.cursor.Load() % uint64(len(mc.order)))
		} else {
			mc.cursor.Store(0)
		}
		mc.mu.Unlock()

		if empty {
			m.byModel.Delete(key)
		}
		return true
	})
}
