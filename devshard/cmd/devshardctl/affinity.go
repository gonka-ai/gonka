package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Affinity decision outcomes for devshard_gateway_affinity_decision_total.
const (
	affinityDecisionHit     = "hit"
	affinityDecisionYielded = "yielded"
	affinityDecisionMiss    = "miss"
)

// sessionTokenSecret keys every session token. Drawn once per process, never persisted.
var sessionTokenSecret = sync.OnceValue(func() []byte { return []byte(rand.Text()) })

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

func membershipTest(members []string) func(participant string) bool {
	memberSet := make(map[string]bool, len(members))
	for _, key := range members {
		memberSet[key] = true
	}
	return func(participant string) bool { return memberSet[participant] }
}

type affinityKeyContextKey struct{}

// withAffinityKey records the client's cache key before normalization strips it from the body.
func withAffinityKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, affinityKeyContextKey{}, key)
}

func affinityKeyFromContext(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(affinityKeyContextKey{}).(string)
	return key, ok
}

// resolveAffinityKey prefers the pooled layer's record, which saw the body before the strip.
func resolveAffinityKey(ctx context.Context, req chatRequest) string {
	if key, recorded := affinityKeyFromContext(ctx); recorded {
		return key
	}
	return req.AffinityKey
}

// affinityKeyFromDocument reads the client's cache key: prompt_cache_key, falling back to user.
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

func deriveSessionToken(secret []byte, escrowID, callerCredential, clientKey string) string {
	if len(secret) == 0 || clientKey == "" {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(escrowID + "\x00" + callerCredential + "\x00" + clientKey))
	return hex.EncodeToString(mac.Sum(nil))
}

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

type affinityEntry struct {
	participant string
	count       int
	firstSeen   time.Time
}

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

func (tracker *affinityTracker) Size() int {
	if tracker == nil {
		return 0
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return len(tracker.byKey)
}

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

// sweepLocked drops expired entries, then live ones down to 90% of the cap.
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

func (tracker *affinityTracker) expiredLocked(entry *affinityEntry) bool {
	if entry.count >= tracker.cfg.MaxRequests {
		return true
	}
	return tracker.now().Sub(entry.firstSeen) >= tracker.cfg.TTL
}
