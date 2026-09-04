// Package sessionaffinity binds a session to one mlnode for bounded KV-cache reuse.
// See proposals/kv-cache-affinity/README.md.
package sessionaffinity

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Decision outcomes for decentralized_api_mlnode_affinity_decision_total.
	DecisionHit       = "hit"
	DecisionYielded   = "yielded"
	DecisionCongested = "congested"
	DecisionMiss      = "miss"

	// yieldMargin is how many more in-flight requests the sticky node may carry.
	yieldMargin = 2
)

type Config struct {
	Enabled     bool
	MaxRequests int
	TTL         time.Duration
	MaxEntries  int
}

func defaultConfig() Config {
	return Config{
		Enabled:     false,
		MaxRequests: 64,
		TTL:         10 * time.Minute,
		MaxEntries:  50_000,
	}
}

func ConfigFromEnv() Config {
	cfg := defaultConfig()
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

type Selection struct {
	Model         string
	SessionID     string
	StickyNodeID  string // "" when the session has no live binding
	StickyUsable  bool   // the bound node is registered, unskipped and available
	StickyLoad    int
	LeastBusyLoad int
}

// bindingKey scopes a session to its escrow: one participant serves many.
type bindingKey struct {
	escrowID  string
	sessionID string
}

type entry struct {
	nodeID    string
	count     int
	firstSeen time.Time
}

// Tracker holds the bindings. A nil *Tracker is an inert no-op.
type Tracker struct {
	cfg            Config
	recordDecision func(decision, model string)
	mu             sync.Mutex
	byKey          map[bindingKey]*entry
	now            func() time.Time
}

// New injects the decision recorder rather than importing it, so the policy is testable
// without the process-global metrics registry.
func New(cfg Config, recordDecision func(decision, model string)) *Tracker {
	if recordDecision == nil {
		recordDecision = func(string, string) {}
	}
	return &Tracker{cfg: cfg, recordDecision: recordDecision, byKey: make(map[bindingKey]*entry), now: time.Now}
}

func (tracker *Tracker) enabled() bool {
	return tracker != nil && tracker.cfg.Enabled
}

// StickyNode returns the session's remembered node while its binding is still live, "" otherwise.
func (tracker *Tracker) StickyNode(escrowID, sessionID string) string {
	if !tracker.enabled() || sessionID == "" {
		return ""
	}
	key := bindingKey{escrowID: escrowID, sessionID: sessionID}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	boundEntry := tracker.byKey[key]
	if boundEntry == nil {
		return ""
	}
	if tracker.expiredLocked(boundEntry) {
		delete(tracker.byKey, key)
		return ""
	}
	return boundEntry.nodeID
}

// ServeSticky records one decision. Call it once per acquire, with the id StickyNode gave it.
func (tracker *Tracker) ServeSticky(selection Selection) bool {
	if !tracker.enabled() {
		return false
	}
	switch {
	case selection.StickyNodeID == "":
		if selection.SessionID != "" {
			tracker.recordDecision(DecisionMiss, selection.Model)
		}
		return false
	case !selection.StickyUsable:
		tracker.recordDecision(DecisionYielded, selection.Model)
		return false
	case selection.StickyLoad-selection.LeastBusyLoad > yieldMargin:
		tracker.recordDecision(DecisionCongested, selection.Model)
		return false
	default:
		tracker.recordDecision(DecisionHit, selection.Model)
		return true
	}
}

func (tracker *Tracker) Record(escrowID, sessionID, nodeID string) {
	if !tracker.enabled() || sessionID == "" || nodeID == "" {
		return
	}
	key := bindingKey{escrowID: escrowID, sessionID: sessionID}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	boundEntry := tracker.byKey[key]
	if boundEntry == nil || boundEntry.nodeID != nodeID || tracker.expiredLocked(boundEntry) {
		if boundEntry == nil && len(tracker.byKey) >= tracker.cfg.MaxEntries {
			tracker.sweepLocked()
		}
		tracker.byKey[key] = &entry{nodeID: nodeID, count: 1, firstSeen: tracker.now()}
		return
	}
	boundEntry.count++
	if boundEntry.count >= tracker.cfg.MaxRequests {
		delete(tracker.byKey, key)
	}
}

func (tracker *Tracker) expiredLocked(boundEntry *entry) bool {
	if boundEntry.count >= tracker.cfg.MaxRequests {
		return true
	}
	return tracker.now().Sub(boundEntry.firstSeen) >= tracker.cfg.TTL
}

// sweepLocked reclaims expired entries, then live ones down to 90% of the cap.
func (tracker *Tracker) sweepLocked() {
	for key, boundEntry := range tracker.byKey {
		if tracker.expiredLocked(boundEntry) {
			delete(tracker.byKey, key)
		}
	}
	if len(tracker.byKey) < tracker.cfg.MaxEntries {
		return
	}
	target := tracker.cfg.MaxEntries - tracker.cfg.MaxEntries/10
	for key := range tracker.byKey {
		if len(tracker.byKey) <= target {
			break
		}
		delete(tracker.byKey, key)
	}
}
