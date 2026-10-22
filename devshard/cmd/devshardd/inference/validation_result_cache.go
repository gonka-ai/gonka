package inference

import (
	"sync"
	"time"

	devshardpkg "devshard"
)

const validationResultCacheTTL = 30 * time.Minute

type validationResultCacheKey struct {
	escrowID    string
	inferenceID uint64
}

type validationResultCacheEntry struct {
	valid   bool
	reason  string
	expires time.Time
}

// validationResultCache stores local Validate verdicts so a host that already
// completed payload/ML work can publish a Phase-B MsgValidationVote without
// re-running the job. A hit lasts until the entry's own expires time.
//
// cur is the open window and prev is the one before it. The next put or get
// after validationResultCacheTTL promotes cur to prev and drops the generation
// before that in one assignment, so unread keys do not stay until process
// exit. A gap of two TTLs drops both windows: every entry in them is already
// past expires. Rotation does not scan.
type validationResultCache struct {
	mu       sync.Mutex
	cur      map[validationResultCacheKey]validationResultCacheEntry
	prev     map[validationResultCacheKey]validationResultCacheEntry
	curStart time.Time
}

// rotateLocked rolls the windows forward. Caller holds c.mu.
func (c *validationResultCache) rotateLocked(now time.Time) {
	if c.curStart.IsZero() {
		c.curStart = now
		return
	}
	elapsed := now.Sub(c.curStart)
	if elapsed < validationResultCacheTTL {
		return
	}
	if elapsed < 2*validationResultCacheTTL {
		c.prev = c.cur
	} else {
		c.prev = nil
	}
	c.cur = nil
	c.curStart = now
}

func (c *validationResultCache) get(escrowID string, inferenceID uint64) (valid bool, reason string, ok bool) {
	if c == nil {
		return false, "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rotateLocked(now)
	key := validationResultCacheKey{escrowID: escrowID, inferenceID: inferenceID}
	if valid, reason, ok = lookupValidationResult(c.cur, key, now); ok {
		return valid, reason, true
	}
	return lookupValidationResult(c.prev, key, now)
}

func (c *validationResultCache) put(escrowID string, inferenceID uint64, result *devshardpkg.ValidateResult) {
	if c == nil || result == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rotateLocked(now)
	if c.cur == nil {
		c.cur = make(map[validationResultCacheKey]validationResultCacheEntry)
	}
	c.cur[validationResultCacheKey{escrowID: escrowID, inferenceID: inferenceID}] = validationResultCacheEntry{
		valid:   result.Valid,
		reason:  result.Reason,
		expires: now.Add(validationResultCacheTTL),
	}
}

// lookupValidationResult returns a live entry. An expired key is removed so a
// later read of that same key does not keep the slot; unread keys leave with
// their generation on rotate.
func lookupValidationResult(gen map[validationResultCacheKey]validationResultCacheEntry, key validationResultCacheKey, now time.Time) (valid bool, reason string, ok bool) {
	ent, found := gen[key]
	if !found {
		return false, "", false
	}
	if now.After(ent.expires) {
		delete(gen, key)
		return false, "", false
	}
	return ent.valid, ent.reason, true
}
