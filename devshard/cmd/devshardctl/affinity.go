package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Affinity decision outcomes for devshard_gateway_affinity_decision_total; see affinityDecision.
const (
	affinityDecisionHit     = "hit"
	affinityDecisionYielded = "yielded"
	affinityDecisionMiss    = "miss"
)

// sessionTokenSecret keys every session token. It is drawn once per process and never
// persisted, so no secret enters the repository or the image.
var sessionTokenSecret = sync.OnceValue(func() []byte { return []byte(rand.Text()) })

// affinityDecision classifies a primary's outcome against the preference the picker was given.
func affinityDecision(stickyParticipant, servedBy string) string {
	switch stickyParticipant {
	case "":
		return affinityDecisionMiss
	case servedBy:
		return affinityDecisionHit
	default:
		return affinityDecisionYielded
	}
}

// membershipTest answers "is this participant still in the group", so a binding to one
// that left is dropped instead of steering the request at a host that cannot serve it.
func membershipTest(members []string) func(participant string) bool {
	memberSet := make(map[string]bool, len(members))
	for _, key := range members {
		memberSet[key] = true
	}
	return func(participant string) bool { return memberSet[participant] }
}

// affinityKeyFromDocument reads prompt_cache_key (fallback: user) as the
// OpenAI-compatible cache key; see proposals/kv-cache-affinity/README.md.
func affinityKeyFromDocument(document *ChatRequestDocument) string {
	for _, name := range []string{"prompt_cache_key", "user"} {
		if value, ok := document.String(name); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// deriveSessionToken binds the client's own string to its escrow and to the credential the caller
// presented, so guessing that string alone no longer reaches another client's cache namespace.
func deriveSessionToken(secret []byte, escrowID, callerCredential, clientKey string) string {
	if len(secret) == 0 || clientKey == "" {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	// NUL appears in neither an escrow id nor an HTTP header value, so this join is unambiguous.
	_, _ = mac.Write([]byte(escrowID + "\x00" + callerCredential + "\x00" + clientKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// affinityConfig tunes the bounded stickiness; OFF unless explicitly enabled.
type affinityConfig struct {
	Enabled     bool
	MaxRequests int
	TTL         time.Duration
	MaxEntries  int
}

func defaultAffinityConfig() affinityConfig {
	return affinityConfig{
		Enabled:     false,
		MaxRequests: 32,
		TTL:         2 * time.Minute,
		MaxEntries:  50_000,
	}
}

// affinityConfigFromEnv reads the tunables from the environment, keeping the
// conservative defaults for anything unset.
func affinityConfigFromEnv() affinityConfig {
	cfg := defaultAffinityConfig()
	cfg.Enabled = readBoolEnv("DEVSHARD_AFFINITY_ENABLED", cfg.Enabled)
	if maxRequests := readInt64Env("DEVSHARD_AFFINITY_MAX_REQUESTS", int64(cfg.MaxRequests)); maxRequests > 0 {
		cfg.MaxRequests = int(maxRequests)
	}
	if ttlMs := readInt64Env("DEVSHARD_AFFINITY_TTL_MS", cfg.TTL.Milliseconds()); ttlMs > 0 {
		cfg.TTL = time.Duration(ttlMs) * time.Millisecond
	}
	if maxEntries := readInt64Env("DEVSHARD_AFFINITY_MAX_ENTRIES", int64(cfg.MaxEntries)); maxEntries > 0 {
		cfg.MaxEntries = int(maxEntries)
	}
	return cfg
}

// affinityEntry is one session's sticky binding.
type affinityEntry struct {
	participant string
	count       int
	firstSeen   time.Time
}

// affinityTracker maps a session key to a bounded sticky participant.
type affinityTracker struct {
	cfg   affinityConfig
	mu    sync.Mutex
	byKey map[string]*affinityEntry
	now   func() time.Time
}

func newAffinityTracker(cfg affinityConfig) *affinityTracker {
	return &affinityTracker{
		cfg:   cfg,
		byKey: make(map[string]*affinityEntry),
		now:   time.Now,
	}
}

func (tracker *affinityTracker) enabled() bool { return tracker != nil && tracker.cfg.Enabled }

// Size reports the number of live bindings, for the affinity_bindings gauge.
func (tracker *affinityTracker) Size() int {
	if tracker == nil {
		return 0
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return len(tracker.byKey)
}

// Pick returns the session's sticky participant if it's still live and a
// member; false means route naturally, then call Record.
func (tracker *affinityTracker) Pick(key string, isMember func(participant string) bool) (string, bool) {
	if !tracker.enabled() || key == "" {
		return "", false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	entry := tracker.byKey[key]
	if entry == nil {
		return "", false
	}
	if tracker.expiredLocked(entry) || (isMember != nil && !isMember(entry.participant)) {
		delete(tracker.byKey, key)
		return "", false
	}
	return entry.participant, true
}

// Record binds key to participant (or bumps the counter), evicting at the
// request bound so the next Pick re-randomises.
func (tracker *affinityTracker) Record(key, participant string) {
	if !tracker.enabled() || key == "" || participant == "" {
		return
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	entry := tracker.byKey[key]
	if entry == nil || entry.participant != participant || tracker.expiredLocked(entry) {
		if entry == nil && len(tracker.byKey) >= tracker.cfg.MaxEntries {
			tracker.sweepLocked()
		}
		tracker.byKey[key] = &affinityEntry{participant: participant, count: 1, firstSeen: tracker.now()}
		return
	}
	entry.count++
	if entry.count >= tracker.cfg.MaxRequests {
		delete(tracker.byKey, key)
	}
}

// sweepLocked drops expired entries, then arbitrary live ones down to 90%
// of the cap if still full. Caller holds tracker.mu; runs only at MaxEntries.
func (tracker *affinityTracker) sweepLocked() {
	for key, entry := range tracker.byKey {
		if tracker.expiredLocked(entry) {
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

// expiredLocked reports whether an entry exhausted its request budget or
// outlived its TTL. Caller holds tracker.mu.
func (tracker *affinityTracker) expiredLocked(entry *affinityEntry) bool {
	if entry.count >= tracker.cfg.MaxRequests {
		return true
	}
	return tracker.now().Sub(entry.firstSeen) >= tracker.cfg.TTL
}
