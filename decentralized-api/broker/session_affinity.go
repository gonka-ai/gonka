package broker

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Decision outcomes for decentralized_api_mlnode_affinity_decision_total; see broker.getLeastBusyNode.
const (
	mlnodeAffinityDecisionHit     = "hit"
	mlnodeAffinityDecisionYielded = "yielded"
	mlnodeAffinityDecisionMiss    = "miss"
)

// nodeAffinityConfig tunes the bounded session -> mlnode stickiness; OFF
// unless explicitly enabled, independently of whatever the gateway sends.
type nodeAffinityConfig struct {
	Enabled     bool
	MaxRequests int
	TTL         time.Duration
	MaxEntries  int
}

func defaultNodeAffinityConfig() nodeAffinityConfig {
	return nodeAffinityConfig{
		Enabled:     false,
		MaxRequests: 64,
		TTL:         10 * time.Minute,
		MaxEntries:  50_000,
	}
}

func nodeAffinityConfigFromEnv() nodeAffinityConfig {
	cfg := defaultNodeAffinityConfig()
	// TrimSpace mirrors devshardd's envBoolOr: the two processes read this one var and must agree.
	if enabled := strings.TrimSpace(os.Getenv("DAPI_MLNODE_AFFINITY_ENABLED")); enabled != "" {
		if value, err := strconv.ParseBool(enabled); err == nil {
			cfg.Enabled = value
		}
	}
	if maxRequests := os.Getenv("DAPI_MLNODE_AFFINITY_MAX_REQUESTS"); maxRequests != "" {
		if value, err := strconv.Atoi(maxRequests); err == nil && value > 0 {
			cfg.MaxRequests = value
		}
	}
	if ttlMs := os.Getenv("DAPI_MLNODE_AFFINITY_TTL_MS"); ttlMs != "" {
		if value, err := strconv.ParseInt(ttlMs, 10, 64); err == nil && value > 0 {
			cfg.TTL = time.Duration(value) * time.Millisecond
		}
	}
	if maxEntries := os.Getenv("DAPI_MLNODE_AFFINITY_MAX_ENTRIES"); maxEntries != "" {
		if value, err := strconv.Atoi(maxEntries); err == nil && value > 0 {
			cfg.MaxEntries = value
		}
	}
	return cfg
}

type nodeAffinityEntry struct {
	nodeID    string
	count     int
	firstSeen time.Time
}

// nodeSessionAffinity maps a session id to a bounded sticky mlnode so repeat requests
// reuse one GPU's warm KV cache. Concurrent-safe; its lock is independent of Broker.mu.
type nodeSessionAffinity struct {
	cfg   nodeAffinityConfig
	mu    sync.Mutex
	byKey map[string]*nodeAffinityEntry
	now   func() time.Time
}

func newNodeSessionAffinity(cfg nodeAffinityConfig) *nodeSessionAffinity {
	return &nodeSessionAffinity{
		cfg:   cfg,
		byKey: make(map[string]*nodeAffinityEntry),
		now:   time.Now,
	}
}

// enabled reports whether this participant has turned on mlnode stickiness.
func (affinity *nodeSessionAffinity) enabled() bool {
	return affinity != nil && affinity.cfg.Enabled
}

// bindingKey scopes a session to its escrow: one participant serves many, and the same
// session id from two of them must not land on one binding.
func bindingKey(escrowID, sessionID string) string {
	return escrowID + "\x00" + sessionID
}

// pick returns the session's sticky node if the binding is still live; false
// means no affinity (caller uses least-busy, then records where it landed).
func (affinity *nodeSessionAffinity) pick(escrowID, sessionID string) (string, bool) {
	if !affinity.enabled() || sessionID == "" {
		return "", false
	}
	key := bindingKey(escrowID, sessionID)
	affinity.mu.Lock()
	defer affinity.mu.Unlock()
	entry := affinity.byKey[key]
	if entry == nil {
		return "", false
	}
	if affinity.expiredLocked(entry) {
		delete(affinity.byKey, key)
		return "", false
	}
	return entry.nodeID, true
}

// record binds a session to its node (or bumps the counter), evicting at the
// request bound so load rebalances.
func (affinity *nodeSessionAffinity) record(escrowID, sessionID, nodeID string) {
	if !affinity.enabled() || sessionID == "" || nodeID == "" {
		return
	}
	key := bindingKey(escrowID, sessionID)
	affinity.mu.Lock()
	defer affinity.mu.Unlock()
	entry := affinity.byKey[key]
	if entry == nil || entry.nodeID != nodeID || affinity.expiredLocked(entry) {
		if entry == nil && len(affinity.byKey) >= affinity.cfg.MaxEntries {
			affinity.sweepLocked()
		}
		affinity.byKey[key] = &nodeAffinityEntry{nodeID: nodeID, count: 1, firstSeen: affinity.now()}
		return
	}
	entry.count++
	if entry.count >= affinity.cfg.MaxRequests {
		delete(affinity.byKey, key)
	}
}

func (affinity *nodeSessionAffinity) expiredLocked(entry *nodeAffinityEntry) bool {
	if entry.count >= affinity.cfg.MaxRequests {
		return true
	}
	return affinity.now().Sub(entry.firstSeen) >= affinity.cfg.TTL
}

// sweepLocked reclaims expired entries, then -- if still full -- arbitrary
// live ones down to 90%. Runs only at MaxEntries. Caller holds affinity.mu.
func (affinity *nodeSessionAffinity) sweepLocked() {
	for key, entry := range affinity.byKey {
		if affinity.expiredLocked(entry) {
			delete(affinity.byKey, key)
		}
	}
	if len(affinity.byKey) < affinity.cfg.MaxEntries {
		return
	}
	target := affinity.cfg.MaxEntries - affinity.cfg.MaxEntries/10
	for key := range affinity.byKey {
		if len(affinity.byKey) <= target {
			break
		}
		delete(affinity.byKey, key)
	}
}
